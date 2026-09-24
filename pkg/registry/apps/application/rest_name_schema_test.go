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
	"strings"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// TestNameCapComesFromTheShippedSchema walks the path a cap travels: the
// chart's `## @name` annotation, through cozyvalues-gen into
// values.schema.json, into the ApplicationDefinition read here off disk, and
// into the name check. Rewriting the cap inside the shipped schema moves the
// limit with it, which a cap held in code would not.
func TestNameCapComesFromTheShippedSchema(t *testing.T) {
	for _, tc := range []struct {
		kind, prefix, definition string
		cap                      int
		explains                 string
	}{
		{"Kubernetes", "kubernetes-", "../../../../packages/system/kubernetes-rd/cozyrds/kubernetes.yaml", 32, "md0"},
		{"Kafka", "kafka-", "../../../../packages/system/kafka-rd/cozyrds/kafka.yaml", 44, "KRaft"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			raw := readEmbeddedOpenAPISchema(t, tc.definition)
			r := newTestREST(t, tc.kind, tc.prefix, raw)

			if errs := r.validateNameLength(strings.Repeat("a", tc.cap)); len(errs) > 0 {
				t.Errorf("name at the declared cap rejected: %v", errs)
			}
			errs := r.validateNameLength(strings.Repeat("a", tc.cap+1))
			if len(errs) != 1 {
				t.Fatalf("expected the declared cap to reject a %d-character name, got %v", tc.cap+1, errs)
			}
			if !strings.Contains(errs[0].Error(), tc.explains) {
				t.Errorf("rejection does not carry the chart's explanation of the limit: %v", errs[0])
			}

			moved := newTestREST(t, tc.kind, tc.prefix, withDeclaredMaxLength(t, raw, 20))
			if errs := moved.validateNameLength(strings.Repeat("a", 20)); len(errs) > 0 {
				t.Errorf("name at the rewritten cap rejected: %v", errs)
			}
			if errs := moved.validateNameLength(strings.Repeat("a", 21)); len(errs) != 1 {
				t.Errorf("rewriting maxLength in the schema did not move the limit: %v", errs)
			}
		})
	}
}

// TestLegacyNameCapsCoverUndeclaredDefinitions pins the upgrade-window floor:
// it holds only while a definition declares nothing, and a declaration replaces
// it outright.
func TestLegacyNameCapsCoverUndeclaredDefinitions(t *testing.T) {
	const undeclared = `{"title":"Chart Values","type":"object","properties":{}}`

	if errs := newTestREST(t, "Kubernetes", "kubernetes-", undeclared).validateNameLength(strings.Repeat("a", 33)); len(errs) != 1 {
		t.Errorf("Kubernetes definition without a declaration lost the 32-character floor: %v", errs)
	}
	if errs := newTestREST(t, "Kafka", "kafka-", undeclared).validateNameLength(strings.Repeat("a", 45)); len(errs) != 1 {
		t.Errorf("Kafka definition without a declaration lost the 44-character floor: %v", errs)
	}

	declared := `{"type":"object","x-cozystack-name":{"type":"string","maxLength":40},"properties":{}}`
	if errs := newTestREST(t, "Kubernetes", "kubernetes-", declared).validateNameLength(strings.Repeat("a", 40)); len(errs) > 0 {
		t.Errorf("a declaration did not replace the floor: %v", errs)
	}

	if errs := newTestREST(t, "Widget", "widget-", undeclared).validateNameLength(strings.Repeat("a", 46)); len(errs) > 0 {
		t.Errorf("a kind with no floor is capped below its Helm budget: %v", errs)
	}
}

// TestInvalidNameSchemaFailsClosed covers every way a declaration can be
// unenforceable as written. Each one refuses creates of that kind with a
// configuration error, instead of dropping the declaration (admitting names
// the chart cannot render) or applying it (rejecting every name and blaming
// the user).
func TestInvalidNameSchemaFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, want string
	}{
		{"null declaration", `null`, "declaration is null"},
		{"null maxLength", `{"maxLength":null}`, "maxLength is null"},
		{"null pattern", `{"maxLength":32,"pattern":null}`, "pattern is null"},
		{"not an object", `"32"`, "decode"},
		{"array", `[32]`, "decode"},
		{"maxLength as string", `{"maxLength":"32"}`, "decode"},
		{"fractional maxLength", `{"maxLength":32.5}`, "decode"},
		{"overflowing maxLength", `{"maxLength":1e20}`, "decode"},
		{"misspelled keyword", `{"maxLenght":32}`, `unknown field "maxLenght"`},
		{"one bad field beside a good one", `{"minLength":"3","maxLength":32}`, "decode"},
		{"non-string type", `{"type":"integer","maxLength":32}`, `type "integer"`},
		{"zero maxLength", `{"maxLength":0}`, "maxLength 0 admits no name"},
		{"negative maxLength", `{"maxLength":-1}`, "maxLength -1 admits no name"},
		{"negative minLength", `{"minLength":-1}`, "minLength -1 is negative"},
		{"minLength above maxLength", `{"minLength":40,"maxLength":32}`, "minLength 40 exceeds maxLength 32"},
		{"minLength above the Helm budget", `{"minLength":47}`, "no name fits"},
		{"non-RE2 pattern", `{"pattern":"^(?!kube)[a-z]+$"}`, "does not compile as RE2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := `{"type":"object","x-cozystack-name":` + tc.declaration + `,"properties":{}}`
			r := newTestREST(t, "Widget", "widget-", schema)

			if r.nameSchemaErr == nil || !strings.Contains(r.nameSchemaErr.Error(), tc.want) {
				t.Fatalf("expected a declaration error containing %q, got %v", tc.want, r.nameSchemaErr)
			}
			errs := r.validateNameLength("ok")
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), "configuration error") || !strings.Contains(errs[0].Error(), "Widget") {
				t.Errorf("expected a configuration error naming the kind for a valid name, got %v", errs)
			}
		})
	}
}

