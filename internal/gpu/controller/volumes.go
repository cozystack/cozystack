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

package controller

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// volumeNodes returns which nodes the PersistentVolumes bound to v's PVCs
// let the VMI run on, or nil when they constrain nothing. A PVC not bound
// yet, as a WaitForFirstConsumer one before its pod is scheduled, adds no
// constraint; once bound to a local volume, the set stays on its node.
func (r *Reconciler) volumeNodes(ctx context.Context, w *world, v *vmi) (func(string) bool, error) {
	var selectors []*corev1.NodeSelector
	for _, name := range v.PVCs {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.Client.Get(ctx, key(v.Namespace, name), pvc); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get PVC %s/%s: %w", v.Namespace, name, err)
		}
		if pvc.Spec.VolumeName == "" {
			continue
		}
		pv := &corev1.PersistentVolume{}
		if err := r.Client.Get(ctx, key("", pvc.Spec.VolumeName), pv); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get PV %s: %w", pvc.Spec.VolumeName, err)
		}
		if a := pv.Spec.NodeAffinity; a != nil && a.Required != nil {
			selectors = append(selectors, a.Required)
		}
	}
	if len(selectors) == 0 {
		return nil, nil
	}
	return func(node string) bool {
		n := w.nodes[node]
		return n != nil && !slices.ContainsFunc(selectors, func(s *corev1.NodeSelector) bool { return !matchesNodeSelector(n, s) })
	}, nil
}

// matchesNodeSelector reports whether n matches one of the terms, as the
// scheduler reads a PV's node affinity: the terms are ORed, an empty term
// matches nothing. An operator it does not know matches nothing.
func matchesNodeSelector(n *corev1.Node, s *corev1.NodeSelector) bool {
	for _, t := range s.NodeSelectorTerms {
		if len(t.MatchExpressions) == 0 && len(t.MatchFields) == 0 {
			continue
		}
		if termMatches(labels.Set(n.Labels), t.MatchExpressions) && fieldsMatch(n.Name, t.MatchFields) {
			return true
		}
	}
	return false
}

// fieldsMatch reads matchFields, which can only name metadata.name with In
// or NotIn: a node name need not be a valid label value.
func fieldsMatch(name string, reqs []corev1.NodeSelectorRequirement) bool {
	for _, req := range reqs {
		in := slices.Contains(req.Values, name)
		switch {
		case req.Key != nodeNameField:
			return false
		case req.Operator == corev1.NodeSelectorOpIn && !in, req.Operator == corev1.NodeSelectorOpNotIn && in:
			return false
		case req.Operator != corev1.NodeSelectorOpIn && req.Operator != corev1.NodeSelectorOpNotIn:
			return false
		}
	}
	return true
}

func termMatches(set labels.Set, reqs []corev1.NodeSelectorRequirement) bool {
	ops := map[corev1.NodeSelectorOperator]selection.Operator{
		corev1.NodeSelectorOpIn: selection.In, corev1.NodeSelectorOpNotIn: selection.NotIn,
		corev1.NodeSelectorOpExists: selection.Exists, corev1.NodeSelectorOpDoesNotExist: selection.DoesNotExist,
		corev1.NodeSelectorOpGt: selection.GreaterThan, corev1.NodeSelectorOpLt: selection.LessThan,
	}
	for _, req := range reqs {
		op, ok := ops[req.Operator]
		if !ok {
			return false
		}
		r, err := labels.NewRequirement(req.Key, op, req.Values)
		if err != nil || !r.Matches(set) {
			return false
		}
	}
	return true
}

// podAllows reports whether n matches p's node selector and required node
// affinity, which KubeVirt fills from the VM and its own labels.
func podAllows(p *corev1.Pod, n *corev1.Node) bool {
	if n == nil {
		return false
	}
	for k, v := range p.Spec.NodeSelector {
		if got, ok := n.Labels[k]; !ok || got != v {
			return false
		}
	}
	a := p.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil ||
		len(a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		return true
	}
	return matchesNodeSelector(n, a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
}
