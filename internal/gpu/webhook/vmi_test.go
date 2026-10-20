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
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
)

const vmiName = "vm-instance-trainer"

// cluster is two Ready nodes serving h100, one serving l40s and a40, and
// an entitled tenant.
func cluster(t *testing.T, extra ...client.Object) []client.Object {
	t.Helper()
	objs := []client.Object{
		class("h100", gpuv1alpha1.GPUModePassthrough, h100RN, maxCount(4)),
		class("l40s", gpuv1alpha1.GPUModeVGPU, l40sRN, maxCount(2)),
		class("a40", gpuv1alpha1.GPUModeVGPU, a40RN, maxCount(2), func(c *gpuv1alpha1.GPUClass) { c.Spec.NodeSelector = map[string]string{"gpu": "l40s"} }),
		node(nodeA, map[string]string{"gpu": "h100"}, map[string]int64{h100RN: 4}),
		node(nodeB, map[string]string{"gpu": "h100"}, map[string]int64{h100RN: 4}),
		node("gpu-v", map[string]string{"gpu": "l40s"}, map[string]int64{l40sRN: 4}),
		namespace(tenantNS, true),
	}
	return append(objs, extra...)
}

func gpusOf(t *testing.T, u *unstructured.Unstructured) []map[string]any {
	t.Helper()
	list, _, err := unstructured.NestedSlice(u.Object, "spec", "domain", "devices", "gpus")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, e.(map[string]any))
	}
	return out
}

func entry(name, rn string) map[string]any { return map[string]any{"name": name, "deviceName": rn} }

func admitVMI(t *testing.T, h *Handler, u *unstructured.Unstructured, dryRun bool) admission.Response {
	t.Helper()
	return h.MutateVMI(context.Background(), createRequest(t, u.Object, dryRun))
}

func TestVMIAttachesHeldDevice(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 2, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	b := startBinder(t, c, placeOn(nodeA))
	u := vmi(t, vmiName, held("train-0"))

	resp := admitVMI(t, newHandler(c), u, false)
	mustAllow(t, resp)

	got := applyPatch(t, u, resp)
	want := []map[string]any{entry("gpudev-0-0", h100RN), entry("gpudev-0-1", h100RN)}
	if g := gpusOf(t, got); !reflect.DeepEqual(g, want) {
		t.Fatalf("gpus = %v, want %v", g, want)
	}
	// The launcher pod would request the sum of both (template.go:1036 in
	// KubeVirt v1.9.0).
	if lim, found, _ := unstructured.NestedMap(got.Object, "spec", "domain", "resources", "limits"); found && lim[h100RN] != nil {
		t.Fatalf("resources carry the GPU name as well: %v", lim)
	}
	d := getDevice(t, c, "train-0")
	if d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != vmiName {
		t.Fatalf("attachment = %v", d.Status.AttachedTo)
	}
	if r := d.Status.BindRequest; r == nil || r.VMI != vmiName || r.Result != gpuv1alpha1.BindSucceeded || !reflect.DeepEqual(r.Devices, []string{"train-0"}) {
		t.Fatalf("bind request = %+v", r)
	}
	if n := len(b.seen()); n != 1 {
		t.Fatalf("binder saw %d requests, want 1", n)
	}
}

