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
	"context"
	"encoding/json"
	"testing"

	internalapiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// An entry the check accepts must not merge into a spec that the application's own
// schema rejects, which is what the check exists to prevent, whether the schema puts
// a constraint on the property itself or in a branch of its parent. The shapes are
// the ones a branch can name: a dictionary, an atomic or free-form object, an object
// without properties, an array, a property that is only a union of types, and a
// partial object the user completes.
func TestInstallationDefaultsAcceptedEntriesMergeIntoValidSpecs(t *testing.T) {
	const (
		dictionaryBranch = `{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}},"allOf":[{"properties":{"m":{"required":["a"]}}}]}`
		atomicBranch     = `{"type":"object","properties":{"s":{"type":"object","x-kubernetes-map-type":"atomic","properties":{"a":{"type":"string"},"b":{"type":"string"}}}},"allOf":[{"properties":{"s":{"required":["a"]}}}]}`
		freeFormBranch   = `{"type":"object","properties":{"f":{"type":"object","x-kubernetes-preserve-unknown-fields":true}},"allOf":[{"properties":{"f":{"minProperties":1}}}]}`
		bareObjectBranch = `{"type":"object","properties":{"c":{"type":"object"}},"allOf":[{"properties":{"c":{"required":["z"]}}}]}`
		nestedBranch     = `{"type":"object","properties":{"outer":{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}},"allOf":[{"properties":{"m":{"required":["a"]}}}]}}}`
		valueBranch      = `{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"object","properties":{"c":{"type":"object","additionalProperties":{"type":"string"}}},"allOf":[{"properties":{"c":{"required":["z"]}}}]}}}}`
		union            = `{"type":"object","properties":{"u":{"oneOf":[{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}},{"type":"string"}]}}}`
		partialBranches  = `{"type":"object","properties":{"n":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}},"anyOf":[{"properties":{"n":{"required":["a"]}}},{"properties":{"n":{"required":["b"]}}}]}`
		rowsBranch       = `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}}},"allOf":[{"properties":{"rows":{"items":{"required":["name"]}}}}]}`
		branchInBranch   = `{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}},"allOf":[{"allOf":[{"properties":{"m":{"required":["a"]}}}]}]}`
		grandchildBranch = `{"type":"object","properties":{"outer":{"type":"object","properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}}}},"allOf":[{"properties":{"outer":{"properties":{"m":{"required":["a"]}}}}}]}`
		partialInBranch  = `{"type":"object","properties":{"nested":{"type":"object","properties":{"name":{"type":"string"},"enabled":{"type":"boolean"}}}},"allOf":[{"allOf":[{"properties":{"nested":{"required":["name"]}}}]}]}`
		partialGrandkid  = `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"object","properties":{"name":{"type":"string"},"enabled":{"type":"boolean"}}}}}},"allOf":[{"properties":{"outer":{"properties":{"inner":{"required":["name"]}}}}}]}`
	)
	for _, tc := range []struct {
		name, schema, entry, user string
		accepted                  bool
	}{
		{"dictionary, key missing", dictionaryBranch, `{"m":{}}`, `{}`, false},
		{"dictionary, key present", dictionaryBranch, `{"m":{"a":"x"}}`, `{}`, true},
		{"atomic object", atomicBranch, `{"s":{"b":"x"}}`, `{}`, false},
		{"free-form object", freeFormBranch, `{"f":{}}`, `{}`, false},
		{"object without properties", bareObjectBranch, `{"c":{}}`, `{}`, false},
		{"dictionary of a nested object", nestedBranch, `{"outer":{"m":{}}}`, `{}`, false},
		{"dictionary inside a dictionary value", valueBranch, `{"m":{"k":{"c":{}}}}`, `{}`, false},
		{"union, incomplete object", union, `{"u":{}}`, `{}`, false},
		{"union, complete object", union, `{"u":{"a":"x"}}`, `{}`, true},
		{"partial object the user completes", partialBranches, `{"n":{"a":"x"}}`, `{"n":{"b":"y"}}`, true},
		{"dictionary, constraint in a branch of a branch", branchInBranch, `{"m":{}}`, `{}`, false},
		{"dictionary of a nested object, constraint set one level up", grandchildBranch, `{"outer":{"m":{}}}`, `{}`, false},
		{"partial object the user completes, constraint in a branch of a branch", partialInBranch, `{"nested":{"enabled":true}}`, `{"nested":{"name":"x"}}`, true},
		{"partial object the user completes, constraint set one level up", partialGrandkid, `{"outer":{"inner":{"enabled":true}}}`, `{"outer":{"inner":{"name":"x"}}}`, true},
		{"array row, incomplete", rowsBranch, `{"rows":[{}]}`, `{}`, false},
		{"array row, complete", rowsBranch, `{"rows":[{"name":"a"}]}`, `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsRESTWithSchema(t, tc.schema, defaultsConfig(`{"widgets":`+tc.entry+`}`))
			app := defaultsApp(tc.user)
			err := r.applyInstallationDefaults(context.Background(), app)
			if (err == nil) != tc.accepted {
				t.Fatalf("entry %s: accepted = %v, want %v (%v)", tc.entry, err == nil, tc.accepted, err)
			}
			if err != nil {
				return
			}
			var external apiextv1.JSONSchemaProps
			if err := json.Unmarshal([]byte(tc.schema), &external); err != nil {
				t.Fatal(err)
			}
			var schema internalapiext.JSONSchemaProps
			if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&external, &schema, nil); err != nil {
				t.Fatal(err)
			}
			validator, _, err := apiextvalidation.NewSchemaValidator(&schema)
			if err != nil {
				t.Fatal(err)
			}
			var merged map[string]any
			if err := json.Unmarshal(app.Spec.Raw, &merged); err != nil {
				t.Fatal(err)
			}
			if errs := apiextvalidation.ValidateCustomResource(field.NewPath("spec"), merged, validator); len(errs) > 0 {
				t.Fatalf("accepted entry %s merges into %s, which the schema rejects: %v", tc.entry, app.Spec.Raw, errs.ToAggregate())
			}
		})
	}
}

// What the merge stores whole is judged whole at any depth, and what it completes
// field by field is judged partial, also when the constraint on it sits in a branch
// of its parent.
func TestInstallationDefaultsJudgeStoredWholeAtEveryDepth(t *testing.T) {
	const dictionaryOfRequired = `{"type":"object","required":["n"],"properties":{"n":{"type":"string"}}}`
	for _, tc := range []struct {
		name, schema, entry, user string
		rejected                  bool
	}{
		{"dictionary value at the root, incomplete", `{"type":"object","additionalProperties":` + dictionaryOfRequired + `}`, `{"a":{}}`, `{}`, true},
		{"dictionary value at the root, complete", `{"type":"object","additionalProperties":` + dictionaryOfRequired + `}`, `{"a":{"n":"x"}}`, `{}`, false},
		{
			"constraint on a partial object set by a branch of its parent",
			`{"type":"object","properties":{"nested":{"type":"object","properties":{"name":{"type":"string"},"enabled":{"type":"boolean"}}}},"allOf":[{"properties":{"nested":{"required":["name"]}}}]}`,
			`{"nested":{"enabled":true}}`, `{"nested":{"name":"x"}}`, false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsRESTWithSchema(t, tc.schema, defaultsConfig(`{"widgets":`+tc.entry+`}`))
			err := r.applyInstallationDefaults(context.Background(), defaultsApp(tc.user))
			if tc.rejected && err == nil {
				t.Fatalf("incomplete entry %s accepted", tc.entry)
			}
			if !tc.rejected && err != nil {
				t.Fatalf("entry %s rejected: %v", tc.entry, err)
			}
		})
	}
}

// The merge completes an object when declaredProperty says so, and validation
// treats a property as partial exactly then; a branch of an allOf, anyOf or oneOf
// takes the answer for the property it constrains, which the test above covers.
// The two sides must not drift apart.
func TestInstallationDefaultsStoredWholeIsTheInverseOfDeclaredProperty(t *testing.T) {
	for name, child := range map[string]string{
		"declared properties":           `{"type":"object","properties":{"a":{"type":"string"}}}`,
		"granular":                      `{"type":"object","x-kubernetes-map-type":"granular","properties":{"a":{"type":"string"}}}`,
		"atomic":                        `{"type":"object","x-kubernetes-map-type":"atomic","properties":{"a":{"type":"string"}}}`,
		"also takes any keys":           `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`,
		"also takes typed keys":         `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":{"type":"string"}}`,
		"closed":                        `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`,
		"preserves unknown fields":      `{"type":"object","properties":{"a":{"type":"string"}},"x-kubernetes-preserve-unknown-fields":true}`,
		"dictionary":                    `{"type":"object","additionalProperties":{"type":"string"}}`,
		"free form":                     `{"type":"object","x-kubernetes-preserve-unknown-fields":true}`,
		"no properties":                 `{"type":"object"}`,
		"scalar":                        `{"type":"string"}`,
		"array":                         `{"type":"array","items":{"type":"string"}}`,
		"declared properties, nullable": `{"type":"object","nullable":true,"properties":{"a":{"type":"string"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var parent apiextv1.JSONSchemaProps
			if err := json.Unmarshal([]byte(`{"type":"object","properties":{"child":`+child+`}}`), &parent); err != nil {
				t.Fatal(err)
			}
			var internal internalapiext.JSONSchemaProps
			if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&parent, &internal, nil); err != nil {
				t.Fatal(err)
			}
			_, completed := declaredProperty(&parent, "child")
			stored := internal.Properties["child"]
			if storedWhole(&stored) == completed {
				t.Fatalf("storedWhole = %v while the merge completes it = %v", storedWhole(&stored), completed)
			}
		})
	}
}
