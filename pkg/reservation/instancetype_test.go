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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// shippedInstancetypes decodes every VirtualMachineClusterInstancetype the
// platform ships, so that the classification is pinned against the real
// catalog rather than against hand-written fixtures.
func shippedInstancetypes(t *testing.T) []map[string]any {
	t.Helper()
	files, err := filepath.Glob("../../packages/system/kubevirt-instancetypes/templates/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no instance type templates found: %v", err)
	}
	var out []map[string]any
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(fh, 4096)
		for {
			var obj map[string]any
			if err := dec.Decode(&obj); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("%s: %v", f, err)
			}
			if obj["kind"] == "VirtualMachineClusterInstancetype" {
				out = append(out, obj)
			}
		}
		fh.Close()
	}
	return out
}

func TestClassifyShippedInstancetypes(t *testing.T) {
	// The expected class per series is written from what each series is,
	// not derived from the spec, so a name-based rule and a spec-based rule
	// disagreeing shows up here. m1 is shared CPU with hugepages, n1 a
	// dedicated series with hugepages, o1 overcommits memory.
	type class struct{ cpu, memory string }
	want := map[string]class{
		"u1":  {KeyCPU, KeyMemory},
		"gn1": {KeyCPU, KeyMemory},
		"m1":  {KeyCPU, "hugepages"},
		"o1":  {KeyCPU, KeyOvercommittedMemory},
		"d1":  {KeyDedicatedCPU, KeyMemory},
		"cx1": {KeyDedicatedCPU, "hugepages"},
		"n1":  {KeyDedicatedCPU, "hugepages"},
		"rt1": {KeyDedicatedCPU, "hugepages"},
	}

	types := shippedInstancetypes(t)
	if len(types) < 60 {
		t.Fatalf("expected the full shipped catalog, decoded only %d instance types", len(types))
	}
	seen := map[string]bool{}
	for _, obj := range types {
		name := obj["metadata"].(map[string]any)["name"].(string)
		series := strings.SplitN(name, ".", 2)[0]
		seen[series] = true
		w, ok := want[series]
		if !ok {
			t.Errorf("%s: series %q has no expected class; add it to this table", name, series)
			continue
		}
		rl, err := ClassifyInstancetype(obj, InstancetypeOptions{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(rl) != 2 {
			t.Errorf("%s: expected one CPU class and one memory class, got %v", name, rl)
		}
		if _, ok := rl[w.cpu]; !ok {
			t.Errorf("%s: expected CPU class %q, got %v", name, w.cpu, rl)
		}
		if w.memory == "hugepages" {
			found := false
			for k := range rl {
				found = found || strings.HasPrefix(k, KeyHugepagesPrefix)
			}
			if !found {
				t.Errorf("%s: expected a hugepages class, got %v", name, rl)
			}
		} else if _, ok := rl[w.memory]; !ok {
			t.Errorf("%s: expected memory class %q, got %v", name, w.memory, rl)
		}
	}
	for series := range want {
		if !seen[series] {
			t.Errorf("series %q is no longer shipped; drop it from this table", series)
		}
	}
}

func TestClassifyShippedFigures(t *testing.T) {
	byName := map[string]map[string]any{}
	for _, obj := range shippedInstancetypes(t) {
		byName[obj["metadata"].(map[string]any)["name"].(string)] = obj
	}
	cases := []struct {
		name string
		want ResourceList
	}{
		// The KubeVirt u1.small is 2Gi; the cozy-lib preset of the same name
		// is 4Gi. The two catalogs share names, not figures.
		{"u1.small", ResourceList{KeyCPU: resource.MustParse("1"), KeyMemory: resource.MustParse("2Gi")}},
		// Eight guest vCPUs on nine pinned cores: the isolated emulator
		// thread takes a core nobody else can be given.
		{"cx1.2xlarge", ResourceList{KeyDedicatedCPU: resource.MustParse("9"), KeyHugepagesPrefix + "2Mi": resource.MustParse("16Gi")}},
		{"d1.large", ResourceList{KeyDedicatedCPU: resource.MustParse("3"), KeyMemory: resource.MustParse("8Gi")}},
		{"m1.large", ResourceList{KeyCPU: resource.MustParse("2"), KeyHugepagesPrefix + "2Mi": resource.MustParse("16Gi")}},
	}
	for _, tc := range cases {
		obj, ok := byName[tc.name]
		if !ok {
			t.Fatalf("%s is not shipped", tc.name)
		}
		got, err := ClassifyInstancetype(obj, InstancetypeOptions{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		assertList(t, tc.name, got, tc.want)
	}
}

func TestClassifyEmulatorThreadParity(t *testing.T) {
	dedicated := func(guest int64) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": "test"},
			"spec": map[string]any{
				"cpu":    map[string]any{"guest": guest, "dedicatedCPUPlacement": true, "isolateEmulatorThread": true},
				"memory": map[string]any{"guest": "1Gi"},
			},
		}
	}
	cases := []struct {
		guest  int64
		parity bool
		want   int64
	}{
		{guest: 8, parity: false, want: 9},
		{guest: 8, parity: true, want: 10},
		{guest: 3, parity: false, want: 4},
		{guest: 3, parity: true, want: 4},
	}
	for _, tc := range cases {
		got, err := ClassifyInstancetype(dedicated(tc.guest), InstancetypeOptions{EmulatorThreadCompleteToEvenParity: tc.parity})
		if err != nil {
			t.Fatal(err)
		}
		cores := got[KeyDedicatedCPU]
		if cores.Value() != tc.want {
			t.Errorf("guest %d parity %v: got %s dedicated cores, want %d", tc.guest, tc.parity, cores.String(), tc.want)
		}
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
	got, err := ClassifyInstancetype(obj, InstancetypeOptions{})
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
		if got, err := ClassifyInstancetype(obj, InstancetypeOptions{}); err == nil {
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
