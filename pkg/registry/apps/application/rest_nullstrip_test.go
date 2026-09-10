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
	"reflect"
	"strings"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
)

// kubernetesSpecSchema builds the REST spec schema from the Kubernetes
// Application's shipped cozyrd. The real schema is the fixture on purpose: the
// bug this file pins came from the interaction between a declared-and-defaulted
// field, a free-form passthrough field and a user-keyed map, and a synthetic
// schema can be made to agree with any behaviour.
func kubernetesSpecSchema(t *testing.T) *structuralschema.Structural {
	t.Helper()
	raw := readEmbeddedOpenAPISchema(t,
		"../../../../packages/system/kubernetes-rd/cozyrds/kubernetes.yaml")
	s, err := buildSpecSchema(raw)
	if err != nil {
		t.Fatalf("buildSpecSchema: %v", err)
	}
	if s == nil {
		t.Fatal("buildSpecSchema returned nil for the shipped Kubernetes schema")
	}
	return s
}

func normalize(t *testing.T, s *structuralschema.Structural, spec string) ([]string, map[string]any) {
	t.Helper()
	r := &REST{specSchema: s}
	app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(spec)}}
	paths, err := r.normalizeSpecNulls(app)
	if err != nil {
		t.Fatalf("normalizeSpecNulls(%s): %v", spec, err)
	}
	var got map[string]any
	if err := json.Unmarshal(app.Spec.Raw, &got); err != nil {
		t.Fatalf("unmarshal normalized spec: %v", err)
	}
	return paths, got
}

func TestNormalizeSpecNulls_RequiredFields(t *testing.T) {
	s := kubernetesSpecSchema(t)

	paths, got := normalize(t, s, `{"host":"k8s","talos":{"registryMirrors":null,"version":null,"imageFactoryURL":"https://factory.example.invalid"}}`)

	want := []string{"talos.registryMirrors", "talos.version"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("stripped paths = %v, want %v", paths, want)
	}
	wantSpec := map[string]any{
		"host":  "k8s",
		"talos": map[string]any{"imageFactoryURL": "https://factory.example.invalid"},
	}
	if !reflect.DeepEqual(got, wantSpec) {
		t.Errorf("normalized spec = %v, want %v", got, wantSpec)
	}
}

func TestNormalizeSpecNulls_OptionalFieldsUntouched(t *testing.T) {
	s := kubernetesSpecSchema(t)

	paths, got := normalize(t, s, `{"talos":null,"storageClass":null,"nodeGroups":null,"version":null}`)

	if len(paths) != 0 {
		t.Errorf("stripped paths = %v, want none: the Kubernetes schema requires none of these at the root", paths)
	}
	for _, key := range []string{"talos", "storageClass", "nodeGroups", "version"} {
		v, ok := got[key]
		if !ok || v != nil {
			t.Errorf("%s = %v (present=%v), want a preserved null", key, v, ok)
		}
	}
}

// TestNormalizeSpecNulls_FreeFormSubtreeUntouched pins the boundary: inside
// registryMirrors the keys are registry hostnames the user invents, and the
// schema declares nothing there. A null in that position may be the payload, so
// it is left exactly as written rather than guessed at.
func TestNormalizeSpecNulls_FreeFormSubtreeUntouched(t *testing.T) {
	s := kubernetesSpecSchema(t)

	paths, got := normalize(t, s, `{"talos":{"registryMirrors":{"ghcr.io":null}}}`)

	if len(paths) != 0 {
		t.Errorf("stripped paths = %v, want none", paths)
	}
	mirrors := got["talos"].(map[string]any)["registryMirrors"].(map[string]any)
	if _, ok := mirrors["ghcr.io"]; !ok {
		t.Errorf("free-form key ghcr.io was dropped: %v", mirrors)
	}
}

