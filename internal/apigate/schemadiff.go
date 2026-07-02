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
	// A nil head means the schema at this path was removed entirely. Dropping a
	// constraint is a widening, never a break, so there is nothing to report.
	// Callers avoid handing us a nil head for genuine field/version removals
	// (those are reported by the parent), so this only guards the defensive
	// case of a child schema that vanished.
	if head == nil {
		return
	}

	// Type constraint changes:
	//   base unconstrained, head constrained  -> breaking (rejects other types)
	//   both constrained, head drops a type   -> breaking (narrowing)
	//   head unconstrained                     -> safe (widening)
	bt, ht := schemaTypes(base), schemaTypes(head)
	switch {
	case len(bt) == 0 && len(ht) > 0:
		*out = append(*out, fmt.Sprintf("%s: type constraint added, now only accepts %s", path, strings.Join(sortedKeys(ht), "/")))
	case len(bt) > 0 && len(ht) > 0:
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

	// CEL validation rules (x-kubernetes-validations): a rule present in head
	// but not in base can reject previously-valid values or updates. Removing a
	// rule is a relaxation and is safe, mirroring how pattern/enum are treated.
	baseRules, headRules := schemaValidationRules(base), schemaValidationRules(head)
	for _, rule := range setDiff(headRules, baseRules) {
		*out = append(*out, fmt.Sprintf("%s: validation rule added (%q)", path, rule))
	}

	// nullable: Kubernetes expresses "may be null" via nullable:true rather than
	// a "null" entry in the type set. Dropping it rejects previously-valid null
	// values, so a true -> false/absent transition is breaking.
	if schemaBool(base, "nullable") && !schemaBool(head, "nullable") {
		*out = append(*out, fmt.Sprintf("%s: no longer nullable (null values rejected)", path))
	}

	// Composition keywords (oneOf/allOf/not): introducing one where base had
	// none can reject previously-valid objects. Conservative like the rest of
	// the file — flag an addition; removing a composition constraint is a
	// relaxation and is safe.
	for _, kw := range []string{"oneOf", "allOf", "not"} {
		if _, inBase := base[kw]; inBase {
			continue
		}
		if _, inHead := head[kw]; inHead {
			*out = append(*out, fmt.Sprintf("%s: %s composition constraint added", path, kw))
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

	// additionalProperties governs undeclared fields; its permissiveness runs
	// false (none) < schema (matching) < true/absent (any).
	//   - open -> false:        breaking (fields once accepted now rejected)
	//   - open -> schema:       recurse; adding a schema to a previously-open
	//                           map is itself a restriction, caught by diffing
	//                           the new schema against an empty (unconstrained)
	//                           base.
	//   - base false -> *:      relaxation (base accepted nothing), so safe.
	//   - open -> true/absent:  relaxation, safe.
	baseOpenAP := base["additionalProperties"] != false
	if baseOpenAP && head["additionalProperties"] == false {
		*out = append(*out, fmt.Sprintf("%s: additionalProperties restricted to false (undeclared fields no longer accepted)", path))
	} else if baseOpenAP {
		if b, h, ok := childSchemas(base, head, "additionalProperties"); ok {
			diffNode(path+"{}", b, h, out)
		}
	}

	// items: recurse whenever head defines an item schema. When base has none,
	// the head schema is diffed against an empty base so that constraining a
	// previously-unconstrained array element is caught; a base schema with no
	// head counterpart is a relaxation and is skipped.
	if b, h, ok := childSchemas(base, head, "items"); ok {
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

// childSchemas extracts a named child schema (items or additionalProperties)
// for recursion. It reports ok only when HEAD carries a schema object; base's
// counterpart is returned when present and an empty schema otherwise. The
// asymmetry is deliberate:
//   - head schema, base schema   -> diff them (nested changes).
//   - head schema, base none     -> diff head against an empty base, so that
//     constraining a previously-unconstrained array element / map value
//     (base absent or true) surfaces as breaking.
//   - head none, base schema     -> ok=false: removing a child schema loosens
//     the contract (relaxation), so it is skipped.
//
// A boolean additionalProperties (true/false) is not a schema object and is
// handled separately by the caller.
func childSchemas(base, head Schema, key string) (Schema, Schema, bool) {
	h, ok := head[key].(map[string]any)
	if !ok {
		return nil, nil, false
	}
	b, _ := base[key].(map[string]any) // nil -> empty (unconstrained) base schema
	return Schema(b), Schema(h), true
}

// schemaValidationRules returns the set of CEL rule expressions declared in a
// node's x-kubernetes-validations, keyed by the rule string so that a reworded
// or tightened rule reads as a new rule.
func schemaValidationRules(s Schema) map[string]struct{} {
	out := map[string]struct{}{}
	raw, ok := s["x-kubernetes-validations"].([]any)
	if !ok {
		return out
	}
	for _, v := range raw {
		if entry, ok := v.(map[string]any); ok {
			if rule, ok := entry["rule"].(string); ok && rule != "" {
				out[rule] = struct{}{}
			}
		}
	}
	return out
}

func schemaString(s Schema, key string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return ""
}

// schemaBool reads a boolean keyword (e.g. nullable), defaulting to false when
// absent or non-boolean.
func schemaBool(s Schema, key string) bool {
	b, _ := s[key].(bool)
	return b
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
