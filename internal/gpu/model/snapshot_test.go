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

package model

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

const (
	h100Name = corev1.ResourceName("nvidia.com/GH100_H100")
	l40sName = corev1.ResourceName("nvidia.com/NVIDIA_L40S-24C")
)

func classes() map[string]Class {
	return map[string]Class{
		"h100":     {Name: "h100", Mode: gpuv1alpha1.GPUModePassthrough, ResourceName: h100Name, NodeSelector: map[string]string{"gpu": "h100"}},
		"h100-alt": {Name: "h100-alt", Mode: gpuv1alpha1.GPUModePassthrough, ResourceName: h100Name, NodeSelector: map[string]string{"gpu": "h100-alt"}},
		"l40s-24c": {Name: "l40s-24c", Mode: gpuv1alpha1.GPUModeVGPU, ResourceName: l40sName, NodeSelector: map[string]string{"gpu": "l40s"}},
	}
}

func quota(kv ...any) corev1.ResourceList {
	out := corev1.ResourceList{}
	for i := 0; i < len(kv); i += 2 {
		out[corev1.ResourceName(kv[i].(string))] = resource.MustParse(kv[i+1].(string))
	}
	return out
}

func dev(ns, class string, count int64, phase gpuv1alpha1.GPUDevicePhase, node string) Device {
	return Device{Namespace: ns, Name: ns + "-" + class, Class: class, Count: count, Phase: phase, NodeName: node}
}