func TestVMIRefusals(t *testing.T) {
	stale := device("busy", "h100", 1, gpuv1alpha1.GPUDeviceAttached, nodeA)
	attached(stale, "another-vmi")
	dying := device("dying", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)
	deleting(dying)
	notReady := node("gpu-down", map[string]string{"gpu": "h100"}, map[string]int64{h100RN: 4})
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	lost := metav1.Condition{Type: gpu.ConditionLostCapacity, Status: metav1.ConditionTrue, Reason: "NodeLostCapacity", LastTransitionTime: metav1.Now()}
	unhealthy := gpuNode(nodeA, lost)
	unhealthy.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: h100RN, Healthy: new(bool)},
	}
	unmarked := device(model.TemplateDeviceName(vmiName, 0), "h100", 1, "", "")
	owned := device(model.TemplateDeviceName(vmiName, 0), "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)
	owned.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	owned.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmiName, UID: "old-uid"}}
	otherSpec := device(model.TemplateDeviceName(vmiName, 0), "h100", 2, "", "")
	otherSpec.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	dyingTemplate := device(model.TemplateDeviceName(vmiName, 1), "h100", 1, "", "")
	dyingTemplate.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	deleting(dyingTemplate)
	deletedClass := class("gone", gpuv1alpha1.GPUModePassthrough, h100RN)
	deleting(deletedClass)
	reusable := device(model.TemplateDeviceName(vmiName, 0), "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)
	reusable.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	elsewhereTemplate := attached(device(model.TemplateDeviceName(vmiName, 1), "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), "other")
	elsewhereTemplate.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}

	tests := []struct {
		name   string
		extra  []client.Object
		vmi    func(*unstructured.Unstructured)
		refs   []model.DeviceRef
		denied []string
	}{
		{name: "a missing device", refs: []model.DeviceRef{held("nope")}, denied: []string{"GPUDevice nope", "does not exist"}},
		{
			name:   "a Pending device",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDevicePending, "")},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "is Pending"},
		},
		{
			name:   "an order the binder has not seen yet",
			extra:  []client.Object{device("train-0", "h100", 1, "", "")},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "is Pending"},
		},
		{
			name:   "a Lost device",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceLost, nodeA)},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "is Lost"},
		},
		{name: "a device being deleted", extra: []client.Object{dying, fine()}, refs: []model.DeviceRef{held("fine"), held("dying")}, denied: []string{"GPUDevice dying", "being deleted"}},
		{name: "a device attached to another VMI", extra: []client.Object{stale, fine()}, refs: []model.DeviceRef{held("fine"), held("busy")}, denied: []string{"GPUDevice busy", "another-vmi"}},
		{
			name:   "a device on a node that is gone",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, "gpu-gone")},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "node gpu-gone", "is gone"},
		},
		{
			name:   "a device on a node the class no longer selects",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v")},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "node gpu-v", "no longer serves GPUClass h100"},
		},
		{
			name: "a device on a node two classes claim by one resource name",
			extra: []client.Object{
				class("h100-twin", gpuv1alpha1.GPUModePassthrough, h100RN, func(c *gpuv1alpha1.GPUClass) { c.Spec.NodeSelector = map[string]string{"gpu": "h100"} }),
				device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
			},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "node gpu-a", "no longer serves GPUClass h100"},
		},
		{
			name:   "a device on a NotReady node that has lost capacity",
			extra:  []client.Object{notReady, gpuNode("gpu-down", lost), device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, "gpu-down")},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "node gpu-down", "lost capacity"},
		},
		{
			name:   "a device its node's healthy GPUs no longer cover",
			extra:  []client.Object{unhealthy, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUDevice train-0", "node gpu-a", "lost capacity"},
		},
		{
			name: "vGPU devices of two classes",
			extra: []client.Object{
				device("v1", "l40s", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v"),
				device("v2", "a40", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v"),
			},
			refs:   []model.DeviceRef{held("v1"), held("v2")},
			denied: []string{"vGPU devices of one class", "l40s", "a40"},
		},
		{
			name: "devices above maxCount",
			extra: []client.Object{
				device("v1", "l40s", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v"),
				device("v2", "l40s", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v"),
			},
			refs:   []model.DeviceRef{held("v1"), held("v2"), tmpl("l40s", 1)},
			denied: []string{"3 GPUs of GPUClass l40s", "maxCount 2"},
		},
		{
			name:   "a gpus entry of the VMI's own",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:   []model.DeviceRef{held("train-0")},
			vmi:    setGPUs(entry("gpu1", "nvidia.com/anything")),
			denied: []string{"GPU entries of its own", "gpus gpu1"},
		},
		{
			name:   "a gpus entry posing as the webhook's",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:   []model.DeviceRef{held("train-0")},
			vmi:    setGPUs(entry("gpudev-0-0", h100RN), entry("gpudev-0-1", h100RN)),
			denied: []string{"GPU entries of its own", "gpus gpudev-0-1"},
		},
		{
			name:   "an entry with the webhook's name and another resource",
			extra:  []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:   []model.DeviceRef{held("train-0")},
			vmi:    setGPUs(entry("gpudev-0-0", l40sRN)),
			denied: []string{"GPU entries of its own", "gpus gpudev-0-0"},
		},
		{
			name:  "a GPU resource name in the VMI's resources",
			extra: []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(u.Object, "1", "spec", "domain", "resources", "limits", l40sRN)
			},
			denied: []string{"GPU entries of its own", "resources " + l40sRN},
		},
		{
			name:  "a host device with a GPU resource name",
			extra: []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{entry("hd", a40RN)}, "spec", "domain", "devices", "hostDevices")
			},
			denied: []string{"GPU entries of its own", "hostDevices hd"},
		},
		{
			name:   "a class outside the KubeVirt permittedHostDevices table",
			extra:  []client.Object{kubeVirt(l40sRN), device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{"GPUClass h100", h100RN, "permittedHostDevices"},
		},
		{
			name:  "a device whose class mode differs from the pool's mode label",
			extra: []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
			vmi: func(u *unstructured.Unstructured) {
				l := u.GetLabels()
				l[labelMode] = "vgpu"
				u.SetLabels(l)
			},
			denied: []string{"GPUClass h100", "mode passthrough", "vgpu"},
		},
		{
			name:   "a VMI without a name",
			refs:   []model.DeviceRef{held("train-0")},
			vmi:    func(u *unstructured.Unstructured) { u.SetName(""); u.SetGenerateName("vm-") },
			denied: []string{"needs a name"},
		},
		{
			name: "malformed references",
			vmi: func(u *unstructured.Unstructured) {
				u.SetAnnotations(map[string]string{gpu.AnnotationDevices: `[{"deviceSelector":{}}]`})
			},
			refs:   []model.DeviceRef{held("train-0")},
			denied: []string{gpu.AnnotationDevices},
		},
		{name: "a template device name taken by an order", extra: []client.Object{unmarked}, refs: []model.DeviceRef{tmpl("h100", 1)}, denied: []string{"GPUDevice " + unmarked.Name, "not created from a deviceTemplate"}},
		{name: "a template device of an earlier VMI", extra: []client.Object{owned}, refs: []model.DeviceRef{tmpl("h100", 1)}, denied: []string{"GPUDevice " + owned.Name, "earlier VMI"}},
		{name: "a template device still being deleted", extra: []client.Object{dyingTemplate, fine()}, refs: []model.DeviceRef{held("fine"), tmpl("h100", 1)}, denied: []string{"GPUDevice " + dyingTemplate.Name, "being deleted"}},
		{name: "a template device of another count", extra: []client.Object{otherSpec}, refs: []model.DeviceRef{tmpl("h100", 1)}, denied: []string{"GPUDevice " + otherSpec.Name, "does not match"}},
		{name: "a template device of another class", extra: []client.Object{otherSpec}, refs: []model.DeviceRef{tmpl("l40s", 2)}, denied: []string{"GPUDevice " + otherSpec.Name, "does not match"}},
		{name: "a template device attached to another VMI", extra: []client.Object{elsewhereTemplate, fine()}, refs: []model.DeviceRef{held("fine"), tmpl("h100", 1)}, denied: []string{"GPUDevice " + elsewhereTemplate.Name, "attached to VMI other"}},
		{
			// A template device's name is predictable; naming it as a held
			// device too would give the VM its GPUs twice.
			name:   "a template device referenced as a held device as well",
			extra:  []client.Object{reusable},
			refs:   []model.DeviceRef{tmpl("h100", 1), held(reusable.Name)},
			denied: []string{"GPUDevice " + reusable.Name, "referenced twice"},
		},
		{
			// A class without maxCount puts no cap on a template's count, and
			// one gpus entry per unit would be built before any budget check.
			name:   "a template larger than any node",
			extra:  []client.Object{class("big", gpuv1alpha1.GPUModePassthrough, h100RN, func(c *gpuv1alpha1.GPUClass) { c.Spec.NodeSelector = map[string]string{"gpu": "h100"} })},
			refs:   []model.DeviceRef{tmpl("big", math.MaxInt32)},
			denied: []string{"deviceTemplate 0", "no node has 2147483647 units of " + h100RN},
		},
		{
			name:   "a template one unit larger than the largest node",
			extra:  []client.Object{class("big", gpuv1alpha1.GPUModePassthrough, h100RN, func(c *gpuv1alpha1.GPUClass) { c.Spec.NodeSelector = map[string]string{"gpu": "h100"} })},
			refs:   []model.DeviceRef{tmpl("big", 5)},
			denied: []string{"deviceTemplate 0", "no node has 5 units of " + h100RN + "; the most one has is 4"},
		},
		{name: "a template of a missing class", refs: []model.DeviceRef{tmpl("nope", 1)}, denied: []string{"GPUClass nope", "does not exist"}},
		{name: "a template of a deleted class", extra: []client.Object{deletedClass}, refs: []model.DeviceRef{tmpl("gone", 1)}, denied: []string{"GPUClass gone", "being deleted"}},
		{
			name:   "template devices above a budget together",
			extra:  []client.Object{tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/h100": "1"})},
			refs:   []model.DeviceRef{tmpl("h100", 1), tmpl("h100", 1)},
			denied: []string{"1 of GPUClass h100", "budget gpu.cozystack.io/h100 of tenant tenant-acme is 1 and 1 is in use"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, cluster(t, tt.extra...)...)
			before := listDevices(t, c)
			b := startBinder(t, c, placeOn(nodeA))
			u := vmi(t, vmiName, tt.refs...)
			if tt.vmi != nil {
				tt.vmi(u)
			}
			mustDeny(t, admitVMI(t, newHandler(c), u, false), tt.denied...)
			if after := listDevices(t, c); !reflect.DeepEqual(before, after) {
				t.Fatalf("a refusal wrote:\nbefore %+v\nafter  %+v", before, after)
			}
			if n := len(b.seen()); n != 0 {
				t.Fatalf("binder saw %d requests for a refused VMI", n)
			}
		})
	}
}

// fine is a held device that passes every check. Listed before a device
// that must be refused, it shows the refusal comes before any write, since
// a late refusal would have attached it first.
func fine() *gpuv1alpha1.GPUDevice {
	return device("fine", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)
}

func setGPUs(entries ...map[string]any) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		list := make([]any, 0, len(entries))
		for _, e := range entries {
			list = append(list, e)
		}
		_ = unstructured.SetNestedSlice(u.Object, list, "spec", "domain", "devices", "gpus")
	}
}

