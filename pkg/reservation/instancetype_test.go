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
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

// fixtureInstancetypes are VirtualMachineClusterInstancetypes in the shapes
// the classifier has to tell apart, cut down to the fields it reads.
var fixtureInstancetypes = []map[string]any{
	instancetype("u1.small", map[string]any{"guest": int64(1)}, map[string]any{"guest": "2Gi"}),
	instancetype("u1.medium", map[string]any{"guest": int64(1)}, map[string]any{"guest": "4Gi"}),
	instancetype("o1.large", map[string]any{"guest": int64(2)}, map[string]any{"guest": "8Gi", "overcommitPercent": int64(50)}),
	instancetype("m1.large", map[string]any{"guest": int64(2)}, map[string]any{"guest": "16Gi", "hugepages": map[string]any{"pageSize": "2Mi"}}),
	instancetype("m1.large1gi", map[string]any{"guest": int64(2)}, map[string]any{"guest": "16Gi", "hugepages": map[string]any{"pageSize": "1Gi"}}),
	instancetype("d1.large", map[string]any{"guest": int64(2), "dedicatedCPUPlacement": true, "isolateEmulatorThread": true}, map[string]any{"guest": "8Gi"}),
	instancetype("cx1.2xlarge", map[string]any{"guest": int64(8), "dedicatedCPUPlacement": true, "isolateEmulatorThread": true}, map[string]any{"guest": "16Gi", "hugepages": map[string]any{"pageSize": "2Mi"}}),
}

func instancetype(name string, cpu, memory map[string]any) map[string]any {
	return map[string]any{
		"kind":     "VirtualMachineClusterInstancetype",
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"cpu": cpu, "memory": memory},
	}
}

func TestClassifyInstancetypes(t *testing.T) {
	want := map[string]ResourceList{
		"u1.small":    {KeyCPU: resource.MustParse("1"), KeyMemory: resource.MustParse("2Gi")},
		"u1.medium":   {KeyCPU: resource.MustParse("1"), KeyMemory: resource.MustParse("4Gi")},
		"o1.large":    {KeyCPU: resource.MustParse("2"), KeyOvercommittedMemory: resource.MustParse("8Gi")},
		"m1.large":    {KeyCPU: resource.MustParse("2"), KeyHugepagesPrefix + "2Mi": resource.MustParse("16Gi")},
		"m1.large1gi": {KeyCPU: resource.MustParse("2"), KeyHugepagesPrefix + "1Gi": resource.MustParse("16Gi")},
		// The isolated emulator thread takes a core nobody else can be given.
		"d1.large":    {KeyDedicatedCPU: resource.MustParse("3"), KeyMemory: resource.MustParse("8Gi")},
		"cx1.2xlarge": {KeyDedicatedCPU: resource.MustParse("9"), KeyHugepagesPrefix + "2Mi": resource.MustParse("16Gi")},
	}
	for _, obj := range fixtureInstancetypes {
		name := obj["metadata"].(map[string]any)["name"].(string)
		got, err := ClassifyInstancetype(obj)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertList(t, name, got, want[name])
	}
}

func TestClassifyEmulatorThreadParity(t *testing.T) {
	// KubeVirt copies an instance type's spec.annotations onto the VMI and
	// pins the extra parity core when the VMI carries the annotation, so the
	// figure is a property of each type, not of the catalog.
	dedicated := func(guest int64, annotations map[string]any) map[string]any {
		spec := map[string]any{
			"cpu":    map[string]any{"guest": guest, "dedicatedCPUPlacement": true, "isolateEmulatorThread": true},
			"memory": map[string]any{"guest": "1Gi"},
		}
		if annotations != nil {
			spec["annotations"] = annotations
		}
		return map[string]any{"metadata": map[string]any{"name": "test"}, "spec": spec}
	}
	parity := map[string]any{EmulatorThreadCompleteToEvenParityAnnotation: ""}
	cases := []struct {
		guest       int64
		annotations map[string]any
		want        int64
	}{
		{guest: 8, want: 9},
		{guest: 8, annotations: parity, want: 10},
		{guest: 8, annotations: map[string]any{EmulatorThreadCompleteToEvenParityAnnotation: "false"}, want: 10},
		{guest: 8, annotations: map[string]any{"example.com/unrelated": ""}, want: 9},
		{guest: 3, want: 4},
		{guest: 3, annotations: parity, want: 4},
	}
	for _, tc := range cases {
		got, err := ClassifyInstancetype(dedicated(tc.guest, tc.annotations))
		if err != nil {
			t.Fatal(err)
		}
		cores := got[KeyDedicatedCPU]
		if cores.Value() != tc.want {
			t.Errorf("guest %d annotations %v: got %s dedicated cores, want %d", tc.guest, tc.annotations, cores.String(), tc.want)
		}
	}

	// Only the type's own annotations reach the VMI; metadata.annotations
	// describe the object and are not copied.
	onMetadata := dedicated(8, nil)
	onMetadata["metadata"].(map[string]any)["annotations"] = parity
	got, err := ClassifyInstancetype(onMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if cores := got[KeyDedicatedCPU]; cores.Value() != 9 {
		t.Errorf("annotation on metadata: got %s dedicated cores, want 9", cores.String())
	}
}

func TestClassifySupplementalIOThreads(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "test"},
		"spec": map[string]any{
			"cpu":             map[string]any{"guest": int64(4), "dedicatedCPUPlacement": true},
			"ioThreadsPolicy": "supplementalPool",
			"ioThreads":       map[string]any{"supplementalPoolThreadCount": int64(2)},
			"memory":          map[string]any{"guest": "1Gi"},
		},
	}
	got, err := ClassifyInstancetype(obj)
	if err != nil {
		t.Fatal(err)
	}
	if cores := got[KeyDedicatedCPU]; cores.Value() != 6 {
		t.Errorf("got %s dedicated cores, want 6", cores.String())
	}
}

func TestClassifyRejectsIncompleteTypes(t *testing.T) {
	cases := map[string]map[string]any{
		"no guest cpu":    {"spec": map[string]any{"memory": map[string]any{"guest": "1Gi"}}},
		"no guest memory": {"spec": map[string]any{"cpu": map[string]any{"guest": int64(1)}}},
		"hugepages and overcommit": {"spec": map[string]any{
			"cpu":    map[string]any{"guest": int64(1)},
			"memory": map[string]any{"guest": "1Gi", "overcommitPercent": int64(50), "hugepages": map[string]any{"pageSize": "2Mi"}},
		}},
		"supplementalPool without a count": {"spec": map[string]any{
			"cpu":             map[string]any{"guest": int64(1), "dedicatedCPUPlacement": true},
			"ioThreadsPolicy": "supplementalPool",
			"memory":          map[string]any{"guest": "1Gi"},
		}},
	}
	for name, obj := range cases {
		if got, err := ClassifyInstancetype(obj); err == nil {
			t.Errorf("%s: expected an error, got %v", name, got)
		}
	}
}

func assertList(t *testing.T, label string, got, want ResourceList) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
		return
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g.Cmp(w) != 0 {
			t.Errorf("%s: %s = %s, want %s (full: %v)", label, k, g.String(), w.String(), got)
		}
	}
}
