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
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func defaultsConfig(raw string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cozy-system", Name: "cozystack-application-defaults"}, Data: map[string]string{"defaults.json": raw}}
}

const freeFormDefaultsSchema = `{"type":"object","x-kubernetes-preserve-unknown-fields":true}`

func defaultsREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	return defaultsRESTWithSchema(t, freeFormDefaultsSchema, objects...)
}

func defaultsRESTWithSchema(t *testing.T, schema string, objects ...client.Object) *REST {
	t.Helper()
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = helmv2.AddToScheme(s)
	_ = cozyv1alpha1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
	return NewREST(c, c, c, &config.Resource{
		Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", Singular: "widget", OpenAPISchema: schema},
		Release:     config.ReleaseConfig{Prefix: "widget-"},
	})
}

// Declared properties let the merge recurse into nested; a free-form root would
// replace it as a whole.
const typedDefaultsSchema = `{"type":"object","properties":{
	"nested":{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}}},
	"list":{"type":"array","items":{"type":"integer"}},
	"flag":{"type":"boolean"},
	"number":{"type":"integer"},
	"text":{"type":"string"},
	"nullable":{"type":"string","nullable":true}}}`

func defaultsApp(raw string) *appsv1alpha1.Application {
	return &appsv1alpha1.Application{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "tenant-test"}, Spec: &apiextv1.JSON{Raw: []byte(raw)}}
}

func assertSpec(t *testing.T, got *apiextv1.JSON, want string) {
	t.Helper()
	if got == nil {
		t.Fatal("missing spec")
	}
	a, b := jsonValue(t, got.Raw), jsonValue(t, []byte(want))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("spec = %s, want %s", got.Raw, want)
	}
}

func TestInstallationDefaultsCreate(t *testing.T) {
	for _, tc := range []struct{ name, spec, want string }{
		{"missing", `{}`, `{"nested":{"a":1,"b":2},"list":[1],"flag":true,"number":4,"text":"default","nullable":"default"}`},
		{"nested", `{"nested":{"a":9}}`, `{"nested":{"a":9,"b":2},"list":[1],"flag":true,"number":4,"text":"default","nullable":"default"}`},
		{"explicit empty values", `{"nested":{},"list":[],"flag":false,"number":0,"text":"","nullable":null}`, `{"nested":{},"list":[],"flag":false,"number":0,"text":"","nullable":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsRESTWithSchema(t, typedDefaultsSchema, defaultsConfig(`{"widgets":{"nested":{"a":1,"b":2},"list":[1],"flag":true,"number":4,"text":"default","nullable":"default"},"otherwidgets":{"text":"other"}}`))
			app := defaultsApp(tc.spec)
			_, err := r.Create(context.Background(), app, func(_ context.Context, obj runtime.Object) error {
				assertSpec(t, obj.(*appsv1alpha1.Application).Spec, tc.want)
				return nil
			}, &metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			hr := &helmv2.HelmRelease{}
			if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, hr); err != nil {
				t.Fatal(err)
			}
			assertSpec(t, hr.Spec.Values, tc.want)
			assertSpec(t, app.Spec, tc.spec)
		})
	}
}

func TestInstallationDefaultsOmittedAndNullSpec(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec *apiextv1.JSON
	}{
		{"omitted", nil},
		{"empty", &apiextv1.JSON{}},
		{"null", &apiextv1.JSON{Raw: []byte("null")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsRESTWithSchema(t, typedDefaultsSchema, defaultsConfig(`{"widgets":{"number":4}}`))
			app := defaultsApp(`{}`)
			app.Spec = tc.spec
			if _, err := r.Create(context.Background(), app, nil, &metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			hr := &helmv2.HelmRelease{}
			if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, hr); err != nil {
				t.Fatal(err)
			}
			assertSpec(t, hr.Spec.Values, `{"number":4}`)
		})
	}
}

func TestInstallationDefaultsRejectUnreadableUserSpec(t *testing.T) {
	r := defaultsRESTWithSchema(t, typedDefaultsSchema, defaultsConfig(`{"widgets":{"number":4}}`))
	_, err := r.Create(context.Background(), defaultsApp(`{`), nil, &metav1.CreateOptions{})
	if !apierrors.IsBadRequest(err) {
		t.Fatalf("unreadable user spec was accepted: %v", err)
	}
	list := &helmv2.HelmReleaseList{}
	if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 0 {
		t.Fatalf("unexpected writes: %v, %v", list.Items, err)
	}
}

func TestInstallationDefaultsMergeContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/installation-defaults-merge.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name         string          `json:"name"`
		Schema       json.RawMessage `json:"schema"`
		User         json.RawMessage `json:"user"`
		Installation json.RawMessage `json:"installation"`
		Expected     json.RawMessage `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("merge fixtures are empty")
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			schema := freeFormDefaultsSchema
			if len(tc.Schema) > 0 {
				schema = string(tc.Schema)
			}
			r := defaultsRESTWithSchema(t, schema, defaultsConfig(`{"widgets":`+string(tc.Installation)+`}`))
			if _, err := r.Create(context.Background(), defaultsApp(string(tc.User)), nil, &metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			hr := &helmv2.HelmRelease{}
			if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, hr); err != nil {
				t.Fatal(err)
			}
			assertSpec(t, hr.Spec.Values, string(tc.Expected))
		})
	}
}

