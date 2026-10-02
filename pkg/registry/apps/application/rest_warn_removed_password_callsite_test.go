/*
Copyright 2024 The Cozystack Authors.

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
	"errors"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// Postgres and MariaDB dropped users[].password from the chart render but the
// key is still accepted and stored, and a leftover value stays the live
// credential. The warning telling an operator that editing it has no effect is
// only useful if wired into the write path; deleting either call site
// (rest.go Create / Update) must fail loudly. These tests drive Create and
// Update end to end and assert the warning surfaces; TestWarnRemovedUserPasswords
// below pins the helper in isolation (which kinds warn, and that a missing
// password stays quiet).

func newPostgresWarnREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{Application: config.ApplicationConfig{Kind: postgresKind}},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &REST{
		c: fakeClient,
		w: fakeClient,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "postgreses",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    postgresKind,
		},
		kindName:      postgresKind,
		releaseConfig: config.ReleaseConfig{Prefix: "postgres-"},
	}
}

func postgresAppWithUserPassword() *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       postgresKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "good-name",
			Namespace: "tenant-foo",
		},
		Spec: &apiextv1.JSON{Raw: []byte(`{"users":{"app":{"password":"hackme"}}}`)},
	}
}

func TestCreate_WarnsOnRemovedUserPassword(t *testing.T) {
	r := newPostgresWarnREST(t)
	app := postgresAppWithUserPassword()

	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), "tenant-foo"), rec)

	// Short-circuit right after the warning (createValidation runs immediately
	// after warnRemovedUserPasswords), so the test does not depend on conversion
	// or the fake client's write path.
	sentinel := errors.New("stop after warning")
	createValidation := func(_ context.Context, _ runtime.Object) error { return sentinel }

	if _, err := r.Create(ctx, app, createValidation, &metav1.CreateOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error after warning, got %v", err)
	}
	assertUserPasswordWarning(t, rec)
}

func TestUpdate_WarnsOnRemovedUserPassword(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "postgres-good-name",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  postgresKind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "good-name",
			},
		},
	}
	r := newPostgresWarnREST(t, existing)
	app := postgresAppWithUserPassword()

	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), "tenant-foo"), rec)

	// updateValidation runs BEFORE warnRemovedUserPasswords, so it must pass for
	// the warning to be reached; the final Update result is irrelevant.
	updateValidation := func(_ context.Context, _, _ runtime.Object) error { return nil }
	_, _, _ = r.Update(
		ctx,
		"good-name",
		newDefaultUpdatedObjectInfo(app),
		nil,
		updateValidation,
		false,
		&metav1.UpdateOptions{},
	)
	assertUserPasswordWarning(t, rec)
}

func assertUserPasswordWarning(t *testing.T, rec *fakeWarningRecorder) {
	t.Helper()
	for _, w := range rec.warnings {
		if strings.Contains(w, `spec.users["app"].password is ignored`) {
			return
		}
	}
	t.Fatalf("expected a spec.users[].password admission warning, got %v", rec.warnings)
}

func TestWarnRemovedUserPasswords(t *testing.T) {
	const rawWithPassword = `{"users":{"app":{"password":"hackme"}}}`
	cases := []struct {
		name     string
		kind     string
		raw      string
		wantWarn bool
	}{
		{"postgres warns", postgresKind, rawWithPassword, true},
		{"mariadb warns", mariadbKind, rawWithPassword, true},
		{"other kind stays quiet", "Redis", rawWithPassword, false},
		{"no password stays quiet", postgresKind, `{"users":{"app":{}}}`, false},
		{"warns on a good user despite a non-object user", postgresKind, `{"users":{"good":{"password":"x"},"bad":"str"}}`, true},
		// A malformed users SHAPE (an array, not a map) carries no per-user password
		// we recognise and must not fail the decode into a spurious warning.
		{"malformed users shape stays quiet", postgresKind, `{"users":["oops"]}`, false},
		// MariaDB force-generates root; a value-supplied users.root is ignored and
		// must warn. Postgres hard-rejects users.postgres at render, so this warning
		// is MariaDB-only.
		{"mariadb warns on users.root", mariadbKind, `{"users":{"root":{"maxUserConnections":5}}}`, true},
		{"postgres does not warn on users.root here", postgresKind, `{"users":{"root":{}}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &REST{kindName: tc.kind}
			app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(tc.raw)}}
			rec := &fakeWarningRecorder{}
			ctx := warning.WithWarningRecorder(context.Background(), rec)
			r.warnRemovedUserPasswords(ctx, app)
			got := len(rec.warnings) > 0
			if got != tc.wantWarn {
				t.Fatalf("kind %s: warned=%v, want %v (warnings: %v)", tc.kind, got, tc.wantWarn, rec.warnings)
			}
		})
	}
}

// A "_"/"-" collision that an UPDATE INTRODUCES (present in the new spec, absent
// from the old) must draw an admission warning in each namespace MariaDB renders
// with a "_"→"-" mapping — usernames, database names, and (database,user) Grant
// pairs — because the render guard for each is install-gated (so it does not wedge
// a release that already inherited the pair), leaving the upgrade-introduced case
// to catch here where both specs are available. An inherited collision, or a
// non-MariaDB kind, stays quiet.
func TestWarnUpgradeIntroducedCollisions(t *testing.T) {
	app := func(raw string) *appsv1alpha1.Application {
		return &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(raw)}}
	}
	cases := []struct {
		name     string
		kind     string
		old      string
		new      string
		wantWarn bool
	}{
		{"introduced user pair warns", mariadbKind, `{"users":{"a_b":{}}}`, `{"users":{"a_b":{},"a-b":{}}}`, true},
		{"introduced user from empty warns", mariadbKind, `{}`, `{"users":{"a_b":{},"a-b":{}}}`, true},
		{"inherited user pair stays quiet", mariadbKind, `{"users":{"a_b":{},"a-b":{}}}`, `{"users":{"a_b":{},"a-b":{},"c":{}}}`, false},
		{"no user collision stays quiet", mariadbKind, `{"users":{"a":{}}}`, `{"users":{"a":{},"b":{}}}`, false},
		{"introduced database pair warns", mariadbKind, `{"databases":{"a_b":{}}}`, `{"databases":{"a_b":{},"a-b":{}}}`, true},
		{"inherited database pair stays quiet", mariadbKind, `{"databases":{"a_b":{},"a-b":{}}}`, `{"databases":{"a_b":{},"a-b":{},"c":{}}}`, false},
		// (database "a", user "b-c") and (database "a-b", user "c") both render Grant
		// name a-b-c.
		{"introduced grant pair warns", mariadbKind, `{}`, `{"databases":{"a":{"roles":{"admin":["b-c"]}},"a-b":{"roles":{"admin":["c"]}}}}`, true},
		{"a user in both roles of one db is no self-collision", mariadbKind, `{}`, `{"databases":{"d":{"roles":{"admin":["u"],"readonly":["u"]}}}}`, false},
		{"postgres never warns (no _/- resource mapping)", postgresKind, `{"users":{"a_b":{}}}`, `{"users":{"a_b":{},"a-b":{}}}`, false},
		{"malformed new users stays quiet", mariadbKind, `{}`, `{"users":["oops"]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &REST{kindName: tc.kind}
			rec := &fakeWarningRecorder{}
			ctx := warning.WithWarningRecorder(context.Background(), rec)
			r.warnUpgradeIntroducedCollisions(ctx, app(tc.old), app(tc.new))
			got := len(rec.warnings) > 0
			if got != tc.wantWarn {
				t.Fatalf("kind %s: warned=%v, want %v (warnings: %v)", tc.kind, got, tc.wantWarn, rec.warnings)
			}
		})
	}
}

func newMariadbWarnREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{{Application: config.ApplicationConfig{Kind: mariadbKind}}},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &REST{
		c:             fakeClient,
		w:             fakeClient,
		gvr:           schema.GroupVersionResource{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "mariadbs"},
		gvk:           schema.GroupVersionKind{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: mariadbKind},
		kindName:      mariadbKind,
		releaseConfig: config.ReleaseConfig{Prefix: "mariadb-"},
	}
}

// The collision warning is held to the same call-site contract as the removed-
// password one: a test that drives Update end to end, so removing the call from
// rest.go fails loudly. The existing release carries no users, so the new pair is
// introduced-from-empty and must warn.
func TestUpdate_WarnsOnUpgradeIntroducedCollision(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mariadb-good-name",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  mariadbKind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "good-name",
			},
		},
	}
	r := newMariadbWarnREST(t, existing)
	app := &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: mariadbKind},
		ObjectMeta: metav1.ObjectMeta{Name: "good-name", Namespace: "tenant-foo"},
		Spec:       &apiextv1.JSON{Raw: []byte(`{"users":{"a_b":{},"a-b":{}}}`)},
	}
	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), "tenant-foo"), rec)
	updateValidation := func(_ context.Context, _, _ runtime.Object) error { return nil }
	_, _, _ = r.Update(ctx, "good-name", newDefaultUpdatedObjectInfo(app), nil, updateValidation, false, &metav1.UpdateOptions{})
	for _, w := range rec.warnings {
		if strings.Contains(w, "map to the same mariadb-operator User name") {
			return
		}
	}
	t.Fatalf("expected an upgrade-introduced collision warning from Update, got %v", rec.warnings)
}

// A MariaDB users.root.password must draw exactly ONE warning (the whole-field
// "users.root is ignored" one), not also the per-user password warning — one field,
// one warning. Removing the root skip in the per-user loop makes this two.
func TestWarnRemovedUserPasswords_MariadbRootSingleWarning(t *testing.T) {
	r := &REST{kindName: mariadbKind}
	app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(`{"users":{"root":{"password":"x"}}}`)}}
	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(context.Background(), rec)
	r.warnRemovedUserPasswords(ctx, app)
	if len(rec.warnings) != 1 {
		t.Fatalf("expected exactly one warning for users.root.password, got %d: %v", len(rec.warnings), rec.warnings)
	}
	if !strings.Contains(rec.warnings[0], "spec.users.root is ignored") {
		t.Fatalf("expected the whole-field root warning, got %q", rec.warnings[0])
	}
}

// Postgres hard-rejects users.postgres at render, so a value-supplied
// users.postgres.password must draw ONE clear key-forbidden warning at admission,
// not the generic (and, on its own, misleading) "password is ignored" one.
func TestWarnRemovedUserPasswords_PostgresPostgresKeyWarnsAboutKey(t *testing.T) {
	r := &REST{kindName: postgresKind}
	app := &appsv1alpha1.Application{Spec: &apiextv1.JSON{Raw: []byte(`{"users":{"postgres":{"password":"x"}}}`)}}
	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(context.Background(), rec)
	r.warnRemovedUserPasswords(ctx, app)
	if len(rec.warnings) != 1 {
		t.Fatalf("expected exactly one warning for users.postgres.password, got %d: %v", len(rec.warnings), rec.warnings)
	}
	if !strings.Contains(rec.warnings[0], "spec.users.postgres is not allowed") {
		t.Fatalf("expected the forbidden-key warning, got %q", rec.warnings[0])
	}
	if strings.Contains(rec.warnings[0], "password is ignored") {
		t.Fatalf("must not emit the misleading password-ignored warning for the forbidden key, got %q", rec.warnings[0])
	}
}
