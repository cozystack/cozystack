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

package webhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	tenantNS = "tenant-acme"
	h100RN   = "nvidia.com/GH100_H100"
	l40sRN   = "nvidia.com/NVIDIA_L40S-24C"
	a40RN    = "nvidia.com/NVIDIA_A40-16Q"
	nodeA    = "gpu-a"
	nodeB    = "gpu-b"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, gpuv1alpha1.AddToScheme, helmv2.AddToScheme, apiextensionsv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	s.AddKnownTypeWithName(kubeVirtGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vmiGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vmiGVK.GroupVersion().WithKind(vmiGVK.Kind+"List"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(kubeVirtGVK.GroupVersion().WithKind(kubeVirtGVK.Kind+"List"), &unstructured.UnstructuredList{})
	return s
}

// kubeVirt is the KubeVirt CR; no names leaves permittedHostDevices unset.
func kubeVirt(names ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(kubeVirtGVK)
	u.SetNamespace(model.KubeVirtNamespace)
	u.SetName(model.KubeVirtName)
	if names != nil {
		var pci []any
		for _, n := range names {
			pci = append(pci, map[string]any{"pciVendorSelector": "10DE:2330", "resourceName": n})
		}
		_ = unstructured.SetNestedSlice(u.Object, pci, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	}
	return u
}

func newClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&gpuv1alpha1.GPUClaim{}, &gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUNode{}).
		Build()
}

func newHandler(c client.Client) *Handler {
	return &Handler{Client: c, Reader: c}
}

type classOpt func(*gpuv1alpha1.GPUClass)

func class(name string, mode gpuv1alpha1.GPUMode, rn string, opts ...classOpt) *gpuv1alpha1.GPUClass {
	c := &gpuv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gpuv1alpha1.GPUClassSpec{
			Vendor: "nvidia", Model: name, Mode: mode,
			NodeSelector: map[string]string{"gpu": name},
			Source:       gpuv1alpha1.GPUClassSource{DevicePlugin: &gpuv1alpha1.DevicePluginSource{ResourceName: rn}},
		},
	}
	if mode == gpuv1alpha1.GPUModeVGPU {
		c.Spec.Profile = "P"
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func maxCount(n int32) classOpt {
	return func(c *gpuv1alpha1.GPUClass) { c.Spec.MaxCount = ptr.To(n) }
}

func freeUnits(free, perNode int32) classOpt {
	return func(c *gpuv1alpha1.GPUClass) {
		c.Status.Free = ptr.To(free)
		c.Status.MaxFreePerNode = ptr.To(perNode)
	}
}

func tolerates(ts ...gpuv1alpha1.GPUClassToleration) classOpt {
	return func(c *gpuv1alpha1.GPUClass) { c.Spec.Tolerations = append(c.Spec.Tolerations, ts...) }
}

func deleting(o client.Object) {
	o.SetDeletionTimestamp(ptr.To(metav1.Now()))
	o.SetFinalizers(append(o.GetFinalizers(), "test/keep"))
}

func claim(name, className string, count int32, phase gpuv1alpha1.GPUClaimPhase, nodeName string) *gpuv1alpha1.GPUClaim {
	return &gpuv1alpha1.GPUClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantNS},
		Spec:       gpuv1alpha1.GPUClaimSpec{ClassName: className, Count: count},
		Status:     gpuv1alpha1.GPUClaimStatus{Phase: phase, NodeName: nodeName},
	}
}

func attached(d *gpuv1alpha1.GPUClaim, vmi string) *gpuv1alpha1.GPUClaim {
	d.Status.AttachedTo = &gpuv1alpha1.GPUClaimAttachment{VMI: vmi}
	return d
}

func node(name string, alloc map[string]int64) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{},
		},
	}
	for k, v := range alloc {
		n.Status.Allocatable[corev1.ResourceName(k)] = *resource.NewQuantity(v, resource.DecimalSI)
	}
	return n
}

func namespace(name string, entitled bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	if entitled {
		ns.Labels[gpu.LabelNamespaceVGPU] = gpu.LabelValueTrue
	}
	return ns
}