func TestVMIPasses(t *testing.T) {
	lostTemplate := device(model.TemplateDeviceName(vmiName, 0), "h100", 1, gpuv1alpha1.GPUDeviceLost, nodeA)
	lostTemplate.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	notReady := node("gpu-down", map[string]string{"gpu": "h100"}, map[string]int64{h100RN: 4})
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	cordoned := node("gpu-cordoned", map[string]string{"gpu": "h100"}, map[string]int64{h100RN: 4})
	cordoned.Spec.Unschedulable = true
	lost := metav1.Condition{Type: gpu.ConditionLostCapacity, Status: metav1.ConditionTrue, Reason: "NodeLostCapacity", LastTransitionTime: metav1.Now()}
	healthy := true
	partlyLost := gpuNode(nodeA, lost)
	partlyLost.Status.Devices = []gpuv1alpha1.GPUNodeDevice{
		{PCIAddress: "0000:41:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: h100RN, Healthy: &healthy},
		{PCIAddress: "0000:42:00.0", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: h100RN, Healthy: new(bool)},
	}

	tests := []struct {
		name  string
		extra []client.Object
		refs  []model.DeviceRef
		vmi   func(*unstructured.Unstructured)
	}{
		{
			// A Lost device already counts against budgets; reusing it is
			// not a new order.
			name:  "a reused Lost template device at an exact budget",
			extra: []client.Object{lostTemplate, tenantRelease(t, tenantNS, map[string]string{"gpu.cozystack.io/h100": "1"})},
			refs:  []model.DeviceRef{tmpl("h100", 1)},
		},
		{
			name: "a template as large as the largest node",
			refs: []model.DeviceRef{tmpl("h100", 4)},
		},
		{
			name:  "a NIC host device next to a GPU device",
			extra: []client.Object{device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
			vmi: func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{entry("rdma", "nvidia.com/hostdev")}, "spec", "domain", "devices", "hostDevices")
			},
		},
		{
			name:  "a device on a NotReady node is gathered, not refused",
			extra: []client.Object{notReady, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, "gpu-down")},
			refs:  []model.DeviceRef{held("train-0")},
		},
		{
			name:  "a device on a cordoned node is left to the binder",
			extra: []client.Object{cordoned, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, "gpu-cordoned")},
			refs:  []model.DeviceRef{held("train-0")},
		},
		{
			name:  "a device its node's healthy GPUs still cover",
			extra: []client.Object{partlyLost, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
		},
		{
			name:  "a class inside the permittedHostDevices table",
			extra: []client.Object{kubeVirt(h100RN), device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
		},
		{
			name:  "a KubeVirt CR without a permittedHostDevices table",
			extra: []client.Object{kubeVirt(), device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)},
			refs:  []model.DeviceRef{held("train-0")},
		},
		{
			name: "a device already attached to this VMI by an earlier refused create",
			extra: []client.Object{
				attached(device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName),
			},
			refs: []model.DeviceRef{held("train-0")},
		},
		{
			name:  "a pool device whose class mode matches the mode label",
			extra: []client.Object{device("v1", "l40s", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v")},
			refs:  []model.DeviceRef{held("v1")},
			vmi: func(u *unstructured.Unstructured) {
				l := u.GetLabels()
				l[labelMode] = "vgpu"
				u.SetLabels(l)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, cluster(t, tt.extra...)...)
			startBinder(t, c, placeOn(nodeA))
			u := vmi(t, vmiName, tt.refs...)
			if tt.vmi != nil {
				tt.vmi(u)
			}
			mustAllow(t, admitVMI(t, newHandler(c), u, false))
		})
	}
}

