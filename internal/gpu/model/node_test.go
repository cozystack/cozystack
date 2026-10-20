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
	"testing"

	corev1 "k8s.io/api/core/v1"
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

const farTaint = "medik8s.io/fence-agents-remediation"

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

func TestEligible(t *testing.T) {
	tolerateFAR := []corev1.Toleration{{Key: farTaint, Operator: corev1.TolerationOpExists}}
	tests := []struct {
		name        string
		node        *corev1.Node
		tolerations []corev1.Toleration
		want        bool
	}{
		{name: "ready, schedulable, untainted", node: readyNode(nil), want: true},
		{name: "cordoned", node: readyNode(func(n *corev1.Node) { n.Spec.Unschedulable = true }), want: false},
		{name: "not ready", node: readyNode(func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse }), want: false},
		{name: "untolerated NoSchedule", node: readyNode(withTaint(farTaint, corev1.TaintEffectNoSchedule)), want: false},
		{name: "untolerated NoExecute", node: readyNode(withTaint("example.com/maintenance", corev1.TaintEffectNoExecute)), want: false},
		{name: "tolerated NoSchedule", node: readyNode(withTaint(farTaint, corev1.TaintEffectNoSchedule)), tolerations: tolerateFAR, want: true},
		{name: "PreferNoSchedule does not count", node: readyNode(withTaint("example.com/soft", corev1.TaintEffectPreferNoSchedule)), want: true},
		// The node.kubernetes.io taints mirror cordon and readiness, which
		// the field and the condition already decide.
		{name: "node.kubernetes.io/unschedulable taint on a schedulable node", node: readyNode(withTaint(corev1.TaintNodeUnschedulable, corev1.TaintEffectNoSchedule)), want: true},
		{name: "node.kubernetes.io/not-ready taint on a ready node", node: readyNode(withTaint(corev1.TaintNodeNotReady, corev1.TaintEffectNoExecute)), want: true},
		{
			name: "Gt toleration does not count",
			node: readyNode(func(n *corev1.Node) {
				n.Spec.Taints = []corev1.Taint{{Key: "example.com/gpu-gen", Value: "5", Effect: corev1.TaintEffectNoSchedule}}
			}),
			tolerations: []corev1.Toleration{{Key: "example.com/gpu-gen", Operator: corev1.TolerationOpGt, Value: "3", Effect: corev1.TaintEffectNoSchedule}},
			want:        false,
		},
		{name: "toleration of another effect", node: readyNode(withTaint(farTaint, corev1.TaintEffectNoExecute)), tolerations: []corev1.Toleration{{Key: farTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}, want: false},
	}
	for _, tc := range tests {
		if got := Eligible(tc.node, tc.tolerations); got != tc.want {
			t.Errorf("%s: Eligible = %v, want %v", tc.name, got, tc.want)
		}
	}
}
