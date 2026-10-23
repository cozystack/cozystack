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

package reservation

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// InstancetypeOptions carries the cluster-wide KubeVirt settings that change
// how many cores an instance type pins.
type InstancetypeOptions struct {
	// EmulatorThreadCompleteToEvenParity mirrors the KubeVirt CR annotation
	// alpha.kubevirt.io/EmulatorThreadCompleteToEvenParity: the isolated
	// emulator thread is then given one or two cores, whichever makes the
	// pinned total even.
	EmulatorThreadCompleteToEvenParity bool
}

// ClassifyInstancetype resolves a VirtualMachineClusterInstancetype into the
// class-keyed reservation of one VM of that type.
//
// The class is read from the type's spec, never from its name, so a type an
// operator adds classifies itself. Cores include the emulator and
// supplemental I/O threads a dedicated type pins, because those are fixed
// properties of the type a tenant can see before ordering; the launcher's
// memory overhead is computed by virt-controller at runtime and is platform
// margin, not part of the reservation.
func ClassifyInstancetype(obj map[string]any, opts InstancetypeOptions) (ResourceList, error) {
	name, _, _ := unstructured.NestedString(obj, "metadata", "name")

	guestCPU, err := nestedInt(obj, "spec", "cpu", "guest")
	if err != nil {
		return nil, fmt.Errorf("instance type %q: spec.cpu.guest: %w", name, err)
	}
	if guestCPU <= 0 {
		return nil, fmt.Errorf("instance type %q: spec.cpu.guest must be positive, got %d", name, guestCPU)
	}
	guestMemoryRaw, found, err := unstructured.NestedFieldNoCopy(obj, "spec", "memory", "guest")
	if err != nil || !found {
		return nil, fmt.Errorf("instance type %q: spec.memory.guest is required", name)
	}
	guestMemory, err := parseQuantity(guestMemoryRaw)
	if err != nil {
		return nil, fmt.Errorf("instance type %q: spec.memory.guest: %w", name, err)
	}

	out := ResourceList{}

	dedicated, _, _ := unstructured.NestedBool(obj, "spec", "cpu", "dedicatedCPUPlacement")
	if dedicated {
		cores := guestCPU
		if isolate, _, _ := unstructured.NestedBool(obj, "spec", "cpu", "isolateEmulatorThread"); isolate {
			cores++
			if opts.EmulatorThreadCompleteToEvenParity && cores%2 == 1 {
				cores++
			}
		}
		if policy, _, _ := unstructured.NestedString(obj, "spec", "ioThreadsPolicy"); policy == "supplementalPool" {
			extra, err := nestedInt(obj, "spec", "ioThreads", "supplementalPoolThreadCount")
			if err != nil {
				return nil, fmt.Errorf("instance type %q: ioThreadsPolicy supplementalPool: spec.ioThreads.supplementalPoolThreadCount: %w", name, err)
			}
			cores += extra
		}
		out[KeyDedicatedCPU] = *resource.NewQuantity(cores, resource.DecimalSI)
	} else {
		out[KeyCPU] = *resource.NewQuantity(guestCPU, resource.DecimalSI)
	}

	pageSize, hasHugepages, _ := unstructured.NestedString(obj, "spec", "memory", "hugepages", "pageSize")
	overcommit, hasOvercommit, _ := unstructured.NestedFieldNoCopy(obj, "spec", "memory", "overcommitPercent")
	overcommitted := hasOvercommit && overcommit != nil && fmt.Sprint(overcommit) != "0"
	switch {
	case hasHugepages && overcommitted:
		return nil, fmt.Errorf("instance type %q: hugepages and overcommitPercent are mutually exclusive", name)
	case hasHugepages:
		if pageSize == "" {
			return nil, fmt.Errorf("instance type %q: spec.memory.hugepages.pageSize is empty", name)
		}
		out[KeyHugepagesPrefix+pageSize] = guestMemory
	case overcommitted:
		// The launcher requests less than the guest here, but the tenant
		// declared the guest figure, and the same page cannot be granted as
		// both overcommitted and ordinary memory.
		out[KeyOvercommittedMemory] = guestMemory
	default:
		out[KeyMemory] = guestMemory
	}
	return out, nil
}

func nestedInt(obj map[string]any, fields ...string) (int64, error) {
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil {
		return 0, err
	}
	if !found || v == nil {
		return 0, fmt.Errorf("is required")
	}
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("must be an integer, got %v", n)
		}
		return int64(n), nil
	default:
		return 0, fmt.Errorf("must be an integer, got %T", v)
	}
}

func parseQuantity(v any) (resource.Quantity, error) {
	switch q := v.(type) {
	case string:
		return resource.ParseQuantity(q)
	case int64:
		return *resource.NewQuantity(q, resource.BinarySI), nil
	case int:
		return *resource.NewQuantity(int64(q), resource.BinarySI), nil
	case float64:
		if q != float64(int64(q)) {
			return resource.Quantity{}, fmt.Errorf("fractional byte count %v", q)
		}
		return *resource.NewQuantity(int64(q), resource.BinarySI), nil
	default:
		return resource.Quantity{}, fmt.Errorf("must be a quantity, got %T", v)
	}
}
