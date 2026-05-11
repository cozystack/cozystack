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
	"reflect"
	"sort"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

func makeApp(name, namespace, rawSpec string) *appsv1alpha1.Application {
	app := &appsv1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	if rawSpec != "" {
		app.Spec = &apiextensionsv1.JSON{Raw: []byte(rawSpec)}
	}
	return app
}

func TestDeprecationMessagesFor_TopAndNested(t *testing.T) {
	app := makeApp("db1", "tenant-x", `{
		"resourcesPreset":"small",
		"sub":{"resourcesPreset":"micro"}
	}`)
	got := deprecationMessagesFor("Postgres", app)
	sort.Strings(got)

	want := []string{
		`Postgres/db1 in tenant-x uses deprecated resourcesPreset "micro" at spec.sub.resourcesPreset; migrate to "t1.micro" (1:1 equivalent CPU and memory)`,
		`Postgres/db1 in tenant-x uses deprecated resourcesPreset "small" at spec.resourcesPreset; migrate to "t1.small" (1:1 equivalent CPU and memory)`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestDeprecationMessagesFor_NewValuesSilent(t *testing.T) {
	app := makeApp("db1", "tenant-x",
		`{"resourcesPreset":"t1.small","sub":{"resourcesPreset":"c1.medium"}}`)
	if got := deprecationMessagesFor("Postgres", app); len(got) != 0 {
		t.Errorf("instance-type values must not produce messages, got %v", got)
	}
}

func TestDeprecationMessagesFor_NilAndEmpty(t *testing.T) {
	if got := deprecationMessagesFor("Postgres", nil); got != nil {
		t.Errorf("nil app produced %v", got)
	}
	if got := deprecationMessagesFor("Postgres", &appsv1alpha1.Application{}); got != nil {
		t.Errorf("app with nil spec produced %v", got)
	}
	emptyRaw := makeApp("x", "ns", "")
	emptyRaw.Spec = &apiextensionsv1.JSON{Raw: []byte("")}
	if got := deprecationMessagesFor("Postgres", emptyRaw); got != nil {
		t.Errorf("empty raw spec produced %v", got)
	}
}

func TestDeprecationMessagesFor_MalformedSpec(t *testing.T) {
	app := makeApp("x", "ns", "{not-json")
	if got := deprecationMessagesFor("Postgres", app); got != nil {
		t.Errorf("malformed spec produced %v", got)
	}
}

// TestREST_warnLegacyPresets verifies the wrapper is wired to the helper
// and that calling it with edge-case inputs does not panic. Output
// correctness is asserted on the helper directly above; this test
// guards the integration so a future refactor that bypasses
// deprecationMessagesFor must replace it with an equivalent path.
func TestREST_warnLegacyPresets_doesNotPanic(t *testing.T) {
	r := &REST{kindName: "Postgres"}
	r.warnLegacyPresets(nil)
	r.warnLegacyPresets(&appsv1alpha1.Application{})
	r.warnLegacyPresets(makeApp("db", "ns", `{"resourcesPreset":"small"}`))
	r.warnLegacyPresets(makeApp("db", "ns", `{"resourcesPreset":"t1.small"}`))
	r.warnLegacyPresets(makeApp("db", "ns", `{broken`))
}
