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
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
)

func readyProvider() *ProviderNetwork {
	return &ProviderNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: provider},
		Status:     ProviderNetworkStatus{Ready: true, ReadyNodes: []string{"worker1", "worker2"}},
	}
}

func zoneVlan(name string, id int, ns, net string) *Vlan {
	return &Vlan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Finalizers: []string{pxv1.Finalizer}, Labels: map[string]string{
			pxv1.LabelZone: zoneName, pxv1.LabelNetworkNamespace: ns, pxv1.LabelNetworkName: net,
		}},
		Spec: VlanSpec{ID: id, Provider: provider},
	}
}

func reconcileZone(t *testing.T, c client.Client) *pxv1.ProxmoxNetworkZone {
	t.Helper()
	r := &ZoneReconciler{Client: c, APIReader: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: zoneName}}); err != nil {
		t.Fatal(err)
	}
	z := &pxv1.ProxmoxNetworkZone{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: zoneName}, z); err != nil {
		t.Fatal(err)
	}
	return z
}

func TestZoneStatusCounts(t *testing.T) {
	c := newClient(t, zone(func(z *pxv1.ProxmoxNetworkZone) {
		z.Spec.VLANRanges = []string{"101-110"}
		z.Spec.ReservedVLANs = []int32{110}
	}), readyProvider(),
		network(tenantA, "subnet-a"), zoneVlan("subnet-a", 101, tenantA, "subnet-a"),
		&Vlan{ObjectMeta: metav1.ObjectMeta{Name: "hand-made"}, Spec: VlanSpec{ID: 102, Provider: provider}},
	)
	z := reconcileZone(t, c)
	if z.Status.TotalVLANs != 9 || z.Status.AllocatedVLANs != 1 || z.Status.FreeVLANs != 7 || z.Status.Networks != 1 {
		t.Fatalf("status = %+v", z.Status)
	}
	if !meta.IsStatusConditionTrue(z.Status.Conditions, pxv1.ConditionReady) {
		t.Fatalf("zone not Ready: %+v", z.Status.Conditions)
	}
}

func TestZoneWithoutProviderNetworkIsNotReady(t *testing.T) {
	c := newClient(t, zone())
	z := reconcileZone(t, c)
	c0 := meta.FindStatusCondition(z.Status.Conditions, pxv1.ConditionReady)
	if c0 == nil || c0.Status != metav1.ConditionFalse || c0.Reason != "ProviderNetworkNotFound" {
		t.Fatalf("Ready = %+v", c0)
	}
}

func TestZoneCollectsOrphansOnlyWhenNoSubnetUsesThem(t *testing.T) {
	stillUsed := &Subnet{ObjectMeta: metav1.ObjectMeta{Name: "subnet-used"}, Spec: SubnetSpec{Vlan: "subnet-used"}}
	c := newClient(t, zone(), readyProvider(), stillUsed,
		zoneVlan("subnet-gone", 101, tenantA, "subnet-gone"),
		zoneVlan("subnet-used", 102, tenantA, "subnet-used"),
	)
	z := reconcileZone(t, c)
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-gone"}, &Vlan{}); !apierrors.IsNotFound(err) {
		t.Fatalf("an unreferenced orphan must be collected: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-used"}, &Vlan{}); err != nil {
		t.Fatalf("an orphan a subnet still uses must stay: %v", err)
	}
	if z.Status.AllocatedVLANs != 1 {
		t.Fatalf("allocated = %d, want 1", z.Status.AllocatedVLANs)
	}
}

func TestZoneReportsVLANsStrandedOutsideTheRange(t *testing.T) {
	c := newClient(t, zone(func(z *pxv1.ProxmoxNetworkZone) { z.Spec.VLANRanges = []string{"101-102"} }), readyProvider(),
		network(tenantA, "subnet-a"), zoneVlan("subnet-a", 150, tenantA, "subnet-a"))
	z := reconcileZone(t, c)
	c0 := meta.FindStatusCondition(z.Status.Conditions, pxv1.ConditionReady)
	if c0 == nil || c0.Status != metav1.ConditionTrue || !strings.Contains(c0.Message, "150 (subnet-a)") {
		t.Fatalf("Ready = %+v", c0)
	}
}

