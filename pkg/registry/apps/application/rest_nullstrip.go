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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// normalizeSpecNulls removes required null properties whose schema supplies a
// non-null default. Helm deletes explicit nulls before validating values, whereas
// omission lets the chart default apply without persisting its current value.
// Optional/nullable properties retain their explicit-null meaning. User-keyed
// maps and array elements do not imply a matching Helm values-default path,
// so defaults in their item schemas alone cannot justify a repair.
func (r *REST) normalizeSpecNulls(app *appsv1alpha1.Application) ([]string, error) {
	if r.specSchema == nil || app == nil || app.Spec == nil || len(app.Spec.Raw) == 0 {
		return nil, nil
	}
	var spec map[string]any
	decoder := json.NewDecoder(bytes.NewReader(app.Spec.Raw))
	decoder.UseNumber()
	if err := decoder.Decode(&spec); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("spec must contain a single JSON object")
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

// Only declared property paths identify defaults independently of user keys.
// In particular, a schema default inside an array item is not a
// default for an element of a user-supplied replacement array.
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
					if _, mustBeSet := required[name]; mustBeSet && prop.Default.Object != nil && !prop.Nullable {
						delete(value, name)
						*stripped = append(*stripped, joinFieldPath(path, name))
					}
					continue
				}
				stripDeclaredNulls(child, &prop, joinFieldPath(path, name), stripped)
				continue
			}
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
