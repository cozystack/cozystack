/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxmoxnetworkcontroller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

const (
	tenantA  = "tenant-a"
	tenantB  = "tenant-b"
	zoneName = "default"
	provider = "pxtenant"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, pxv1.AddToScheme, AddMirrorsToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	gv := schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1alpha1"}
	s.AddKnownTypeWithName(gv.WithKind("ProxmoxMachine"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("ProxmoxMachineList"), &unstructured.UnstructuredList{})
	return s
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&pxv1.ProxmoxNetwork{}, &pxv1.ProxmoxNetworkZone{})
	for field, fn := range map[string]func(*pxv1.ProxmoxNetwork) string{
		indexNetworkZone:   func(n *pxv1.ProxmoxNetwork) string { return n.Spec.Zone },
		indexNetworkSubnet: func(n *pxv1.ProxmoxNetwork) string { return n.Spec.Subnet },
		indexNetworkPool:   func(n *pxv1.ProxmoxNetwork) string { return n.Spec.IPPool },
	} {
		b = b.WithIndex(&pxv1.ProxmoxNetwork{}, field, func(o client.Object) []string {
			return []string{fn(o.(*pxv1.ProxmoxNetwork))}
		})
	}
	return b.Build()
}

func namespace(name string, lbls map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
}

func zone(mutate ...func(*pxv1.ProxmoxNetworkZone)) *pxv1.ProxmoxNetworkZone {
	z := &pxv1.ProxmoxNetworkZone{
		ObjectMeta: metav1.ObjectMeta{Name: zoneName},
		Spec: pxv1.ProxmoxNetworkZoneSpec{
			Bridge:          "vmbr70",
			ProviderNetwork: provider,
			VLANRanges:      []string{"101-103"},
		},
	}
	for _, m := range mutate {
		m(z)
	}
	return z
}

func network(ns, subnet string) *pxv1.ProxmoxNetwork {
	return &pxv1.ProxmoxNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: subnet, Namespace: ns},
		Spec:       pxv1.ProxmoxNetworkSpec{Zone: zoneName, Subnet: subnet, IPPool: subnet},
	}
}

// subnet is a Kube-OVN Subnet already bound to the Vlan named after it and
// reported Ready, with the VM range .100-.199 excluded.
func subnet(name, cidr24 string) *Subnet {
	return &Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: SubnetSpec{
			Vpc: "vpc-x", CIDRBlock: cidr24 + ".0/24", Gateway: cidr24 + ".1",
			ExcludeIps: []string{cidr24 + ".100.." + cidr24 + ".199"}, Vlan: name, LogicalGateway: true,
		},
		Status: SubnetStatus{Conditions: []KubeOVNCondition{{Type: "Validated", Status: "True"}, {Type: "Ready", Status: "True"}}},
	}
}

func pool(ns, name, cidr24 string, used int) *InClusterIPPool {
	return &InClusterIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Finalizers: []string{ipamProtectPoolFinalizer}},
		Spec:       InClusterIPPoolSpec{Addresses: []string{cidr24 + ".100-" + cidr24 + ".199"}, Prefix: 24, Gateway: cidr24 + ".1"},
		Status:     InClusterIPPoolStatus{Addresses: &InClusterIPPoolAddresses{Total: 100, Used: used, Free: 100 - used}},
	}
}

func reconcileNet(t *testing.T, c client.Client, ns, name string) ctrl.Result {
	t.Helper()
	return reconcileNetWith(t, &NetworkReconciler{Client: c, APIReader: c}, ns, name)
}

func reconcileNetWith(t *testing.T, r *NetworkReconciler, ns, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ns, Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s/%s: %v", ns, name, err)
	}
	return res
}

// later is a clock that runs d ahead of the wall clock, as if that much time
// had passed since the network was deleted.
func later(d time.Duration) clock {
	return func() time.Time { return time.Now().Add(d) }
}

