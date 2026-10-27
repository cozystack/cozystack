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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// ActivePods is a VMI's status.activePods: the UIDs of the pods KubeVirt
// runs it in, scheduled or not.
func ActivePods(vmi *unstructured.Unstructured) sets.Set[types.UID] {
	out := sets.New[types.UID]()
	active, _, _ := unstructured.NestedMap(vmi.Object, "status", "activePods")
	for uid := range active {
		out.Insert(types.UID(uid))
	}
	return out
}

// LauncherVMI returns the VMI a pod is the launcher of: the KubeVirt VMI
// its owner reference names, and only when that VMI lists the pod in
// status.activePods. Whoever creates a pod sets its owner references and
// labels, while no tenant role can write a VMI's status. activePods returns
// the named VMI's active pods, nil when there is no such VMI.
func LauncherVMI(p *corev1.Pod, activePods func(types.NamespacedName) (sets.Set[types.UID], error)) (types.NamespacedName, bool, error) {
	for _, ref := range p.OwnerReferences {
		if ref.Kind != "VirtualMachineInstance" || ref.APIVersion != "kubevirt.io/v1" {
			continue
		}
		k := types.NamespacedName{Namespace: p.Namespace, Name: ref.Name}
		active, err := activePods(k)
		if err != nil {
			return types.NamespacedName{}, false, err
		}
		if !active.Has(p.UID) {
			return types.NamespacedName{}, false, nil
		}
		return k, true, nil
	}
	return types.NamespacedName{}, false, nil
}

// CoveredForBudget maps a VMI to the units, per resource name, that its
// attached claims account for, so its launcher pod does not count them
// against a raw budget key a second time. Every attached claim counts, a
// Lost one included, since it still counts against budgets itself; node
// capacity needs another rule, where a Lost claim holds nothing.
func CoveredForBudget(claims []*gpuv1alpha1.GPUClaim, classes map[string]Class) map[types.NamespacedName]map[corev1.ResourceName]int64 {
	out := map[types.NamespacedName]map[corev1.ResourceName]int64{}
	for _, d := range claims {
		rn := classes[d.Spec.ClassName].ResourceName
		if d.Status.AttachedTo == nil || rn == "" {
			continue
		}
		k := types.NamespacedName{Namespace: d.Namespace, Name: d.Status.AttachedTo.VMI}
		if out[k] == nil {
			out[k] = map[corev1.ResourceName]int64{}
		}
		out[k][rn] += int64(d.Spec.Count)
	}
	return out
}