// tenantRelease is the HelmRelease of the tenant owning ns, carrying its
// declared resourceQuotas.
func tenantRelease(t *testing.T, ns string, quotas map[string]string) *helmv2.HelmRelease {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"resourceQuotas": quotas})
	if err != nil {
		t.Fatal(err)
	}
	hrName, hrNamespace, _ := model.TenantReleaseRef(ns)
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: hrName, Namespace: hrNamespace,
			Labels: map[string]string{appsv1alpha1.ApplicationKindLabel: "Tenant"}},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: raw}},
	}
}

func refsAnnotation(t *testing.T, refs ...model.ClaimRef) string {
	t.Helper()
	s, err := model.FormatClaimRefs(refs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func held(name string) model.ClaimRef { return model.ClaimRef{Claim: name} }

func tmpl(className string, count int32) model.ClaimRef {
	return model.ClaimRef{ClaimTemplate: &model.ClaimTemplate{ClassName: className, Count: count}}
}

// vmi returns a referencing VMI as virt-controller creates it from a VM.
func vmi(t *testing.T, name string, refs ...model.ClaimRef) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachineInstance",
		"metadata": map[string]any{
			"name":        name,
			"namespace":   tenantNS,
			"uid":         "vmi-uid-" + name,
			"labels":      map[string]any{gpu.LabelClaimReference: gpu.LabelValueTrue},
			"annotations": map[string]any{gpu.AnnotationClaims: refsAnnotation(t, refs...)},
		},
		"spec": map[string]any{
			"domain": map[string]any{
				"devices":   map[string]any{"disks": []any{map[string]any{"name": "root"}}},
				"resources": map[string]any{"requests": map[string]any{"memory": "1Gi"}},
			},
		},
	}}
	return u
}

func createRequest(t *testing.T, obj any, dryRun bool) admission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID:       "uid",
		Operation: admissionv1.Create,
		Namespace: tenantNS,
		Object:    runtime.RawExtension{Raw: raw},
		DryRun:    ptr.To(dryRun),
	}}
}

// applyPatch returns obj, marshalled, with the response's JSON patch applied.
func applyPatch(t *testing.T, obj any, resp admission.Response) []byte {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Patches) == 0 {
		return raw
	}
	ops, err := json.Marshal(resp.Patches)
	if err != nil {
		t.Fatal(err)
	}
	p, err := jsonpatch.DecodePatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Apply(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func patchedVMI(t *testing.T, u *unstructured.Unstructured, resp admission.Response) *unstructured.Unstructured {
	t.Helper()
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(applyPatch(t, u.Object, resp), &out.Object); err != nil {
		t.Fatal(err)
	}
	return out
}

func getClaim(t *testing.T, c client.Client, name string) *gpuv1alpha1.GPUClaim {
	t.Helper()
	d := &gpuv1alpha1.GPUClaim{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNS, Name: name}, d); err != nil {
		t.Fatalf("get GPUClaim %s: %v", name, err)
	}
	return d
}

func listClaims(t *testing.T, c client.Client) []gpuv1alpha1.GPUClaim {
	t.Helper()
	l := &gpuv1alpha1.GPUClaimList{}
	if err := c.List(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func mustAllow(t *testing.T, resp admission.Response) {
	t.Helper()
	if !resp.Allowed {
		t.Fatalf("refused: %s", resp.Result.Message)
	}
}

func mustDeny(t *testing.T, resp admission.Response, parts ...string) {
	t.Helper()
	if resp.Allowed {
		t.Fatalf("allowed, want a refusal naming %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(resp.Result.Message, p) {
			t.Fatalf("refusal %q does not name %q", resp.Result.Message, p)
		}
	}
}

// activeVMI is a VMI in the tenant namespace that lists pods as active.
func activeVMI(name string, pods ...*corev1.Pod) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(vmiGVK)
	u.SetNamespace(tenantNS)
	u.SetName(name)
	active := map[string]any{}
	for _, p := range pods {
		active[string(p.UID)] = p.Spec.NodeName
	}
	_ = unstructured.SetNestedMap(u.Object, active, "status", "activePods")
	return u
}

// launcherPod is a virt-launcher pod of VMI owner, as KubeVirt renders it.
func launcherPod(name string, owner *unstructured.Unstructured) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: tenantNS, UID: types.UID("uid-" + name),
			Labels: map[string]string{labelKubeVirt: valueVirtLauncher, gpu.LabelClaimReference: gpu.LabelValueTrue},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance",
				Name: owner.GetName(), UID: owner.GetUID(), Controller: ptr.To(true)}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "compute"}}},
	}
}