func getNet(t *testing.T, c client.Client, ns, name string) *pxv1.ProxmoxNetwork {
	t.Helper()
	n := &pxv1.ProxmoxNetwork{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cond(n *pxv1.ProxmoxNetwork, t string) *metav1.Condition {
	return meta.FindStatusCondition(n.Status.Conditions, t)
}

func wantCond(t *testing.T, n *pxv1.ProxmoxNetwork, typ string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	c := cond(n, typ)
	if c == nil {
		t.Fatalf("%s: condition %s missing; have %+v", n.Name, typ, n.Status.Conditions)
	}
	if c.Status != status || (reason != "" && c.Reason != reason) {
		t.Fatalf("%s: %s = %s/%s (%s), want %s/%s", n.Name, typ, c.Status, c.Reason, c.Message, status, reason)
	}
}

func TestNetworkBecomesReady(t *testing.T) {
	c := newClient(t, namespace(tenantA, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))

	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")

	if n.Status.VLAN != 101 || n.Status.Bridge != "vmbr70" || n.Status.Gateway != "10.208.64.1" || n.Status.Prefix != 24 {
		t.Fatalf("status = %+v", n.Status)
	}
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if !controllerutil.ContainsFinalizer(n, pxv1.Finalizer) {
		t.Fatal("network has no finalizer")
	}

	v := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, v); err != nil {
		t.Fatal(err)
	}
	if v.Spec.ID != 101 || v.Spec.Provider != provider || v.Labels[pxv1.LabelZone] != zoneName ||
		v.Labels[pxv1.LabelNetworkNamespace] != tenantA || !controllerutil.ContainsFinalizer(v, pxv1.Finalizer) {
		t.Fatalf("vlan = %+v", v)
	}
	s := &Subnet{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, s); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(s, pxv1.Finalizer) {
		t.Fatal("the subnet must carry the finalizer while the network exists")
	}
	ns := &corev1.Namespace{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: tenantA}, ns); err != nil {
		t.Fatal(err)
	}
	if got := ns.Annotations[pxv1.AnnotationNamespaceNetworks]; got != "vmbr70/101/subnet-a" {
		t.Fatalf("namespace annotation = %q", got)
	}
}

func TestTwoTenantsGetDistinctVLANsAndHandMadeOnesAreSkipped(t *testing.T) {
	handMade := &Vlan{ObjectMeta: metav1.ObjectMeta{Name: "legacy"}, Spec: VlanSpec{ID: 101, Provider: provider}}
	otherProvider := &Vlan{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"}, Spec: VlanSpec{ID: 102, Provider: "other"}}
	c := newClient(t, namespace(tenantA, nil), namespace(tenantB, nil), zone(), handMade, otherProvider,
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0),
		network(tenantB, "subnet-b"), subnet("subnet-b", "10.208.128"), pool(tenantB, "subnet-b", "10.208.128", 0))

	reconcileNet(t, c, tenantA, "subnet-a")
	reconcileNet(t, c, tenantB, "subnet-b")
	a, b := getNet(t, c, tenantA, "subnet-a"), getNet(t, c, tenantB, "subnet-b")
	if a.Status.VLAN != 102 {
		t.Fatalf("tenant A got %d; 101 is taken by a hand-made Vlan, 102 on another provider is free", a.Status.VLAN)
	}
	if b.Status.VLAN != 103 {
		t.Fatalf("tenant B got %d, want 103", b.Status.VLAN)
	}
}

func TestRestartAdoptsTheVlanInsteadOfAllocatingAgain(t *testing.T) {
	// A crash after the Vlan create and before the status write leaves a Vlan
	// and an empty status. The next pass must keep that VLAN.
	orphanedByCrash := &Vlan{
		ObjectMeta: metav1.ObjectMeta{Name: "subnet-a", Labels: map[string]string{
			pxv1.LabelZone: zoneName, pxv1.LabelNetworkNamespace: tenantA, pxv1.LabelNetworkName: "subnet-a",
		}},
		Spec: VlanSpec{ID: 103, Provider: provider},
	}
	c := newClient(t, namespace(tenantA, nil), zone(), orphanedByCrash,
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))

	reconcileNet(t, c, tenantA, "subnet-a")
	adopted := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, adopted); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(adopted, pxv1.Finalizer) {
		t.Fatalf("the adopted Vlan lacks the finalizer: %v", adopted.Finalizers)
	}
	for range 2 {
		reconcileNet(t, c, tenantA, "subnet-a")
	}
	wantUnwritten(t, c, adopted)
	n := getNet(t, c, tenantA, "subnet-a")
	if n.Status.VLAN != 103 {
		t.Fatalf("VLAN = %d, want the adopted 103", n.Status.VLAN)
	}
	var vlans VlanList
	if err := c.List(context.Background(), &vlans); err != nil {
		t.Fatal(err)
	}
	if len(vlans.Items) != 1 {
		t.Fatalf("%d Vlans exist, want 1", len(vlans.Items))
	}
}

