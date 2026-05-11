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

package presets

import (
	"reflect"
	"sort"
	"testing"
)

func TestLegacyMappingCoverage(t *testing.T) {
	want := []string{"nano", "micro", "small", "medium", "large", "xlarge", "2xlarge"}
	for _, k := range want {
		if _, ok := LegacyMapping[k]; !ok {
			t.Errorf("LegacyMapping missing legacy key %q", k)
		}
	}
	if len(LegacyMapping) != len(want) {
		t.Errorf("LegacyMapping has %d entries, expected %d", len(LegacyMapping), len(want))
	}
}

func TestFindLegacyPresets_TopLevel(t *testing.T) {
	spec := map[string]any{"resourcesPreset": "small"}
	got := FindLegacyPresets(spec)
	want := []Finding{{Path: "spec.resourcesPreset", Current: "small", Replacement: "t1.small"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFindLegacyPresets_Nested(t *testing.T) {
	spec := map[string]any{
		"resourcesPreset": "small",
		"clickhouseKeeper": map[string]any{
			"resourcesPreset": "micro",
		},
		"dashboards": map[string]any{
			"resourcesPreset": "medium",
		},
	}
	got := FindLegacyPresets(spec)
	sort.Slice(got, func(i, j int) bool { return got[i].Path < got[j].Path })

	want := []Finding{
		{Path: "spec.clickhouseKeeper.resourcesPreset", Current: "micro", Replacement: "t1.micro"},
		{Path: "spec.dashboards.resourcesPreset", Current: "medium", Replacement: "c1.small"},
		{Path: "spec.resourcesPreset", Current: "small", Replacement: "t1.small"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestFindLegacyPresets_AllLegacyValues(t *testing.T) {
	for legacy, want := range LegacyMapping {
		spec := map[string]any{"resourcesPreset": legacy}
		got := FindLegacyPresets(spec)
		if len(got) != 1 {
			t.Fatalf("legacy %q: expected 1 finding, got %d", legacy, len(got))
		}
		if got[0].Replacement != want {
			t.Errorf("legacy %q: replacement=%q, want %q", legacy, got[0].Replacement, want)
		}
	}
}

func TestFindLegacyPresets_NewValuesIgnored(t *testing.T) {
	cases := []string{
		"t1.nano", "t1.4xlarge",
		"c1.medium", "s1.small", "u1.large", "m1.xlarge",
	}
	for _, v := range cases {
		spec := map[string]any{"resourcesPreset": v}
		if got := FindLegacyPresets(spec); len(got) != 0 {
			t.Errorf("instance-type %q reported as legacy: %+v", v, got)
		}
	}
}

func TestFindLegacyPresets_NonStringIgnored(t *testing.T) {
	spec := map[string]any{
		"resourcesPreset": 42,
		"sub": map[string]any{
			"resourcesPreset": nil,
		},
	}
	if got := FindLegacyPresets(spec); len(got) != 0 {
		t.Errorf("non-string values reported as legacy: %+v", got)
	}
}

func TestFindLegacyPresets_InsideArray(t *testing.T) {
	spec := map[string]any{
		"replicas": []any{
			map[string]any{"resourcesPreset": "large"},
			map[string]any{"resourcesPreset": "t1.large"},
		},
	}
	got := FindLegacyPresets(spec)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Path != "spec.replicas[0].resourcesPreset" {
		t.Errorf("path = %q, want spec.replicas[0].resourcesPreset", got[0].Path)
	}
	if got[0].Replacement != "c1.medium" {
		t.Errorf("replacement = %q, want c1.medium", got[0].Replacement)
	}
}

func TestFindLegacyPresets_Empty(t *testing.T) {
	if got := FindLegacyPresets(nil); got != nil {
		t.Errorf("nil spec produced findings: %+v", got)
	}
	if got := FindLegacyPresets(map[string]any{}); got != nil {
		t.Errorf("empty spec produced findings: %+v", got)
	}
}

func TestFormatDeprecationMessages(t *testing.T) {
	findings := []Finding{
		{Path: "spec.resourcesPreset", Current: "small", Replacement: "t1.small"},
		{Path: "spec.clickhouseKeeper.resourcesPreset", Current: "micro", Replacement: "t1.micro"},
	}
	got := FormatDeprecationMessages("Postgres", "tenant-x", "db1", findings)
	want := []string{
		`Postgres/db1 in tenant-x uses deprecated resourcesPreset "small" at spec.resourcesPreset; migrate to "t1.small" (1:1 equivalent CPU and memory)`,
		`Postgres/db1 in tenant-x uses deprecated resourcesPreset "micro" at spec.clickhouseKeeper.resourcesPreset; migrate to "t1.micro" (1:1 equivalent CPU and memory)`,
	}
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				t.Errorf("line %d:\n got:  %s\n want: %s", i, got[i], want[i])
			}
		}
	}
}

func TestFormatDeprecationMessages_Empty(t *testing.T) {
	got := FormatDeprecationMessages("Postgres", "ns", "name", nil)
	if len(got) != 0 {
		t.Errorf("expected no messages for nil findings, got %d", len(got))
	}
}
