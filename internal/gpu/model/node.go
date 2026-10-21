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

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// Cordoned reports whether the node is cordoned. Only spec.unschedulable
// counts: a taint, such as the one Fence Agents Remediation sets, is not a
// cordon and does not trigger the class's onCordon policy.
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

// mirrorsCordonOrReadiness reports whether Kubernetes sets the taint for a
// cordon or a node that is not Ready, which Cordoned and Ready already
// decide.
func mirrorsCordonOrReadiness(key string) bool {
	switch key {
	case corev1.TaintNodeUnschedulable, corev1.TaintNodeNotReady, corev1.TaintNodeUnreachable:
		return true
	}
	return false
}

// UntoleratedTaint returns the first NoSchedule or NoExecute taint that
// tolerations do not tolerate, or nil, skipping the taints that mirror a
// cordon or readiness. Gt and Lt tolerations never match: where their
// feature gate is off the scheduler does not honour them, and a node is
// better skipped than bound to in vain.
func UntoleratedTaint(n *corev1.Node, tolerations []corev1.Toleration) *corev1.Taint {
	for i := range n.Spec.Taints {
		taint := &n.Spec.Taints[i]
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if mirrorsCordonOrReadiness(taint.Key) {
			continue
		}
		tolerated := false
		for j := range tolerations {
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

// Eligible reports whether new binds, moves, rebinds and gathers may use
// the node, and whether its units count in a class's capacity.
// tolerations are those of the virt-launcher pod, nil when no VMI is
// known.
func Eligible(n *corev1.Node, tolerations []corev1.Toleration) bool {
	return !Cordoned(n) && Ready(n) && UntoleratedTaint(n, tolerations) == nil
}

// ServedUnits is how many units of rn the node can serve. The agent's
// functions that carry rn count with kubelet's allocatable count for them,
// or one each when healthy and without a count: kubelet lists only healthy
// plugin devices, so one bad mdev slot does not take its parent's other
// slots with it. The node's allocatable caps that sum, since a plugin that
// names a device by IOMMU group reports it on every function of the group,
// and judges alone a name no function carries.
func ServedUnits(n *corev1.Node, devices []gpuv1alpha1.GPUNodeDevice, rn corev1.ResourceName) int64 {
	var served int64
	mapped := false
	for _, d := range devices {
		if corev1.ResourceName(d.ResourceName) != rn {
			continue
		}
		mapped = true
		switch {
		case d.Allocatable != nil:
			served += int64(*d.Allocatable)
		case d.Healthy != nil && *d.Healthy:
			served++
		}
	}
	q := n.Status.Allocatable[rn]
	if !mapped || q.Value() < served {
		return q.Value()
	}
	return served
}