func TestVlanOfAnotherNetworkIsNotAdopted(t *testing.T) {
	foreign := &Vlan{
		ObjectMeta: metav1.ObjectMeta{Name: "subnet-a", Labels: map[string]string{
			pxv1.LabelNetworkNamespace: tenantB, pxv1.LabelNetworkName: "subnet-a",
		}},
		Spec: VlanSpec{ID: 101, Provider: provider},
	}
	c := newClient(t, namespace(tenantA, nil), zone(), foreign,
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionVLANAllocated, metav1.ConditionFalse, "VLANNameTaken")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "VLANNameTaken")
}

func TestZoneExhausted(t *testing.T) {
	c := newClient(t, namespace(tenantA, nil), zone(func(z *pxv1.ProxmoxNetworkZone) {
		z.Spec.VLANRanges = []string{"101"}
		z.Spec.ReservedVLANs = []int32{101}
	}), network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionVLANAllocated, metav1.ConditionFalse, "ZoneExhausted")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "ZoneExhausted")
	if n.Status.VLAN != 0 {
		t.Fatalf("VLAN = %d on an exhausted zone", n.Status.VLAN)
	}
}

func TestStaticAssignment(t *testing.T) {
	static := func(vlan int32) func(*pxv1.ProxmoxNetworkZone) {
		return func(z *pxv1.ProxmoxNetworkZone) {
			z.Spec.StaticAssignments = []pxv1.StaticVLANAssignment{{Namespace: tenantA, Name: "subnet-a", VLAN: vlan}}
		}
	}
	c := newClient(t, namespace(tenantA, nil), zone(static(150)),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	if n := getNet(t, c, tenantA, "subnet-a"); n.Status.VLAN != 150 {
		t.Fatalf("VLAN = %d, want the static 150 even outside the range", n.Status.VLAN)
	}

	taken := &Vlan{ObjectMeta: metav1.ObjectMeta{Name: "legacy"}, Spec: VlanSpec{ID: 150, Provider: provider}}
	c = newClient(t, namespace(tenantA, nil), zone(static(150)), taken,
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionVLANAllocated, metav1.ConditionFalse, "StaticVLANInUse")
}

func TestNamespaceSelector(t *testing.T) {
	sel := func(z *pxv1.ProxmoxNetworkZone) {
		z.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"proxmox": "yes"}}
	}
	c := newClient(t, namespace(tenantA, nil), zone(sel),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionVLANAllocated, metav1.ConditionFalse, "NamespaceNotAllowed")
	var vlans VlanList
	_ = c.List(context.Background(), &vlans)
	if len(vlans.Items) != 0 {
		t.Fatal("a refused namespace must not get a VLAN")
	}
}

func TestPoolThatOverlapsKubeOVNIsRefused(t *testing.T) {
	s := subnet("subnet-a", "10.208.64")
	s.Spec.ExcludeIps = []string{"10.208.64.100..10.208.64.150"}
	c := newClient(t, namespace(tenantA, nil), zone(), network(tenantA, "subnet-a"), s, pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionIPPoolReady, metav1.ConditionFalse, "PoolOverlapsSubnet")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "PoolOverlapsSubnet")
}

func TestSubnetNotBoundAndNotReady(t *testing.T) {
	s := subnet("subnet-a", "10.208.64")
	s.Spec.Vlan = ""
	c := newClient(t, namespace(tenantA, nil), zone(), network(tenantA, "subnet-a"), s, pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionSubnetReady, metav1.ConditionFalse, "SubnetNotBound")
	got := &Subnet{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, got)
	if controllerutil.ContainsFinalizer(got, pxv1.Finalizer) {
		t.Fatal("a subnet that is not bound to the VLAN must not get the finalizer")
	}

	s = subnet("subnet-a", "10.208.64")
	s.Status.Conditions = []KubeOVNCondition{{Type: "Validated", Status: "False", Message: "failed to get vlan"}}
	c = newClient(t, namespace(tenantA, nil), zone(), network(tenantA, "subnet-a"), s, pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionSubnetReady, metav1.ConditionFalse, "SubnetInvalid")
}

func TestKubeOVNConflictIsSurfaced(t *testing.T) {
	c := newClient(t, namespace(tenantA, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	v := &Vlan{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, v)
	v.Status.Conflict = true
	if err := c.Update(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionVLANAllocated, metav1.ConditionFalse, "VLANConflict")
}

func TestPoolExhaustionKeepsTheNetworkReady(t *testing.T) {
	p := pool(tenantA, "subnet-a", "10.208.64", 100)
	c := newClient(t, namespace(tenantA, nil), zone(), network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), p)
	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionIPPoolReady, metav1.ConditionTrue, "PoolExhausted")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
}

