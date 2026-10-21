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
	"sort"
	"strings"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// EffectiveMaxCount returns how many GPUs of the class one VM may take,
// and false when nothing beyond one node's GPUs limits it.
func EffectiveMaxCount(spec *gpuv1alpha1.GPUClassSpec) (int32, bool) {
	if spec.MaxCount != nil {
		return *spec.MaxCount, true
	}
	if spec.Mode == gpuv1alpha1.GPUModeVGPU {
		return 1, true
	}
	return 0, false
}

// ResourceNames is the effective GPU resource-name set, sorted: the
// classes' resource names, the configured names (the hoster's list and the
// KubeVirt table's GPU names) and the names node agents report.
func ResourceNames(classes []gpuv1alpha1.GPUClass, configured []string, nodes []gpuv1alpha1.GPUNode) []string {
	set := map[string]struct{}{}
	add := func(n string) {
		if n != "" {
			set[n] = struct{}{}
		}
	}
	for i := range classes {
		if dp := classes[i].Spec.Source.DevicePlugin; dp != nil {
			add(dp.ResourceName)
		}
	}
	for _, n := range configured {
		add(n)
	}
	for i := range nodes {
		for _, n := range nodes[i].Status.ResourceNames {
			add(n)
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FormatResourceNames renders names for the settings ConfigMap.
func FormatResourceNames(names []string) string {
	return strings.Join(names, ",")
}

// ParseResourceNames reads the settings ConfigMap value.
func ParseResourceNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
