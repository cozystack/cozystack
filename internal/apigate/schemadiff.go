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

package apigate

import (
	"fmt"
	"sort"
	"strings"
)

// diffSchema walks a base and head OpenAPIv3Schema node in lockstep and
// returns a description of every *breaking* difference — one that can reject a
// request or stored object the base schema accepted. Additive changes (a new
// optional property, a widened type, an added enum value, a relaxed
// constraint, a changed description or default) return nothing.
//
// The rules are deliberately conservative in the safe direction: when a
// constraint's equivalence cannot be proven cheaply (e.g. a changed regex
// pattern), the change is treated as breaking rather than risk waving through
// a real incompatibility.
func diffSchema(path string, base, head Schema) []string {
	var out []string
	diffNode(path, base, head, &out)
	sort.Strings(out)
	return out
}

func diffNode(path string, base, head Schema, out *[]string) {
	// Type narrowing: any type the base accepted that head no longer accepts
	// is breaking. Widening (head adds types) is safe.
	bt, ht := schemaTypes(base), schemaTypes(head)
	if len(bt) > 0 { // base constrained the type at all
		if removed := setDiff(bt, ht); len(removed) > 0 {
			*out = append(*out, fmt.Sprintf("%s: type narrowed, no longer accepts %s", path, strings.Join(removed, "/")))
		}
	}

	// Enum: removing an allowed value, or introducing an enum where none
	// existed, shrinks the accepted set.
	be, hasBaseEnum := schemaEnum(base)
	he, hasHeadEnum := schemaEnum(head)
	switch {
	case !hasBaseEnum && hasHeadEnum:
		*out = append(*out, fmt.Sprintf("%s: enum constraint added (previously unrestricted)", path))
	case hasBaseEnum && hasHeadEnum:
		if removed := setDiff(be, he); len(removed) > 0 {
			*out = append(*out, fmt.Sprintf("%s: enum value(s) removed: %s", path, strings.Join(removed, ", ")))
		}
	}

	// Pattern: an added or changed regex may reject previously-valid strings.
	// A removed pattern is a relaxation and is safe.
	bp := schemaString(base, "pattern")
	hp := schemaString(head, "pattern")
	if hp != "" && hp != bp {
		if bp == "" {
			*out = append(*out, fmt.Sprintf("%s: pattern constraint added (%q)", path, hp))
		} else {
			*out = append(*out, fmt.Sprintf("%s: pattern changed (%q -> %q)", path, bp, hp))
		}
	}

	// Numeric and length bounds: a tightened or newly-added bound can reject
	// previously-valid values.
	diffLowerBound(path, "minimum", base, head, out)
	diffLowerBound(path, "minLength", base, head, out)
	diffLowerBound(path, "minItems", base, head, out)
	diffUpperBound(path, "maximum", base, head, out)
	diffUpperBound(path, "maxLength", base, head, out)
	diffUpperBound(path, "maxItems", base, head, out)

	// Required: a field required in head but not in base breaks clients (and
	// stored objects) that omit it — whether the field is newly added or was
	// previously optional.
	baseReq, headReq := schemaRequired(base), schemaRequired(head)
	for _, name := range sortedKeys(headReq) {
		if !baseReq[name] {
			*out = append(*out, fmt.Sprintf("%s: field %q is now required", path, name))
		}
	}

	// Properties: recurse into shared properties; flag removals. A brand-new
	// property is additive unless it is required (handled above).
	baseProps, headProps := schemaProps(base), schemaProps(head)
	for _, name := range sortedKeys(baseProps) {
		child := childPath(path, name)
		hp, ok := headProps[name]
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: field %q was removed", path, name))
			continue
		}
		diffNode(child, baseProps[name], hp, out)
	}

	// Map values (additionalProperties as a schema) and array items: recurse so
	// nested breaks in maps/lists are caught.
	if b, h, ok := schemaChild(base, head, "additionalProperties"); ok {
		diffNode(path+"{}", b, h, out)
	}
	if b, h, ok := schemaChild(base, head, "items"); ok {
		diffNode(path+"[]", b, h, out)
	}
}