func deleteNet(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	if err := c.Delete(context.Background(), getNet(t, c, ns, name)); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteWaitsForTheLastAddress(t *testing.T) {
	c := newClient(t, namespace(tenantA, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 2))
	reconcileNet(t, c, tenantA, "subnet-a")

	// Helm removes the whole subnet at once: network, Kube-OVN Subnet, pool.
	deleteNet(t, c, tenantA, "subnet-a")
	s := &Subnet{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, s)
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	res := reconcileNet(t, c, tenantA, "subnet-a")
	if res.RequeueAfter == 0 {
		t.Fatal("an in-use network must requeue")
	}
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "InUse")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); err != nil {
		t.Fatalf("the Vlan must survive while addresses are allocated: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Subnet{}); err != nil {
		t.Fatalf("the Subnet must survive while addresses are allocated: %v", err)
	}
	ns := &corev1.Namespace{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: tenantA}, ns)
	if ns.Annotations[pxv1.AnnotationNamespaceNetworks] == "" {
		t.Fatal("a network still in use must stay in the namespace annotation, or capmox updates on its machines would be refused")
	}

	// The machines go away; the provider empties and releases the pool.
	p := &InClusterIPPool{}
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, p)
	p.Status.Addresses.Used, p.Status.Addresses.Free = 0, 100
	p.Finalizers = nil
	if err := c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	// First pass: our finalizer leaves the deleting Subnet, which then goes;
	// second pass: with the Subnet gone the Vlan and the network are released.
	reconcileNet(t, c, tenantA, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("network still exists: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Vlan must be released: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Subnet{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Subnet must be let go: %v", err)
	}

	// The NotFound pass clears the namespace annotation.
	reconcileNet(t, c, tenantA, "subnet-a")
	_ = c.Get(context.Background(), client.ObjectKey{Name: tenantA}, ns)
	if v, ok := ns.Annotations[pxv1.AnnotationNamespaceNetworks]; ok {
		t.Fatalf("namespace annotation left behind: %q", v)
	}
}

func TestDeleteKeepsTheVlanWhileTheSubnetExists(t *testing.T) {
	c := newClient(t, namespace(tenantA, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	deleteNet(t, c, tenantA, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SubnetStillExists")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); err != nil {
		t.Fatalf("the Vlan must stay while a live subnet references it: %v", err)
	}

	// The subnet moving off the Vlan does not free it either: with spec.vlan
	// cleared, Kube-OVN never touches the localnet port's tag again, so the
	// port keeps the old tag until the logical switch goes.
	s := &Subnet{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, s)
	s.Spec.Vlan = ""
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SubnetStillExists")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); err != nil {
		t.Fatalf("the Vlan must stay while the unbound subnet still exists: %v", err)
	}
}

func TestDeleteWaitsForKubeOVNToDeleteTheSubnet(t *testing.T) {
	// Kube-OVN keeps its own finalizer while pod IPs remain, and validates the
	// subnet's Vlan before it would ever remove it. Releasing the Vlan before
	// the subnet is gone would leave the subnet Terminating for good.
	s := subnet("subnet-a", "10.208.64")
	s.Finalizers = []string{"kubeovn.io/kube-ovn-controller"}
	c := newClient(t, namespace(tenantA, nil), zone(), network(tenantA, "subnet-a"), s, pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	deleteNet(t, c, tenantA, "subnet-a")
	live := &Subnet{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, live)
	if err := c.Delete(context.Background(), live); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		reconcileNet(t, c, tenantA, "subnet-a")
	}
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SubnetDeleting")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); err != nil {
		t.Fatalf("the Vlan must stay while Kube-OVN still holds the subnet: %v", err)
	}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, live)
	if controllerutil.ContainsFinalizer(live, pxv1.Finalizer) {
		t.Fatal("our finalizer must leave a deleting subnet so Kube-OVN can finish")
	}

	// Kube-OVN finishes: its finalizer goes and the subnet with it.
	live.Finalizers = nil
	if err := c.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	reconcileNet(t, c, tenantA, "subnet-a")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Vlan must be released once the subnet is gone: %v", err)
	}
}

