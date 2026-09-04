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

// TestNormalizeSpecNulls_RequiredFields pins the fix: a required field written
// with nothing under it is dropped, so the chart default applies to it instead
// of being deleted along with the null by Helm's value coalescing. Before this,
// `registryMirrors:` failed the whole HelmRelease with "at '/talos': missing
// property 'registryMirrors'" - a property the user had in fact written.
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

// TestNormalizeSpecNulls_OptionalFieldsUntouched is the guard on the boundary:
// where the schema permits a field to be absent, a null keeps the meaning it has
// today - the chart default is suppressed and the key never reaches the template.
// Repairing these too would change what a running application renders: 167
// optional fields across the catalogue carry a non-trivial default, placeholder
// credentials and example S3 buckets among them.
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

// TestNormalizeSpecNulls_UserKeyedMapEntry covers both halves of a map whose
// keys the user chooses: the entry itself stays (dropping it would drop a node
// group the user asked for), while a declared field inside it is normalized the
// same way as anywhere else.
func TestNormalizeSpecNulls_UserKeyedMapEntry(t *testing.T) {
	s := kubernetesSpecSchema(t)

	paths, got := normalize(t, s, `{"nodeGroups":{"md0":null,"md1":{"instanceType":null,"storageClass":null,"minReplicas":2}}}`)

	if !reflect.DeepEqual(paths, []string{"nodeGroups.md1.instanceType"}) {
		t.Errorf("stripped paths = %v, want [nodeGroups.md1.instanceType]", paths)
	}
	groups := got["nodeGroups"].(map[string]any)
	if v, ok := groups["md0"]; !ok || v != nil {
		t.Errorf("md0 = %v (present=%v), want a preserved null", v, ok)
	}
	md1 := groups["md1"].(map[string]any)
	if _, ok := md1["instanceType"]; ok {
		t.Errorf("md1.instanceType is required by the schema and survived: %v", md1)
	}
	if v, ok := md1["storageClass"]; !ok || v != nil {
		t.Errorf("md1.storageClass = %v (present=%v), want a preserved null: the schema allows it to be absent", v, ok)
	}
	if md1["minReplicas"] != float64(2) {
		t.Errorf("md1.minReplicas = %v, want 2", md1["minReplicas"])
	}
}

// TestStripDeclaredNulls_ArrayPositionsKept pins that array elements are never
// removed - an index is part of the address of every element after it - while a
// declared field inside an element is still normalized.
func TestStripDeclaredNulls_ArrayPositionsKept(t *testing.T) {
	s := &structuralschema.Structural{
		Generic: structuralschema.Generic{Type: "object"},
		Properties: map[string]structuralschema.Structural{
			"items": {
				Generic: structuralschema.Generic{Type: "array"},
				Items: &structuralschema.Structural{
					Generic: structuralschema.Generic{Type: "object"},
					Properties: map[string]structuralschema.Structural{
						"name": {Generic: structuralschema.Generic{Type: "string"}},
					},
					ValueValidation: &structuralschema.ValueValidation{
						Required: []string{"name"},
					},
				},
			},
		},
	}

	spec := map[string]any{"items": []any{map[string]any{"name": nil}, nil}}
	var stripped []string
	stripDeclaredNulls(spec, s, "", &stripped)

	if !reflect.DeepEqual(stripped, []string{"items[0].name"}) {
		t.Errorf("stripped = %v, want [items[0].name]", stripped)
	}
	list := spec["items"].([]any)
	if len(list) != 2 {
		t.Fatalf("array length = %d, want 2 (elements must keep their positions)", len(list))
	}
	if len(list[0].(map[string]any)) != 0 {
		t.Errorf("items[0] = %v, want {}", list[0])
	}
	if list[1] != nil {
		t.Errorf("items[1] = %v, want the null left in place", list[1])
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
