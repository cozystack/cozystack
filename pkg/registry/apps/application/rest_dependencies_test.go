/*
Copyright 2024 The Cozystack Authors.

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

package application

import (
	"encoding/json"
	"reflect"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	"github.com/cozystack/cozystack/pkg/config"
)

// specJSON is a test-only helper that marshals v to JSON bytes and panics on
// error. It is defined here (in the _test.go file) so it is never compiled into
// production binaries. Only for use in test fixtures where the input is always
// a valid in-memory Go map.
func specJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TestExtractValuesPath_Array verifies that the glob segment "[*]" expands an
// array and the following ".name" segment collects all name strings.
func TestExtractValuesPath_Array(t *testing.T) {
	obj := map[string]any{
		"disks": []any{
			map[string]any{"name": "a"},
			map[string]any{"name": "b"},
		},
	}

	got, err := extractValuesPath(obj, "disks[*].name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractValuesPath = %v, want %v", got, want)
	}
}

// TestExtractValuesPath_Empty verifies that an empty array produces an empty
// (nil) result without an error.
func TestExtractValuesPath_Empty(t *testing.T) {
	obj := map[string]any{
		"disks": []any{},
	}

	got, err := extractValuesPath(obj, "disks[*].name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected empty result for empty array, got %v", got)
	}
}

// TestExtractValuesPath_MissingKey verifies that a path that does not exist
// in the object returns an empty (nil) result without an error.
func TestExtractValuesPath_MissingKey(t *testing.T) {
	obj := map[string]any{
		"storage": "replicated",
	}

	got, err := extractValuesPath(obj, "disks[*].name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected nil result for missing field, got %v", got)
	}
}

// TestExtractValuesPath_NestedWildcard verifies that multi-level dot-separated
// paths with an intermediate glob work correctly.
func TestExtractValuesPath_NestedWildcard(t *testing.T) {
	obj := map[string]any{
		"foo": map[string]any{
			"bar": []any{
				map[string]any{"baz": "x"},
				map[string]any{"baz": "y"},
				map[string]any{"baz": "z"},
			},
		},
	}

	got, err := extractValuesPath(obj, "foo.bar[*].baz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractValuesPath = %v, want %v", got, want)
	}
}

// TestExtractValuesPath_NonStringValues verifies that non-string leaf values
// (numbers, booleans) are silently skipped and do not appear in the result.
func TestExtractValuesPath_NonStringValues(t *testing.T) {
	obj := map[string]any{
		"items": []any{
			map[string]any{"val": "keep-me"},
			map[string]any{"val": 42},
			map[string]any{"val": true},
			map[string]any{"val": "also-keep"},
		},
	}

	got, err := extractValuesPath(obj, "items[*].val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"keep-me", "also-keep"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractValuesPath = %v, want %v", got, want)
	}
}

// TestBuildDependsOnFromMappings_SingleDisk verifies the complete round-trip
// with a single disk: JSON spec → extract values → render name → DependencyReference.
func TestBuildDependsOnFromMappings_SingleDisk(t *testing.T) {
	raw := specJSON(map[string]any{
		"disks": []any{
			map[string]any{"name": "disk-alpha"},
		},
	})

	mappings := []config.DependencyMapping{
		{
			ValuesPath:   "disks[*].name",
			NameTemplate: "vm-disk-{{ .value }}",
		},
	}

	got, err := buildDependsOnFromMappings(raw, mappings, "tenant-foo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []helmv2.DependencyReference{
		{Name: "vm-disk-disk-alpha", Namespace: "tenant-foo"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDependsOnFromMappings =\n  %v\nwant\n  %v", got, want)
	}
}

// TestBuildDependsOnFromMappings_MultipleDisks verifies that all disks from the
// spec are emitted as dependency references.
func TestBuildDependsOnFromMappings_MultipleDisks(t *testing.T) {
	raw := specJSON(map[string]any{
		"disks": []any{
			map[string]any{"name": "disk-alpha"},
			map[string]any{"name": "disk-beta"},
		},
	})

	mappings := []config.DependencyMapping{
		{
			ValuesPath:   "disks[*].name",
			NameTemplate: "vm-disk-{{ .value }}",
		},
	}

	got, err := buildDependsOnFromMappings(raw, mappings, "tenant-foo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []helmv2.DependencyReference{
		{Name: "vm-disk-disk-alpha", Namespace: "tenant-foo"},
		{Name: "vm-disk-disk-beta", Namespace: "tenant-foo"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildDependsOnFromMappings =\n  %v\nwant\n  %v", got, want)
	}
}

// TestBuildDependsOnFromMappings_NoMappings verifies that an empty mappings
// slice returns nil without inspecting the spec at all.
func TestBuildDependsOnFromMappings_NoMappings(t *testing.T) {
	raw := specJSON(map[string]any{
		"disks": []any{
			map[string]any{"name": "disk-alpha"},
		},
	})

	got, err := buildDependsOnFromMappings(raw, nil, "tenant-foo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Errorf("expected nil result for empty mappings, got %v", got)
	}
}

// TestBuildDependsOnFromMappings_NilSpec verifies that a nil spec returns nil
// without panicking.
func TestBuildDependsOnFromMappings_NilSpec(t *testing.T) {
	got, err := buildDependsOnFromMappings(nil, []config.DependencyMapping{
		{
			ValuesPath:   "disks[*].name",
			NameTemplate: "vm-disk-{{ .value }}",
		},
	}, "tenant-foo")
	if err != nil {
		t.Fatalf("unexpected error for nil spec: %v", err)
	}

	if got != nil {
		t.Errorf("expected nil result for nil spec, got %v", got)
	}
}

// TestBuildDependsOnFromMappings_InvalidNameTemplate verifies that an invalid
// Go template in NameTemplate causes buildDependsOnFromMappings to return an
// error rather than panicking or producing a silently wrong name.
// The template "{{ .value | undefinedFunc }}" fails at parse time because
// undefinedFunc is not a registered template function.
func TestBuildDependsOnFromMappings_InvalidNameTemplate(t *testing.T) {
	raw := specJSON(map[string]any{
		"disks": []any{
			map[string]any{"name": "disk-alpha"},
		},
	})

	mappings := []config.DependencyMapping{
		{
			ValuesPath:   "disks[*].name",
			NameTemplate: "{{ .value | undefinedFunc }}",
		},
	}

	_, err := buildDependsOnFromMappings(raw, mappings, "tenant-foo")
	if err == nil {
		t.Fatal("expected error for invalid nameTemplate, got nil")
	}
}
