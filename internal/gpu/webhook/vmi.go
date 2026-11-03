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
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

// licenseVolume names the license volume and its disk. A VMI takes vGPUs
// of one class only, so it has at most one.
const licenseVolume = "gpu-license"

// MutateVMI resolves the class entries of a labelled VMI and writes one
// gpus entry per GPU unit with the class's resource name, and the license
// disk of a licensed vGPU class. KubeVirt renders the launcher pod from
// the VMI spec, which nothing else can change afterwards, so the entries
// are complete here or the VMI is refused. Capacity is not checked: a VM
// short of a free unit waits in Scheduling.
func (h *Handler) MutateVMI(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.Object.Raw, &u.Object); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if u.GetLabels()[gpu.LabelGPUReference] != gpu.LabelValueTrue {
		return admission.Allowed("")
	}
	entries, err := model.ParseClassEntries(u.GetAnnotations()[gpu.AnnotationGPUs])
	if err != nil {
		return admission.Denied(fmt.Sprintf("annotation %s: %v", gpu.AnnotationGPUs, err))
	}
	classes, err := h.classes(ctx)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	entitled, err := h.entitled(ctx, req.Namespace)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	kv, err := h.kubeVirt(ctx)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	var permitted sets.Set[string]
	var tableGPUs []string
	if kv != nil {
		permitted, tableGPUs = model.PermittedResourceNames(kv), model.PermittedGPUNames(kv)
	}
	if problem := entriesProblem(entries, classes, entitled, permitted); problem != "" {
		return admission.Denied(problem)
	}

	desired := desiredGPUs(entries, classes)
	projected := make([]model.Class, 0, len(classes))
	for _, c := range classes {
		projected = append(projected, model.ClassFrom(c))
	}
	names := model.GPUResourceNames(h.ResourceNames, projected, tableGPUs)
	if own := ownGPUEntries(u, desired, names); len(own) > 0 {
		return admission.Denied("the VMI carries GPU entries of its own next to class entries: " + strings.Join(own, ", "))
	}
	ops := gpusPatch(u, desired)
	// Plan the license patch against the devices parent the GPU patch creates.
	if err := unstructured.SetNestedSlice(u.Object, desired, "spec", "domain", "devices", "gpus"); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	licenseOps, problem := licensePatch(u, entries, classes)
	if problem != "" {
		return admission.Denied(problem)
	}
	ops = append(ops, licenseOps...)
	if len(ops) == 0 {
		return admission.Allowed("")
	}
	return admission.Patched("", ops...)
}

func entriesProblem(entries []model.ClassEntry, classes map[string]*cozyv1alpha1.GPUClass, entitled bool, permitted sets.Set[string]) string {
	projected := make([]model.Class, 0, len(classes))
	for _, c := range classes {
		projected = append(projected, model.ClassFrom(c))
	}
	// Status is asynchronous; the live class list must also enforce uniqueness.
	conflicts := model.Conflicts(projected)
	units := map[string]int64{}
	vgpu := sets.New[string]()
	// Bound tenant-controlled expansion before constructing per-unit maps.
	// This is a response-size guard, not a capacity check.
	var total int64
	for _, e := range entries {
		total += int64(e.Count)
		if total > gpu.MaxVMIUnits {
			return "a VMI can expand at most 1024 GPU units from class entries"
		}

		c := classes[e.ClassName]
		switch {
		case c == nil:
			return fmt.Sprintf("GPUClass %s does not exist", e.ClassName)
		case !c.DeletionTimestamp.IsZero():
			return fmt.Sprintf("GPUClass %s is being deleted", e.ClassName)
		case conflicts.Has(e.ClassName) || meta.IsStatusConditionTrue(c.Status.Conditions, cozyv1alpha1.GPUClassResourceNameConflict):
			return fmt.Sprintf("GPUClass %s shares its resource name with another class and is not offered", e.ClassName)
		case c.Spec.Mode == cozyv1alpha1.GPUModeVGPU && !entitled:
			return fmt.Sprintf("GPUClass %s is vGPU and the tenant is not entitled to vGPU", e.ClassName)
		}
		if rn := model.ClassFrom(c).ResourceName; permitted != nil && !permitted.Has(string(rn)) {
			return fmt.Sprintf("GPUClass %s: resource name %s is not in the KubeVirt permittedHostDevices table, so the VM could not start", e.ClassName, rn)
		}
		if c.Spec.Mode == cozyv1alpha1.GPUModeVGPU {
			vgpu.Insert(e.ClassName)
		}
		units[e.ClassName] += int64(e.Count)
	}
	if vgpu.Len() > 1 {
		return "a VM takes vGPUs of one class only: " + strings.Join(sets.List(vgpu), ", ")
	}
	names := make([]string, 0, len(units))
	for n := range units {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if limit, ok := model.EffectiveMaxCount(&classes[n].Spec); ok && units[n] > int64(limit) {
			return fmt.Sprintf("%d GPUs of GPUClass %s exceed its maxCount %d per VM", units[n], n, limit)
		}
	}
	return ""
}

