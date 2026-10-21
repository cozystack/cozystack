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

package agent

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func TestResolveDeviceID(t *testing.T) {
	sysfs := writeSysfs(t)
	gpus, err := scan(sysfs)
	if err != nil {
		t.Fatal(err)
	}
	r := resolver{sysfs: sysfs, gpus: gpus}

	tests := []struct {
		name string
		id   string
		want []string
	}{
		{"PCI address of a GPU", "0000:61:00.0", []string{"0000:61:00.0"}},
		{"PCI address of a VF", "0000:41:00.5", []string{"0000:41:00.5"}},
		{"PCI address of a non-GPU function", "0000:03:00.0", nil},
		{"PCI address of a GPU's audio function", "0000:61:00.1", nil},
		{"PCI address not on the node", "0000:99:00.0", nil},
		{"mdev UUID resolves to its parent", mdevA100, []string{"0000:81:00.0"}},
		{"unknown mdev UUID", "00000000-0000-4000-8000-000000000000", nil},
		{"mdev of a non-GPU function", "5c4f8e2a-1b3d-4f6a-8e9c-0d1e2f3a4b5c", nil},
		{"IOMMU group of a GPU", "42", []string{"0000:a1:00.0"}},
		{"IOMMU group holding an mdev resolves to the mdev parent", "43", []string{"0000:c1:00.0"}},
		{"IOMMU group of a GPU and its audio function", "61", []string{"0000:61:00.0"}},
		{"IOMMU group of a NIC", "7", nil},
		// NVIDIA's container device plugin can report plain indexes, which
		// must not read as the group of a GPU on the host driver.
		{"IOMMU group of a GPU not bound to vfio", "44", nil},
		{"IOMMU group not on the node", "999", nil},
		{"path traversal", "../../bus/mdev", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.resolve(tt.id); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolve(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestScanClassifiesGPUFunctions(t *testing.T) {
	got, err := scan(writeSysfs(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]gpuv1alpha1.GPUNodeDevice{
		"0000:41:00.0": {PCIAddress: "0000:41:00.0", Model: "L40S", Function: gpuv1alpha1.PCIFunctionPF, VFs: 2},
		"0000:41:00.4": {PCIAddress: "0000:41:00.4", Model: "L40S", Function: gpuv1alpha1.PCIFunctionVF},
		"0000:41:00.5": {PCIAddress: "0000:41:00.5", Model: "L40S", Function: gpuv1alpha1.PCIFunctionVF},
		"0000:61:00.0": {PCIAddress: "0000:61:00.0", Model: "H100 SXM5 80GB", Function: gpuv1alpha1.PCIFunctionWhole},
		"0000:81:00.0": {PCIAddress: "0000:81:00.0", Model: "A100 PCIe 80GB", Function: gpuv1alpha1.PCIFunctionWhole},
		"0000:a1:00.0": {PCIAddress: "0000:a1:00.0", Model: "A10", Function: gpuv1alpha1.PCIFunctionWhole},
		"0000:c1:00.0": {PCIAddress: "0000:c1:00.0", Model: "10de-9999", Function: gpuv1alpha1.PCIFunctionWhole},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scan:\n%s\nwant:\n%s", dump(got), dump(want))
	}
}

func TestInventory(t *testing.T) {
	sysfs := writeSysfs(t)
	alloc := &podresourcesv1.AllocatableResourcesResponse{Devices: []*podresourcesv1.ContainerDevices{
		devs("nvidia.com/L40S-24C", "0000:41:00.4", "0000:41:00.5"),
		devs("nvidia.com/NVIDIA_A100-40C", mdevA100, mdevA100b),
		devs("nvidia.com/A10", "42"),
		devs("nvidia.com/GRID_C1", "43"),
		devs("nvidia.com/hostdev", "7"),
	}}
	pods := []*podresourcesv1.PodResources{
		podUsing("tenant-a", "virt-launcher-a", devs("nvidia.com/L40S-24C", "0000:41:00.4")),
		podUsing("tenant-b", "virt-launcher-done", devs("nvidia.com/L40S-24C", "0000:41:00.5")),
		podUsing("tenant-c", "virt-launcher-gone", devs("nvidia.com/NVIDIA_A100-40C", mdevA100)),
		podUsing("tenant-d", "virt-launcher-d", devs("nvidia.com/A10", "42"), devs("nvidia.com/hostdev", "7")),
		// In use, but gone from allocatable: the plugin reported it unhealthy.
		podUsing("tenant-e", "virt-launcher-e", devs("nvidia.com/GH100_H100_SXM5_80GB", "0000:61:00.0")),
	}
	running := map[string]bool{
		"tenant-a/virt-launcher-a": true,
		"tenant-d/virt-launcher-d": true,
		"tenant-e/virt-launcher-e": true,
	}
	got, err := Inventory(sysfs, alloc, pods, func(ns, name string) bool { return running[ns+"/"+name] })
	if err != nil {
		t.Fatal(err)
	}

	want := gpuv1alpha1.GPUNodeStatus{
		Devices: []gpuv1alpha1.GPUNodeDevice{
			{PCIAddress: "0000:41:00.0", Model: "L40S", Function: gpuv1alpha1.PCIFunctionPF, VFs: 2},
			{PCIAddress: "0000:41:00.4", Model: "L40S", Function: gpuv1alpha1.PCIFunctionVF, ResourceName: "nvidia.com/L40S-24C", Healthy: ptr.To(true), Allocatable: ptr.To[int32](1),
				UsedBy: &gpuv1alpha1.GPUPodReference{Namespace: "tenant-a", Pod: "virt-launcher-a"}},
			{PCIAddress: "0000:41:00.5", Model: "L40S", Function: gpuv1alpha1.PCIFunctionVF, ResourceName: "nvidia.com/L40S-24C", Healthy: ptr.To(true), Allocatable: ptr.To[int32](1)},
			{PCIAddress: "0000:61:00.0", Model: "H100 SXM5 80GB", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: "nvidia.com/GH100_H100_SXM5_80GB", Healthy: ptr.To(false), Allocatable: ptr.To[int32](0),
				UsedBy: &gpuv1alpha1.GPUPodReference{Namespace: "tenant-e", Pod: "virt-launcher-e"}},
			{PCIAddress: "0000:81:00.0", Model: "A100 PCIe 80GB", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: "nvidia.com/NVIDIA_A100-40C", Healthy: ptr.To(true), Allocatable: ptr.To[int32](2)},
			{PCIAddress: "0000:a1:00.0", Model: "A10", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: "nvidia.com/A10", Healthy: ptr.To(true), Allocatable: ptr.To[int32](1),
				UsedBy: &gpuv1alpha1.GPUPodReference{Namespace: "tenant-d", Pod: "virt-launcher-d"}},
			{PCIAddress: "0000:c1:00.0", Model: "10de-9999", Function: gpuv1alpha1.PCIFunctionWhole, ResourceName: "nvidia.com/GRID_C1", Healthy: ptr.To(true), Allocatable: ptr.To[int32](1)},
		},
		ResourceNames: []string{
			"nvidia.com/A10",
			"nvidia.com/GH100_H100_SXM5_80GB",
			"nvidia.com/GRID_C1",
			"nvidia.com/L40S-24C",
			"nvidia.com/NVIDIA_A100-40C",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Inventory:\n%s\nwant:\n%s", dump(got), dump(want))
	}
}

// On 1.32 and 1.33 List still returns pods in a final phase, and a device
// they held may already be allocated to a new pod.
func TestInventoryUsedByCountsOnlyLivePods(t *testing.T) {
	sysfs := writeSysfs(t)
	alloc := &podresourcesv1.AllocatableResourcesResponse{Devices: []*podresourcesv1.ContainerDevices{
		devs("nvidia.com/A10", "42"),
	}}
	tests := []struct {
		name    string
		live    bool
		wantUse bool
	}{
		{"live pod holds the device", true, true},
		{"pod in a final phase or gone does not", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pods := []*podresourcesv1.PodResources{podUsing("tenant-a", "p", devs("nvidia.com/A10", "42"))}
			got, err := Inventory(sysfs, alloc, pods, func(string, string) bool { return tt.live })
			if err != nil {
				t.Fatal(err)
			}
			d := deviceAt(t, got, "0000:a1:00.0")
			if (d.UsedBy != nil) != tt.wantUse {
				t.Errorf("usedBy = %+v, want in use %v", d.UsedBy, tt.wantUse)
			}
		})
	}
}

// A device listed only under a dead pod tells nothing about the plugin, so
// it sets neither the resource name nor the health of its function.
func TestInventoryHealthIgnoresDeadPods(t *testing.T) {
	sysfs := writeSysfs(t)
	pods := []*podresourcesv1.PodResources{podUsing("tenant-a", "p", devs("nvidia.com/A10", "42"))}
	got, err := Inventory(sysfs, &podresourcesv1.AllocatableResourcesResponse{}, pods, func(string, string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if d := deviceAt(t, got, "0000:a1:00.0"); d.Healthy != nil || d.ResourceName != "" || d.Allocatable != nil {
		t.Errorf("device = %+v, want no plugin data", d)
	}
	if len(got.ResourceNames) != 0 {
		t.Errorf("resourceNames = %v, want none", got.ResourceNames)
	}
}

func TestPodLive(t *testing.T) {
	for phase, want := range map[string]bool{
		"Pending": true, "Running": true, "Unknown": true,
		"Succeeded": false, "Failed": false,
	} {
		if got := podLive(corev1.PodPhase(phase)); got != want {
			t.Errorf("podLive(%s) = %v, want %v", phase, got, want)
		}
	}
}

// Every model name must be the tail of the resource name the sandbox
// plugin derives for the same PCI device ID, which the platform's
// passthrough defaults record.
func TestModelsMatchPassthroughDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/core/platform/files/gpu-passthrough-defaults.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var defaults struct {
		PCIHostDevices []struct {
			PCIVendorSelector string `json:"pciVendorSelector"`
			ResourceName      string `json:"resourceName"`
		} `json:"pciHostDevices"`
	}
	if err := yaml.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	if len(defaults.PCIHostDevices) == 0 {
		t.Fatal("no defaults parsed")
	}
	seen := map[string]bool{}
	space := regexp.MustCompile(`\s+`)
	for _, d := range defaults.PCIHostDevices {
		vendor, device, _ := strings.Cut(strings.ToLower(d.PCIVendorSelector), ":")
		if vendor != vendorNVIDIA {
			continue
		}
		seen[device] = true
		m, ok := models[device]
		if !ok {
			t.Errorf("no model for %s (%s)", device, d.ResourceName)
			continue
		}
		if slug := "_" + space.ReplaceAllString(strings.ToUpper(m), "_"); !strings.HasSuffix(d.ResourceName, slug) {
			t.Errorf("model %q of %s does not end %s", m, device, d.ResourceName)
		}
	}
	for device := range models {
		if !seen[device] {
			t.Errorf("model of %s has no passthrough default", device)
		}
	}
}

func deviceAt(t *testing.T, st gpuv1alpha1.GPUNodeStatus, addr string) gpuv1alpha1.GPUNodeDevice {
	t.Helper()
	for _, d := range st.Devices {
		if d.PCIAddress == addr {
			return d
		}
	}
	t.Fatalf("no device %s in %s", addr, dump(st))
	return gpuv1alpha1.GPUNodeDevice{}
}

// The count is per resource name: a function another plugin also claims
// reports only the devices of the name it carries.
func TestInventoryCountsAllocatableOfTheReportedName(t *testing.T) {
	alloc := &podresourcesv1.AllocatableResourcesResponse{Devices: []*podresourcesv1.ContainerDevices{
		devs("nvidia.com/B_SECOND", mdevA100, mdevA100b),
		devs("nvidia.com/A_FIRST", mdevA100),
	}}
	got, err := Inventory(writeSysfs(t), alloc, nil, func(string, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	d := deviceAt(t, got, "0000:81:00.0")
	if d.ResourceName != "nvidia.com/A_FIRST" || ptr.Deref(d.Allocatable, -1) != 1 {
		t.Errorf("device = %s, want A_FIRST with 1 allocatable", dump(d))
	}
}

// A name whose GPUs are all unhealthy and in use stays a GPU name, as it
// does on the PCI path; a dead pod's hold does not count.
func TestInventoryReportsNamesOfHeldUUIDDevices(t *testing.T) {
	sysfs := writeSysfs(t)
	pods := []*podresourcesv1.PodResources{
		podUsing("tenant-a", "p", devs("nvidia.com/gpu", "GPU-b1028956-cfa2-0990-bf4a-5da9abb51763")),
	}
	for _, live := range []bool{true, false} {
		got, err := Inventory(sysfs, &podresourcesv1.AllocatableResourcesResponse{}, pods, func(string, string) bool { return live })
		if err != nil {
			t.Fatal(err)
		}
		if has := slices.Contains(got.ResourceNames, "nvidia.com/gpu"); has != live {
			t.Errorf("live %v: resourceNames = %v", live, got.ResourceNames)
		}
	}
}

// NVIDIA's container plugin reports GPUs by NVML UUID, which sysfs cannot
// map to a PCI function. Its resource name still joins resourceNames.
func TestInventoryReportsNamesOfUUIDDevices(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"GPU UUID", "GPU-b1028956-cfa2-0990-bf4a-5da9abb51763", true},
		{"MIG UUID", "MIG-b1028956-cfa2-0990-bf4a-5da9abb51763", true},
		{"MIG UUID of an older driver", "MIG-GPU-b1028956-cfa2-0990-bf4a-5da9abb51763/3/0", true},
		{"time-sliced replica", "GPU-b1028956-cfa2-0990-bf4a-5da9abb51763::2", true},
		{"index", "0", false},
		{"truncated UUID", "GPU-b1028956", false},
		{"no prefix", "b1028956-cfa2-0990-bf4a-5da9abb51763", false},
		{"text before the prefix", "vGPU-b1028956-cfa2-0990-bf4a-5da9abb51763", false},
		{"suffix after the UUID", "GPU-b1028956-cfa2-0990-bf4a-5da9abb51763x", false},
		{"GPU UUID with instance indexes", "GPU-b1028956-cfa2-0990-bf4a-5da9abb51763/3/0", false},
	}
	sysfs := writeSysfs(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alloc := &podresourcesv1.AllocatableResourcesResponse{Devices: []*podresourcesv1.ContainerDevices{
				devs("nvidia.com/gpu", tt.id),
			}}
			got, err := Inventory(sysfs, alloc, nil, func(string, string) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			if has := slices.Contains(got.ResourceNames, "nvidia.com/gpu"); has != tt.want {
				t.Errorf("resourceNames = %v, want nvidia.com/gpu %v", got.ResourceNames, tt.want)
			}
			for _, d := range got.Devices {
				if d.ResourceName != "" {
					t.Errorf("device %s got resource name %s", d.PCIAddress, d.ResourceName)
				}
			}
		})
	}
}