func TestDeleteWaitsForProxmoxMachinesOnThePool(t *testing.T) {
	// A namespace deletion removes IPAddressClaims, and the IPAM provider
	// releases their addresses at once, while capmox is still destroying the
	// VMs. A ProxmoxMachine that references the pool keeps the network.
	machine := &unstructured.Unstructured{}
	machine.SetGroupVersionKind(schema.GroupVersionKind{Group: "infrastructure.cluster.x-k8s.io", Version: "v1alpha1", Kind: "ProxmoxMachine"})
	machine.SetNamespace(tenantA)
	machine.SetName("worker-0")
	_ = unstructured.SetNestedField(machine.Object, map[string]any{
		"apiGroup": "ipam.cluster.x-k8s.io", "kind": "InClusterIPPool", "name": "subnet-a",
	}, "spec", "network", "default", "ipv4PoolRef")

	c := newClient(t, namespace(tenantA, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0), machine)
	reconcileNet(t, c, tenantA, "subnet-a")
	deleteNet(t, c, tenantA, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "InUse")

	if err := c.Delete(context.Background(), machine); err != nil {
		t.Fatal(err)
	}
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SubnetStillExists")
}

func TestVLANIsReusableAfterRelease(t *testing.T) {
	c := newClient(t, namespace(tenantA, nil), namespace(tenantB, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	if v := getNet(t, c, tenantA, "subnet-a").Status.VLAN; v != 101 {
		t.Fatalf("first allocation = %d", v)
	}
	deleteNet(t, c, tenantA, "subnet-a")
	s := &Subnet{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, s)
	_ = c.Delete(context.Background(), s)
	reconcileNet(t, c, tenantA, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")

	for _, o := range []client.Object{network(tenantB, "subnet-b"), subnet("subnet-b", "10.208.128"), pool(tenantB, "subnet-b", "10.208.128", 0)} {
		if err := c.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	reconcileNet(t, c, tenantB, "subnet-b")
	if v := getNet(t, c, tenantB, "subnet-b").Status.VLAN; v != 101 {
		t.Fatalf("released VLAN not reused: got %d", v)
	}
}

func TestDynamicAllocationSkipsPinnedVLANs(t *testing.T) {
	pin := func(z *pxv1.ProxmoxNetworkZone) {
		z.Spec.StaticAssignments = []pxv1.StaticVLANAssignment{{Namespace: tenantB, Name: "subnet-b", VLAN: 101}}
	}
	c := newClient(t, namespace(tenantA, nil), zone(pin),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0))
	reconcileNet(t, c, tenantA, "subnet-a")
	if v := getNet(t, c, tenantA, "subnet-a").Status.VLAN; v != 102 {
		t.Fatalf("got VLAN %d; 101 is pinned to another network", v)
	}
}

// deletedWithListedSubnets deletes tenant A's network and its Subnet after
// Kube-OVN has listed names on the network's Vlan, and runs the first finalize
// pass, which lets the Subnet go.
func deletedWithListedSubnets(t *testing.T, listed []string, objs ...client.Object) client.Client {
	t.Helper()
	c := newClient(t, append([]client.Object{namespace(tenantA, nil), zone(),
		network(tenantA, "subnet-a"), subnet("subnet-a", "10.208.64"), pool(tenantA, "subnet-a", "10.208.64", 0)}, objs...)...)
	reconcileNet(t, c, tenantA, "subnet-a")
	setVlanSubnets(t, c, "subnet-a", listed...)
	deleteNet(t, c, tenantA, "subnet-a")
	deleteSubnet(t, c, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Subnet{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Subnet must be gone after the first pass: %v", err)
	}
	return c
}

func setVlanSubnets(t *testing.T, c client.Client, vlan string, subnets ...string) {
	t.Helper()
	v := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: vlan}, v); err != nil {
		t.Fatal(err)
	}
	v.Status.Subnets = subnets
	if err := c.Update(context.Background(), v); err != nil {
		t.Fatal(err)
	}
}

func deleteSubnet(t *testing.T, c client.Client, name string) {
	t.Helper()
	s := &Subnet{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, s); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// pruneTimers reads the per-subnet prune timers the controller keeps on a
// Vlan.
func pruneTimers(t *testing.T, c client.Client, vlan string) map[string]time.Time {
	t.Helper()
	v := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: vlan}, v); err != nil {
		t.Fatal(err)
	}
	raw := map[string]string{}
	if s, ok := v.Annotations[pxv1.AnnotationDeletedSubnets]; ok {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			t.Fatalf("annotation %s = %q: %v", pxv1.AnnotationDeletedSubnets, s, err)
		}
	}
	out := map[string]time.Time{}
	for n, s := range raw {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("timer of %s = %q: %v", n, s, err)
		}
		out[n] = ts
	}
	return out
}