// desiredGPUs is one gpus entry per GPU unit, named after the entry's
// position, so a reinvocation finds exactly the entries it wrote.
func desiredGPUs(entries []model.ClassEntry, classes map[string]*cozyv1alpha1.GPUClass) []any {
	var out []any
	for i, e := range entries {
		rn := string(model.ClassFrom(classes[e.ClassName]).ResourceName)
		for j := int32(0); j < e.Count; j++ {
			out = append(out, map[string]any{"name": fmt.Sprintf("%s%d-%d", gpu.VMIGPUNamePrefix, i, j), "deviceName": rn})
		}
	}
	return out
}

// ownGPUEntries lists the VMI's GPU entries that are not exactly what this
// webhook writes: any other gpus entry, whatever its name, and a
// hostDevices entry or a spec.domain.resources key with a GPU resource
// name. Entries from an instance type are on the VMI already.
func ownGPUEntries(u *unstructured.Unstructured, desired []any, names sets.Set[string]) []string {
	var out []string
	gpus, _, _ := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "gpus")
	for _, e := range gpus {
		if !containsEqual(desired, e) {
			m, _ := e.(map[string]any)
			name, _ := m["name"].(string)
			out = append(out, "gpus "+name)
		}
	}
	hostDevices, _, _ := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "hostDevices")
	for _, e := range hostDevices {
		m, _ := e.(map[string]any)
		if dn, _ := m["deviceName"].(string); names.Has(dn) {
			name, _ := m["name"].(string)
			out = append(out, "hostDevices "+name)
		}
	}
	keys := sets.New[string]()
	for _, kind := range []string{"requests", "limits"} {
		rl, _, _ := unstructured.NestedMap(u.Object, "spec", "domain", "resources", kind)
		for k := range rl {
			if names.Has(k) {
				keys.Insert(k)
			}
		}
	}
	for _, k := range sets.List(keys) {
		out = append(out, "resources "+k)
	}
	return out
}

func containsEqual(list []any, v any) bool {
	for _, e := range list {
		if reflect.DeepEqual(e, v) {
			return true
		}
	}
	return false
}

func gpusPatch(u *unstructured.Unstructured, desired []any) []jsonpatch.Operation {
	current, _, _ := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "gpus")
	if reflect.DeepEqual(current, desired) {
		return nil
	}
	return []jsonpatch.Operation{setOp(u, desired, "spec", "domain", "devices", "gpus")}
}

// setOp sets the field at path to value, creating the parents a JSON
// patch cannot add into.
func setOp(u *unstructured.Unstructured, value any, path ...string) jsonpatch.Operation {
	i := len(path)
	for i > 1 {
		if _, ok, _ := unstructured.NestedFieldNoCopy(u.Object, path[:i-1]...); ok {
			break
		}
		i--
	}
	for j := len(path) - 1; j >= i; j-- {
		value = map[string]any{path[j]: value}
	}
	return jsonpatch.NewOperation("add", "/"+strings.Join(path[:i], "/"), value)
}

func appendOp(u *unstructured.Unstructured, value any, path ...string) jsonpatch.Operation {
	if list, ok, _ := unstructured.NestedSlice(u.Object, path...); ok && len(list) > 0 {
		return jsonpatch.NewOperation("add", "/"+strings.Join(path, "/")+"/-", value)
	}
	return setOp(u, []any{value}, path...)
}

// licensePatch adds the license copy of the VMI's licensed vGPU class as a
// secret volume with the GPULICENSE label, and its disk after the VMI's
// other disks without a bootOrder, so the boot order does not change.
// The copy may not exist yet: kubelet then keeps the launcher pod from
// starting until it does.
func licensePatch(u *unstructured.Unstructured, entries []model.ClassEntry, classes map[string]*cozyv1alpha1.GPUClass) ([]jsonpatch.Operation, string) {
	var class string
	for _, e := range entries {
		if c := classes[e.ClassName]; c.Spec.Mode == cozyv1alpha1.GPUModeVGPU && c.Spec.License != nil {
			class = e.ClassName
		}
	}
	if class == "" {
		return nil, ""
	}
	volume := map[string]any{"name": licenseVolume, "secret": map[string]any{
		"secretName":  gpu.LicenseSecretName(class),
		"volumeLabel": gpu.LicenseVolumeLabel,
	}}
	disk := map[string]any{"name": licenseVolume, "disk": map[string]any{"bus": "virtio"}}
	var ops []jsonpatch.Operation
	for _, want := range []struct {
		value map[string]any
		kind  string
		path  []string
	}{
		{volume, "volume", []string{"spec", "volumes"}},
		{disk, "disk", []string{"spec", "domain", "devices", "disks"}},
	} {
		list, _, _ := unstructured.NestedSlice(u.Object, want.path...)
		found := false
		for _, e := range list {
			m, _ := e.(map[string]any)
			if m["name"] != licenseVolume {
				continue
			}
			if !reflect.DeepEqual(m, want.value) {
				return nil, fmt.Sprintf("the VMI has a %s %s of its own, which the license of GPUClass %s needs", want.kind, licenseVolume, class)
			}
			found = true
		}
		if !found {
			ops = append(ops, appendOp(u, want.value, want.path...))
		}
	}
	return ops, ""
}
