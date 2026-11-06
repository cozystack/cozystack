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
	"strings"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const installationDefaultsSchema = `{
  "type":"object", "required":["required"], "allOf":[{"properties":{"replicas":{"maximum":8}}}], "properties":{
    "required":{"type":"string"},
    "replicas":{"type":"integer","minimum":0,"maximum":10},
    "mode":{"type":"string","enum":["safe","fast"]},
    "nested":{"type":"object","required":["name"],"allOf":[{"required":["name"]}],"properties":{"name":{"type":"string"},"enabled":{"type":"boolean"}}},
    "rows":{"type":"array","minItems":1,"items":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}},
    "map":{"type":"object","additionalProperties":{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"enabled":{"type":"boolean"}}}},
    "free":{"type":"object","additionalProperties":true},
    "preserved":{"type":"object","x-kubernetes-preserve-unknown-fields":true,"properties":{"known":{"type":"integer"}}},
    "closed":{"type":"object","additionalProperties":false},
    "choice":{"type":"string","oneOf":[{"enum":["safe"]},{"enum":["fast"]}]},
    "pair":{"type":"object","minProperties":2,"properties":{"a":{"type":"integer"},"b":{"type":"integer"}}},
    "either":{"type":"object","properties":{"url":{"type":"string"},"path":{"type":"string"}},"oneOf":[{"required":["url"]},{"required":["path"]}]},
    "nullable":{"type":"string","nullable":true}
  }
}`

func TestInstallationDefaultsSchemaValidation(t *testing.T) {
	for _, tc := range []struct {
		name, values, errorPath string
	}{
		{"partial object", `{"nested":{"enabled":false}}`, ""},
		{"empty object", `{"nested":{}}`, ""},
		{"partial dictionary value", `{"map":{"entry":{"enabled":true}}}`, ""},
		{"complete array", `{"rows":[{"name":"worker"}]}`, ""},
		{"free form", `{"free":{"arbitrary":{"nested":true}}}`, ""},
		{"preserve unknown", `{"preserved":{"arbitrary":{"nested":true}}}`, ""},
		{"nullable", `{"nullable":null}`, ""},
		{"partial object under minProperties", `{"pair":{"a":1}}`, ""},
		{"scalar oneOf accepted", `{"choice":"safe"}`, ""},
		{"scalar oneOf rejected", `{"choice":"invalid"}`, "choice"},
		{"one alternative of a oneOf", `{"either":{"url":"https://example.com"}}`, ""},
		{"no alternative of a oneOf", `{"either":{}}`, ""},
		{"wrong type inside oneOf object", `{"either":{"url":7}}`, "either.url"},
		{"unknown field", `{"typo":true}`, "typo"},
		{"unknown metadata", `{"metadata":{"name":"wrong"}}`, "metadata"},
		{"nested unknown", `{"nested":{"typo":true}}`, "nested"},
		{"array unknown", `{"rows":[{"name":"worker","typo":true}]}`, "rows"},
		{"dictionary unknown", `{"map":{"entry":{"typo":true}}}`, "map"},
		{"closed object", `{"closed":{"typo":true}}`, "closed"},
		{"preserved known type", `{"preserved":{"known":"wrong"}}`, "preserved.known"},
		{"wrong type", `{"replicas":"three"}`, "replicas"},
		{"number is not a string", `{"nullable":7}`, "nullable"},
		{"decimal integer", `{"replicas":2.0}`, ""},
		{"integer with composed constraint", `{"replicas":2}`, ""},
		{"wrong nested type", `{"nested":{"enabled":"yes"}}`, "nested.enabled"},
		{"minimum", `{"replicas":-1}`, "replicas"},
		{"maximum", `{"replicas":11}`, "replicas"},
		{"composed constraint", `{"replicas":9}`, "replicas"},
		{"enum", `{"mode":"typo"}`, "mode"},
		{"nonnullable", `{"mode":null}`, "mode"},
		{"incomplete array", `{"rows":[{}]}`, "rows"},
		{"array constraint", `{"rows":[]}`, "rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsREST(t, defaultsConfig(`{"widgets":`+tc.values+`,"futurecatalog":{"unknown":true}}`))
			r = NewREST(r.c, r.w, r.c, &config.Resource{
				Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", OpenAPISchema: installationDefaultsSchema},
				Release:     config.ReleaseConfig{Prefix: "widget-"},
			})
			app := defaultsApp(`{"required":"user","nested":{"name":"user"}}`)
			validationErr := r.applyInstallationDefaults(context.Background(), app.DeepCopy())
			admissionCalled := false
			_, err := r.Create(context.Background(), app, func(context.Context, runtime.Object) error {
				admissionCalled = true
				return nil
			}, &metav1.CreateOptions{})
			if tc.errorPath == "" {
				// A rejected entry never fails the create, so only the direct call shows it.
				if validationErr != nil {
					t.Fatalf("valid entry rejected: %v", validationErr)
				}
				if err != nil || !admissionCalled {
					t.Fatalf("create: %v, admission: %v", err, admissionCalled)
				}
				return
			}
			if validationErr == nil || !strings.Contains(validationErr.Error(), "applicationDefaults[widgets]") || !strings.Contains(validationErr.Error(), tc.errorPath) {
				t.Fatalf("expected configuration error at %s, got %v", tc.errorPath, validationErr)
			}
			if err != nil || !admissionCalled {
				t.Fatalf("invalid defaults blocked ordinary creation: %v", err)
			}
			list := &helmv2.HelmReleaseList{}
			if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 1 {
				t.Fatalf("unexpected writes: %v, %v", list.Items, err)
			}
			assertSpec(t, list.Items[0].Spec.Values, string(app.Spec.Raw))
		})
	}
}

