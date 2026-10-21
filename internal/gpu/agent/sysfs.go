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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

const (
	vendorNVIDIA = "10de"
	classDisplay = 0x03
)

const uuid = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

var (
	// The GPUNode schema admits only this shape; a function outside it, such
	// as one in a VMD domain, cannot be reported.
	pciAddress = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	mdevUUID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// The NVML GPU and MIG UUIDs NVIDIA's container device plugin reports,
	// with its "::<n>" suffix on time-sliced replicas
	// (k8s-device-plugin v0.19.0 api/config/v1/replicas.go:126-161,
	// internal/rm/devices.go:243-244).
	nvmlUUID = regexp.MustCompile(`^(GPU-` + uuid + `|MIG-` + uuid + `|MIG-GPU-` + uuid + `/[0-9]+/[0-9]+)(::[0-9]+)?$`)
)

// scan returns the NVIDIA display-class PCI functions under sysfs, keyed by
// PCI address.
func scan(sysfs string) (map[string]gpuv1alpha1.GPUNodeDevice, error) {
	entries, err := os.ReadDir(filepath.Join(sysfs, "bus/pci/devices"))
	if err != nil {
		return nil, fmt.Errorf("listing PCI devices: %w", err)
	}
	out := map[string]gpuv1alpha1.GPUNodeDevice{}
	for _, e := range entries {
		addr := e.Name()
		if !pciAddress.MatchString(addr) {
			continue
		}
		dir := filepath.Join(sysfs, "bus/pci/devices", addr)
		class, err := readUint(filepath.Join(dir, "class"), 16)
		if err != nil {
			return nil, err
		}
		vendor, err := readID(filepath.Join(dir, "vendor"))
		if err != nil {
			return nil, err
		}
		if class>>16 != classDisplay || vendor != vendorNVIDIA {
			continue
		}
		device, err := readID(filepath.Join(dir, "device"))
		if err != nil {
			return nil, err
		}
		d := gpuv1alpha1.GPUNodeDevice{PCIAddress: addr, Model: model(vendor, device), Function: gpuv1alpha1.PCIFunctionWhole}
		if _, err := os.Lstat(filepath.Join(dir, "physfn")); err == nil {
			d.Function = gpuv1alpha1.PCIFunctionVF
		} else if vfs, err := readUint(filepath.Join(dir, "sriov_numvfs"), 10); err == nil && vfs > 0 {
			d.Function = gpuv1alpha1.PCIFunctionPF
			d.VFs = int32(vfs)
		}
		out[addr] = d
	}
	return out, nil
}

// resolver maps a device ID from kubelet's pod-resources API to the GPU
// functions it stands for. Plugins differ in what they report: the PCI
// address (NVIDIA's sandbox plugin, passthrough), the mdev UUID (the same
// plugin, vGPU) or the IOMMU group number (KubeVirt's own plugins).
type resolver struct {
	sysfs string
	gpus  map[string]gpuv1alpha1.GPUNodeDevice
}

func (r resolver) resolve(id string) []string {
	switch {
	case pciAddress.MatchString(id):
		if _, ok := r.gpus[id]; ok {
			return []string{id}
		}
	case mdevUUID.MatchString(id):
		if parent, ok := r.mdevParent(id); ok {
			return []string{parent}
		}
	default:
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return nil
		}
		entries, err := os.ReadDir(filepath.Join(r.sysfs, "kernel/iommu_groups", id, "devices"))
		if err != nil {
			return nil
		}
		var out []string
		for _, e := range entries {
			name := e.Name()
			if _, ok := r.gpus[name]; ok && r.onVFIO(name) {
				out = append(out, name)
			} else if parent, ok := r.mdevParent(name); ok {
				out = append(out, parent)
			}
		}
		return out
	}
	return nil
}

// onVFIO tells whether a function is bound to a vfio driver. KubeVirt's
// own plugins pass only such functions through, and the check keeps a bare
// index from NVIDIA's container plugin from reading as an IOMMU group.
func (r resolver) onVFIO(addr string) bool {
	link, err := os.Readlink(filepath.Join(r.sysfs, "bus/pci/devices", addr, "driver"))
	return err == nil && strings.Contains(filepath.Base(link), "vfio")
}

// mdevParent reads the parent PCI function of an mdev, the directory its
// sysfs node sits in.
func (r resolver) mdevParent(uuid string) (string, bool) {
	link, err := os.Readlink(filepath.Join(r.sysfs, "bus/mdev/devices", uuid))
	if err != nil {
		return "", false
	}
	parent := filepath.Base(filepath.Dir(link))
	_, ok := r.gpus[parent]
	return parent, ok
}

func readID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"), nil
}

func readUint(path string, base int) (uint64, error) {
	s, err := readID(path)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(s, base, 31)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", path, err)
	}
	return v, nil
}