// backdatePruneTimers moves the start of every prune timer on the Vlan into
// the past, as if the passes so far had run that much earlier. That suits the
// zone's orphan collection and timers a gone network left behind; a deleting
// network restarts timers from before its own deletion, so its tests move its
// clock forward instead (later).
func backdatePruneTimers(t *testing.T, c client.Client, vlan string, by time.Duration) {
	t.Helper()
	timers := pruneTimers(t, c, vlan)
	if len(timers) == 0 {
		t.Fatalf("Vlan %s has no prune timers", vlan)
	}
	raw := map[string]string{}
	for n, ts := range timers {
		raw[n] = ts.Add(-by).UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	v := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: vlan}, v); err != nil {
		t.Fatal(err)
	}
	v.Annotations[pxv1.AnnotationDeletedSubnets] = string(b)
	if err := c.Update(context.Background(), v); err != nil {
		t.Fatal(err)
	}
}

func wantVlan(t *testing.T, c client.Client, exists bool, why string) {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, &Vlan{})
	if exists && err != nil {
		t.Fatalf("%s: %v", why, err)
	}
	if !exists && !apierrors.IsNotFound(err) {
		t.Fatalf("%s: %v", why, err)
	}
}

func TestDeleteWaitsForKubeOVNToPruneTheDeletedSubnet(t *testing.T) {
	// Kube-OVN drops a deleted subnet from Vlan.status.subnets only after it
	// has deleted the subnet's logical switch and localnet port, which it
	// starts once the Subnet object is gone. Until then the VLAN must stay
	// allocated, or another network could get it while the old switch still
	// sits on it.
	c := deletedWithListedSubnets(t, []string{"subnet-a"})
	res := reconcileNet(t, c, tenantA, "subnet-a")
	if res.RequeueAfter <= 0 || res.RequeueAfter > 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want a short poll", res.RequeueAfter)
	}
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	wantCond(t, n, pxv1.ConditionSwitchDeleted, metav1.ConditionFalse, "ListedOnVlan")
	wantVlan(t, c, true, "the Vlan must stay while Kube-OVN may still be deleting the switch")
	started := cond(n, pxv1.ConditionSwitchDeleted).LastTransitionTime
	timers := pruneTimers(t, c, "subnet-a")
	if len(timers) != 1 || timers["subnet-a"].IsZero() {
		t.Fatalf("prune timers = %v, want one for subnet-a", timers)
	}
	rv := getNet(t, c, tenantA, "subnet-a").ResourceVersion
	timed := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, timed); err != nil {
		t.Fatal(err)
	}

	// Later passes keep the start of the wait and write nothing.
	reconcileNet(t, c, tenantA, "subnet-a")
	wantUnwritten(t, c, timed)
	n = getNet(t, c, tenantA, "subnet-a")
	if got := cond(n, pxv1.ConditionSwitchDeleted).LastTransitionTime; !got.Equal(&started) {
		t.Fatalf("the prune wait restarted: %v, first %v", got, started)
	}
	if got := pruneTimers(t, c, "subnet-a"); !got["subnet-a"].Equal(timers["subnet-a"]) {
		t.Fatalf("the prune timer restarted: %v, first %v", got, timers)
	}
	if n.ResourceVersion != rv {
		t.Fatal("a pass that changes nothing must not write the network's status")
	}
	wantVlan(t, c, true, "the Vlan must stay while Kube-OVN may still be deleting the switch")

	// Kube-OVN has deleted the switch and prunes the name.
	setVlanSubnets(t, c, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	wantVlan(t, c, false, "the Vlan must be released once Kube-OVN has pruned the subnet")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("network still exists: %v", err)
	}
}