func TestVMICreatesTemplateDevices(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	b := startBinder(t, c, placeOn(nodeB))
	u := vmi(t, vmiName, held("train-0"), tmpl("h100", 2))

	resp := admitVMI(t, newHandler(c), u, false)
	mustAllow(t, resp)

	name := model.TemplateDeviceName(vmiName, 1)
	d := getDevice(t, c, name)
	if d.Labels[gpu.LabelTemplateDevice] != gpu.LabelValueTrue {
		t.Fatalf("template device labels = %v", d.Labels)
	}
	if d.Spec.ClassName != "h100" || d.Spec.Count != 2 || len(d.OwnerReferences) != 0 {
		t.Fatalf("template device = %+v", d)
	}
	if d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != vmiName || d.Status.NodeName != nodeB {
		t.Fatalf("template device status = %+v", d.Status)
	}
	if seen := b.seen(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Devices, []string{"train-0", name}) {
		t.Fatalf("requests = %+v, want one naming the whole set", seen)
	}
	want := []map[string]any{entry("gpudev-0-0", h100RN), entry("gpudev-1-0", h100RN), entry("gpudev-1-1", h100RN)}
	if g := gpusOf(t, applyPatch(t, u, resp)); !reflect.DeepEqual(g, want) {
		t.Fatalf("gpus = %v, want %v", g, want)
	}
}

