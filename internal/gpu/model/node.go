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
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
)

// Cordoned reports whether the node is cordoned. Only spec.unschedulable
// counts: a taint, such as the one Fence Agents Remediation sets, is not a
// cordon and goes through UntoleratedTaint instead.
func Cordoned(n *corev1.Node) bool {
	return n.Spec.Unschedulable
}

// Ready reports whether the node's Ready condition is True.
func Ready(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// mirrorsCordonOrReadiness reports whether the taint is one Kubernetes
// sets for a cordon or a node that is not Ready, which Cordoned and Ready
// already decide: the cordon taint as NoSchedule, the readiness taints with
// either effect. The node lifecycle controller removes these forms from a
// schedulable Ready node, a hand-applied one included, so seeing one there
// is a passing state. It never sets the cordon taint as NoExecute, so that
// one counts like any other taint.
func mirrorsCordonOrReadiness(t *corev1.Taint) bool {
	switch t.Key {
	case corev1.TaintNodeUnschedulable:
		return t.Effect == corev1.TaintEffectNoSchedule
	case corev1.TaintNodeNotReady, corev1.TaintNodeUnreachable:
		return true
	}
	return false
}

// UntoleratedTaint returns the first NoSchedule or NoExecute taint of the
// node that class c does not tolerate indefinitely, or nil, skipping the
// taints that mirror a cordon or readiness. Gt and Lt tolerations never match: where
// their feature gate is off the scheduler does not honour them, and a
// unit is better left out of free than counted in vain.
func UntoleratedTaint(n *corev1.Node, c Class) *corev1.Taint {
	return untoleratedTaint(n.Spec.Taints, c.Tolerations)
}

func untoleratedTaint(taints []corev1.Taint, tolerations []corev1.Toleration) *corev1.Taint {
	for i := range taints {
		taint := &taints[i]
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if mirrorsCordonOrReadiness(taint) {
			continue
		}
		tolerated := false
		for j := range tolerations {
			if taint.Effect == corev1.TaintEffectNoExecute && tolerations[j].TolerationSeconds != nil {
				continue
			}
			if tolerations[j].ToleratesTaint(logr.Discard(), taint, false) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return taint
		}
	}
	return nil
}

// Node is the part of a Node that capacity reads.
type Node struct {
	Name        string
	Allocatable map[corev1.ResourceName]int64
	// Usable is false for a cordoned or not Ready node, which adds nothing
	// to any class whatever its taints.
	Usable bool
	Taints []corev1.Taint
}

func NodeFrom(n *corev1.Node) Node {
	alloc := make(map[corev1.ResourceName]int64, len(n.Status.Allocatable))
	for k, v := range n.Status.Allocatable {
		alloc[k] = v.Value()
	}
	return Node{Name: n.Name, Allocatable: alloc, Usable: !Cordoned(n) && Ready(n), Taints: n.Spec.Taints}
}

// FreeOnNode returns the free units of class c on node n: its allocatable
// count of the class's resource name minus what the pods scheduled there
// request of it, never below zero. A cordoned or not Ready node, or one
// with a taint the class does not tolerate, has none.
func FreeOnNode(c Class, n Node, pods []Pod) int64 {
	if !n.Usable || untoleratedTaint(n.Taints, c.Tolerations) != nil {
		return 0
	}
	free := n.Allocatable[c.ResourceName]
	for _, p := range pods {
		if p.NodeName == n.Name {
			free -= p.Units[c.ResourceName]
		}
	}
	return max(free, 0)
}