// diffLowerBound flags a lower-bound (minimum/minLength/minItems) that was
// added or raised — both reject values the base accepted.
func diffLowerBound(path, key string, base, head Schema, out *[]string) {
	bv, hasB := schemaNumber(base, key)
	hv, hasH := schemaNumber(head, key)
	if !hasH {
		return
	}
	if !hasB {
		*out = append(*out, fmt.Sprintf("%s: %s constraint added (%s)", path, key, formatNum(hv)))
	} else if hv > bv {
		*out = append(*out, fmt.Sprintf("%s: %s raised (%s -> %s)", path, key, formatNum(bv), formatNum(hv)))
	}
}

// diffUpperBound flags an upper-bound (maximum/maxLength/maxItems) that was
// added or lowered.
func diffUpperBound(path, key string, base, head Schema, out *[]string) {
	bv, hasB := schemaNumber(base, key)
	hv, hasH := schemaNumber(head, key)
	if !hasH {
		return
	}
	if !hasB {
		*out = append(*out, fmt.Sprintf("%s: %s constraint added (%s)", path, key, formatNum(hv)))
	} else if hv < bv {
		*out = append(*out, fmt.Sprintf("%s: %s lowered (%s -> %s)", path, key, formatNum(bv), formatNum(hv)))
	}
}

// --- schema field accessors -------------------------------------------------

// schemaTypes returns the set of JSON types a node accepts, gathering both the
// scalar/array `type` keyword and any `anyOf[].type` (as used by the
// int-or-string quantity fields). An empty set means "unconstrained".
func schemaTypes(s Schema) map[string]struct{} {
	out := map[string]struct{}{}
	switch t := s["type"].(type) {
	case string:
		if t != "" {
			out[t] = struct{}{}
		}
	case []any:
		for _, v := range t {
			if str, ok := v.(string); ok {
				out[str] = struct{}{}
			}
		}
	}
	if anyOf, ok := s["anyOf"].([]any); ok {
		for _, v := range anyOf {
			if sub, ok := v.(map[string]any); ok {
				if str, ok := sub["type"].(string); ok && str != "" {
					out[str] = struct{}{}
				}
			}
		}
	}
	return out
}

// schemaEnum returns the enum value set and whether an enum was declared.
func schemaEnum(s Schema) (map[string]struct{}, bool) {
	raw, ok := s["enum"].([]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]struct{}, len(raw))
	for _, v := range raw {
		out[fmt.Sprintf("%v", v)] = struct{}{}
	}
	return out, true
}

// schemaRequired returns the set of required property names at a node.
func schemaRequired(s Schema) map[string]bool {
	out := map[string]bool{}
	if raw, ok := s["required"].([]any); ok {
		for _, v := range raw {
			if name, ok := v.(string); ok {
				out[name] = true
			}
		}
	}
	return out
}

// schemaProps returns the child property schemas at a node.
func schemaProps(s Schema) map[string]Schema {
	out := map[string]Schema{}
	if raw, ok := s["properties"].(map[string]any); ok {
		for name, v := range raw {
			if sub, ok := v.(map[string]any); ok {
				out[name] = Schema(sub)
			}
		}
	}
	return out
}

// schemaChild extracts a named child schema (e.g. items, additionalProperties)
// from both nodes, reporting ok only when at least one side carries a schema
// object worth recursing into. A boolean additionalProperties (true/false) is
// not a recursable schema and yields ok=false.
func schemaChild(base, head Schema, key string) (Schema, Schema, bool) {
	b, bOK := base[key].(map[string]any)
	h, hOK := head[key].(map[string]any)
	if !bOK && !hOK {
		return nil, nil, false
	}
	return Schema(b), Schema(h), true
}

func schemaString(s Schema, key string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return ""
}

// schemaNumber reads a numeric keyword. JSON numbers decode as float64 through
// sigs.k8s.io/yaml; integers may also appear as int/int64 depending on the
// decoder, so both are handled.
func schemaNumber(s Schema, key string) (float64, bool) {
	switch v := s[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// --- helpers ----------------------------------------------------------------

func childPath(parent, name string) string {
	return parent + "." + name
}

func setDiff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func formatNum(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