func TestInstallationDefaultsConfigurationErrors(t *testing.T) {
	for _, raw := range []string{``, `{`, `null`, `[]`, `{} {}`, `{"":{}}`, `{"_widgets":{}}`, `{"widgets":null}`, `{"widgets":[]}`, `{"otherwidgets":false}`, `{"widgets":{"_cluster":{}}}`, `{"otherwidgets":{"_cluster":{}}}`} {
		t.Run(raw, func(t *testing.T) {
			r := defaultsREST(t, defaultsConfig(raw))
			if err := r.applyInstallationDefaults(context.Background(), defaultsApp(`{}`)); err == nil {
				t.Fatal("invalid configuration was accepted for merge")
			}
			list := &helmv2.HelmReleaseList{}
			if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 0 {
				t.Fatalf("unexpected writes: %v, %v", list.Items, err)
			}
		})
	}
}

func TestInstallationDefaultsMissingAndGeneric(t *testing.T) {
	for _, cm := range []*corev1.ConfigMap{nil, defaultsConfig(`{}`), defaultsConfig(`{"unrelated":{"replicas":5}}`)} {
		r := defaultsREST(t)
		if cm != nil {
			if err := r.c.Create(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
		}
		_, err := r.Create(context.Background(), defaultsApp(`{"replicas":2}`), nil, &metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := defaultsREST(t, defaultsConfig(`{"otherwidgets":{"replicas":7},"widgets":{"replicas":2}}`))
	r.gvr.Resource = "otherwidgets"
	obj, err := r.Create(context.Background(), defaultsApp(`{}`), nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"replicas":7}`)
}

func TestInstallationDefaultsApplyCreateAndDryRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		r := defaultsREST(t, defaultsConfig(`{"widgets":{"replicas":3}}`))
		opts := &metav1.UpdateOptions{FieldManager: "apply-client"}
		if dry {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		ctx := request.WithNamespace(context.Background(), "tenant-test")
		obj, created, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(defaultsApp(`{}`)), func(_ context.Context, obj runtime.Object) error {
			assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"replicas":3}`)
			return nil
		}, nil, true, opts)
		if err != nil || !created {
			t.Fatalf("created=%v, err=%v", created, err)
		}
		assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"replicas":3}`)
		err = r.c.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, &helmv2.HelmRelease{})
		if dry && !apierrors.IsNotFound(err) {
			t.Fatalf("dry run persisted release: %v", err)
		}
		if !dry && err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallationDefaultsExistingLifecycle(t *testing.T) {
	r := defaultsREST(t, defaultsConfig(`{"widgets":{"replicas":3}}`))
	ctx := request.WithNamespace(context.Background(), "tenant-test")
	if _, err := r.Create(ctx, defaultsApp(`{}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	key := client.ObjectKeyFromObject(defaultsConfig(""))
	if err := r.c.Get(ctx, key, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["defaults.json"] = `{"widgets":{"replicas":9,"newField":true}}`
	if err := r.c.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	r.defaultsReader = &failingDefaultsReader{Reader: r.c}
	obj, err := r.Get(ctx, "example", &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"replicas":3}`)
	list, err := r.List(ctx, &metainternalversion.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	apps := list.(*appsv1alpha1.ApplicationList).Items
	if len(apps) != 1 {
		t.Fatalf("apps=%v", apps)
	}
	assertSpec(t, apps[0].Spec, `{"replicas":3}`)
	w, err := r.Watch(ctx, &metainternalversion.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	// The ConfigMap is readable again and holds a field the application lacks, so an
	// update that applied defaults would add it.
	r.defaultsReader = r.c
	updated, created, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(defaultsApp(`{"replicas":4}`)), nil, nil, false, &metav1.UpdateOptions{})
	if err != nil || created {
		t.Fatalf("created=%v, err=%v", created, err)
	}
	assertSpec(t, updated.(*appsv1alpha1.Application).Spec, `{"replicas":4}`)
	select {
	case event := <-w.ResultChan():
		if event.Type != watch.Modified {
			t.Fatalf("event=%v", event.Type)
		}
		assertSpec(t, event.Object.(*appsv1alpha1.Application).Spec, `{"replicas":4}`)
	case <-time.After(5 * time.Second):
		t.Fatal("missing application watch event")
	}
}

type failingDefaultsReader struct{ client.Reader }

func (r *failingDefaultsReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "cozystack-application-defaults", errors.New("access denied"))
}

func TestInstallationDefaultsDirectReaderAndAccessError(t *testing.T) {
	r := defaultsREST(t)
	direct := defaultsREST(t, defaultsConfig(`{"widgets":{"replicas":6}}`))
	r.defaultsReader = direct.c
	obj, err := r.Create(context.Background(), defaultsApp(`{}`), nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"replicas":6}`)
	r = defaultsREST(t)
	r.defaultsReader = &failingDefaultsReader{Reader: r.c}
	if _, err := r.Create(context.Background(), defaultsApp(`{}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("configuration access must not block creation: %v", err)
	}
	list := &helmv2.HelmReleaseList{}
	if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 1 {
		t.Fatalf("unexpected writes: %v, %v", list.Items, err)
	}
	assertSpec(t, list.Items[0].Spec.Values, `{}`)
}

func TestInstallationDefaultsPreserveNumbers(t *testing.T) {
	r := defaultsREST(t, defaultsConfig(`{"widgets":{"defaultNumber":9007199254740993,"preciseNumber":0.123456789123456789}}`))
	if _, err := r.Create(context.Background(), defaultsApp(`{"userNumber":9007199254740995}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, hr); err != nil {
		t.Fatal(err)
	}
	assertSpec(t, hr.Spec.Values, `{"defaultNumber":9007199254740993,"userNumber":9007199254740995,"preciseNumber":0.123456789123456789}`)
}