func refusalReason(err error) RefusalReason {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

func TestCheckOrderBudgets(t *testing.T) {
	tests := []struct {
		name       string
		quotas     map[string]corev1.ResourceList
		devices    []Device
		pods       []Pod
		ns, class  string
		count      int64
		entitled   bool
		wantReason RefusalReason
		wantKeyNS  string
	}{
		{
			name: "no GPU key anywhere is limited only by capacity",
			ns:   "tenant-acme-dev", class: "h100", count: 8,
		},
		{
			name:   "class key on the tenant fits",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/h100", "2")},
			ns:     "tenant-acme", class: "h100", count: 2,
		},
		{
			name:    "class key on the tenant exceeded",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/h100", "2")},
			devices: []Device{dev("tenant-acme", "h100", 1, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme", class: "h100", count: 2,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme",
		},
		{
			name:    "ancestor total counts the whole subtree",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "4")},
			devices: []Device{dev("tenant-acme-a", "h100", 2, gpuv1alpha1.GPUDeviceAttached, "n1"), dev("tenant-acme-b-c", "l40s-24c", 1, gpuv1alpha1.GPUDeviceLost, "")},
			ns:      "tenant-acme-d", class: "h100", count: 2,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme",
		},
		{
			name:    "pending devices do not count",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "4")},
			devices: []Device{dev("tenant-acme-a", "h100", 3, gpuv1alpha1.GPUDevicePending, "")},
			ns:      "tenant-acme-d", class: "h100", count: 4,
		},
		{
			name:    "devices outside the subtree do not count",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "4")},
			devices: []Device{dev("tenant-other", "h100", 4, gpuv1alpha1.GPUDeviceBound, "n1"), dev("tenant-acmex", "h100", 4, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme", class: "h100", count: 4,
		},
		{
			name:   "a child's larger key does not lift the ancestor's",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/h100", "1"), "tenant-acme-dev": quota("gpu.cozystack.io/h100", "10")},
			ns:     "tenant-acme-dev", class: "h100", count: 2,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme",
		},
		{
			name:   "a child key adds a limit",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/h100", "10"), "tenant-acme-dev": quota("gpu.cozystack.io/h100", "1")},
			ns:     "tenant-acme-dev", class: "h100", count: 2,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme-dev",
		},
		{
			name:    "sibling keys may overlap",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/h100", "4"), "tenant-acme-a": quota("gpu.cozystack.io/h100", "4"), "tenant-acme-b": quota("gpu.cozystack.io/h100", "4")},
			devices: []Device{dev("tenant-acme-b", "h100", 2, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme-a", class: "h100", count: 2,
		},
		{
			name:    "a class key does not limit another class",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/l40s-24c", "1")},
			devices: []Device{dev("tenant-acme", "l40s-24c", 1, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme", class: "h100", count: 3,
		},
		{
			name:    "an ancestor GPU key caps a child with its own cpu quota",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "1"), "tenant-acme-dev": quota("cpu", "8")},
			devices: []Device{dev("tenant-acme", "h100", 1, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme-dev", class: "h100", count: 1,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme",
		},
		{
			name:   "root keys cap every tenant",
			quotas: map[string]corev1.ResourceList{"tenant-root": quota("gpu.cozystack.io/total", "0")},
			ns:     "tenant-acme", class: "h100", count: 1,
			wantReason: ReasonBudget, wantKeyNS: "tenant-root",
		},
		{
			name:   "raw key without a GPU key blocks the class",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "4")},
			ns:     "tenant-acme-dev", class: "h100", count: 1,
			wantReason: ReasonRawKey, wantKeyNS: "tenant-acme",
		},
		{
			name:   "raw key does not block a class of another resource name",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "4")},
			ns:     "tenant-acme", class: "l40s-24c", count: 1, entitled: true,
		},
		{
			name:    "raw key with total caps devices and raw-name pods together",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "3", "gpu.cozystack.io/total", "10")},
			devices: []Device{dev("tenant-acme-a", "h100-alt", 1, gpuv1alpha1.GPUDeviceBound, "n2")},
			pods:    []Pod{{Namespace: "tenant-acme-b", NodeName: "n1", Units: map[corev1.ResourceName]int64{h100Name: 1}}},
			ns:      "tenant-acme", class: "h100", count: 2,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme",
		},
		{
			name:   "raw key with class key fits",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "3", "gpu.cozystack.io/h100", "3")},
			pods:   []Pod{{Namespace: "tenant-acme", NodeName: "n1", Units: map[corev1.ResourceName]int64{h100Name: 1}}},
			ns:     "tenant-acme", class: "h100", count: 2,
		},
		{
			name:   "raw key on an ancestor blocks until that ancestor has a GPU key",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "3"), "tenant-acme-dev": quota("gpu.cozystack.io/total", "3")},
			ns:     "tenant-acme-dev", class: "h100", count: 1,
			wantReason: ReasonRawKey, wantKeyNS: "tenant-acme",
		},
		{
			name: "vgpu class without entitlement",
			ns:   "tenant-acme", class: "l40s-24c", count: 1,
			wantReason: ReasonNotEntitled,
		},
		{
			name: "vgpu class with entitlement and no key",
			ns:   "tenant-acme", class: "l40s-24c", count: 1, entitled: true,
		},
		{
			name:    "total caps vgpu",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "1")},
			devices: []Device{dev("tenant-acme", "h100", 1, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme", class: "l40s-24c", count: 1, entitled: true,
			wantReason: ReasonBudget, wantKeyNS: "tenant-acme",
		},
		{
			name: "unknown class",
			ns:   "tenant-acme", class: "a100", count: 1,
			wantReason: ReasonClassNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Snapshot{Classes: classes(), Quotas: tc.quotas, Devices: tc.devices, Pods: tc.pods}
			err := s.CheckOrder(tc.ns, tc.class, tc.count, tc.entitled)
			if got := refusalReason(err); got != tc.wantReason {
				t.Fatalf("reason = %q, want %q (err %v)", got, tc.wantReason, err)
			}
			if err == nil {
				return
			}
			var r *Refusal
			errors.As(err, &r)
			if r.Namespace != tc.wantKeyNS {
				t.Errorf("refusing tenant = %q, want %q", r.Namespace, tc.wantKeyNS)
			}
			if msg := err.Error(); !strings.Contains(msg, tc.class) {
				t.Errorf("message %q does not name the class", msg)
			}
		})
	}
}

