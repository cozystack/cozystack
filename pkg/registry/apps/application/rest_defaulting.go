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
	"fmt"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// applySpecDefaults applies default values to the Application spec based on the schema
func (r *REST) applySpecDefaults(app *appsv1alpha1.Application) error {
	if r.specSchema == nil {
		return nil
	}
	var m map[string]any
	if app.Spec != nil && len(app.Spec.Raw) > 0 {
		if err := json.Unmarshal(app.Spec.Raw, &m); err != nil {
			return err
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	if err := defaultLikeKubernetes(&m, r.specSchema); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	app.Spec = &apiextv1.JSON{Raw: raw}
	return nil
}

func defaultLikeKubernetes(root *map[string]any, s *structuralschema.Structural) error {
	v := any(*root)
	nv, err := applyDefaults(v, s, true)
	if err != nil {
		return err
	}
	obj, ok := nv.(map[string]any)
	if !ok && nv != nil {
		return fmt.Errorf("internal error: applyDefaults returned non-map type %T for object root", nv)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	*root = obj
	return nil
}

func applyDefaults(v any, s *structuralschema.Structural, top bool) (any, error) {
	if s == nil {
		return v, nil
	}

	effType := s.Type
	if effType == "" {
		switch {
		case len(s.Properties) > 0 || (s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil):
			effType = "object"
		case s.Items != nil:
			effType = "array"
		default:
			// scalar
		}
	}

	switch effType {
	case "object":
		mv, isMap := v.(map[string]any)
		if !isMap || v == nil {
			if s.Default.Object != nil && !top {
				if dm, ok := s.Default.Object.(map[string]any); ok {
					mv = cloneMap(dm)
				}
			}
			if mv == nil {
				mv = map[string]any{}
			}
		}

		for name, ps := range s.Properties {
			if _, ok := mv[name]; !ok {
				if ps.Default.Object != nil {
					mv[name] = clone(ps.Default.Object)
				}
			}
			if cur, ok := mv[name]; ok {
				cv, err := applyDefaults(cur, &ps, false)
				if err != nil {
					return nil, err
				}
				mv[name] = cv
			}
		}

		if s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil {
			ap := s.AdditionalProperties.Structural
			for k, cur := range mv {
				if _, isKnown := s.Properties[k]; isKnown {
					continue
				}
				cv, err := applyDefaults(cur, ap, false)
				if err != nil {
					return nil, err
				}
				mv[k] = cv
			}
		}
		return mv, nil

	case "array":
		sl, isSlice := v.([]any)
		if !isSlice || v == nil {
			if s.Default.Object != nil {
				if ds, ok := s.Default.Object.([]any); ok {
					sl = cloneSlice(ds)
				}
			}
			if sl == nil {
				sl = []any{}
			}
		}
		if s.Items != nil {
			for i := range sl {
				cv, err := applyDefaults(sl[i], s.Items, false)
				if err != nil {
					return nil, err
				}
				sl[i] = cv
			}
		}
		return sl, nil

	default:
		if v == nil && s.Default.Object != nil {
			return clone(s.Default.Object), nil
		}
		return v, nil
	}
}

func clone(x any) any {
	switch t := x.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		return cloneSlice(t)
	default:
		return t
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = clone(v)
	}
	return out
}

func cloneSlice(s []any) []any {
	out := make([]any, len(s))
	for i := range s {
		out[i] = clone(s[i])
	}
	return out
}
