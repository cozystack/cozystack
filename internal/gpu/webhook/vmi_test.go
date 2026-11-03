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
	"reflect"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

const (
	passthrough = cozyv1alpha1.GPUModePassthrough
	vgpu        = cozyv1alpha1.GPUModeVGPU
)

func gpusOf(t *testing.T, obj map[string]any) []any {
	t.Helper()
	gpus, _, _ := unstructured.NestedSlice(obj, "spec", "domain", "devices", "gpus")
	return gpus
}

func entry(name, device string) map[string]any {
	return map[string]any{"name": name, "deviceName": device}
}

func TestMutateVMIWritesOneEntryPerUnit(t *testing.T) {
	h := newHandler(t, namespace(false), kubeVirt(h100RN, l40sRN), class("h100", passthrough, h100RN), class("a100", passthrough, l40sRN))
	obj := vmi(`[{"className":"h100","count":2},{"className":"a100"}]`, nil)
	resp := h.MutateVMI(ctx, request(t, admissionv1.Create, obj))
	expectAllowed(t, resp)
	got := gpusOf(t, patched(t, obj, resp))
	want := []any{entry("gpu-0-0", h100RN), entry("gpu-0-1", h100RN), entry("gpu-1-0", l40sRN)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gpus = %v, want %v", got, want)
	}
}

func TestMutateVMIIsIdempotent(t *testing.T) {
	h := newHandler(t, namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN, maxCount(2), licensed("token")))
	obj := vmi(`[{"className":"l40s","count":2}]`, nil)
	first := patched(t, obj, h.MutateVMI(ctx, request(t, admissionv1.Create, obj)))
	resp := h.MutateVMI(ctx, request(t, admissionv1.Create, first))
	expectAllowed(t, resp)
	if len(resp.Patches) != 0 {
		t.Errorf("reinvocation patched %v", resp.Patches)
	}
}

func TestMutateVMILeavesOtherObjectsAlone(t *testing.T) {
	h := newHandler(t)
	unlabelled := vmi(`[{"className":"h100"}]`, nil)
	delete(unlabelled["metadata"].(map[string]any), "labels")
	for name, req := range map[string]admission.Request{
		"unlabelled": request(t, admissionv1.Create, unlabelled),
		"update":     request(t, admissionv1.Update, vmi(`[{"className":"h100"}]`, nil)),
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.MutateVMI(ctx, req)
			expectAllowed(t, resp)
			if len(resp.Patches) != 0 {
				t.Errorf("patched %v", resp.Patches)
			}
		})
	}
}