func TestInstallationDefaultsLeaveDeveloperDefaultsOnRead(t *testing.T) {
	r := defaultsREST(t, defaultsConfig(`{"widgets":{"optional":null,"empty":{},"install":"installation"}}`))
	r = NewREST(r.c, r.w, r.c, &config.Resource{
		Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", OpenAPISchema: `{"type":"object","required":["required"],"properties":{"required":{"type":"string","default":"developer-required"},"optional":{"type":"string","nullable":true},"empty":{"type":"object","properties":{"child":{"type":"string","default":"developer-child"}}},"install":{"type":"string","default":"developer-install"},"developer":{"type":"string","default":"developer"}}}`},
		Release:     config.ReleaseConfig{Prefix: "widget-"},
	})
	if _, err := r.Create(context.Background(), defaultsApp(`{"required":null}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, hr); err != nil {
		t.Fatal(err)
	}
	assertSpec(t, hr.Spec.Values, `{"optional":null,"empty":{},"install":"installation"}`)
}

func TestInstallationDefaultsAdmissionRejection(t *testing.T) {
	r := defaultsREST(t, defaultsConfig(`{"widgets":{"replicas":3}}`))
	sentinel := errors.New("admission rejected defaults")
	_, err := r.Create(context.Background(), defaultsApp(`{}`), func(_ context.Context, obj runtime.Object) error {
		assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"replicas":3}`)
		return sentinel
	}, &metav1.CreateOptions{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	list := &helmv2.HelmReleaseList{}
	if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 0 {
		t.Fatalf("writes=%v, err=%v", list.Items, err)
	}
}

func TestInstallationDefaultsBeforeQuotas(t *testing.T) {
	parent := tenantHelmRelease(t, "parent", "tenant-root", map[string]string{"cpu": "2"})
	r := newTenantREST(t, parent, defaultsConfig(`{"tenants":{"resourceQuotas":{"cpu":"4"}}}`))
	r.defaultsReader = r.c
	var err error
	r.defaultsValidator, err = buildInstallationDefaultsValidator(readEmbeddedOpenAPISchema(t, "../../../../packages/system/tenant-rd/cozyrds/tenant.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Create(context.Background(), childApplication(t, "child", "tenant-root-parent", nil), nil, &metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) {
		t.Fatalf("quota from defaults must be rejected: %v", err)
	}
	if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-root-parent", Name: "tenant-child"}, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected write: %v", err)
	}
}
