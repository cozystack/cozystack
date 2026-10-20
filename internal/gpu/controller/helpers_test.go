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

package controller

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	cosiv1alpha1 "sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	ns     = "tenant-acme"
	rnL40S = "nvidia.com/L40S"
	rnH100 = "nvidia.com/H100"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func testScheme(withKubeVirt bool) *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = gpuv1alpha1.AddToScheme(s)
	_ = cozyv1alpha1.AddToScheme(s)
	_ = helmv2.AddToScheme(s)
	_ = cosiv1alpha1.AddToScheme(s)
	if withKubeVirt {
		for _, gvk := range []struct{ kind string }{{vmiGVK.Kind}, {vmGVK.Kind}, {kubeVirtGVK.Kind}} {
			gv := vmiGVK.GroupVersion()
			s.AddKnownTypeWithName(gv.WithKind(gvk.kind), &unstructured.Unstructured{})
			s.AddKnownTypeWithName(gv.WithKind(gvk.kind+"List"), &unstructured.UnstructuredList{})
		}
	}
	return s
}

type env struct {
	t   *testing.T
	c   client.Client
	r   *Reconciler
	now time.Time
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	return newEnvWith(t, true, objs...)
}

func newEnvWith(t *testing.T, enabled bool, objs ...client.Object) *env {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(enabled)).
		WithObjects(objs...).
		WithStatusSubresource(&gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUClaim{}, &gpuv1alpha1.GPUNode{}).
		Build()
	e := &env{t: t, c: c, now: t0}
	e.r = &Reconciler{Client: c, Reader: c, Now: func() time.Time { return e.now }, Config: gpu.Config{
		Enabled:           enabled,
		LostCapacityGrace: 5 * time.Minute,
		AttachmentGrace:   2 * time.Minute,
	}}
	return e
}

// pass runs the reconciler as its watches would after the objects
// changed: again after each pass, since a pass's own writes (a deleted
// VMI, a detached claim) are events that trigger the next one.
func (e *env) pass() {
	e.t.Helper()
	for i := 0; i < 4; i++ {
		if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: passKey}); err != nil {
			e.t.Fatalf("pass: %v", err)
		}
	}
}

func (e *env) claim(name string) *gpuv1alpha1.GPUClaim {
	e.t.Helper()
	d := &gpuv1alpha1.GPUClaim{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, d); err != nil {
		e.t.Fatalf("get GPUClaim %s: %v", name, err)
	}
	return d
}

func (e *env) gone(obj client.Object) bool {
	e.t.Helper()
	err := e.c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)
	if err == nil {
		return false
	}
	if client.IgnoreNotFound(err) != nil {
		e.t.Fatalf("get %s: %v", obj.GetName(), err)
	}
	return true
}

func (e *env) class(name string) *gpuv1alpha1.GPUClass {
	e.t.Helper()
	c := &gpuv1alpha1.GPUClass{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Name: name}, c); err != nil {
		e.t.Fatalf("get GPUClass %s: %v", name, err)
	}
	return c
}

func (e *env) update(obj client.Object) {
	e.t.Helper()
	if err := e.c.Update(context.Background(), obj); err != nil {
		e.t.Fatalf("update %s: %v", obj.GetName(), err)
	}
}

func (e *env) updateStatus(obj client.Object) {
	e.t.Helper()
	if err := e.c.Status().Update(context.Background(), obj); err != nil {
		e.t.Fatalf("update status %s: %v", obj.GetName(), err)
	}
}

func (e *env) create(obj client.Object) {
	e.t.Helper()
	if err := e.c.Create(context.Background(), obj); err != nil {
		e.t.Fatalf("create %s: %v", obj.GetName(), err)
	}
}

func (e *env) delete(obj client.Object) {
	e.t.Helper()
	if err := e.c.Delete(context.Background(), obj); err != nil {
		e.t.Fatalf("delete %s: %v", obj.GetName(), err)
	}
}

type nodeOpt func(*corev1.Node)

func node(name string, alloc map[string]int64, opts ...nodeOpt) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{},
			Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(t0.Add(-time.Hour)),
			}},
		},
	}
	for k, v := range alloc {
		n.Status.Allocatable[corev1.ResourceName(k)] = *resource.NewQuantity(v, resource.DecimalSI)
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

func cordoned(n *corev1.Node) { n.Spec.Unschedulable = true }

func notReadySince(at time.Time) nodeOpt {
	return func(n *corev1.Node) {
		n.Status.Conditions[0].Status = corev1.ConditionFalse
		n.Status.Conditions[0].LastTransitionTime = metav1.NewTime(at)
	}
}

func labelled(k, v string) nodeOpt { return func(n *corev1.Node) { n.Labels[k] = v } }

func tainted(k string, effect corev1.TaintEffect) nodeOpt {
	return func(n *corev1.Node) { n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: k, Effect: effect}) }
}

type classOpt func(*gpuv1alpha1.GPUClass)