func TestRefusalMessageNamesClassCountAndLimit(t *testing.T) {
	s := &Snapshot{Classes: classes(), Quotas: map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/h100", "2")}}
	err := s.CheckOrder("tenant-acme", "h100", 3, false)
	if err == nil {
		t.Fatal("order above the budget was accepted")
	}
	for _, want := range []string{"h100", "3", "gpu.cozystack.io/h100", "2", "tenant-acme"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
}

func TestRemainingBudget(t *testing.T) {
	tests := []struct {
		name        string
		quotas      map[string]corev1.ResourceList
		devices     []Device
		pods        []Pod
		ns, class   string
		entitled    bool
		want        int64
		wantLimited bool
	}{
		{name: "no key is unlimited", ns: "tenant-acme", class: "h100"},
		{
			name:    "smallest over the walk",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "4"), "tenant-acme-dev": quota("gpu.cozystack.io/h100", "3")},
			devices: []Device{dev("tenant-acme-other", "l40s-24c", 2, gpuv1alpha1.GPUDeviceBound, "n1")},
			ns:      "tenant-acme-dev", class: "h100", want: 2, wantLimited: true,
		},
		{
			name:    "over budget clamps at zero",
			quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "1")},
			devices: []Device{dev("tenant-acme", "h100", 2, gpuv1alpha1.GPUDeviceLost, "")},
			ns:      "tenant-acme", class: "h100", want: 0, wantLimited: true,
		},
		{name: "vgpu without entitlement is zero", ns: "tenant-acme", class: "l40s-24c", want: 0, wantLimited: true},
		{name: "vgpu with entitlement and no key is unlimited", ns: "tenant-acme", class: "l40s-24c", entitled: true},
		{name: "unknown class is zero", ns: "tenant-acme", class: "a100", want: 0, wantLimited: true},
		{
			name:   "raw key alone blocks",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "4")},
			ns:     "tenant-acme", class: "h100", want: 0, wantLimited: true,
		},
		{
			name:   "raw key with total counts raw-name pods",
			quotas: map[string]corev1.ResourceList{"tenant-acme": quota(string(h100Name), "4", "gpu.cozystack.io/total", "10")},
			pods:   []Pod{{Namespace: "tenant-acme-x", Units: map[corev1.ResourceName]int64{h100Name: 3}}},
			ns:     "tenant-acme", class: "h100", want: 1, wantLimited: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Snapshot{Classes: classes(), Quotas: tc.quotas, Devices: tc.devices, Pods: tc.pods}
			got, limited := s.RemainingBudget(tc.ns, tc.class, tc.entitled)
			if got != tc.want || limited != tc.wantLimited {
				t.Errorf("RemainingBudget = (%d, %v), want (%d, %v)", got, limited, tc.want, tc.wantLimited)
			}
		})
	}
}

// CheckOrder and RemainingBudget answer the same question; an order of
// exactly the remaining budget fits and one more does not.
func TestRemainingBudgetAgreesWithCheckOrder(t *testing.T) {
	s := &Snapshot{
		Classes: classes(),
		Quotas:  map[string]corev1.ResourceList{"tenant-acme": quota("gpu.cozystack.io/total", "5", string(h100Name), "4"), "tenant-acme-dev": quota("gpu.cozystack.io/h100", "3")},
		Devices: []Device{dev("tenant-acme", "l40s-24c", 1, gpuv1alpha1.GPUDeviceBound, "n1"), dev("tenant-acme-dev", "h100", 1, gpuv1alpha1.GPUDeviceAttached, "n1")},
		Pods:    []Pod{{Namespace: "tenant-acme-x", Units: map[corev1.ResourceName]int64{h100Name: 1}}},
	}
	left, limited := s.RemainingBudget("tenant-acme-dev", "h100", false)
	if !limited || left != 2 {
		t.Fatalf("RemainingBudget = (%d, %v), want (2, true)", left, limited)
	}
	if err := s.CheckOrder("tenant-acme-dev", "h100", left, false); err != nil {
		t.Errorf("order of the remaining budget refused: %v", err)
	}
	if err := s.CheckOrder("tenant-acme-dev", "h100", left+1, false); err == nil {
		t.Error("order above the remaining budget accepted")
	}
}

func node(name string, labels map[string]string, eligible bool, alloc map[corev1.ResourceName]int64) Node {
	return Node{Name: name, Labels: labels, Eligible: eligible, Allocatable: alloc}
}

