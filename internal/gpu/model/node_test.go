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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func readyNode(mutate func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	n.Name = "gpu-node-3"
	if mutate != nil {
		mutate(n)
	}
	return n
}

func withTaint(key string, effect corev1.TaintEffect) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Effect: effect})
	}
}

const (
	farTaint       = "medik8s.io/fence-agents-remediation"
	dedicatedTaint = "nvidia.com/gpu"
)

var tolerateDedicated = []corev1.Toleration{{Key: dedicatedTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}

func TestCordoned(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{"unschedulable", readyNode(func(n *corev1.Node) { n.Spec.Unschedulable = true }), true},
		{"schedulable", readyNode(nil), false},
		{"Fence Agents Remediation taint alone", readyNode(withTaint(farTaint, corev1.TaintEffectNoSchedule)), false},
		{"unschedulable taint without the field", readyNode(withTaint(corev1.TaintNodeUnschedulable, corev1.TaintEffectNoSchedule)), false},
	}
	for _, tc := range tests {
		if got := Cordoned(tc.node); got != tc.want {
			t.Errorf("%s: Cordoned = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{"ready", readyNode(nil), true},
		{"not ready", readyNode(func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse }), false},
		{"unknown", readyNode(func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionUnknown }), false},
		{"no condition", readyNode(func(n *corev1.Node) { n.Status.Conditions = nil }), false},
	}
	for _, tc := range tests {
		if got := Ready(tc.node); got != tc.want {
			t.Errorf("%s: Ready = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUntoleratedTaint(t *testing.T) {
	tolerateFAR := []corev1.Toleration{{Key: farTaint, Operator: corev1.TolerationOpExists}}
	tests := []struct {
		name        string
		node        *corev1.Node
		tolerations []corev1.Toleration
		want        string
	}{
		{name: "untainted", node: readyNode(nil)},
		{name: "untolerated NoSchedule", node: readyNode(withTaint(farTaint, corev1.TaintEffectNoSchedule)), want: farTaint},
		{name: "untolerated NoExecute", node: readyNode(withTaint("example.com/maintenance", corev1.TaintEffectNoExecute)), want: "example.com/maintenance"},
		{name: "tolerated NoSchedule", node: readyNode(withTaint(farTaint, corev1.TaintEffectNoSchedule)), tolerations: tolerateFAR},
		{name: "PreferNoSchedule does not count", node: readyNode(withTaint("example.com/soft", corev1.TaintEffectPreferNoSchedule))},
		// These taints mirror cordon and readiness, which the field and the
		// condition already decide.
		{name: "node.kubernetes.io/unschedulable", node: readyNode(withTaint(corev1.TaintNodeUnschedulable, corev1.TaintEffectNoSchedule))},
		// Kubernetes sets the cordon taint only as NoSchedule, so a NoExecute
		// one is a hoster's taint like any other.
		{name: "node.kubernetes.io/unschedulable as NoExecute", node: readyNode(withTaint(corev1.TaintNodeUnschedulable, corev1.TaintEffectNoExecute)), want: corev1.TaintNodeUnschedulable},
		{name: "node.kubernetes.io/not-ready", node: readyNode(withTaint(corev1.TaintNodeNotReady, corev1.TaintEffectNoExecute))},
		{name: "node.kubernetes.io/unreachable", node: readyNode(withTaint(corev1.TaintNodeUnreachable, corev1.TaintEffectNoExecute))},
		{name: "node.kubernetes.io/not-ready as NoSchedule", node: readyNode(withTaint(corev1.TaintNodeNotReady, corev1.TaintEffectNoSchedule))},
		{name: "node.kubernetes.io/unreachable as NoSchedule", node: readyNode(withTaint(corev1.TaintNodeUnreachable, corev1.TaintEffectNoSchedule))},
		// Pressure, network and out-of-service taints have no field or
		// condition here that decides them, so they count like any other.
		{name: "disk-pressure", node: readyNode(withTaint(corev1.TaintNodeDiskPressure, corev1.TaintEffectNoSchedule)), want: corev1.TaintNodeDiskPressure},
		{name: "network-unavailable", node: readyNode(withTaint(corev1.TaintNodeNetworkUnavailable, corev1.TaintEffectNoSchedule)), want: corev1.TaintNodeNetworkUnavailable},
		{name: "out-of-service", node: readyNode(withTaint(corev1.TaintNodeOutOfService, corev1.TaintEffectNoExecute)), want: corev1.TaintNodeOutOfService},
		{
			name: "Gt toleration does not count",
			node: readyNode(func(n *corev1.Node) {
				n.Spec.Taints = []corev1.Taint{{Key: "example.com/gpu-gen", Value: "5", Effect: corev1.TaintEffectNoSchedule}}
			}),
			tolerations: []corev1.Toleration{{Key: "example.com/gpu-gen", Operator: corev1.TolerationOpGt, Value: "3", Effect: corev1.TaintEffectNoSchedule}},
			want:        "example.com/gpu-gen",
		},
		{name: "toleration of another effect", node: readyNode(withTaint(farTaint, corev1.TaintEffectNoExecute)), tolerations: []corev1.Toleration{{Key: farTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}, want: farTaint},
		{
			name: "first untolerated taint after a tolerated one",
			node: readyNode(func(n *corev1.Node) {
				withTaint(dedicatedTaint, corev1.TaintEffectNoSchedule)(n)
				withTaint(farTaint, corev1.TaintEffectNoSchedule)(n)
			}),
			tolerations: tolerateDedicated,
			want:        farTaint,
		},
	}
	for _, tc := range tests {
		got := UntoleratedTaint(tc.node, Class{Name: "h100", Tolerations: tc.tolerations})
		var key string
		if got != nil {
			key = got.Key
		}
		if key != tc.want {
			t.Errorf("%s: UntoleratedTaint = %q, want %q", tc.name, key, tc.want)
		}
	}
}

func TestEnduringToleration(t *testing.T) {
	finite, zero, negative := int64(300), int64(0), int64(-1)
	for _, tc := range []struct {
		name    string
		seconds *int64
	}{
		{"finite", &finite}, {"zero", &zero}, {"negative", &negative}, {"enduring", nil},
	} {
		for _, effect := range []corev1.TaintEffect{corev1.TaintEffectNoExecute, corev1.TaintEffectNoSchedule} {
			t.Run(tc.name+"/"+string(effect), func(t *testing.T) {
				taint := corev1.Taint{Key: dedicatedTaint, Effect: effect}
				toleration := corev1.Toleration{Key: dedicatedTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: tc.seconds}
				c := Class{ResourceName: h100Name, Tolerations: []corev1.Toleration{toleration}}
				n := Node{Name: "gpu-node-3", Usable: true, Allocatable: map[corev1.ResourceName]int64{h100Name: 4}, Taints: []corev1.Taint{taint}}
				want := int64(0)
				if effect == corev1.TaintEffectNoExecute && tc.seconds == nil {
					want = 4
				}
				if got := FreeOnNode(c, n, nil); got != want {
					t.Errorf("FreeOnNode = %d, want %d", got, want)
				}
				node := readyNode(withTaint(dedicatedTaint, effect))
				if got := UntoleratedTaint(node, c); (got == nil) != (want > 0) {
					t.Errorf("UntoleratedTaint = %v, want tolerated %v", got, want > 0)
				}
			})
		}
	}
	for _, effect := range []corev1.TaintEffect{corev1.TaintEffectNoExecute, corev1.TaintEffectNoSchedule} {
		for _, finiteFirst := range []bool{true, false} {
			finiteToleration := corev1.Toleration{Key: dedicatedTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &finite}
			enduring := corev1.Toleration{Key: dedicatedTaint, Operator: corev1.TolerationOpExists}
			tolerations := []corev1.Toleration{finiteToleration, enduring}
			if !finiteFirst {
				tolerations[0], tolerations[1] = tolerations[1], tolerations[0]
			}
			n := Node{Usable: true, Allocatable: map[corev1.ResourceName]int64{h100Name: 4}, Taints: []corev1.Taint{{Key: dedicatedTaint, Effect: effect}}}
			if got := FreeOnNode(Class{ResourceName: h100Name, Tolerations: tolerations}, n, nil); got != 4 {
				t.Errorf("finiteFirst=%v effect=%s: FreeOnNode = %d, want 4", finiteFirst, effect, got)
			}
		}
	}
}

func TestNodeFrom(t *testing.T) {
	n := readyNode(func(n *corev1.Node) {
		n.Status.Allocatable = corev1.ResourceList{h100Name: resource.MustParse("8")}
		n.Spec.Taints = []corev1.Taint{{Key: dedicatedTaint, Effect: corev1.TaintEffectNoSchedule}}
	})
	got := NodeFrom(n)
	if got.Name != "gpu-node-3" || got.Allocatable[h100Name] != 8 || !got.Usable || !reflect.DeepEqual(got.Taints, n.Spec.Taints) {
		t.Errorf("NodeFrom = %+v", got)
	}
	for name, mutate := range map[string]func(*corev1.Node){
		"cordoned":  func(n *corev1.Node) { n.Spec.Unschedulable = true },
		"not ready": func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse },
	} {
		if NodeFrom(readyNode(mutate)).Usable {
			t.Errorf("NodeFrom of a %s node is usable", name)
		}
	}
}

func TestFreeOnNode(t *testing.T) {
	h100 := Class{Name: "h100", ResourceName: h100Name, Tolerations: tolerateDedicated}
	node := func(mutate func(*corev1.Node)) Node {
		return NodeFrom(readyNode(func(n *corev1.Node) {
			n.Status.Allocatable = corev1.ResourceList{h100Name: resource.MustParse("4"), l40sName: resource.MustParse("16")}
			if mutate != nil {
				mutate(n)
			}
		}))
	}
	pod := func(nodeName string, units int64) Pod {
		return Pod{Namespace: "tenant-acme", NodeName: nodeName, Units: map[corev1.ResourceName]int64{h100Name: units}}
	}
	tests := []struct {
		name  string
		class Class
		node  Node
		pods  []Pod
		want  int64
	}{
		{name: "allocatable when nothing runs", class: h100, node: node(nil), want: 4},
		{name: "minus pod requests on the node", class: h100, node: node(nil), pods: []Pod{pod("gpu-node-3", 1), pod("gpu-node-3", 2)}, want: 1},
		{name: "pods on other nodes do not count", class: h100, node: node(nil), pods: []Pod{pod("gpu-node-4", 3)}, want: 4},
		{name: "unscheduled pods do not count", class: h100, node: node(nil), pods: []Pod{pod("", 3)}, want: 4},
		{name: "other resource names do not count", class: h100, node: node(nil), pods: []Pod{{NodeName: "gpu-node-3", Units: map[corev1.ResourceName]int64{l40sName: 8}}}, want: 4},
		{name: "never below zero", class: h100, node: node(nil), pods: []Pod{pod("gpu-node-3", 6)}, want: 0},
		{name: "cordoned", class: h100, node: node(func(n *corev1.Node) { n.Spec.Unschedulable = true }), want: 0},
		{name: "not ready", class: h100, node: node(func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse }), want: 0},
		{name: "untolerated taint", class: h100, node: node(withTaint(farTaint, corev1.TaintEffectNoSchedule)), want: 0},
		{name: "tolerated taint", class: h100, node: node(withTaint(dedicatedTaint, corev1.TaintEffectNoSchedule)), want: 4},
		{name: "taint mirroring readiness", class: h100, node: node(withTaint(corev1.TaintNodeNotReady, corev1.TaintEffectNoExecute)), want: 4},
		{name: "node that does not advertise the name", class: Class{Name: "a100", ResourceName: "nvidia.com/GA100_A100"}, node: node(nil), want: 0},
		{name: "class without a resource name", class: Class{Name: "x"}, node: node(nil), want: 0},
	}
	for _, tc := range tests {
		if got := FreeOnNode(tc.class, tc.node, tc.pods); got != tc.want {
			t.Errorf("%s: FreeOnNode = %d, want %d", tc.name, got, tc.want)
		}
	}
}
