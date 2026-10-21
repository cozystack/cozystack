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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
)

const vmiUID = types.UID("vmi-uid")

func launcher(affinity *corev1.Affinity) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "virt-launcher-" + vmiName + "-x7k2p", Namespace: tenantNS,
			Labels: map[string]string{"kubevirt.io": "virt-launcher", gpu.LabelDeviceReference: gpu.LabelValueTrue},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmiName, UID: vmiUID, Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{
			Affinity:   affinity,
			Containers: []corev1.Container{{Name: "compute"}},
		},
	}
}

func pin(n string) corev1.NodeSelectorRequirement {
	return corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{n}}
}

func pinnedPod(t *testing.T, pod *corev1.Pod, resp admission.Response) *corev1.Pod {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(raw, &u.Object); err != nil {
		t.Fatal(err)
	}
	patched, err := json.Marshal(applyPatch(t, u, resp).Object)
	if err != nil {
		t.Fatal(err)
	}
	out := &corev1.Pod{}
	if err := json.Unmarshal(patched, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func requiredTerms(p *corev1.Pod) []corev1.NodeSelectorTerm {
	if p.Spec.Affinity == nil || p.Spec.Affinity.NodeAffinity == nil ||
		p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return nil
	}
	return p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
}

func TestPodPin(t *testing.T) {
	zone := corev1.NodeSelectorRequirement{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"z1"}}
	pool := corev1.NodeSelectorRequirement{Key: "node-pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu"}}
	required := func(terms ...corev1.NodeSelectorTerm) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms},
		}}
	}

	tests := []struct {
		name     string
		affinity *corev1.Affinity
		want     []corev1.NodeSelectorTerm
	}{
		{
			name: "a pod without affinity gets the pin as its only term",
			want: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{pin(nodeA)}}},
		},
		{
			// LiveUpdate rewrites the VMI's affinity from the VM template; the
			// launcher pod renders it, and the pin joins every term.
			name:     "the pin joins every term of an affinity LiveUpdate synced",
			affinity: required(corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{zone}}, corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{pool}}),
			want: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{zone}, MatchFields: []corev1.NodeSelectorRequirement{pin(nodeA)}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{pool}, MatchFields: []corev1.NodeSelectorRequirement{pin(nodeA)}},
			},
		},
		{
			name:     "a pod with only preferred node affinity gets a required term",
			affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1, Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{zone}}}}}},
			want:     []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{pin(nodeA)}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, attached(device("train-0", "h100", 2, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName))
			h := newHandler(c)
			pod := launcher(tt.affinity)
			pod.Spec.SchedulerName = "tenant-scheduler"

			resp := h.PinPod(context.Background(), createRequest(t, pod, false))
			mustAllow(t, resp)
			got := pinnedPod(t, pod, resp)
			if terms := requiredTerms(got); !reflect.DeepEqual(terms, tt.want) {
				t.Fatalf("terms = %+v\nwant    %+v", terms, tt.want)
			}
			if got.Spec.NodeName != "" || got.Spec.SchedulerName != "tenant-scheduler" {
				t.Fatalf("pin touched nodeName or schedulerName: %q %q", got.Spec.NodeName, got.Spec.SchedulerName)
			}

			again := h.PinPod(context.Background(), createRequest(t, got, false))
			mustAllow(t, again)
			if len(again.Patches) != 0 {
				t.Fatalf("reinvocation patched again: %v", again.Patches)
			}
		})
	}
}

func TestPodPinRefusals(t *testing.T) {
	older := attached(device("old-template", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeB), vmiName)
	older.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmiName, UID: "older-uid"}}
	tests := []struct {
		name   string
		objs   []client.Object
		denied []string
	}{
		{name: "no device attached to the VMI", denied: []string{"VMI " + vmiName, "no GPUDevice"}},
		{
			name: "devices on two nodes",
			objs: []client.Object{
				attached(device("d1", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName),
				attached(device("d2", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeB), vmiName),
			},
			denied: []string{"d1", "d2", nodeA, nodeB},
		},
		{
			name:   "a device that holds no node",
			objs:   []client.Object{attached(device("d1", "h100", 1, gpuv1alpha1.GPUDeviceLost, nodeA), vmiName)},
			denied: []string{"GPUDevice d1", "Lost"},
		},
		{
			name:   "only a template device of an earlier VMI",
			objs:   []client.Object{older},
			denied: []string{"no GPUDevice"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(newClient(t, tt.objs...))
			mustDeny(t, h.PinPod(context.Background(), createRequest(t, launcher(nil), false)), tt.denied...)
		})
	}
}

func TestPodPinSkipsEarlierVMIsTemplateDevice(t *testing.T) {
	older := attached(device("old-template", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeB), vmiName)
	older.OwnerReferences = []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance", Name: vmiName, UID: "older-uid"}}
	c := newClient(t, older, attached(device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceAttached, nodeA), vmiName))
	pod := launcher(nil)

	resp := newHandler(c).PinPod(context.Background(), createRequest(t, pod, false))
	mustAllow(t, resp)
	want := []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{pin(nodeA)}}}
	if terms := requiredTerms(pinnedPod(t, pod, resp)); !reflect.DeepEqual(terms, want) {
		t.Fatalf("terms = %+v, want %+v", terms, want)
	}
}

// With two replicas the pod may reach one whose cache has the attachment
// but not yet the binder's answer, which can move the device; the pin
// follows the API server.
func TestPodPinReadsPastAStaleCache(t *testing.T) {
	stale := attached(device("train-0", "h100", 1, gpuv1alpha1.GPUDeviceBound, nodeA), vmiName)
	stale.Status.BindRequest = &gpuv1alpha1.BindRequest{ID: "r", VMI: vmiName, Devices: []string{"train-0"}, RequestedAt: metav1.Now()}
	answered := stale.DeepCopy()
	answered.Status.NodeName = nodeB
	answered.Status.BindRequest.Result = gpuv1alpha1.BindSucceeded

	h := newHandler(newClient(t, stale))
	h.Reader = newClient(t, answered)
	pod := launcher(nil)
	resp := h.PinPod(context.Background(), createRequest(t, pod, false))
	mustAllow(t, resp)
	want := []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{pin(nodeB)}}}
	if terms := requiredTerms(pinnedPod(t, pod, resp)); !reflect.DeepEqual(terms, want) {
		t.Fatalf("terms = %+v, want %+v", terms, want)
	}
}