func TestMutateVMIRefuses(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		obj     map[string]any
		want    string
	}{
		{
			name:    "bad annotation",
			objects: []client.Object{namespace(false), kubeVirt()},
			obj:     vmi(`[{"className":"h100","count":0}]`, nil),
			want:    "annotation cozystack.io/gpus",
		},
		{
			name:    "missing class",
			objects: []client.Object{namespace(false), kubeVirt()},
			obj:     vmi(`[{"className":"h100"}]`, nil),
			want:    "GPUClass h100 does not exist",
		},
		{
			name:    "class being deleted",
			objects: []client.Object{namespace(false), kubeVirt(), class("h100", passthrough, h100RN, deleting)},
			obj:     vmi(`[{"className":"h100"}]`, nil),
			want:    "GPUClass h100 is being deleted",
		},
		{
			name:    "conflicting class",
			objects: []client.Object{namespace(false), kubeVirt(), class("h100", passthrough, h100RN, conflicting)},
			obj:     vmi(`[{"className":"h100"}]`, nil),
			want:    "GPUClass h100 shares its resource name with another class",
		},
		{
			name:    "two vGPU classes",
			objects: []client.Object{namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN), class("a40", vgpu, a40RN)},
			obj:     vmi(`[{"className":"l40s"},{"className":"a40"}]`, nil),
			want:    "a VM takes vGPUs of one class only: a40, l40s",
		},
		{
			name:    "above maxCount across entries",
			objects: []client.Object{namespace(false), kubeVirt(), class("h100", passthrough, h100RN, maxCount(2))},
			obj:     vmi(`[{"className":"h100","count":2},{"className":"h100"}]`, nil),
			want:    "3 GPUs of GPUClass h100 exceed its maxCount 2 per VM",
		},
		{
			name:    "vGPU default maxCount",
			objects: []client.Object{namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN)},
			obj:     vmi(`[{"className":"l40s","count":2}]`, nil),
			want:    "exceed its maxCount 1 per VM",
		},
		{
			name:    "unentitled vGPU",
			objects: []client.Object{namespace(false), kubeVirt(), class("l40s", vgpu, l40sRN)},
			obj:     vmi(`[{"className":"l40s"}]`, nil),
			want:    "GPUClass l40s is vGPU and the tenant is not entitled to vGPU",
		},
		{
			name:    "unentitled vGPU without a namespace label",
			objects: []client.Object{kubeVirt(), class("l40s", vgpu, l40sRN)},
			obj:     vmi(`[{"className":"l40s"}]`, nil),
			want:    "not entitled to vGPU",
		},
		{
			name:    "not in permittedHostDevices",
			objects: []client.Object{namespace(false), kubeVirt(l40sRN), class("h100", passthrough, h100RN)},
			obj:     vmi(`[{"className":"h100"}]`, nil),
			want:    "resource name nvidia.com/GH100_H100 is not in the KubeVirt permittedHostDevices table",
		},
		{
			name:    "own gpus entry",
			objects: []client.Object{namespace(false), kubeVirt(), class("h100", passthrough, h100RN)},
			obj:     vmi(`[{"className":"h100"}]`, map[string]any{"domain": map[string]any{"devices": map[string]any{"gpus": []any{entry("gpu1", "nvidia.com/anything")}}}}),
			want:    "GPU entries of its own next to class entries: gpus gpu1",
		},
		{
			name:    "own entry that only looks like the webhook's",
			objects: []client.Object{namespace(false), kubeVirt(), class("h100", passthrough, h100RN)},
			obj:     vmi(`[{"className":"h100"}]`, map[string]any{"domain": map[string]any{"devices": map[string]any{"gpus": []any{entry("gpu-0-0", l40sRN)}}}}),
			want:    "gpus gpu-0-0",
		},
		{
			name:    "own host device on a GPU name from the table",
			objects: []client.Object{namespace(false), kubeVirt(h100RN, "nvidia.com/H200"), class("h100", passthrough, h100RN)},
			obj:     vmi(`[{"className":"h100"}]`, map[string]any{"domain": map[string]any{"devices": map[string]any{"hostDevices": []any{entry("hd", "nvidia.com/H200")}}}}),
			want:    "hostDevices hd",
		},
		{
			name:    "own resource request on a hoster-listed name",
			objects: []client.Object{namespace(false), kubeVirt(), class("h100", passthrough, h100RN)},
			obj: vmi(`[{"className":"h100"}]`, map[string]any{"domain": map[string]any{"devices": map[string]any{},
				"resources": map[string]any{"limits": map[string]any{"nvidia.com/gpu": "1"}}}}),
			want: "resources nvidia.com/gpu",
		},
		{
			name:    "a foreign volume under the license volume's name",
			objects: []client.Object{namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN, licensed("token"))},
			obj: vmi(`[{"className":"l40s"}]`, map[string]any{"domain": map[string]any{"devices": map[string]any{}},
				"volumes": []any{map[string]any{"name": licenseVolume, "containerDisk": map[string]any{"image": "x"}}}}),
			want: "volume gpu-license",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(t, tc.objects...)
			h.ResourceNames = []string{"nvidia.com/gpu"}
			expectDenied(t, h.MutateVMI(ctx, request(t, admissionv1.Create, tc.obj)), tc.want)
		})
	}
}

// A NIC next to a GPU class, for GPUDirect RDMA, is not a GPU entry.
func TestMutateVMIAllowsANICNextToAClass(t *testing.T) {
	kv := kubeVirt(h100RN)
	pci, _, _ := unstructured.NestedSlice(kv.Object, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	pci = append(pci, map[string]any{"pciVendorSelector": "15B3:101E", "resourceName": "nvidia.com/hostdev"})
	_ = unstructured.SetNestedSlice(kv.Object, pci, "spec", "configuration", "permittedHostDevices", "pciHostDevices")
	h := newHandler(t, namespace(false), kv, class("h100", passthrough, h100RN))
	obj := vmi(`[{"className":"h100"}]`, map[string]any{"domain": map[string]any{"devices": map[string]any{"hostDevices": []any{entry("nic", "nvidia.com/hostdev")}}}})
	expectAllowed(t, h.MutateVMI(ctx, request(t, admissionv1.Create, obj)))
}

func TestMutateVMIAddsTheLicenseDisk(t *testing.T) {
	h := newHandler(t, namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN, licensed("token")))
	disks := []any{map[string]any{"name": "root", "disk": map[string]any{"bus": "virtio"}, "bootOrder": int64(1)}}
	obj := vmi(`[{"className":"l40s"}]`, map[string]any{
		"domain":  map[string]any{"devices": map[string]any{"disks": disks}},
		"volumes": []any{map[string]any{"name": "root", "dataVolume": map[string]any{"name": "vm1"}}},
	})
	resp := h.MutateVMI(ctx, request(t, admissionv1.Create, obj))
	expectAllowed(t, resp)
	out := patched(t, obj, resp)
	gotDisks, _, _ := unstructured.NestedSlice(out, "spec", "domain", "devices", "disks")
	if len(gotDisks) != 2 || !reflect.DeepEqual(gotDisks[1], map[string]any{"name": licenseVolume, "disk": map[string]any{"bus": "virtio"}}) {
		t.Errorf("disks = %v, want the root disk then the license disk without bootOrder", gotDisks)
	}
	gotVolumes, _, _ := unstructured.NestedSlice(out, "spec", "volumes")
	want := map[string]any{"name": licenseVolume, "secret": map[string]any{"secretName": "gpuclass-l40s-license", "volumeLabel": "GPULICENSE"}}
	if len(gotVolumes) != 2 || !reflect.DeepEqual(gotVolumes[1], want) {
		t.Errorf("volumes = %v, want the root volume then %v", gotVolumes, want)
	}
}