func reconcileZoneResult(t *testing.T, c client.Client) (ctrl.Result, *pxv1.ProxmoxNetworkZone) {
	t.Helper()
	r := &ZoneReconciler{Client: c, APIReader: c}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: zoneName}})
	if err != nil {
		t.Fatal(err)
	}
	z := &pxv1.ProxmoxNetworkZone{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: zoneName}, z); err != nil {
		t.Fatal(err)
	}
	return res, z
}

// orphanListing is a zone Vlan whose network is gone, with names Kube-OVN
// lists in its status.
func orphanListing(name string, id int, listed ...string) *Vlan {
	v := zoneVlan(name, id, tenantA, name)
	v.Status.Subnets = listed
	return v
}

func vlanExists(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Name: name}, &Vlan{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestZoneOrphanWithADeletedListedSubnetIsCollectedAfterThePruneTimeout(t *testing.T) {
	// The Subnet is gone but Kube-OVN still lists it, as after a lost delete
	// event. The name holds the orphan only for the prune timeout, as it would
	// hold a deleting network's Vlan; it is not counted as held for good.
	c := newClient(t, zone(), readyProvider(), orphanListing("subnet-gone", 101, "subnet-gone"))
	res, z := reconcileZoneResult(t, c)
	if !vlanExists(t, c, "subnet-gone") {
		t.Fatal("the orphan must stay while Kube-OVN may still be deleting the switch")
	}
	if z.Status.AllocatedVLANs != 1 || !strings.Contains(meta.FindStatusCondition(z.Status.Conditions, pxv1.ConditionReady).Message, "1 orphaned Vlan(s)") {
		t.Fatalf("a held orphan must be counted: %+v", z.Status)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > DefaultSubnetPruneTimeout {
		t.Fatalf("RequeueAfter = %v, want the rest of the prune timeout", res.RequeueAfter)
	}
	if timers := pruneTimers(t, c, "subnet-gone"); len(timers) != 1 || timers["subnet-gone"].IsZero() {
		t.Fatalf("prune timers = %v, want one for subnet-gone", timers)
	}
	timed := &Vlan{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "subnet-gone"}, timed); err != nil {
		t.Fatal(err)
	}
	reconcileZoneResult(t, c)
	wantUnwritten(t, c, timed)

	backdatePruneTimers(t, c, "subnet-gone", DefaultSubnetPruneTimeout+time.Minute)
	_, z = reconcileZoneResult(t, c)
	if vlanExists(t, c, "subnet-gone") {
		t.Fatal("an orphan held only by a deleted subnet must be collected after the prune timeout")
	}
	if z.Status.AllocatedVLANs != 0 {
		t.Fatalf("allocated = %d, want 0", z.Status.AllocatedVLANs)
	}
}

func TestZoneOrphanUsesTheConfiguredPruneTimeout(t *testing.T) {
	c := newClient(t, zone(), readyProvider(), orphanListing("subnet-gone", 101, "subnet-gone"))
	reconcileZoneResult(t, c)
	backdatePruneTimers(t, c, "subnet-gone", DefaultSubnetPruneTimeout+time.Minute)
	r := &ZoneReconciler{Client: c, APIReader: c, SubnetPruneTimeout: time.Hour}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: zoneName}}); err != nil {
		t.Fatal(err)
	}
	if !vlanExists(t, c, "subnet-gone") {
		t.Fatal("the orphan must stay until the configured prune timeout runs out, not the default one")
	}
	reconcileZoneResult(t, c)
	if vlanExists(t, c, "subnet-gone") {
		t.Fatal("the orphan must be collected once the default prune timeout has run out")
	}
}

