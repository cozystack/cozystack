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

// Package defaulting applies an ApplicationDefinition's structural-schema
// defaults to application values. It is shared by the aggregated apiserver,
// which defaults on the read path, and by anything that has to reason about
// stored values, which carry only the keys a tenant actually wrote.
package defaulting

import (
	"encoding/json"
	"fmt"
	"strings"

	internalapiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
)

// BuildSchema parses an OpenAPI-v3 JSON schema string and returns the
// structural-schema form used for defaulting. Returns (nil, nil) when raw
// is empty after trimming. Returns (nil, error) for malformed JSON,
// v1→internal conversion failures, or structural-schema construction
// errors — callers can decide whether to log-and-continue or fail hard.
func BuildSchema(raw string) (*structuralschema.Structural, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var v1js apiextv1.JSONSchemaProps
	if err := json.Unmarshal([]byte(raw), &v1js); err != nil {
		return nil, fmt.Errorf("unmarshal v1 OpenAPI schema: %w", err)
	}
	scheme := runtime.NewScheme()
	_ = internalapiext.AddToScheme(scheme)
	_ = apiextv1.AddToScheme(scheme)
	var ijs internalapiext.JSONSchemaProps
	if err := scheme.Convert(&v1js, &ijs, nil); err != nil {
		return nil, fmt.Errorf("convert v1->internal JSONSchemaProps: %w", err)
	}
	s, err := structuralschema.NewStructural(&ijs)
	if err != nil {
		return nil, fmt.Errorf("build structural schema: %w", err)
	}
	return s, nil
}

// Apply defaults root in place against s the way the Kubernetes apiserver
// defaults a custom resource. A nil schema leaves root untouched.
func Apply(root *map[string]any, s *structuralschema.Structural) error {
	if s == nil {
		return nil
	}
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