func TestNormalizeSpecNulls_NoDefaultOrUserCollection(t *testing.T) {
	// Even an item schema default is not a Helm default for a user-created entry.
	s, err := buildSpecSchema(`{"type":"object","required":["missing","nullDefault","nullable","zero","false","empty"],"properties":{
  "missing":{"type":"string"},"nullDefault":{"type":"string","default":null},
  "nullable":{"type":"string","nullable":true,"default":"keep-null"},
  "zero":{"type":"integer","default":0},"false":{"type":"boolean","default":false},"empty":{"type":"string","default":""},
  "entries":{"type":"object","additionalProperties":{"type":"object","required":["name"],"properties":{"name":{"type":"string","default":"example"}}}},
  "items":{"type":"array","items":{"type":"object","required":["name"],"properties":{"name":{"type":"string","default":"example"}}}}
 }}`)
	if err != nil {
		t.Fatal(err)
	}
	paths, got := normalize(t, s, `{"missing":null,"nullDefault":null,"nullable":null,"zero":null,"false":null,"empty":null,"entries":{"mine":{"name":null},"blank":null},"items":[{"name":null},null]}`)
	if !reflect.DeepEqual(paths, []string{"empty", "false", "zero"}) {
		t.Fatalf("removed paths=%v", paths)
	}
	for _, key := range []string{"missing", "nullDefault", "nullable"} {
		if value, ok := got[key]; !ok || value != nil {
			t.Errorf("%s null changed", key)
		}
	}
	if !reflect.DeepEqual(got["entries"], map[string]any{"mine": map[string]any{"name": nil}, "blank": nil}) {
		t.Error("user map entries changed")
	}
	if !reflect.DeepEqual(got["items"], []any{map[string]any{"name": nil}, nil}) {
		t.Error("user array changed")
	}
}

func TestNormalizeSpecNulls_PreservesOtherValuesExactly(t *testing.T) {
	app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(`{"talos":{"registryMirrors":null},"large":9007199254740993,"negative":-9007199254740993,"text":"null","bool":false,"empty":[],"object":{}}`)}}
	r := &REST{specSchema: kubernetesSpecSchema(t)}
	if _, err := r.normalizeSpecNulls(app); err != nil {
		t.Fatal(err)
	}
	if string(app.Spec.Raw) != `{"bool":false,"empty":[],"large":9007199254740993,"negative":-9007199254740993,"object":{},"talos":{},"text":"null"}` {
		t.Fatalf("unrelated values changed: %s", app.Spec.Raw)
	}
}

// TestNormalizeSpecNulls_NoSchemaOrNoNulls pins the no-op paths: a kind whose
// ApplicationDefinition ships no schema must pass its spec through untouched,
// and a spec without nulls must not be re-serialized (re-marshalling reorders
// keys, which would show up as spurious HelmRelease churn).
func TestNormalizeSpecNulls_NoSchemaOrNoNulls(t *testing.T) {
	const spec = `{"b":1,"a":{"c":2}}`

	noSchema := &REST{}
	app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(spec)}}
	paths, err := noSchema.normalizeSpecNulls(app)
	if err != nil || paths != nil {
		t.Errorf("no schema: paths=%v err=%v, want no-op", paths, err)
	}
	if string(app.Spec.Raw) != spec {
		t.Errorf("no schema: spec rewritten to %s", app.Spec.Raw)
	}

	withSchema := &REST{specSchema: kubernetesSpecSchema(t)}
	clean := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(`{"host":"k8s"}`)}}
	paths, err = withSchema.normalizeSpecNulls(clean)
	if err != nil || paths != nil {
		t.Errorf("clean spec: paths=%v err=%v, want no-op", paths, err)
	}
	if string(clean.Spec.Raw) != `{"host":"k8s"}` {
		t.Errorf("clean spec rewritten to %s", clean.Spec.Raw)
	}
}

func TestEmptyFieldsWarning(t *testing.T) {
	short := emptyFieldsWarning([]string{"talos.version", "talos.registryMirrors"})
	if !strings.Contains(short, "talos.version, talos.registryMirrors") ||
		!strings.Contains(short, "the chart default applies") {
		t.Errorf("unexpected warning: %q", short)
	}

	many := make([]string, maxWarnedNullPaths+3)
	for i := range many {
		many[i] = "f"
	}
	long := emptyFieldsWarning(many)
	if !strings.Contains(long, "(and 3 more)") {
		t.Errorf("long warning not truncated: %q", long)
	}
}