func TestMutateVMIAddsTheLicenseDiskToAVMIWithoutDisks(t *testing.T) {
	h := newHandler(t, namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN, licensed("token")))
	obj := vmi(`[{"className":"l40s"}]`, nil)
	out := patched(t, obj, h.MutateVMI(ctx, request(t, admissionv1.Create, obj)))
	if d, _, _ := unstructured.NestedSlice(out, "spec", "domain", "devices", "disks"); len(d) != 1 {
		t.Errorf("disks = %v", d)
	}
	if v, _, _ := unstructured.NestedSlice(out, "spec", "volumes"); len(v) != 1 {
		t.Errorf("volumes = %v", v)
	}
}

func TestMutateVMIAddsNoLicenseDiskForAnUnlicensedClass(t *testing.T) {
	h := newHandler(t, namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN), class("h100", passthrough, h100RN, licensedPassthrough))
	for _, name := range []string{"l40s", "h100"} {
		obj := vmi(`[{"className":"`+name+`"}]`, nil)
		out := patched(t, obj, h.MutateVMI(ctx, request(t, admissionv1.Create, obj)))
		if v, ok, _ := unstructured.NestedSlice(out, "spec", "volumes"); ok {
			t.Errorf("%s: volumes = %v", name, v)
		}
	}
}

func licensedPassthrough(c *cozyv1alpha1.GPUClass) {
	c.Spec.License = &cozyv1alpha1.GPUClassLicense{SecretName: "token", FeatureType: 1}
}

// The annotation is tenant input; expansion must be bounded before any
// per-unit maps are allocated, including across repeated class entries.
func TestVMIExpansionBudget(t *testing.T) {
	classes := map[string]*cozyv1alpha1.GPUClass{"h100": class("h100", passthrough, h100RN)}
	for _, counts := range [][]int32{{2147483647}, {600, 600}} {
		var entries []model.ClassEntry
		for _, count := range counts {
			entries = append(entries, model.ClassEntry{ClassName: "h100", Count: count})
		}
		if got := entriesProblem(entries, classes, false, nil); !strings.Contains(got, "at most 1024 GPU units") {
			t.Errorf("counts %v: refusal = %q", counts, got)
		}
	}
	if got := entriesProblem([]model.ClassEntry{{ClassName: "h100", Count: 1024}}, classes, false, nil); got != "" {
		t.Errorf("bounded expansion refused: %s", got)
	}
}

func TestMutateVMIExpansionRefusalHasNoSideEffects(t *testing.T) {
	c := class("h100", passthrough, h100RN)
	h := newHandler(t, c, namespace(false), kubeVirt())
	before := c.DeepCopy()
	resp := h.MutateVMI(ctx, request(t, admissionv1.Create, vmi(`[{"className":"h100","count":1025}]`, nil)))
	expectDenied(t, resp, "at most 1024 GPU units")
	if len(resp.Patches) != 0 || len(resp.Patch) != 0 {
		t.Fatalf("refusal returned a partial patch: %+v", resp)
	}
	after := &cozyv1alpha1.GPUClass{}
	if err := h.Reader.Get(ctx, client.ObjectKey{Name: c.Name}, after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Status, after.Status) {
		t.Errorf("refusal changed class status: before %+v, after %+v", before.Status, after.Status)
	}
}

func TestMutateVMICreatesDevicesWithoutLosingGPUs(t *testing.T) {
	h := newHandler(t, namespace(true), kubeVirt(), class("l40s", vgpu, l40sRN, licensed("token")))
	obj := vmi(`[{"className":"l40s"}]`, map[string]any{"domain": map[string]any{}})
	resp := h.MutateVMI(ctx, request(t, admissionv1.Create, obj))
	expectAllowed(t, resp)
	out := patched(t, obj, resp)
	if got := gpusOf(t, out); !reflect.DeepEqual(got, []any{entry("gpu-0-0", l40sRN)}) {
		t.Errorf("license patch replaced GPU entries: %v", got)
	}
	if disks, _, _ := unstructured.NestedSlice(out, "spec", "domain", "devices", "disks"); len(disks) != 1 {
		t.Errorf("license disk missing: %v", disks)
	}
}

func TestMutateVMIChecksConflictsBeforeStatusCatchesUp(t *testing.T) {
	older := class("h100-original", passthrough, h100RN)
	older.CreationTimestamp = metav1.Unix(1, 0)
	newer := class("h100-alias", passthrough, h100RN)
	newer.CreationTimestamp = metav1.Unix(2, 0)
	h := newHandler(t, namespace(false), kubeVirt(), older, newer)
	expectDenied(t, h.MutateVMI(ctx, request(t, admissionv1.Create, vmi(`[{"className":"h100-alias"}]`, nil))), "shares its resource name")
	expectAllowed(t, h.MutateVMI(ctx, request(t, admissionv1.Create, vmi(`[{"className":"h100-original"}]`, nil))))
}
