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

package server

import (
	"encoding/json"
	"strings"
	"testing"

	celopenapi "k8s.io/apiserver/pkg/cel/openapi"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// tenantOpenAPISchema mirrors the shape of the real Tenant "Chart Values"
// schema (packages/system/tenant-rd/cozyrds/tenant.yaml): a typed object with
// declared properties and no top-level additionalProperties, plus a nested
// map that uses the safe object-form additionalProperties.
const tenantOpenAPISchema = `{
  "title": "Chart Values",
  "type": "object",
  "properties": {
    "host": {"type": "string"},
    "etcd": {"type": "boolean"},
    "resourceQuotas": {
      "type": "object",
      "additionalProperties": {
        "anyOf": [{"type": "integer"}, {"type": "string"}],
        "x-kubernetes-int-or-string": true
      }
    }
  }
}`

func newObjectContainer() *spec.Schema {
	return &spec.Schema{SchemaProps: spec.SchemaProps{
		Type:       spec.StringOrArray{"object"},
		Properties: map[string]spec.Schema{},
	}}
}

// TestPatchSpecOpenSpecPublishesPreserveUnknownFields asserts that the open
// ".spec" cozystack-api injects is published as
// x-kubernetes-preserve-unknown-fields:true and never as the boolean
// additionalProperties:true form that crashes the VAP type-checker (#2863).
// It covers both code paths: a schemaless resource and a custom schema that
// declares properties but no top-level additionalProperties (like Tenant).
func TestPatchSpecOpenSpecPublishesPreserveUnknownFields(t *testing.T) {
	cases := map[string]string{
		"schemaless":           "",
		"custom-no-additional": tenantOpenAPISchema,
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			target := newObjectContainer()
			if err := patchSpec(target, raw); err != nil {
				t.Fatalf("patchSpec: %v", err)
			}
			specSchema := target.Properties["spec"]

			if specSchema.AdditionalProperties != nil {
				t.Errorf("spec must not carry additionalProperties; got %#v", specSchema.AdditionalProperties)
			}
			if v, ok := specSchema.Extensions.GetBool("x-kubernetes-preserve-unknown-fields"); !ok || !v {
				t.Errorf("spec must set x-kubernetes-preserve-unknown-fields:true; extensions=%v", specSchema.Extensions)
			}
			if !specSchema.Type.Contains("object") {
				t.Errorf("spec must be type object; got %v", specSchema.Type)
			}

			// The published JSON is the actual contract KCM type-checks against.
			out, err := json.Marshal(specSchema)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(out), `"additionalProperties":true`) {
				t.Errorf("published spec still emits boolean additionalProperties:true (#2863): %s", out)
			}
			if !strings.Contains(string(out), `"x-kubernetes-preserve-unknown-fields":true`) {
				t.Errorf("published spec missing x-kubernetes-preserve-unknown-fields:true: %s", out)
			}
		})
	}
}

// TestPatchSpecKeepsObjectFormAdditionalProperties ensures we only rewrite the
// open/free-form spec: a custom schema that already declares an object-form
// additionalProperties (a real map type, which is safe for the type-checker)
// must be left untouched rather than overwritten with the extension.
func TestPatchSpecKeepsObjectFormAdditionalProperties(t *testing.T) {
	raw := `{"type":"object","additionalProperties":{"type":"string"}}`
	target := newObjectContainer()
	if err := patchSpec(target, raw); err != nil {
		t.Fatalf("patchSpec: %v", err)
	}
	specSchema := target.Properties["spec"]
	if specSchema.AdditionalProperties == nil || specSchema.AdditionalProperties.Schema == nil {
		t.Fatalf("object-form additionalProperties must be preserved; got %#v", specSchema.AdditionalProperties)
	}
	if v, ok := specSchema.Extensions.GetBool("x-kubernetes-preserve-unknown-fields"); ok && v {
		t.Errorf("must not add preserve-unknown-fields when a real map schema is declared")
	}
}

// TestPatchedSpecDoesNotPanicVAPTypeChecker is the behavioral regression test
// for #2863. SchemaDeclType is the exact entry point kube-controller-manager's
// ValidatingAdmissionPolicy status controller uses to type-check a VAP against
// the published resource schema. Before the fix, the boolean
// additionalProperties:true on ".spec" made it nil-dereference
// (k8s.io/apiserver/pkg/cel/openapi.isExtension), crash-looping KCM. With the
// preserve-unknown-fields form it returns a valid type.
//
// This test panics (fails) on the pre-fix code and passes after it.
func TestPatchedSpecDoesNotPanicVAPTypeChecker(t *testing.T) {
	cases := map[string]string{
		"schemaless":  "",
		"tenant-like": tenantOpenAPISchema,
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// Resource-shaped object, as published per kind by cozystack-api.
			obj := &spec.Schema{SchemaProps: spec.SchemaProps{
				Type: spec.StringOrArray{"object"},
				Properties: map[string]spec.Schema{
					"apiVersion": {SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"string"}}},
					"kind":       {SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{"string"}}},
					"spec":       {},
				},
			}}
			if err := patchSpec(obj, raw); err != nil {
				t.Fatalf("patchSpec: %v", err)
			}

			// Round-trip through JSON so we type-check the serialized form that
			// is actually served on /openapi/v3 and read by KCM.
			rawJSON, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var published spec.Schema
			if err := json.Unmarshal(rawJSON, &published); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// isResourceRoot=true matches how the type-checker treats a matched
			// top-level resource.
			declType := celopenapi.SchemaDeclType(&published, true)
			if declType == nil {
				t.Fatalf("SchemaDeclType returned nil for %s", name)
			}
		})
	}
}
