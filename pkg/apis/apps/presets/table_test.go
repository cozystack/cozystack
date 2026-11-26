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
	"os"
	"reflect"
	"regexp"
	"testing"
)

func TestTableMatchesChart(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/library/cozy-lib/templates/_resourcepresets.tpl")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`"([a-z0-9.]+)"\s+\(dict ([^)]*)\)`)
	cpu := regexp.MustCompile(`"cpu"\s+"([^"]+)"`)
	memory := regexp.MustCompile(`"memory"\s+"([^"]+)"`)
	var want []Preset
	seen := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		c, mem := cpu.FindStringSubmatch(m[2]), memory.FindStringSubmatch(m[2])
		if c == nil || mem == nil || seen[m[1]] {
			t.Fatalf("invalid or duplicate preset: %q", m[0])
		}
		seen[m[1]] = true
		want = append(want, Preset{Name: m[1], CPU: c[1], Memory: mem[1], Replacement: LegacyMapping[m[1]]})
	}
	if len(want) != 47 {
		t.Fatalf("parsed %d presets, want 47", len(want))
	}
	if got := Table(); !reflect.DeepEqual(got, want) {
		t.Fatalf("table = %v, want %v", got, want)
	}
}

func TestTableReturnsCopy(t *testing.T) {
	first := Table()
	first[0].Name = "changed"
	if Table()[0].Name != "t1.nano" {
		t.Fatal("caller mutated the preset table")
	}
}
