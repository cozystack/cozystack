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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestPermittedNames(t *testing.T) {
	kv := &unstructured.Unstructured{Object: map[string]any{}}
	if PermittedResourceNames(kv) != nil || PermittedGPUNames(kv) != nil {
		t.Fatal("an unset table permits everything and names no GPU")
	}
	_ = unstructured.SetNestedField(kv.Object, map[string]any{
		"pciHostDevices": []any{
			map[string]any{"pciVendorSelector": "10de:26B9", "resourceName": "nvidia.com/L40S"},
			map[string]any{"pciVendorSelector": "15B3:101E", "resourceName": "nvidia.com/hostdev"},
		},
		"mediatedDevices": []any{
			map[string]any{"mdevNameSelector": "GRID A100-40C", "resourceName": "nvidia.com/GRID_A100-40C"},
		},
		"usbHostDevices": []any{map[string]any{"resourceName": "kubevirt.io/usb"}},
	}, "spec", "configuration", "permittedHostDevices")

	want := sets.New("nvidia.com/L40S", "nvidia.com/hostdev", "nvidia.com/GRID_A100-40C")
	if got := PermittedResourceNames(kv); !got.Equal(want) {
		t.Fatalf("permitted = %v, want %v", sets.List(got), sets.List(want))
	}
	got := PermittedGPUNames(kv)
	slices.Sort(got)
	if !slices.Equal(got, []string{"nvidia.com/GRID_A100-40C", "nvidia.com/L40S"}) {
		t.Fatalf("GPU names = %v: the 10DE PCI and mdev entries, not the NIC", got)
	}
}