func TestVMIReusesTemplateDevice(t *testing.T) {
	name := model.TemplateDeviceName(vmiName, 0)
	spare := device(name, "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA)
	spare.Labels = map[string]string{gpu.LabelTemplateDevice: gpu.LabelValueTrue}
	c := newClient(t, cluster(t, spare)...)
	startBinder(t, c, placeOn(nodeA))

	mustAllow(t, admitVMI(t, newHandler(c), vmi(t, vmiName, tmpl("h100", 1)), false))
	if n := len(listDevices(t, c)); n != 1 {
		t.Fatalf("%d devices, want the reused one only", n)
	}
	if d := getDevice(t, c, name); d.Status.AttachedTo == nil || d.Status.AttachedTo.VMI != vmiName {
		t.Fatalf("reused device not attached: %+v", d.Status)
	}
}

// The API server calls the webhook again after virt-api's mutator: the
// second call admits without a new device, attachment or request.
func TestVMIReinvocationIsIdempotent(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	b := startBinder(t, c, placeOn(nodeA))
	h := newHandler(c)
	u := vmi(t, vmiName, held("train-0"), tmpl("h100", 1))

	first := admitVMI(t, h, u, false)
	mustAllow(t, first)
	patched := applyPatch(t, u, first)
	after := listDevices(t, c)

	second := admitVMI(t, h, patched, false)
	mustAllow(t, second)
	if len(second.Patches) != 0 {
		t.Fatalf("reinvocation patched again: %v", second.Patches)
	}
	if again := listDevices(t, c); !reflect.DeepEqual(after, again) {
		t.Fatalf("reinvocation wrote:\nbefore %+v\nafter  %+v", after, again)
	}
	if n := len(b.seen()); n != 1 {
		t.Fatalf("binder saw %d requests, want 1", n)
	}
	if n := len(after); n != 2 {
		t.Fatalf("%d devices, want the held one and one template device", n)
	}
}

// A stale Succeeded request for a VMI of the same name, left by an earlier
// run of the VM, is not taken as this create's answer.
func TestVMIStaleRequestIsRenewed(t *testing.T) {
	d := attached(device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName)
	d.Status.BindRequest = &gpuv1alpha1.BindRequest{
		ID: "old", VMI: vmiName, Devices: []string{"train-0"},
		RequestedAt: metav1.NewTime(time.Now().Add(-time.Hour)), Result: gpuv1alpha1.BindSucceeded,
	}
	c := newClient(t, cluster(t, d)...)
	b := startBinder(t, c, placeOn(nodeB))

	mustAllow(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("train-0")), false))
	if n := len(b.seen()); n != 1 {
		t.Fatalf("binder saw %d requests, want a fresh one", n)
	}
	if got := getDevice(t, c, "train-0"); got.Status.NodeName != nodeB || got.Status.BindRequest.ID == "old" {
		t.Fatalf("device = %+v", got.Status)
	}
}

