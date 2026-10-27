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

type permittedEntry struct {
	resourceName, vendorSelector string
	mediated                     bool
}

// permittedEntries reads the KubeVirt CR's PCI and mediated
// permittedHostDevices lists, where GPUs are declared, and false when the
// table is unset.
func permittedEntries(kv *unstructured.Unstructured) ([]permittedEntry, bool) {
	table, ok, _ := unstructured.NestedMap(kv.Object, "spec", "configuration", "permittedHostDevices")
	if !ok || table == nil {
		return nil, false
	}
	var out []permittedEntry
	for _, list := range []string{"pciHostDevices", "mediatedDevices"} {
		entries, _, _ := unstructured.NestedSlice(table, list)
		for _, e := range entries {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			name, ok := m["resourceName"].(string)
			if !ok {
				continue
			}
			selector, _ := m["pciVendorSelector"].(string)
			out = append(out, permittedEntry{resourceName: name, vendorSelector: selector, mediated: list == "mediatedDevices"})
		}
	}
	return out, true
}

// PermittedResourceNames returns the resource names of the KubeVirt CR's
// permittedHostDevices table, or nil when the table is unset: KubeVirt
// checks GPU entries against it only when it is non-nil
// (renderresources.go validatePermittedHostDevices in v1.9.0).
func PermittedResourceNames(kv *unstructured.Unstructured) sets.Set[string] {
	entries, ok := permittedEntries(kv)
	if !ok {
		return nil
	}
	out := sets.New[string]()
	for _, e := range entries {
		out.Insert(e.resourceName)
	}
	return out
}

// PermittedGPUNames returns the table's GPU names: its NVIDIA (vendor
// 10DE) PCI entries and its mediated entries. They join the effective GPU
// resource-name set besides the hoster's list; a NIC in the table, such as
// nvidia.com/hostdev of a ConnectX VF, is not a GPU.
func PermittedGPUNames(kv *unstructured.Unstructured) []string {
	entries, _ := permittedEntries(kv)
	var out []string
	for _, e := range entries {
		if e.mediated || strings.HasPrefix(strings.ToUpper(e.vendorSelector), "10DE:") {
			out = append(out, e.resourceName)
		}
	}
	return out
}
