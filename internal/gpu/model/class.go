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
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// Class is the part of a GPUClass that capacity and admission read.
type Class struct {
	Name         string
	Created      metav1.Time
	Mode         cozyv1alpha1.GPUMode
	ResourceName corev1.ResourceName
	Tolerations  []corev1.Toleration
}

// ClassFrom projects a GPUClass.
func ClassFrom(c *cozyv1alpha1.GPUClass) Class {
	out := Class{Name: c.Name, Created: c.CreationTimestamp, Mode: c.Spec.Mode}
	for _, t := range c.Spec.Tolerations {
		out.Tolerations = append(out.Tolerations, t.Toleration())
	}
	if dp := c.Spec.Source.DevicePlugin; dp != nil {
		out.ResourceName = corev1.ResourceName(dp.ResourceName)
	}
	return out
}

// EffectiveMaxCount returns how many GPUs of the class one VM may take,
// and false when nothing beyond one node's GPUs limits it.
func EffectiveMaxCount(spec *cozyv1alpha1.GPUClassSpec) (int32, bool) {
	if spec.MaxCount != nil {
		return *spec.MaxCount, true
	}
	if spec.Mode == cozyv1alpha1.GPUModeVGPU {
		return 1, true
	}
	return 0, false
}

// Conflicts returns the classes whose resource name an older class
// already has, so a new class with a mistyped name cannot take an offer
// tenants run on out of service. Age is the creation time, the name
// breaking a tie.
func Conflicts(classes []Class) sets.Set[string] {
	byName := map[corev1.ResourceName][]Class{}
	for _, c := range classes {
		if c.ResourceName != "" {
			byName[c.ResourceName] = append(byName[c.ResourceName], c)
		}
	}
	out := sets.New[string]()
	for _, group := range byName {
		slices.SortFunc(group, func(a, b Class) int {
			if c := a.Created.Compare(b.Created.Time); c != 0 {
				return c
			}
			return strings.Compare(a.Name, b.Name)
		})
		for _, c := range group[1:] {
			out.Insert(c.Name)
		}
	}
	return out
}