func TestDeleteReleasesTheVLANWhenKubeOVNNeverPrunes(t *testing.T) {
	// A kube-ovn-controller restart between removing its Subnet finalizer and
	// handling the delete event loses the event: the next leader deletes the
	// orphaned switch at startup but never prunes the name. The network must
	// not wait for it forever.
	c := deletedWithListedSubnets(t, []string{"subnet-a"})
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	expired := later(DefaultSubnetPruneTimeout + time.Minute)

	// A longer configured timeout has not run out yet.
	reconcileNetWith(t, &NetworkReconciler{Client: c, APIReader: c, SubnetPruneTimeout: time.Hour, now: expired}, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	wantVlan(t, c, true, "the Vlan must stay until the configured timeout runs out")

	reconcileNetWith(t, &NetworkReconciler{Client: c, APIReader: c, now: expired}, tenantA, "subnet-a")
	wantVlan(t, c, false, "a Vlan whose listed subnets are all gone must be released after the timeout")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("network still exists: %v", err)
	}
}

func TestLiveSubnetOnTheVlanHoldsItPastThePruneTimeout(t *testing.T) {
	// A live subnet listed on the Vlan holds it for as long as it exists, even
	// when its spec.vlan no longer names the Vlan: Kube-OVN never prunes the
	// name while the subnet lives, and its localnet port may still carry the
	// VLAN's tag (always, when spec.vlan is cleared as here).
	elsewhere := &Subnet{ObjectMeta: metav1.ObjectMeta{Name: "someone-else"}}
	c := deletedWithListedSubnets(t, []string{"subnet-a", "someone-else"}, elsewhere)
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "VLANStillReferenced")
	r := &NetworkReconciler{Client: c, APIReader: c, now: later(DefaultSubnetPruneTimeout + time.Minute)}
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "VLANStillReferenced")
	wantVlan(t, c, true, "a live subnet on the Vlan must hold it whatever the timeout")

	// Kube-OVN prunes the network's own subnet: the prune wait is over and
	// its timer goes with it.
	setVlanSubnets(t, c, "subnet-a", "someone-else")
	reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "VLANStillReferenced")
	if sd := cond(n, pxv1.ConditionSwitchDeleted); sd != nil {
		t.Fatalf("no deleted subnet is listed any more, yet %s = %+v", pxv1.ConditionSwitchDeleted, sd)
	}
	v := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, v); err != nil {
		t.Fatal(err)
	}
	if a, ok := v.Annotations[pxv1.AnnotationDeletedSubnets]; ok {
		t.Fatalf("no deleted subnet is listed any more, yet the Vlan carries %s=%q", pxv1.AnnotationDeletedSubnets, a)
	}

	// The other subnet is deleted next. Its prune gets a fresh timeout, not
	// what is left of the first one.
	deleteSubnet(t, c, "someone-else")
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	wantVlan(t, c, true, "a newly deleted subnet must get the whole prune timeout")

	setVlanSubnets(t, c, "subnet-a")
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantVlan(t, c, false, "the Vlan must be released once nothing is listed on it")
}

func TestSubnetDeletedLaterGetsItsOwnPruneTimeout(t *testing.T) {
	// The network's own subnet is never pruned (Kube-OVN lost its delete),
	// while another listed subnet lives on past the prune timeout. When that
	// one is deleted, Kube-OVN may still be deleting its switch: it must get
	// a full timeout of its own, not inherit the first one's expired wait.
	elsewhere := &Subnet{ObjectMeta: metav1.ObjectMeta{Name: "someone-else"}}
	c := deletedWithListedSubnets(t, []string{"subnet-a", "someone-else"}, elsewhere)
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "VLANStillReferenced")
	first := pruneTimers(t, c, "subnet-a")["subnet-a"]

	// The prune timeout passes before the other subnet is deleted.
	r := &NetworkReconciler{Client: c, APIReader: c, now: later(DefaultSubnetPruneTimeout + time.Minute)}
	deleteSubnet(t, c, "someone-else")
	before := r.now.Now().Add(-time.Second)
	res := reconcileNetWith(t, r, tenantA, "subnet-a")
	n := getNet(t, c, tenantA, "subnet-a")
	wantCond(t, n, pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	wantVlan(t, c, true, "a subnet deleted just now must not inherit an expired prune timer")
	if res.RequeueAfter <= 0 || res.RequeueAfter > 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want a short poll", res.RequeueAfter)
	}
	timers := pruneTimers(t, c, "subnet-a")
	if !timers["subnet-a"].Equal(first) {
		t.Fatalf("the first subnet's timer moved: %v, want %v", timers["subnet-a"], first)
	}
	if got := timers["someone-else"]; got.Before(before) {
		t.Fatalf("the newly deleted subnet's timer = %v, want a fresh one", got)
	}

	// Kube-OVN prunes the subnet it was deleting. The one left was gone for
	// longer than the timeout, so the VLAN is released.
	setVlanSubnets(t, c, "subnet-a", "subnet-a")
	reconcileNetWith(t, r, tenantA, "subnet-a")
	wantVlan(t, c, false, "the Vlan must be released once every listed subnet left has timed out")
}