func TestVMIDryRunWritesNothing(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	b := startBinder(t, c, placeOn(nodeA))
	before := listDevices(t, c)
	u := vmi(t, vmiName, held("train-0"), tmpl("h100", 1))

	resp := admitVMI(t, newHandler(c), u, true)
	mustAllow(t, resp)
	if after := listDevices(t, c); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry run wrote:\nbefore %+v\nafter  %+v", before, after)
	}
	if n := len(b.seen()); n != 0 {
		t.Fatalf("dry run sent %d bind requests", n)
	}
	if g := gpusOf(t, applyPatch(t, u, resp)); len(g) != 2 {
		t.Fatalf("dry run gpus = %v, want the entries a real create gets", g)
	}
}

func TestVMIDryRunStillRefuses(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceLost, nodeA))...)
	mustDeny(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("train-0")), true), "is Lost")
}

func TestVMIBinderRefusalClearsWrites(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	startBinder(t, c, func([]*gpuv1alpha1.GPUDevice) (string, string) {
		return "", "GPUDevice train-0 (Bound, gpu-a): no Ready node has room for all of them"
	})

	mustDeny(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("train-0"), tmpl("h100", 1)), false),
		"no Ready node has room for all of them")
	d := getDevice(t, c, "train-0")
	if d.Status.AttachedTo != nil || d.Status.BindRequest != nil {
		t.Fatalf("refusal left %+v", d.Status)
	}
	if n := len(listDevices(t, c)); n != 1 {
		t.Fatalf("refusal left the template device: %d devices", n)
	}
}

func TestVMIBinderTimeoutClearsWrites(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	h := newHandler(c)
	h.BindTimeout = 50 * time.Millisecond

	mustDeny(t, admitVMI(t, h, vmi(t, vmiName, held("train-0"), tmpl("h100", 1)), false), "did not answer")
	d := getDevice(t, c, "train-0")
	if d.Status.AttachedTo != nil || d.Status.BindRequest != nil {
		t.Fatalf("timeout left %+v", d.Status)
	}
	if n := len(listDevices(t, c)); n != 1 {
		t.Fatalf("timeout left the template device: %d devices", n)
	}
}

