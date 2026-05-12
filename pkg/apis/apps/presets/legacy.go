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

// Package presets exposes the legacy-to-instance-type mapping used when
// migrating resourcesPreset values from the flat naming scheme
// (nano/micro/small/.../2xlarge) to the new <series>.<size> form
// (t1/c1/s1/u1/m1 × 8 sizes).
//
// The same table is mirrored in three other places — keep them in sync:
//   - packages/library/cozy-lib/templates/_resourcepresets.tpl (legacy block)
//   - packages/core/platform/images/migrations/migrations/39
//   - docs/operations/resource-presets.md
package presets

import "fmt"

// LegacyMapping is the canonical 1:1 conversion table from legacy flat
// preset names to their instance-type equivalents. CPU and memory of the
// returned preset match the legacy value exactly.
var LegacyMapping = map[string]string{
	"nano":    "t1.nano",
	"micro":   "t1.micro",
	"small":   "t1.small",
	"medium":  "c1.small",
	"large":   "c1.medium",
	"xlarge":  "c1.large",
	"2xlarge": "c1.xlarge",
}

// Finding is a single deprecated value located inside an Application spec.
type Finding struct {
	// Path is a dotted/indexed JSON path from the spec root, e.g.
	// "spec.resourcesPreset" or "spec.clickhouseKeeper.resourcesPreset".
	Path string
	// Current is the legacy value the user provided.
	Current string
	// Replacement is the suggested instance-type value with identical
	// CPU and memory.
	Replacement string
}

// FindLegacyPresets walks the decoded JSON spec recursively and returns
// every resourcesPreset field whose value matches a legacy flat name.
// Values already in instance-type form (and any non-string values) are
// ignored.
func FindLegacyPresets(spec map[string]any) []Finding {
	var out []Finding
	walk(spec, "spec", &out)
	return out
}

// FormatDeprecationMessages builds one human-readable deprecation line per
// finding. The format matches what cozystack-api emits via klog so the
// log output is unit-testable without intercepting klog itself.
func FormatDeprecationMessages(kind, namespace, name string, findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, fmt.Sprintf(
			"%s/%s in %s uses deprecated resourcesPreset %q at %s; "+
				"migrate to %q (1:1 equivalent CPU and memory)",
			kind, name, namespace,
			f.Current, f.Path, f.Replacement,
		))
	}
	return out
}

func walk(v any, path string, out *[]Finding) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			childPath := path + "." + k
			if k == "resourcesPreset" {
				s, isStr := child.(string)
				if !isStr {
					continue
				}
				if replacement, isLegacy := LegacyMapping[s]; isLegacy {
					*out = append(*out, Finding{
						Path:        childPath,
						Current:     s,
						Replacement: replacement,
					})
				}
				continue
			}
			walk(child, childPath, out)
		}
	case []any:
		for i, item := range t {
			walk(item, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}