func class(name, resourceName string, opts ...classOpt) *gpuv1alpha1.GPUClass {
	c := &gpuv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Finalizers: []string{gpu.FinalizerClass}},
		Spec: gpuv1alpha1.GPUClassSpec{
			Vendor: "nvidia", Model: name, Mode: gpuv1alpha1.GPUModePassthrough, OnCordon: gpuv1alpha1.OnCordonMove,
			Source: gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: resourceName}},
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func stay(c *gpuv1alpha1.GPUClass) { c.Spec.OnCordon = gpuv1alpha1.OnCordonStay }

func vgpu(c *gpuv1alpha1.GPUClass) {
	c.Spec.Mode = gpuv1alpha1.GPUModeVGPU
	c.Spec.Profile = "L40S-24C"
}

func selecting(k, v string) classOpt {
	return func(c *gpuv1alpha1.GPUClass) { c.Spec.NodeSelector = map[string]string{k: v} }
}

type claimOpt func(*gpuv1alpha1.GPUClaim)

var created = t0.Add(-time.Hour)

func claim(name, className string, count int32, opts ...claimOpt) *gpuv1alpha1.GPUClaim {
	d := &gpuv1alpha1.GPUClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Finalizers: []string{gpu.FinalizerClaim},
			CreationTimestamp: metav1.NewTime(created), UID: types.UID("uid-" + name)},
		Spec: gpuv1alpha1.GPUClaimSpec{ClassName: className, Count: count},
	}
	for _, o := range opts {
		o(d)
	}
	created = created.Add(time.Second)
	return d
}

func bound(node string) claimOpt {
	return func(d *gpuv1alpha1.GPUClaim) {
		d.Status.Phase = gpuv1alpha1.GPUClaimBound
		d.Status.NodeName = node
	}
}

func lost(node string) claimOpt {
	return func(d *gpuv1alpha1.GPUClaim) {
		d.Status.Phase = gpuv1alpha1.GPUClaimLost
		d.Status.NodeName = node
	}
}

func attached(vmi string) claimOpt {
	return func(d *gpuv1alpha1.GPUClaim) {
		d.Status.AttachedTo = &gpuv1alpha1.GPUClaimAttachment{VMI: vmi}
		if d.Status.Phase == gpuv1alpha1.GPUClaimBound {
			d.Status.Phase = gpuv1alpha1.GPUClaimAttached
		}
	}
}

// template marks the claim as the controller creates it for a
// claimTemplate entry of vmi: labelled and owned by the VMI.
func template(vmi string) claimOpt {
	return func(d *gpuv1alpha1.GPUClaim) {
		d.Labels = map[string]string{gpu.LabelTemplateClaim: gpu.LabelValueTrue}
		d.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance",
			Name: vmi, UID: types.UID("vmi-uid-" + vmi)}}
		d.Status.Phase = gpuv1alpha1.GPUClaimPending
	}
}

// recorded is the attachment the VMI webhook writes at admission, before
// the VMI exists: the phase stays as the binder left it.
func recorded(vmi string) claimOpt {
	return func(d *gpuv1alpha1.GPUClaim) { d.Status.AttachedTo = &gpuv1alpha1.GPUClaimAttachment{VMI: vmi} }
}

func inNamespace(namespace string) claimOpt {
	return func(d *gpuv1alpha1.GPUClaim) { d.Namespace = namespace }
}

func vmiObj(name, phase, nodeName, ownerVM string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(vmiGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetUID(types.UID("vmi-uid-" + name))
	if ownerVM != "" {
		u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Name: ownerVM,
			UID: types.UID("vm-uid-" + ownerVM), Controller: ptr.To(true)}})
	}
	_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	if nodeName != "" {
		_ = unstructured.SetNestedField(u.Object, nodeName, "status", "nodeName")
	}
	// KubeVirt lists the launcher pod launcherPod("virt-launcher-<name>") makes.
	_ = unstructured.SetNestedMap(u.Object, map[string]any{string(podUID("virt-launcher-" + name)): nodeName}, "status", "activePods")
	return u
}

func podUID(name string) types.UID { return types.UID("pod-uid-" + name) }

func vmObj(name, runStrategy string, claims ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(vmGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, runStrategy, "spec", "runStrategy")
	if len(claims) > 0 {
		refs := make([]map[string]string, 0, len(claims))
		for _, d := range claims {
			refs = append(refs, map[string]string{"claim": d})
		}
		b, _ := json.Marshal(refs)
		_ = unstructured.SetNestedField(u.Object, string(b), "spec", "template", "metadata", "annotations", gpu.AnnotationClaims)
	}
	return u
}