// Two VMIs whose sets share a device start at once; at most one of them
// attaches it, and the loser leaves nothing behind.
func TestVMIConcurrentStartsShareNoDevice(t *testing.T) {
	for i := 0; i < 20; i++ {
		c := newClient(t, cluster(t,
			device("d1", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
			device("d2", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
			device("d3", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
		)...)
		startBinder(t, c, placeOn(nodeA))
		h := newHandler(c)
		vmis := map[string][]model.DeviceRef{"vmi-x": {held("d1"), held("d2")}, "vmi-y": {held("d2"), held("d3")}}
		results := map[string]admission.Response{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, refs := range vmis {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := admitVMI(t, h, vmi(t, name, refs...), false)
				mu.Lock()
				results[name] = r
				mu.Unlock()
			}()
		}
		wg.Wait()
		if results["vmi-x"].Allowed && results["vmi-y"].Allowed {
			t.Fatal("both VMIs admitted with a shared device")
		}
		winner := ""
		for name, r := range results {
			if r.Allowed {
				winner = name
			}
		}
		for _, d := range listDevices(t, c) {
			if a := d.Status.AttachedTo; a != nil && a.VMI != winner {
				t.Fatalf("refused VMI %s kept device %s", a.VMI, d.Name)
			}
		}
	}
}

// Clearing the tenant's vGPU flag keeps what it holds and refuses the
// next template device.
func TestVMIClearedEntitlement(t *testing.T) {
	objs := cluster(t, device("v1", "l40s", 1, gpuv1alpha1.GPUDeviceBound, "gpu-v"))
	for i, o := range objs {
		if ns, ok := o.(*corev1.Namespace); ok {
			objs[i] = namespace(ns.Name, false)
		}
	}
	c := newClient(t, objs...)
	startBinder(t, c, placeOn("gpu-v"))
	h := newHandler(c)

	mustAllow(t, admitVMI(t, h, vmi(t, "held-only", held("v1")), false))
	mustDeny(t, admitVMI(t, h, vmi(t, "with-template", tmpl("l40s", 1)), false), "GPUClass l40s", "not entitled to vGPU")
}

// A recent request answered for another set of devices is not this
// create's answer.
func TestVMIRequestForAnotherSetIsRenewed(t *testing.T) {
	d := attached(device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName)
	d.Status.BindRequest = &gpuv1alpha1.BindRequest{
		ID: "other-set", VMI: vmiName, Devices: []string{"train-0", "train-1"},
		RequestedAt: metav1.Now(), Result: gpuv1alpha1.BindSucceeded,
	}
	c := newClient(t, cluster(t, d)...)
	b := startBinder(t, c, placeOn(nodeA))

	mustAllow(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("train-0")), false))
	if n := len(b.seen()); n != 1 {
		t.Fatalf("binder saw %d requests, want a fresh one", n)
	}
}

// A refusal clears only what this call wrote: an attachment an earlier
// call made for the same VMI stays for the controller to expire.
func TestVMIRefusalKeepsEarlierAttachment(t *testing.T) {
	c := newClient(t, cluster(t, attached(device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName))...)
	startBinder(t, c, func([]*gpuv1alpha1.GPUDevice) (string, string) { return "", "no room" })

	mustDeny(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("train-0")), false), "no room")
	got := getDevice(t, c, "train-0")
	if got.Status.AttachedTo == nil || got.Status.BindRequest != nil {
		t.Fatalf("status = %+v, want the earlier attachment and no request", got.Status)
	}
}

func TestVMIRefusesASplitPlacement(t *testing.T) {
	c := newClient(t, cluster(t,
		device("d1", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
		device("d2", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA),
	)...)
	startBinder(t, c, placeOn(splitNodes))
	mustDeny(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("d1"), held("d2")), false), "more than one node")
}

// The KubeVirt CR is read through the manager's cache: the manager client
// reads unstructured objects live, so the table here exists only in the
// cache reader, and the refusal must still come from it.
func TestPermittedTableIsReadThroughTheCache(t *testing.T) {
	c := newClient(t, cluster(t, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	h := newHandler(c)
	h.Cache = newClient(t, kubeVirt(l40sRN))
	mustDeny(t, admitVMI(t, h, vmi(t, vmiName, held("train-0")), false), "permittedHostDevices")
}

// The controller's NotInPermittedHostDevices condition reads pciHostDevices
// and mediatedDevices only, and the refusal must agree with it.
func TestPermittedTableIgnoresUSB(t *testing.T) {
	kv := kubeVirt()
	_ = unstructured.SetNestedSlice(kv.Object, []any{map[string]any{"resourceName": h100RN}},
		"spec", "configuration", "permittedHostDevices", "usb")
	c := newClient(t, cluster(t, kv, device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA))...)
	mustDeny(t, admitVMI(t, newHandler(c), vmi(t, vmiName, held("train-0")), false), "permittedHostDevices")
}