func TestInstallationDefaultsValidationApplyAndDryRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		r := defaultsREST(t, defaultsConfig(`{"widgets":{"typo":true}}`))
		r = NewREST(r.c, r.w, r.c, &config.Resource{
			Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", OpenAPISchema: installationDefaultsSchema},
			Release:     config.ReleaseConfig{Prefix: "widget-"},
		})
		opts := &metav1.UpdateOptions{FieldManager: "apply-client"}
		if dry {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		admissionCalled := false
		_, created, err := r.Update(request.WithNamespace(context.Background(), "tenant-test"), "example", rest.DefaultUpdatedObjectInfo(defaultsApp(`{}`)), func(context.Context, runtime.Object) error {
			admissionCalled = true
			return nil
		}, nil, true, opts)
		if err != nil || !admissionCalled || !created {
			t.Fatalf("create through apply (dry=%v): %v, admission=%v", dry, err, admissionCalled)
		}
		list := &helmv2.HelmReleaseList{}
		want := 1
		if dry {
			want = 0
		}
		if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != want {
			t.Fatalf("unexpected writes: %v, %v", list.Items, err)
		}
	}
}

func TestInstallationDefaultsPostgresSchema(t *testing.T) {
	r := defaultsREST(t, defaultsConfig(`{"postgreses":{"storageClass":"replicated","size":"20Gi"}}`))
	r = NewREST(r.c, r.w, r.c, &config.Resource{
		Application: config.ApplicationConfig{Kind: "Postgres", Plural: "postgreses", OpenAPISchema: readEmbeddedOpenAPISchema(t, "../../../../packages/system/postgres-rd/cozyrds/postgres.yaml")},
		Release:     config.ReleaseConfig{Prefix: "postgres-"},
	})
	_, err := r.Create(context.Background(), defaultsApp(`{"size":"40Gi"}`), nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "postgres-example"}, hr); err != nil {
		t.Fatal(err)
	}
	assertSpec(t, hr.Spec.Values, `{"storageClass":"replicated","size":"40Gi"}`)
}

func TestInstallationDefaultsRequireReadableSchema(t *testing.T) {
	for _, raw := range []string{"", "{", "null", `{}`, `{"type":"array","items":{"type":"string"}}`} {
		for _, entry := range []string{`{}`, `{"replicas":1}`} {
			r := defaultsREST(t, defaultsConfig(`{"widgets":`+entry+`}`))
			r = NewREST(r.c, r.w, r.c, &config.Resource{Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", OpenAPISchema: raw}})
			if err := r.applyInstallationDefaults(context.Background(), defaultsApp(`{}`)); err == nil {
				t.Fatalf("schema %q accepted for configured defaults %s", raw, entry)
			}
		}
	}
}

func TestInstallationDefaultsDoNotRequireSchemaWithoutEntry(t *testing.T) {
	for _, schema := range []string{"", `{}`} {
		r := defaultsREST(t, defaultsConfig(`{"futurecatalog":{"replicas":3}}`))
		r = NewREST(r.c, r.w, r.c, &config.Resource{
			Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", OpenAPISchema: schema},
			Release:     config.ReleaseConfig{Prefix: "widget-"},
		})
		var got recordedWarnings
		ctx := warning.WithWarningRecorder(context.Background(), &got)
		if _, err := r.Create(ctx, defaultsApp(`{"user":"saved"}`), nil, &metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		// A failed defaults read never fails the create, so the warning is the signal.
		if len(got) != 0 {
			t.Fatalf("unconfigured application must not require a defaults schema: %q", []string(got))
		}
	}
}

// A CEL rule such as an immutability check has no meaning on a partial object, so
// the entry is checked against the structural schema only.
func TestInstallationDefaultsDoNotEvaluateCELRules(t *testing.T) {
	schema := `{"type":"object","properties":{"size":{"type":"string","x-kubernetes-validations":[{"rule":"self == 'never'","message":"always fails"}]}}}`
	r := defaultsRESTWithSchema(t, schema, defaultsConfig(`{"widgets":{"size":"20Gi"}}`))
	created, err := r.Create(context.Background(), defaultsApp(`{}`), nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertSpec(t, created.(*appsv1alpha1.Application).Spec, `{"size":"20Gi"}`)
}
