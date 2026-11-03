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
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

const (
	tenantNS = "tenant-acme"
	h100RN   = "nvidia.com/GH100_H100"
	l40sRN   = "nvidia.com/NVIDIA_L40S-24C"
	a40RN    = "nvidia.com/NVIDIA_A40-16Q"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	s.AddKnownTypeWithName(kubeVirtGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(kubeVirtGVK.GroupVersion().WithKind(kubeVirtGVK.Kind+"List"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(vmiGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vmiGVK.GroupVersion().WithKind(vmiGVK.Kind+"List"), &unstructured.UnstructuredList{})
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

func namespace(entitled bool) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: tenantNS, Labels: map[string]string{
		gpu.LabelNamespaceVGPU: map[bool]string{true: "true", false: "false"}[entitled],
	}}}
}

func newHandler(t *testing.T, objs ...client.Object) *Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return &Handler{Reader: c}
}

type classOpt func(*cozyv1alpha1.GPUClass)

func class(name string, mode cozyv1alpha1.GPUMode, rn string, opts ...classOpt) *cozyv1alpha1.GPUClass {
	c := &cozyv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cozyv1alpha1.GPUClassSpec{
			Vendor: "nvidia", Model: name, Mode: mode,
			Source: cozyv1alpha1.GPUClassSource{DevicePlugin: &cozyv1alpha1.DevicePluginSource{ResourceName: rn}},
		},
	}
	if mode == cozyv1alpha1.GPUModeVGPU {
		c.Spec.Profile = "P"
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func maxCount(n int32) classOpt {
	return func(c *cozyv1alpha1.GPUClass) { c.Spec.MaxCount = ptr.To(n) }
}

func licensed(secret string) classOpt {
	return func(c *cozyv1alpha1.GPUClass) {
		c.Spec.License = &cozyv1alpha1.GPUClassLicense{SecretName: secret, FeatureType: 1}
	}
}

func tolerates(ts ...cozyv1alpha1.GPUClassToleration) classOpt {
	return func(c *cozyv1alpha1.GPUClass) { c.Spec.Tolerations = append(c.Spec.Tolerations, ts...) }
}

func conflicting(c *cozyv1alpha1.GPUClass) {
	meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: cozyv1alpha1.GPUClassResourceNameConflict, Status: metav1.ConditionTrue, Reason: "Shared"})
}

func deleting(c *cozyv1alpha1.GPUClass) {
	c.SetDeletionTimestamp(ptr.To(metav1.Now()))
	c.SetFinalizers([]string{"test/keep"})
}

func vmi(annotation string, spec map[string]any) map[string]any {
	md := map[string]any{
		"name":      "vm1",
		"namespace": tenantNS,
		"labels":    map[string]any{gpu.LabelGPUReference: "true"},
	}
	if annotation != "" {
		md["annotations"] = map[string]any{gpu.AnnotationGPUs: annotation}
	}
	if spec == nil {
		spec = map[string]any{"domain": map[string]any{"devices": map[string]any{}}}
	}
	return map[string]any{"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachineInstance", "metadata": md, "spec": spec}
}

func request(t *testing.T, op admissionv1.Operation, obj map[string]any) admission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: op, Namespace: tenantNS, Object: runtime.RawExtension{Raw: raw}}}
}

func patched(t *testing.T, obj map[string]any, resp admission.Response) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(obj)
	if len(resp.Patches) == 0 {
		return obj
	}
	ops, _ := json.Marshal(resp.Patches)
	p, err := jsonpatch.DecodePatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Apply(raw)
	if err != nil {
		t.Fatalf("apply %s: %v", ops, err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return m
}

func expectDenied(t *testing.T, resp admission.Response, want string) {
	t.Helper()
	if resp.Allowed {
		t.Fatalf("allowed, want a refusal with %q", want)
	}
	if got := resp.Result.Message; !strings.Contains(got, want) {
		t.Errorf("refused with %q, want %q", got, want)
	}
}

func expectAllowed(t *testing.T, resp admission.Response) {
	t.Helper()
	if !resp.Allowed {
		msg := ""
		if resp.Result != nil {
			msg = resp.Result.Message
		}
		t.Fatalf("refused: %s", msg)
	}
}

var ctx = context.Background()
