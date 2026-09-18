/*
Copyright 2026 The Cozystack Authors.

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

package endpointattachment

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

const (
	vpcName = "virtualprivatecloud-net"
	vpcIP   = "10.10.0.4"
)

// newCozyplaneClient builds a client on a cluster that serves cozyplane's
// FloatingIP.
func newCozyplaneClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(floatingIPGVK, meta.RESTScopeNamespace)
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithRESTMapper(m).
		WithStatusSubresource(&cozyv1alpha1.EndpointAttachment{}).
		WithObjects(objs...).
		Build()
}

// launcherPod is a VM's launcher pod. A non-empty vpc models the cozyplane
// flavour of the VM chart, which asks for a VPC attachment by annotation; ip
// is what cozyplane's CNI stamps back once it has attached the pod.
func launcherPod(name, vpc, ip string, annotations map[string]string) *corev1.Pod {
	ann := map[string]string{}
	for k, v := range annotations {
		ann[k] = v
	}
	if vpc != "" {
		ann[vpcAnnotation] = vpc
	}
	if ip != "" {
		ann[vpcIPAnnotation] = ip
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("pod-" + name), Annotations: ann}}
}

func wholeIPVM(method cozyv1alpha1.ExposureMethod) (*cozyv1alpha1.EndpointAttachment, []client.Object) {
	lb := cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: method}
	if method == cozyv1alpha1.ExposureMethodPortList {
		lb.Ports = []int32{22}
	}
	ea := attachment("box-ip", "ea-1", "VMInstance", "box", "vm-instance-box", lb)
	return ea, []client.Object{
		helmRelease("vm-instance-box", "hr-1", "VMInstance", "box"),
		endpoint("vm-instance-box", "VMInstance", "box", map[string]string{"vm": "box"}, SentinelPort),
		claimObject("held", "public", "IPv4", true, "203.0.113.7"),
		classObject("public", ""),
		ea,
	}
}

func ownedFloatingIPs(t *testing.T, c client.Client, ea *cozyv1alpha1.EndpointAttachment) []unstructured.Unstructured {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(floatingIPGVK.GroupVersion().WithKind("FloatingIPList"))
	if err := c.List(context.TODO(), list, client.InNamespace(ns)); err != nil {
		t.Fatalf("list FloatingIPs: %v", err)
	}
	var out []unstructured.Unstructured
	for _, u := range list.Items {
		if isControlledBy(&u, ea) {
			out = append(out, u)
		}
	}
	return out
}

func onlyFloatingIP(t *testing.T, c client.Client, ea *cozyv1alpha1.EndpointAttachment) *unstructured.Unstructured {
	t.Helper()
	got := ownedFloatingIPs(t, c, ea)
	if len(got) != 1 {
		t.Fatalf("expected exactly one owned FloatingIP, got %d", len(got))
	}
	return &got[0]
}

// existingFloatingIP is a FloatingIP the attachment created on an earlier
// reconcile.
func existingFloatingIP(ea *cozyv1alpha1.EndpointAttachment, target string) *unstructured.Unstructured {
	u := newFloatingIP()
	u.SetNamespace(ns)
	u.SetName("box-ip-fip01")
	u.SetUID("fip-1")
	u.SetOwnerReferences([]metav1.OwnerReference{controllerOwnerReference(ea)})
	u.Object["spec"] = floatingIPSpec(vpcTarget{VPC: vpcName, IP: target}, "held", "")
	return u
}

func setFloatingIPStatus(t *testing.T, c client.Client, fip *unstructured.Unstructured, phase, address string, conds ...map[string]interface{}) {
	t.Helper()
	live := newFloatingIP()
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(fip), live); err != nil {
		t.Fatalf("get FloatingIP: %v", err)
	}
	status := map[string]interface{}{"phase": phase}
	if address != "" {
		status["address"] = address
	}
	var list []interface{}
	for _, c := range conds {
		list = append(list, c)
	}
	if list != nil {
		status["conditions"] = list
	}
	live.Object["status"] = status
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("set FloatingIP status: %v", err)
	}
}

// TestVPCWholeIPBindsFloatingIP is the cozyplane path: a whole-IP backend in a
// VPC gets a FloatingIP on its VPC address wearing the attachment's claim,
// and no cozy-proxy Service, whatever the cozy-proxy declaration says.
func TestVPCWholeIPBindsFloatingIP(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newCozyplaneClient(t, append(objs,
		launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)

	got := run(t, c, &Reconciler{CozyProxyContractImplemented: false}, ea)
	if n := len(renderedServices(t, c, ea)); n != 0 {
		t.Fatalf("a VPC backend must not get a cozy-proxy Service, got %d", n)
	}
	fip := onlyFloatingIP(t, c, ea)
	spec, _, _ := unstructured.NestedMap(fip.Object, "spec")
	if v, _, _ := unstructured.NestedString(spec, "vpcRef", "name"); v != vpcName {
		t.Fatalf("vpcRef = %q, want %q", v, vpcName)
	}
	if v, _, _ := unstructured.NestedString(spec, "target"); v != vpcIP {
		t.Fatalf("target = %q, want %q", v, vpcIP)
	}
	if v, _, _ := unstructured.NestedString(spec, "addressClaimName"); v != "held" {
		t.Fatalf("addressClaimName = %q, want the attachment's claim", v)
	}
	if fip.GetLabels()[OwnerLabel] != ea.Name {
		t.Fatalf("ownership label = %v", fip.GetLabels())
	}
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonFloatingIPNotReady)

	setFloatingIPStatus(t, c, fip, "Pending", "", map[string]interface{}{
		"type": "TargetExclusive", "status": "False", "reason": "Contended", "message": "another FloatingIP binds the target",
	})
	got = run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonFloatingIPNotReady)
	if msg := meta.FindStatusCondition(got.Status.Conditions, cozyv1alpha1.ConditionProvisioned).Message; !strings.Contains(msg, "TargetExclusive") {
		t.Fatalf("the FloatingIP's failing condition must reach the attachment, got %q", msg)
	}

	setFloatingIPStatus(t, c, fip, "Ready", "203.0.113.7")
	got = run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentAttached {
		t.Fatalf("phase = %s, want Attached", got.Status.Phase)
	}
	if got.Status.ServiceName != "" || len(got.Status.Addresses) != 1 || got.Status.Addresses[0] != "203.0.113.7" {
		t.Fatalf("status = %+v", got.Status)
	}
}

// TestVPCClaimWornByOwnFloatingIP: once cozyplane associates the claim with
// the Service its FloatingIP owns, the attachment must not read that as the
// claim being in use elsewhere.
func TestVPCClaimWornByOwnFloatingIP(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	fip := existingFloatingIP(ea, vpcIP)
	fipSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "box-ip-fip01-abcde", UID: "fip-svc",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "sdn.cozystack.io/v1alpha1", Kind: "FloatingIP", Name: fip.GetName(), UID: fip.GetUID(), Controller: new(true)}}}}
	c := newCozyplaneClient(t, append(objs, fip, fipSvc,
		addressObject("203.0.113.7", &types.NamespacedName{Namespace: ns, Name: fipSvc.Name}),
		launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	setFloatingIPStatus(t, c, fip, "Ready", "203.0.113.7")

	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)
}

// TestVPCReplacesRenderedService: a VM that joins a VPC after its
// attachment rendered a cozy-proxy Service gets the Service withdrawn before
// the FloatingIP wears the claim.
func TestVPCReplacesRenderedService(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newCozyplaneClient(t, append(objs,
		launcherPod("virt-launcher-box", "", "", nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	r := &Reconciler{CozyProxyContractImplemented: true}
	run(t, c, r, ea)
	old := onlyRendered(t, c, ea)

	pod := &corev1.Pod{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "virt-launcher-box"}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Annotations = map[string]string{vpcAnnotation: vpcName, vpcIPAnnotation: vpcIP}
	if err := c.Update(context.TODO(), pod); err != nil {
		t.Fatal(err)
	}
	run(t, c, r, ea)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(old), &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the cozy-proxy Service must be withdrawn, got %v", err)
	}
	onlyFloatingIP(t, c, ea)
}

// TestVPCLeftBehindFallsBackToService: a VM no longer in a VPC loses its
// FloatingIP and gets the cozy-proxy Service again.
func TestVPCLeftBehindFallsBackToService(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newCozyplaneClient(t, append(objs, existingFloatingIP(ea, vpcIP),
		launcherPod("virt-launcher-box", "", "", nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
	if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
		t.Fatalf("FloatingIP must be withdrawn, %d left", n)
	}
	onlyRendered(t, c, ea)
}

// TestKubeOVNBackendKeepsCozyProxyService: on the kube-ovn flavour the VM
// joins a VPC through a secondary Multus NIC and keeps its pod-network NIC,
// so the pod carries no cozyplane annotation and the cozy-proxy Service,
// which maps to that NIC's address, stays the path.
func TestKubeOVNBackendKeepsCozyProxyService(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	multus := map[string]string{"k8s.v1.cni.cncf.io/networks": `[{"name":"subnet-84dbec17","namespace":"tenant-foo"}]`}
	c := newCozyplaneClient(t, append(objs,
		launcherPod("virt-launcher-box", "", "", multus),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
	svc := onlyRendered(t, c, ea)
	if svc.Labels[ServiceProxyNameLabel] != ServiceProxyName || svc.Annotations[WholeIPAnnotation] != "true" {
		t.Fatalf("expected the cozy-proxy whole-IP Service, got labels=%v annotations=%v", svc.Labels, svc.Annotations)
	}
	if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
		t.Fatalf("no FloatingIP on a kube-ovn backend, got %d", n)
	}
}

func TestVPCRefusals(t *testing.T) {
	tests := []struct {
		name       string
		method     cozyv1alpha1.ExposureMethod
		pods       []*corev1.Pod
		served     bool
		withFIP    bool
		wantReason string
	}{
		{name: "FloatingIP not served", method: cozyv1alpha1.ExposureMethodWholeIP, served: false,
			pods: []*corev1.Pod{launcherPod("virt-launcher-box", vpcName, vpcIP, nil)}, wantReason: cozyv1alpha1.ReasonDatapathUnavailable},
		{name: "PortList", method: cozyv1alpha1.ExposureMethodPortList, served: true, withFIP: true,
			pods: []*corev1.Pod{launcherPod("virt-launcher-box", vpcName, vpcIP, nil)}, wantReason: cozyv1alpha1.ReasonMethodUnsupportedInVPC},
		{name: "VPC in another namespace", method: cozyv1alpha1.ExposureMethodWholeIP, served: true, withFIP: true,
			pods: []*corev1.Pod{launcherPod("virt-launcher-box", "tenant-parent/"+vpcName, vpcIP, nil)}, wantReason: cozyv1alpha1.ReasonVPCNotInNamespace},
		{name: "two VPC addresses", method: cozyv1alpha1.ExposureMethodWholeIP, served: true, withFIP: true,
			pods:       []*corev1.Pod{launcherPod("virt-launcher-a", vpcName, "10.10.0.4", nil), launcherPod("virt-launcher-b", vpcName, "10.10.0.5", nil)},
			wantReason: cozyv1alpha1.ReasonMultipleBackends},
		{name: "inside and outside a VPC", method: cozyv1alpha1.ExposureMethodWholeIP, served: true, withFIP: true,
			pods:       []*corev1.Pod{launcherPod("virt-launcher-a", vpcName, vpcIP, nil), launcherPod("virt-launcher-b", "", "", nil)},
			wantReason: cozyv1alpha1.ReasonMultipleBackends},
		{name: "VPC address not stamped yet", method: cozyv1alpha1.ExposureMethodWholeIP, served: true,
			pods: []*corev1.Pod{launcherPod("virt-launcher-box", vpcName, "", nil)}, wantReason: cozyv1alpha1.ReasonWaitingForVPCAddress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ea, objs := wholeIPVM(tt.method)
			var names []string
			for _, p := range tt.pods {
				objs = append(objs, p)
				names = append(names, p.Name)
			}
			objs = append(objs, endpointSlice("vm-instance-box-1", "vm-instance-box", true, names...))
			if tt.withFIP {
				objs = append(objs, existingFloatingIP(ea, vpcIP))
			}
			c := newClient(t, objs...)
			if tt.served {
				c = newCozyplaneClient(t, objs...)
			}
			got := run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
			expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, tt.wantReason)
			if n := len(renderedServices(t, c, ea)); n != 0 {
				t.Fatalf("no cozy-proxy Service may be rendered for a VPC backend, got %d", n)
			}
			if tt.served {
				if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
					t.Fatalf("no FloatingIP may stand on a refusal, got %d", n)
				}
			}
		})
	}
}

// TestVPCLiveMigrationKeepsOneBinding: during a live migration the source
// and target launcher pods share the VM's VPC address, which is one target,
// not two backends.
func TestVPCLiveMigrationKeepsOneBinding(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newCozyplaneClient(t, append(objs,
		launcherPod("virt-launcher-src", vpcName, vpcIP, nil),
		launcherPod("virt-launcher-dst", vpcName, vpcIP, nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-src", "virt-launcher-dst"))...)
	run(t, c, nil, ea)
	fip := onlyFloatingIP(t, c, ea)
	if v, _, _ := unstructured.NestedString(fip.Object, "spec", "target"); v != vpcIP {
		t.Fatalf("target = %q", v)
	}
}

// TestVPCStoppedVMKeepsBinding: a stopped VM has no launcher pod, so nothing
// says where it is; the FloatingIP it already has stays instead of flipping
// to a cozy-proxy Service until the VM starts again.
func TestVPCStoppedVMKeepsBinding(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	fip := existingFloatingIP(ea, vpcIP)
	c := newCozyplaneClient(t, append(objs, fip)...)
	got := run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
	if n := len(renderedServices(t, c, ea)); n != 0 {
		t.Fatalf("a stopped VPC VM must not get a cozy-proxy Service, got %d", n)
	}
	onlyFloatingIP(t, c, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonFloatingIPNotReady)
}

// TestVPCTargetFollowsTheVM: a new VPC address re-points the FloatingIP in
// place.
func TestVPCTargetFollowsTheVM(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	c := newCozyplaneClient(t, append(objs, existingFloatingIP(ea, "10.10.0.9"),
		launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	run(t, c, nil, ea)
	fip := onlyFloatingIP(t, c, ea)
	if fip.GetName() != "box-ip-fip01" {
		t.Fatalf("the FloatingIP must be updated in place, got %q", fip.GetName())
	}
	if v, _, _ := unstructured.NestedString(fip.Object, "spec", "target"); v != vpcIP {
		t.Fatalf("target = %q, want %q", v, vpcIP)
	}
}

// TestVPCDetachWithdrawsFloatingIP: the endpoint going away withdraws the
// binding and keeps the claim, as on the Service path.
func TestVPCDetachWithdrawsFloatingIP(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	var kept []client.Object
	for _, o := range objs {
		if _, isSvc := o.(*corev1.Service); !isSvc {
			kept = append(kept, o)
		}
	}
	c := newCozyplaneClient(t, append(kept, existingFloatingIP(ea, vpcIP))...)
	got := run(t, c, nil, ea)
	if n := len(ownedFloatingIPs(t, c, ea)); n != 0 {
		t.Fatalf("FloatingIP must be withdrawn on detach, %d left", n)
	}
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentDetached {
		t.Fatalf("phase = %s, want Detached", got.Status.Phase)
	}
}

// TestVPCFloatingIPNamesTheApiserverAccepts: cozyplane names the Service it
// owns after the FloatingIP plus "-", so the FloatingIP name must leave room
// for that inside a DNS-1035 label.
func TestVPCFloatingIPNamesTheApiserverAccepts(t *testing.T) {
	long := strings.Repeat("a", 253)
	lb := cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodWholeIP}
	ea := attachment(long, "ea-1", "VMInstance", "box", "vm-instance-box", lb)
	_, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	objs[len(objs)-1] = ea
	c := newCozyplaneClient(t, append(objs,
		launcherPod("virt-launcher-box", vpcName, vpcIP, nil),
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"))...)
	run(t, c, nil, ea)
	fip := onlyFloatingIP(t, c, ea)
	if errs := apivalidation.NameIsDNS1035Label(fip.GetName()+"-", true); len(errs) > 0 {
		t.Fatalf("cozyplane's Service generateName %q would be refused: %v", fip.GetName()+"-", errs)
	}
}

// TestTerminatingLauncherIsNotABackend: the source pod of a finished
// migration is terminating and no longer a binding target.
func TestTerminatingLauncherIsNotABackend(t *testing.T) {
	ea, objs := wholeIPVM(cozyv1alpha1.ExposureMethodWholeIP)
	slice := endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-old", "virt-launcher-new")
	slice.Endpoints[0].Conditions.Terminating = new(true)
	c := newCozyplaneClient(t, append(objs, slice,
		launcherPod("virt-launcher-old", vpcName, "10.10.0.9", nil),
		launcherPod("virt-launcher-new", vpcName, vpcIP, nil))...)
	run(t, c, nil, ea)
	fip := onlyFloatingIP(t, c, ea)
	if v, _, _ := unstructured.NestedString(fip.Object, "spec", "target"); v != vpcIP {
		t.Fatalf("target = %q, want the non-terminating pod's %q", v, vpcIP)
	}
}