func TestSubnetMovedToAnotherVlanStillHoldsTheOldOne(t *testing.T) {
	// A hand-made subnet moved from this Vlan to another existing one: Kube-OVN
	// lists it on both and retags its localnet port from the new Vlan's update
	// handler, but nothing here can see whether that retag ran. The old Vlan
	// stays held for as long as the subnet exists.
	other := &Vlan{ObjectMeta: metav1.ObjectMeta{Name: "other"}, Spec: VlanSpec{ID: 200, Provider: provider},
		Status: VlanStatus{Subnets: []string{"moved"}}}
	moved := &Subnet{ObjectMeta: metav1.ObjectMeta{Name: "moved"}, Spec: SubnetSpec{Vlan: "other"}}
	c := deletedWithListedSubnets(t, []string{"moved"}, other, moved)
	for range 2 {
		reconcileNet(t, c, tenantA, "subnet-a")
		wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "VLANStillReferenced")
	}
	wantVlan(t, c, true, "a live subnet listed on the Vlan must hold it even after it moved to another Vlan")

	deleteSubnet(t, c, "moved")
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	setVlanSubnets(t, c, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	wantVlan(t, c, false, "the Vlan must be released once Kube-OVN has pruned the moved subnet")
}

func TestRecreatedNetworkGetsAFullPruneTimeout(t *testing.T) {
	// A deleting network's finalizer is removed by hand while it waits for
	// Kube-OVN to prune its subnet, and the vpc chart recreates the network
	// and the subnet under the same names before the zone collects the Vlan.
	// The new network adopts the Vlan but not the old prune timer: kept, it
	// would release the VLAN the moment the new subnet is deleted, while
	// Kube-OVN may still be deleting its switch.
	c := deletedWithListedSubnets(t, []string{"subnet-a"})
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	backdatePruneTimers(t, c, "subnet-a", DefaultSubnetPruneTimeout+time.Hour)

	n := getNet(t, c, tenantA, "subnet-a")
	n.Finalizers = nil
	if err := c.Update(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("network still exists: %v", err)
	}
	if err := c.Create(context.Background(), network(tenantA, "subnet-a")); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), subnet("subnet-a", "10.208.64")); err != nil {
		t.Fatal(err)
	}
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionTrue, "Ready")
	if timers := pruneTimers(t, c, "subnet-a"); len(timers) != 0 {
		t.Fatalf("the recreated network adopted the old prune timers %v", timers)
	}

	deleteNet(t, c, tenantA, "subnet-a")
	deleteSubnet(t, c, "subnet-a")
	reconcileNet(t, c, tenantA, "subnet-a")
	before := time.Now().Add(-time.Second)
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	wantVlan(t, c, true, "a subnet deleted just now must get a full prune timeout, not a timer from before the network was recreated")
	if got := pruneTimers(t, c, "subnet-a")["subnet-a"]; got.Before(before) {
		t.Fatalf("the prune timer = %v, want a fresh one", got)
	}
}

func TestDeleteRestartsPruneTimersFromBeforeTheDeletion(t *testing.T) {
	// A timer on the Vlan that started before the network was deleted is not
	// this network's: an earlier network of the same name or the orphan
	// collection left it, and the network never adopted the Vlan to drop it
	// (its zone was gone, say). It must not cut this network's wait short.
	c := deletedWithListedSubnets(t, []string{"subnet-a"})
	stale := getNet(t, c, tenantA, "subnet-a").DeletionTimestamp.Add(-time.Hour).UTC().Format(time.RFC3339)
	v := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-a"}, v); err != nil {
		t.Fatal(err)
	}
	v.Annotations = map[string]string{pxv1.AnnotationDeletedSubnets: `{"subnet-a":"` + stale + `"}`}
	if err := c.Update(context.Background(), v); err != nil {
		t.Fatal(err)
	}

	before := time.Now().Add(-time.Second)
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	wantVlan(t, c, true, "a timer from before the network's deletion must not release its VLAN")
	if got := pruneTimers(t, c, "subnet-a")["subnet-a"]; got.Before(before) {
		t.Fatalf("the prune timer = %v, want a fresh one", got)
	}
}