func kubeVirt(permitted ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(kubeVirtGVK)
	u.SetNamespace("cozy-kubevirt")
	u.SetName("kubevirt")
	entries := make([]any, 0, len(permitted))
	for _, p := range permitted {
		entries = append(entries, map[string]any{"resourceName": p, "pciVendorSelector": "10DE:26B9"})
	}
	_ = unstructured.SetNestedSlice(u.Object, entries, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	return u
}

func launcherPod(name, vmi, nodeName string, units map[string]int64) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: podUID(name)},
		Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{
			Name: "compute", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{}, Requests: corev1.ResourceList{}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if vmi != "" {
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmi,
			UID: types.UID("vmi-uid-" + vmi), Controller: ptr.To(true)}}
	}
	for k, v := range units {
		q := *resource.NewQuantity(v, resource.DecimalSI)
		p.Spec.Containers[0].Resources.Limits[corev1.ResourceName(k)] = q
		p.Spec.Containers[0].Resources.Requests[corev1.ResourceName(k)] = q
	}
	return p
}

// tenant is a tenant HelmRelease in tenant-root that owns namespace
// tenant-<name>, declaring quotas.
func tenant(name string, quotas map[string]string) *helmv2.HelmRelease {
	b, _ := json.Marshal(map[string]any{"resourceQuotas": quotas})
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-" + name, Namespace: "tenant-root",
			Labels: map[string]string{appsv1alpha1.ApplicationKindLabel: tenantKind}},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: b}},
	}
}

func namespace(name string, vgpuEntitled bool) *corev1.Namespace {
	n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	if vgpuEntitled {
		n.Labels[gpu.LabelNamespaceVGPU] = gpu.LabelValueTrue
	}
	return n
}

func waiting(d *gpuv1alpha1.GPUClaim) *metav1.Condition {
	return meta.FindStatusCondition(d.Status.Conditions, ConditionWaiting)
}

func expectPlacement(t *testing.T, d *gpuv1alpha1.GPUClaim, phase gpuv1alpha1.GPUClaimPhase, node string) {
	t.Helper()
	if d.Status.Phase != phase || d.Status.NodeName != node {
		t.Fatalf("GPUClaim %s: got %s on %q, want %s on %q (conditions %+v)",
			d.Name, d.Status.Phase, d.Status.NodeName, phase, node, d.Status.Conditions)
	}
}

// refs writes the VMI webhook's claim references onto a VMI: each argument
// is a claim name, or "class:count" for a claimTemplate entry.
func refs(u *unstructured.Unstructured, entries ...string) *unstructured.Unstructured {
	var out []model.ClaimRef
	for _, e := range entries {
		class, count, ok := strings.Cut(e, ":")
		if !ok {
			out = append(out, model.ClaimRef{Claim: e})
			continue
		}
		n, _ := strconv.Atoi(count)
		out = append(out, model.ClaimRef{ClaimTemplate: &model.ClaimTemplate{ClassName: class, Count: int32(n)}})
	}
	a, _ := model.FormatClaimRefs(out)
	ann := u.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[gpu.AnnotationClaims] = a
	u.SetAnnotations(ann)
	return u
}

// gatedPod is the virt-launcher pod of vmi as the pod webhook admits it:
// labelled, gated, and, like most launcher pods, with the required term
// KubeVirt adds for host-model CPUs.
func gatedPod(vmi string) *corev1.Pod {
	p := launcherPod("virt-launcher-"+vmi, vmi, "", nil)
	p.Labels = map[string]string{gpu.LabelClaimReference: gpu.LabelValueTrue, "kubevirt.io": "virt-launcher"}
	p.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: gpu.SchedulingGateClaims}}
	p.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "host-model-cpu.node.kubevirt.io/x", Operator: corev1.NodeSelectorOpDoesNotExist}},
		}}},
	}}
	p.Status.Phase = corev1.PodPending
	p.CreationTimestamp = metav1.NewTime(created)
	created = created.Add(time.Second)
	return p
}

func (e *env) pod(name string) *corev1.Pod {
	e.t.Helper()
	p := &corev1.Pod{}
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, p); err != nil {
		e.t.Fatalf("get pod %s: %v", name, err)
	}
	return p
}

// expectReleased checks that the pod left the gate pinned to node, with the
// pin appended to its existing required term.
func expectReleased(t *testing.T, p *corev1.Pod, node string) {
	t.Helper()
	if gated(p) {
		t.Fatalf("pod %s is still gated", p.Name)
	}
	if got := pinnedNode(p); got != node {
		t.Fatalf("pod %s pinned to %q, want %q", p.Name, got, node)
	}
}

func expectGated(t *testing.T, p *corev1.Pod) {
	t.Helper()
	if !gated(p) || pinnedNode(p) != "" {
		t.Fatalf("pod %s: gated %t, pinned to %q; want gated and unpinned", p.Name, gated(p), pinnedNode(p))
	}
}

// pinnedPod is the launcher pod of vmi after the controller released it
// onto node, before the scheduler placed it.
func pinnedPod(vmi, node string) *corev1.Pod {
	p := gatedPod(vmi)
	p.Spec.SchedulingGates = nil
	terms := p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	terms[0].MatchFields = []corev1.NodeSelectorRequirement{{Key: nodeNameField, Operator: corev1.NodeSelectorOpIn, Values: []string{node}}}
	return p
}
