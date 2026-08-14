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

package openapi

import (
	"sort"
	"strings"
	"testing"

	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// The two tests below guard the published OpenAPI document against the class of
// defect in https://github.com/cozystack/cozystack/issues/3806: a definition
// name that is a Go import path rather than a dotted Kubernetes model name.
//
// Since Kubernetes 0.35 the apiserver's DefinitionNamer.GetDefinitionName
// returns the model name it was given verbatim — it no longer converts the Go
// import-path form into the "friendly" reversed-path form — so whatever name
// this generated map uses becomes both the published definition key and, JSON
// pointer escaped, the $ref that points at it. A name containing "/" therefore
// ships as a definition keyed on the raw path while every reference to it is
// spelled with "~1" in place of each slash, and clients resolve a $ref by
// trimming "#/definitions/" without unescaping (kube-openapi
// pkg/util/proto/document.go). The two spellings never meet, so the reference
// dangles and client-side validation fails on every resource of the group:
//
//	error validating data: SchemaError(…core/v1alpha1.Option.spec):
//	unknown model in reference: "github.com~1cozystack~1…v1alpha1.OptionSpec"
//
// The fix for a group is the +k8s:openapi-model-package marker in its doc.go
// plus an OpenAPIModelName method per type (see each package's model_name.go).
// Nothing asserted that, which is why the core and sdn groups shipped broken in
// v1.6.0 and v1.6.1. These tests assert the invariant rather than today's set of
// names, so a future group that omits the marker fails here instead of in a
// cluster.

// buildDefinitions builds the generated definition map, recording every model
// name the generated code passes to the reference callback. Refs are built
// exactly as kube-openapi's builder builds them, so the recorded name and the
// emitted $ref differ in the same way they do in the served document.
func buildDefinitions() (defs map[string]common.OpenAPIDefinition, refNames []string) {
	defs = GetOpenAPIDefinitions(func(path string) spec.Ref {
		refNames = append(refNames, path)
		return spec.MustCreateRef("#/definitions/" + common.EscapeJsonPointer(path))
	})
	return defs, refNames
}

// sortedKeys returns the definition names in a stable order so failures are
// reproducible rather than map-iteration ordered.
func sortedKeys(defs map[string]common.OpenAPIDefinition) []string {
	out := make([]string, 0, len(defs))
	for name := range defs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestDefinitionNamesAreDottedModelNames asserts that no published definition
// name contains a slash. A slash-bearing name is a Go import path that escaped
// as-is, which means the group it belongs to is missing its
// +k8s:openapi-model-package marker and its OpenAPIModelName methods.
func TestDefinitionNamesAreDottedModelNames(t *testing.T) {
	defs, _ := buildDefinitions()

	// Guard against the assertion going vacuous: the map must be populated and
	// must actually cover cozystack's own types, not only the vendored
	// apimachinery and apiextensions models that already declare their names.
	if len(defs) == 0 {
		t.Fatal("GetOpenAPIDefinitions returned no definitions; this test is asserting nothing")
	}
	own := 0
	for _, name := range sortedKeys(defs) {
		if strings.Contains(name, "cozystack") {
			own++
		}
	}
	if own == 0 {
		t.Fatal("no cozystack-owned definitions found; this test is asserting nothing")
	}

	for _, name := range sortedKeys(defs) {
		if strings.Contains(name, "/") {
			t.Errorf("definition %q is a Go import path, not a dotted model name: "+
				"every $ref to it is escaped with ~1 and will not resolve. Add "+
				"+k8s:openapi-model-package=<dotted package> to that package's doc.go and an "+
				"OpenAPIModelName method for the type in its model_name.go, then re-run make generate",
				name)
		}
	}
}

// TestDefinitionRefsResolve asserts that every $ref the generated document
// emits resolves to a published definition, resolving it the way a client does:
// trim "#/definitions/" and look the remainder up verbatim, with no
// unescaping. This is the failure the user sees, so it is checked directly
// rather than only through the slash-freedom proxy above; it also catches a
// group whose types disagree with each other (some named, some not) and a ref
// to a model that is not published at all.
func TestDefinitionRefsResolve(t *testing.T) {
	defs, refNames := buildDefinitions()

	if len(refNames) == 0 {
		t.Fatal("generated definitions emitted no $ref; this test is asserting nothing")
	}

	// Index the published definitions under the key a client sees, which is the
	// definition name exactly as published.
	published := make(map[string]struct{}, len(defs))
	for name := range defs {
		published[name] = struct{}{}
	}

	seen := make(map[string]struct{}, len(refNames))
	for _, modelName := range refNames {
		// The served $ref, built the way kube-openapi's builder builds it.
		ref := "#/definitions/" + common.EscapeJsonPointer(modelName)
		// The lookup a client performs on it.
		resolved := strings.TrimPrefix(ref, "#/definitions/")
		if _, ok := published[resolved]; ok {
			continue
		}
		if _, dup := seen[resolved]; dup {
			continue
		}
		seen[resolved] = struct{}{}
		t.Errorf("$ref %q does not resolve: no definition is published under %q (model name %q)",
			ref, resolved, modelName)
	}

	// Dependencies must agree with the refs: the generated code lists them from
	// the same set, and consumers walk them to build the transitive closure.
	for _, name := range sortedKeys(defs) {
		for _, dep := range defs[name].Dependencies {
			if _, ok := published[dep]; !ok {
				t.Errorf("definition %q declares dependency %q, which is not published", name, dep)
			}
		}
	}
}
