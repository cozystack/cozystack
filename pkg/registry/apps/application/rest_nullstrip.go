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

package application

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// maxWarnedNullPaths bounds the number of field paths named in the warning
// handed back to the client, so a spec that nulls out dozens of fields does not
// turn into a multi-kilobyte response header.
const maxWarnedNullPaths = 8

// normalizeSpecNulls repairs a JSON null on a field the schema marks required,
// by removing the key so the chart's own default applies to it.
//
// The spec reaches flux verbatim as HelmRelease .spec.values (see
// convertApplicationToHelmRelease), and Helm's value coalescing deletes a
// null-valued key together with the chart default underneath it, before the
// result is validated against values.schema.json. A required field written with
// nothing under it is therefore rejected for being absent - "at '/talos':
// missing property 'registryMirrors'", naming a property the spec does contain -
// and the whole release fails to render. Nothing reaches the user that would
// point at the empty key they actually typed.
//
// Only required properties are repaired, and that boundary is the point. A
// required property's null cannot be load-bearing for anyone, because today it
// fails the render outright; resolving it to the chart default is pure repair.
// Where the schema permits the field to be absent, a null keeps the meaning it
// has today - the chart default is suppressed and the key does not reach the
// template - and that meaning is worth keeping: 167 optional fields across the
// catalogue carry a non-trivial default, placeholders such as "<password>" and
// example S3 buckets among them, so resolving a user's null to one of those
// would be worse than leaving the key out.
//
// Filling in the schema default instead of removing the key would also pin
// today's value into the stored HelmRelease and freeze the application on it,
// so a later bump of that default in the chart would never reach an application
// that had once been created with an empty key.
//
// Free-form subtrees are left alone, as is a null map entry whose key the user
// chose (a node group, a registry host): there the null may be the payload, and
// dropping the entry would drop the user's intent along with it. Array elements
// keep their positions for the same reason.
//
// Returns the dotted paths that were removed, sorted, so the caller can tell
// the user what happened to them.
func (r *REST) normalizeSpecNulls(app *appsv1alpha1.Application) ([]string, error) {
	if r.specSchema == nil || app == nil || app.Spec == nil || len(app.Spec.Raw) == 0 {
		return nil, nil
	}
	var spec map[string]any
	if err := json.Unmarshal(app.Spec.Raw, &spec); err != nil {
		return nil, err
	}
	if spec == nil {
		return nil, nil
	}

	var stripped []string
	stripDeclaredNulls(spec, r.specSchema, "", &stripped)
	if len(stripped) == 0 {
		return nil, nil
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	app.Spec = &apiextv1.JSON{Raw: raw}
	sort.Strings(stripped)
	return stripped, nil
}

// stripDeclaredNulls walks a value alongside its structural schema, deleting
// every null whose key the schema declares as a property *and* lists as
// required. Dispatch is on the shape of the value rather than on schema.Type,
// which is empty for a schema that carries only properties or only items.
func stripDeclaredNulls(v any, s *structuralschema.Structural, path string, stripped *[]string) {
	if s == nil || v == nil {
		return
	}

	switch value := v.(type) {
	case map[string]any:
		required := requiredNames(s)
		// Keys are collected up front: the loop deletes from the map it walks,
		// and a sorted walk keeps the reported paths stable across calls.
		for _, name := range sortedKeys(value) {
			child := value[name]
			if prop, declared := s.Properties[name]; declared {
				if child == nil {
					if _, mustBeSet := required[name]; mustBeSet {
						delete(value, name)
						*stripped = append(*stripped, joinFieldPath(path, name))
					}
					continue
				}
				stripDeclaredNulls(child, &prop, joinFieldPath(path, name), stripped)
				continue
			}
			if s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil {
				stripDeclaredNulls(child, s.AdditionalProperties.Structural, joinFieldPath(path, name), stripped)
			}
		}
	case []any:
		if s.Items == nil {
			return
		}
		for i := range value {
			stripDeclaredNulls(value[i], s.Items, fmt.Sprintf("%s[%d]", path, i), stripped)
		}
	}
}

// requiredNames returns the property names the schema marks required. The
// validation half of a structural schema is an optional pointer, absent for
// every schema that constrains nothing.
func requiredNames(s *structuralschema.Structural) map[string]struct{} {
	if s.ValueValidation == nil || len(s.ValueValidation.Required) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(s.ValueValidation.Required))
	for _, name := range s.ValueValidation.Required {
		names[name] = struct{}{}
	}
	return names
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinFieldPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// emptyFieldsWarning renders the client-facing warning for stripped nulls. The
// normalization is deliberately visible: an empty key is a typo often enough
// that silently resolving it would trade one confusing outcome for another.
func emptyFieldsWarning(paths []string) string {
	shown := paths
	suffix := ""
	if len(shown) > maxWarnedNullPaths {
		shown = shown[:maxWarnedNullPaths]
		suffix = fmt.Sprintf(" (and %d more)", len(paths)-maxWarnedNullPaths)
	}
	return fmt.Sprintf("spec: %s left empty%s; treated as unset, the chart default applies", strings.Join(shown, ", "), suffix)
}
