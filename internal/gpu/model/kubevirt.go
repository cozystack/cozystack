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
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
)

// The KubeVirt CR the platform renders permittedHostDevices into.
const (
	KubeVirtNamespace = "cozy-kubevirt"
	KubeVirtName      = "kubevirt"
)

// PermittedResourceNames returns the resource names of the KubeVirt CR's
// PCI and mediated permittedHostDevices lists, where GPUs are declared, or
// nil when the table is unset: KubeVirt checks GPU entries against it only
// when it is non-nil (renderresources.go validatePermittedHostDevices in
// v1.9.0).
func PermittedResourceNames(kv *unstructured.Unstructured) sets.Set[string] {
	table, ok, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "permittedHostDevices")
	if !ok || table == nil {
		return nil
	}
	out := sets.New[string]()
	for _, list := range []string{"pciHostDevices", "mediatedDevices"} {
		entries, _, _ := unstructured.NestedSlice(table, list)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if name, ok := m["resourceName"].(string); ok {
					out.Insert(name)
				}
			}
		}
	}
	return out
}

// PermittedGPUNames returns the table's GPU names: its NVIDIA (vendor
// 10DE) PCI entries and its mediated entries. A NIC in the table, such as
// nvidia.com/hostdev of a ConnectX VF, is not a GPU.
func PermittedGPUNames(kv *unstructured.Unstructured) []string {
	table, ok, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "permittedHostDevices")
	if !ok || table == nil {
		return nil
	}
	var out []string
	for _, list := range []string{"pciHostDevices", "mediatedDevices"} {
		entries, _, _ := unstructured.NestedSlice(table, list)
		for _, e := range entries {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["resourceName"].(string)
			selector, _ := m["pciVendorSelector"].(string)
			if name != "" && (list == "mediatedDevices" || strings.HasPrefix(strings.ToUpper(selector), "10DE:")) {
				out = append(out, name)
			}
		}
	}
	return out
}