func TestZoneOrphanIsCollectedOnceKubeOVNPrunes(t *testing.T) {
	c := newClient(t, zone(), readyProvider(), orphanListing("subnet-gone", 101, "subnet-gone"))
	reconcileZoneResult(t, c)
	if !vlanExists(t, c, "subnet-gone") {
		t.Fatal("the orphan must stay while Kube-OVN still lists the deleted subnet")
	}
	setVlanSubnets(t, c, "subnet-gone")
	reconcileZoneResult(t, c)
	if vlanExists(t, c, "subnet-gone") {
		t.Fatal("the orphan must be collected once Kube-OVN has pruned the subnet")
	}
}

func TestZoneOrphanWithALiveListedSubnetIsHeldPastThePruneTimeout(t *testing.T) {
	// A live subnet listed on the orphan holds it with no time limit, even when
	// its spec.vlan names another Vlan now, as on the network side.
	moved := &Subnet{ObjectMeta: metav1.ObjectMeta{Name: "moved-away"}, Spec: SubnetSpec{Vlan: "elsewhere"}}
	c := newClient(t, zone(), readyProvider(), moved, orphanListing("subnet-gone", 101, "subnet-gone", "moved-away"))
	res, _ := reconcileZoneResult(t, c)
	if res.RequeueAfter != resyncPeriod {
		t.Fatalf("RequeueAfter = %v; a live subnet holds the orphan with no deadline", res.RequeueAfter)
	}
	backdatePruneTimers(t, c, "subnet-gone", DefaultSubnetPruneTimeout+time.Minute)
	_, z := reconcileZoneResult(t, c)
	if !vlanExists(t, c, "subnet-gone") {
		t.Fatal("a live subnet listed on the orphan must hold it whatever the timeout")
	}
	if z.Status.AllocatedVLANs != 1 {
		t.Fatalf("allocated = %d, want 1", z.Status.AllocatedVLANs)
	}
	if timers := pruneTimers(t, c, "subnet-gone"); len(timers) != 1 || timers["subnet-gone"].IsZero() {
		t.Fatalf("prune timers = %v, want one for subnet-gone only", timers)
	}

	// Once the live subnet is deleted, its own prune gets a full timeout.
	deleteSubnet(t, c, "moved-away")
	res, _ = reconcileZoneResult(t, c)
	if !vlanExists(t, c, "subnet-gone") {
		t.Fatal("a subnet deleted just now must not inherit an expired prune timer")
	}
	if res.RequeueAfter <= DefaultSubnetPruneTimeout-time.Minute || res.RequeueAfter > DefaultSubnetPruneTimeout {
		t.Fatalf("RequeueAfter = %v, want about a full prune timeout", res.RequeueAfter)
	}
}

func TestZoneOrphanKeepsTheNetworksPruneTimers(t *testing.T) {
	// Someone removes the network's finalizer by hand while it waits for
	// Kube-OVN to prune its deleted subnet. The orphan collection goes on with
	// the timer the network started, rather than starting over.
	c := deletedWithListedSubnets(t, []string{"subnet-a"}, readyProvider())
	reconcileNet(t, c, tenantA, "subnet-a")
	wantCond(t, getNet(t, c, tenantA, "subnet-a"), pxv1.ConditionReady, metav1.ConditionFalse, "SwitchDeleting")
	backdatePruneTimers(t, c, "subnet-a", DefaultSubnetPruneTimeout-time.Minute)

	n := getNet(t, c, tenantA, "subnet-a")
	n.Finalizers = nil
	if err := c.Update(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantA, Name: "subnet-a"}, &pxv1.ProxmoxNetwork{}); !apierrors.IsNotFound(err) {
		t.Fatalf("network still exists: %v", err)
	}
	res, _ := reconcileZoneResult(t, c)
	if !vlanExists(t, c, "subnet-a") {
		t.Fatal("the orphan must stay until the network's prune timer runs out")
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > time.Minute {
		t.Fatalf("RequeueAfter = %v, want what is left of the network's timer", res.RequeueAfter)
	}
	backdatePruneTimers(t, c, "subnet-a", time.Minute)
	reconcileZoneResult(t, c)
	if vlanExists(t, c, "subnet-a") {
		t.Fatal("the orphan must be collected once the network's prune timer has run out")
	}
}
