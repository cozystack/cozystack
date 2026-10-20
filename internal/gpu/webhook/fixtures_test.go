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
	"sync"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	s.AddKnownTypeWithName(kubeVirtGVK.GroupVersion().WithKind(kubeVirtGVK.Kind+"List"), &unstructured.UnstructuredList{})
	return s
}

// kubeVirt is the KubeVirt CR; a nil table leaves permittedHostDevices unset.
func kubeVirt(names ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(kubeVirtGVK)
	u.SetNamespace(kubeVirtNamespace)
	u.SetName(kubeVirtName)
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
		WithStatusSubresource(&gpuv1alpha1.GPUDevice{}, &gpuv1alpha1.GPUClass{}, &gpuv1alpha1.GPUNode{}).
		Build()
}

func newHandler(c client.Client) *Handler {
	return &Handler{Client: c, Reader: c, Cache: c, BindTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond}
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

func deleting(o client.Object) {
	o.SetDeletionTimestamp(ptr.To(metav1.Now()))
	o.SetFinalizers(append(o.GetFinalizers(), "test/keep"))
}

func device(name, className string, count int32, phase gpuv1alpha1.GPUDevicePhase, nodeName string) *gpuv1alpha1.GPUDevice {
	return &gpuv1alpha1.GPUDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantNS},
		Spec:       gpuv1alpha1.GPUDeviceSpec{ClassName: className, Count: count},
		Status:     gpuv1alpha1.GPUDeviceStatus{Phase: phase, NodeName: nodeName},
	}
}

func attached(d *gpuv1alpha1.GPUDevice, vmi string) *gpuv1alpha1.GPUDevice {
	d.Status.AttachedTo = &gpuv1alpha1.GPUDeviceAttachment{VMI: vmi}
	return d
}

func node(name string, labels map[string]string, alloc map[string]int64) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
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
	hrNamespace, hrName := tenantReleaseKey(ns)
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: hrName, Namespace: hrNamespace,
			Labels: map[string]string{appsv1alpha1.ApplicationKindLabel: "Tenant"}},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: raw}},
	}
}

func gpuNode(name string, conds ...metav1.Condition) *gpuv1alpha1.GPUNode {
	return &gpuv1alpha1.GPUNode{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: gpuv1alpha1.GPUNodeStatus{Conditions: conds}}
}

func refsAnnotation(t *testing.T, refs ...model.DeviceRef) string {
	t.Helper()
	s, err := model.FormatDeviceRefs(refs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func held(name string) model.DeviceRef { return model.DeviceRef{Device: name} }

func tmpl(className string, count int32) model.DeviceRef {
	return model.DeviceRef{DeviceTemplate: &model.DeviceTemplate{ClassName: className, Count: count}}
}

// vmi returns a referencing VMI as virt-controller creates it from a VM.
func vmi(t *testing.T, name string, refs ...model.DeviceRef) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachineInstance",
		"metadata": map[string]any{
			"name":        name,
			"namespace":   tenantNS,
			"labels":      map[string]any{gpu.LabelDeviceReference: gpu.LabelValueTrue},
			"annotations": map[string]any{gpu.AnnotationDevices: refsAnnotation(t, refs...)},
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

// applyPatch returns obj with the response's JSON patch applied.
func applyPatch(t *testing.T, obj *unstructured.Unstructured, resp admission.Response) *unstructured.Unstructured {
	t.Helper()
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatal(err)
	}
	out, err := patchJSON(raw, resp)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(out, &u.Object); err != nil {
		t.Fatal(err)
	}
	return u
}

func getDevice(t *testing.T, c client.Client, name string) *gpuv1alpha1.GPUDevice {
	t.Helper()
	d := &gpuv1alpha1.GPUDevice{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNS, Name: name}, d); err != nil {
		t.Fatalf("get GPUDevice %s: %v", name, err)
	}
	return d
}

func listDevices(t *testing.T, c client.Client) []gpuv1alpha1.GPUDevice {
	t.Helper()
	l := &gpuv1alpha1.GPUDeviceList{}
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

// fakeBinder answers complete bind requests the way the leader's binder
// does (internal/gpu/doc.go): a whole set on one node, or nothing moves.
type fakeBinder struct {
	c client.Client
	// decide returns the node for a set, or a refusal message.
	decide func(set []*gpuv1alpha1.GPUDevice) (node, refusal string)

	mu       sync.Mutex
	requests []gpuv1alpha1.BindRequest
}

func startBinder(t *testing.T, c client.Client, decide func([]*gpuv1alpha1.GPUDevice) (string, string)) *fakeBinder {
	t.Helper()
	b := &fakeBinder{c: c, decide: decide}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			b.pass(ctx)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return b
}

// splitNodes makes the binder put the first device on nodeA and the rest
// on nodeB, which a correct binder never does.
const splitNodes = "split"

// placeOn is a decide that puts every set on one node.
func placeOn(n string) func([]*gpuv1alpha1.GPUDevice) (string, string) {
	return func([]*gpuv1alpha1.GPUDevice) (string, string) { return n, "" }
}

func (b *fakeBinder) seen() []gpuv1alpha1.BindRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]gpuv1alpha1.BindRequest(nil), b.requests...)
}

func (b *fakeBinder) pass(ctx context.Context) {
	l := &gpuv1alpha1.GPUDeviceList{}
	if err := b.c.List(ctx, l); err != nil {
		return
	}
	byName := map[string]*gpuv1alpha1.GPUDevice{}
	for i := range l.Items {
		byName[l.Items[i].Name] = &l.Items[i]
	}
	answered := map[string]bool{}
	for _, d := range byName {
		req := d.Status.BindRequest
		if req == nil || req.Result != "" || answered[req.ID] {
			continue
		}
		var set []*gpuv1alpha1.GPUDevice
		complete := true
		for _, name := range req.Devices {
			m := byName[name]
			if m == nil || m.Status.BindRequest == nil || m.Status.BindRequest.ID != req.ID {
				complete = false
				break
			}
			set = append(set, m)
		}
		if !complete {
			continue
		}
		answered[req.ID] = true
		b.mu.Lock()
		b.requests = append(b.requests, *req)
		b.mu.Unlock()
		target, refusal := b.decide(set)
		for _, m := range set {
			if refusal != "" {
				m.Status.BindRequest.Result = gpuv1alpha1.BindRefused
				m.Status.BindRequest.Message = refusal
			} else {
				if !model.HoldsGPUs(m.Status.Phase) {
					m.Status.Phase = gpuv1alpha1.GPUDeviceBound
				}
				m.Status.NodeName = target
				if target == splitNodes && m != set[0] {
					m.Status.NodeName = nodeB
				} else if target == splitNodes {
					m.Status.NodeName = nodeA
				}
				m.Status.BoundAt = ptr.To(metav1.Now())
				m.Status.BindRequest.Result = gpuv1alpha1.BindSucceeded
			}
			_ = b.c.Status().Update(ctx, m)
		}
	}
}

func patchJSON(doc []byte, resp admission.Response) ([]byte, error) {
	if len(resp.Patches) == 0 {
		return doc, nil
	}
	ops, err := json.Marshal(resp.Patches)
	if err != nil {
		return nil, err
	}
	p, err := jsonpatch.DecodePatch(ops)
	if err != nil {
		return nil, err
	}
	return p.Apply(doc)
}

func resourceQuantity(n int64) *resource.Quantity { return resource.NewQuantity(n, resource.DecimalSI) }
