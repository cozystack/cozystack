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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// Class is the part of a GPUClass that capacity and admission read.
type Class struct {
	Name              string
	UID               types.UID
	Created           metav1.Time
	Mode              cozyv1alpha1.GPUMode
	ResourceName      corev1.ResourceName
	OwnedResourceName corev1.ResourceName
	Tolerations       []corev1.Toleration
}

func ClassFrom(c *cozyv1alpha1.GPUClass) Class {
	out := Class{Name: c.Name, UID: c.UID, Created: c.CreationTimestamp, Mode: c.Spec.Mode, OwnedResourceName: corev1.ResourceName(c.Status.OwnedResourceName)}
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

// ResourceNameCandidates selects an existing owner, or a creation-time/name
// candidate for an unreserved name. A reservation survives a changed request;
// duplicates block that name even when their holders request other names.
// Candidates are not offered until their ownership is acknowledged.
func ResourceNameCandidates(classes []Class) map[corev1.ResourceName]types.UID {
	reserved := map[corev1.ResourceName][]Class{}
	requested := map[corev1.ResourceName][]Class{}
	for _, c := range classes {
		if c.OwnedResourceName != "" {
			reserved[c.OwnedResourceName] = append(reserved[c.OwnedResourceName], c)
		}
		if c.ResourceName != "" {
			requested[c.ResourceName] = append(requested[c.ResourceName], c)
		}
	}
	out := map[corev1.ResourceName]types.UID{}
	for name, owners := range reserved {
		if len(owners) == 1 && owners[0].UID != "" {
			out[name] = owners[0].UID
		}
	}
	for name, group := range requested {
		if _, held := reserved[name]; held {
			continue
		}
		slices.SortFunc(group, func(a, b Class) int {
			if c := a.Created.Compare(b.Created.Time); c != 0 {
				return c
			}
			return strings.Compare(a.Name, b.Name)
		})
		if group[0].UID != "" {
			out[name] = group[0].UID
		}
	}
	return out
}

// ConfirmedResourceNames returns exact requested names uniquely acknowledged
// by their current object UIDs in this snapshot. It is not a transaction with
// future status writes or a capacity reservation.
func ConfirmedResourceNames(classes []Class) map[types.UID]corev1.ResourceName {
	candidates := ResourceNameCandidates(classes)
	out := map[types.UID]corev1.ResourceName{}
	for _, c := range classes {
		if c.UID != "" && c.ResourceName != "" && c.ResourceName == c.OwnedResourceName && candidates[c.ResourceName] == c.UID {
			out[c.UID] = c.ResourceName
		}
	}
	return out
}

// Conflicts returns classes whose requested name is reserved or selected for
// another UID, or has ambiguous ownership. A nonconflicting candidate may still
// be pending acknowledgement; admission must use ConfirmedResourceNames too.
func Conflicts(classes []Class) sets.Set[string] {
	candidates := ResourceNameCandidates(classes)
	out := sets.New[string]()
	for _, c := range classes {
		if c.ResourceName == "" {
			continue
		}
		if owner, ok := candidates[c.ResourceName]; !ok || owner != c.UID {
			out.Insert(c.Name)
		}
	}
	return out
}