// TestNameSchemaEnforcesMinLengthAndPattern covers the other constraints a
// chart can declare, so a declaration is never silently a no-op.
func TestNameSchemaEnforcesMinLengthAndPattern(t *testing.T) {
	const schema = `{
	  "title": "Chart Values",
	  "type": "object",
	  "x-cozystack-name": {
	    "type": "string",
	    "description": "the chart splits the release name on dashes",
	    "minLength": 3,
	    "pattern": "^[a-z][a-z0-9]*$"
	  },
	  "properties": {}
	}`
	r := newTestREST(t, "Widget", "widget-", schema)

	if errs := r.validateNameFormat("widget"); len(errs) > 0 {
		t.Errorf("conforming name rejected: %v", errs)
	}
	if errs := r.validateNameFormat("ab"); len(errs) != 1 {
		t.Errorf("expected minLength to reject a 2-character name, got %v", errs)
	}
	if errs := r.validateNameFormat("has-dash"); len(errs) != 1 {
		t.Errorf("expected pattern to reject %q, got %v", "has-dash", errs)
	}

	exact := newTestREST(t, "Widget", "widget-", `{"type":"object","x-cozystack-name":{"minLength":8,"maxLength":8},"properties":{}}`)
	if exact.nameSchemaErr != nil {
		t.Fatalf("minLength equal to maxLength is a valid declaration, got %v", exact.nameSchemaErr)
	}
	if errs := append(exact.validateNameFormat("abcdefgh"), exact.validateNameLength("abcdefgh")...); len(errs) > 0 {
		t.Errorf("name of exactly the declared length rejected: %v", errs)
	}
}

// TestUndescribedDeclarationStillExplainsItself keeps a rejection pointing at
// its source when the chart declares a limit without saying why.
func TestUndescribedDeclarationStillExplainsItself(t *testing.T) {
	r := newTestREST(t, "Widget", "widget-", `{"type":"object","x-cozystack-name":{"maxLength":10,"minLength":3},"properties":{}}`)

	errs := append(r.validateNameLength(strings.Repeat("a", 11)), r.validateNameFormat("ab")...)
	if len(errs) != 2 {
		t.Fatalf("expected both limits to reject, got %v", errs)
	}
	for _, err := range errs {
		if !strings.Contains(err.Error(), "limit declared by the Widget application") {
			t.Errorf("rejection does not say where the limit comes from: %v", err)
		}
	}
}

// TestParseNameSchemaDeclaresNothing covers the common case: schemas with no
// declaration, which must not produce one.
func TestParseNameSchemaDeclaresNothing(t *testing.T) {
	for _, in := range []string{"", "   ", `{"title":"Chart Values","type":"object"}`} {
		got, err := parseNameSchema(in)
		if err != nil {
			t.Errorf("parseNameSchema(%q) returned error: %v", in, err)
		}
		if got != nil {
			t.Errorf("parseNameSchema(%q) returned a declaration where the schema has none", in)
		}
	}
	if _, err := parseNameSchema("{not json"); err == nil {
		t.Error("parseNameSchema accepted malformed JSON without error")
	}
}

// withDeclaredMaxLength returns the schema with its name declaration's
// maxLength replaced.
func withDeclaredMaxLength(t *testing.T, raw string, maxLen int) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	declared, ok := doc[appsv1alpha1.NameSchemaExtension].(map[string]any)
	if !ok {
		t.Fatalf("schema carries no %s", appsv1alpha1.NameSchemaExtension)
	}
	declared["maxLength"] = maxLen
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return string(out)
}

// newTestREST builds a REST the way the server does at start-up, so the tests
// exercise the wiring in NewREST rather than a hand-set field.
func newTestREST(t *testing.T, kind, prefix, openAPISchema string) *REST {
	t.Helper()
	return NewREST(nil, nil, &config.Resource{
		Application: config.ApplicationConfig{
			Kind:          kind,
			Singular:      strings.ToLower(kind),
			Plural:        strings.ToLower(kind) + "s",
			OpenAPISchema: openAPISchema,
		},
		Release: config.ReleaseConfig{Prefix: prefix},
	})
}
