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
	"bytes"
	"encoding/json"
	"strings"
	"text/template"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	"github.com/cozystack/cozystack/pkg/config"
)

// buildDependsOnFromMappings inspects the raw JSON bytes of an Application
// spec, evaluates each DependencyMapping rule, and returns a deduplicated
// list of HelmRelease dependency references.
//
// raw must be the JSON-encoded spec (may be nil or empty — returns nil, nil).
// namespace is the namespace used to populate DependencyReference.Namespace.
//
// Missing fields and empty arrays are treated as "no values" and produce no
// entries rather than an error, so existing ApplicationDefinitions that do not
// declare any DependencyMappings are unaffected.
func buildDependsOnFromMappings(raw []byte, mappings []config.DependencyMapping, namespace string) ([]helmv2.DependencyReference, error) {
	if len(raw) == 0 || len(mappings) == 0 {
		return nil, nil
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var refs []helmv2.DependencyReference

	for _, m := range mappings {
		values, err := extractValuesPath(obj, m.ValuesPath)
		if err != nil {
			return nil, err
		}

		tmpl, err := template.New("").Parse(m.NameTemplate)
		if err != nil {
			return nil, err
		}

		for _, v := range values {
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, map[string]string{"value": v}); err != nil {
				return nil, err
			}
			name := buf.String()
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			refs = append(refs, helmv2.DependencyReference{
				Name:      name,
				Namespace: namespace,
			})
		}
	}

	return refs, nil
}

// extractValuesPath walks obj along the dot-separated path and returns all
// string leaf values reachable from that path.
//
// A path segment ending with "[*]" (e.g. "disks[*]") expands every element of
// the array at that key; subsequent segments are applied to each element.
// Segments that do not exist or whose value is not a map/array/string are
// silently skipped, so missing fields never cause errors.
//
// Non-string leaf values (numbers, booleans, null) are silently skipped —
// only string values produce dependency entries.
func extractValuesPath(obj map[string]any, path string) ([]string, error) {
	parts := strings.Split(path, ".")
	nodes := []any{obj}
	return collectValues(nodes, parts), nil
}

// collectValues recursively resolves parts against each node in nodes and
// collects all string leaves encountered.
func collectValues(nodes []any, parts []string) []string {
	if len(parts) == 0 || len(nodes) == 0 {
		return nil
	}

	seg := parts[0]
	rest := parts[1:]
	expand := strings.HasSuffix(seg, "[*]")
	key := strings.TrimSuffix(seg, "[*]")

	var next []any
	for _, node := range nodes {
		m, ok := node.(map[string]any)
		if !ok {
			continue
		}
		val, exists := m[key]
		if !exists {
			continue
		}
		if expand {
			arr, ok := val.([]any)
			if !ok {
				continue
			}
			next = append(next, arr...)
		} else {
			next = append(next, val)
		}
	}

	if len(rest) == 0 {
		// Leaf: collect string values.
		var out []string
		for _, n := range next {
			if s, ok := n.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}

	return collectValues(next, rest)
}