func TestCapacity(t *testing.T) {
	h100 := map[string]string{"gpu": "h100"}
	s := &Snapshot{
		Classes: classes(),
		Nodes: []Node{
			node("n1", h100, true, map[corev1.ResourceName]int64{h100Name: 8}),
			node("n2", h100, true, map[corev1.ResourceName]int64{h100Name: 4}),
			node("n3", h100, false, map[corev1.ResourceName]int64{h100Name: 8}),
			node("n4", map[string]string{"gpu": "l40s"}, true, map[corev1.ResourceName]int64{l40sName: 16, h100Name: 8}),
		},
		Devices: []Device{
			dev("tenant-a", "h100", 2, gpuv1alpha1.GPUDeviceBound, "n1"),
			dev("tenant-b", "h100", 1, gpuv1alpha1.GPUDeviceAttached, "n1"),
			dev("tenant-c", "h100", 4, gpuv1alpha1.GPUDeviceLost, "n1"),
			dev("tenant-d", "h100", 4, gpuv1alpha1.GPUDevicePending, ""),
			dev("tenant-e", "h100", 1, gpuv1alpha1.GPUDeviceBound, "n2"),
		},
		Pods: []Pod{
			{Namespace: "tenant-f", NodeName: "n1", Units: map[corev1.ResourceName]int64{h100Name: 1}},
			{Namespace: "tenant-g", NodeName: "n2", Units: map[corev1.ResourceName]int64{h100Name: 5}},
		},
	}
	if got := s.FreeOnNode("h100", "n1"); got != 4 {
		t.Errorf("FreeOnNode(n1) = %d, want 4 (8 - 3 bound - 1 raw pod)", got)
	}
	if got := s.FreeOnNode("h100", "n2"); got != 0 {
		t.Errorf("FreeOnNode(n2) = %d, want 0 (clamped)", got)
	}
	free, maxFree := s.ClassCapacity("h100")
	if free != 4 || maxFree != 4 {
		t.Errorf("ClassCapacity(h100) = (%d, %d), want (4, 4): ineligible n3 and unmatched n4 must not count", free, maxFree)
	}
	if got := s.FreeOnNode("h100", "n3"); got != 0 {
		t.Errorf("FreeOnNode(ineligible n3) = %d, want 0", got)
	}
	if got := s.FreeOnNode("h100", "n4"); got != 0 {
		t.Errorf("FreeOnNode(unmatched n4) = %d, want 0", got)
	}
}

func TestResourceNameConflictTakesTheNodeOutOfBothClasses(t *testing.T) {
	s := &Snapshot{
		Classes: classes(),
		Nodes: []Node{
			node("both", map[string]string{"gpu": "h100"}, true, map[corev1.ResourceName]int64{h100Name: 8}),
			node("alt", map[string]string{"gpu": "h100-alt"}, true, map[corev1.ResourceName]int64{h100Name: 2}),
		},
	}
	if got := s.Conflicts("both"); len(got) != 0 {
		t.Fatalf("Conflicts(both) = %v before the overlap", got)
	}
	s.Classes["h100-alt"] = Class{Name: "h100-alt", Mode: gpuv1alpha1.GPUModePassthrough, ResourceName: h100Name, NodeSelector: map[string]string{}}
	got := s.Conflicts("both")
	if len(got) != 2 || got[0] != "h100" || got[1] != "h100-alt" {
		t.Errorf("Conflicts(both) = %v, want [h100 h100-alt]", got)
	}
	if s.Serves("h100", "both") || s.Serves("h100-alt", "both") {
		t.Error("a conflicted node still serves a class")
	}
	if free, _ := s.ClassCapacity("h100"); free != 0 {
		t.Errorf("h100 free = %d, want 0", free)
	}
	if free, _ := s.ClassCapacity("h100-alt"); free != 2 {
		t.Errorf("h100-alt free = %d, want 2 from the unconflicted node", free)
	}
}

func TestEmptyNodeSelectorMatchesEveryNode(t *testing.T) {
	s := &Snapshot{
		Classes: map[string]Class{"any": {Name: "any", ResourceName: h100Name}},
		Nodes:   []Node{node("n1", map[string]string{"x": "y"}, true, map[corev1.ResourceName]int64{h100Name: 1})},
	}
	if !s.Serves("any", "n1") {
		t.Error("a class without nodeSelector does not serve n1")
	}
}
