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
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// The `_` values-key namespace is the platform's only actual boundary against a
// tenant setting a platform-owned chart value, so it is worth a test that drives
// the REST verbs rather than the helper.
//
// Nothing else stops such a key. Application.Spec is a raw apiextensionsv1.JSON,
// so no Go struct prunes an undeclared field; values.schema.json carries no
// additionalProperties, so Helm accepts extras; and pkg/cmd/server/openapi.go
// deliberately re-opens the published spec ("mark the top level open so
// undocumented Helm values are accepted rather than rejected") when the
// documented schema does not declare additionalProperties. Removing a field from
// the generated schema therefore hides it from the dashboard and documents an
// intent; it does not deny a write. validateNoInternalKeys does.
//
// site-router's guest serial console is the first value to rely on this: the
// switch is `_logSerialConsole` precisely so a tenant cannot turn a guest console
// on for their own gateway, and the tests below pin both halves of that boundary.
// The chart-side half (the un-prefixed name renders nothing) lives in
// packages/apps/site-router/tests/serial_console_log_test.yaml.

// newInternalKeysREST builds a SiteRouter-kind REST over a fake client with the
// schemes Create/Update need to actually write a HelmRelease, so an accepted
// write is observed as a stored object rather than inferred from a nil error.
func newInternalKeysREST(t *testing.T, objs ...client.Object) (*REST, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{{Application: config.ApplicationConfig{Kind: siteRouterKindName}}},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &REST{
		c: fc,
		gvr: schema.GroupVersionResource{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "siterouters",
		},
		gvk: schema.GroupVersionKind{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: siteRouterKindName,
		},
		kindName:      siteRouterKindName,
		releaseConfig: config.ReleaseConfig{Prefix: "site-router-"},
	}, fc
}

// appWithSpec builds a SiteRouter Application whose spec is exactly the given
// values, so a test can submit the shape a tenant would submit.
func appWithSpec(t *testing.T, name string, values map[string]interface{}) *appsv1alpha1.Application {
	t.Helper()
	return &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: siteRouterKindName},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-test"},
		Spec:       jsonSpec(t, values),
	}
}

// TestCreate_RejectsReservedInternalKey is the load-bearing half: the key that
// actually switches the gateway console on cannot be submitted at all.
func TestCreate_RejectsReservedInternalKey(t *testing.T) {
	r, _ := newInternalKeysREST(t, cozystackCM(defaultClusterCIDRs()))
	ctx := request.WithNamespace(context.Background(), "tenant-test")

	_, err := r.Create(ctx, appWithSpec(t, "gw", map[string]interface{}{
		"_logSerialConsole": true,
	}), nil, &metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected Create to reject the reserved key _logSerialConsole, got nil; the guest console is tenant-reachable")
	}
	if !apierrors.IsBadRequest(err) {
		t.Errorf("expected a BadRequest status error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "_logSerialConsole") {
		t.Errorf("rejection %q should name the offending key", err.Error())
	}
}

// TestUpdate_RejectsReservedInternalKey pins the same refusal on Update, because
// a boundary enforced only on create is escaped by creating then editing.
func TestUpdate_RejectsReservedInternalKey(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "site-router-gw",
			Namespace: "tenant-test",
			Labels: map[string]string{
				ApplicationKindLabel:  siteRouterKindName,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "gw",
			},
		},
	}
	r, _ := newInternalKeysREST(t, existing, cozystackCM(defaultClusterCIDRs()))
	ctx := request.WithNamespace(context.Background(), "tenant-test")

	_, _, err := r.Update(ctx, "gw", newDefaultUpdatedObjectInfo(appWithSpec(t, "gw", map[string]interface{}{
		"_logSerialConsole": true,
	})), nil, nil, false, &metav1.UpdateOptions{})
	if err == nil {
		t.Fatalf("expected Update to reject the reserved key _logSerialConsole, got nil; the console can be switched on by editing")
	}
	if !apierrors.IsBadRequest(err) {
		t.Errorf("expected a BadRequest status error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "_logSerialConsole") {
		t.Errorf("rejection %q should name the offending key", err.Error())
	}
}

// TestCreate_AcceptsUnprefixedKeyWhichTheChartIgnores records the other half
// exactly as it is, rather than as it might be assumed to be. The un-prefixed
// name a tenant would reach for is NOT refused: it is accepted and lands in
// spec.values. It is inert only because no template reads it, which
// packages/apps/site-router/tests/serial_console_log_test.yaml asserts against
// the rendered VM.
//
// Keeping this explicit matters. If someone later "tidies" the chart to read
// .Values.logSerialConsole, nothing in the API layer would object, and this test
// is where the reason that would be a privilege escalation is written down.
func TestCreate_AcceptsUnprefixedKeyWhichTheChartIgnores(t *testing.T) {
	r, fc := newInternalKeysREST(t, cozystackCM(defaultClusterCIDRs()))
	ctx := request.WithNamespace(context.Background(), "tenant-test")

	if _, err := r.Create(ctx, appWithSpec(t, "gw", map[string]interface{}{
		"logSerialConsole": true,
	}), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("the un-prefixed key is not reserved and must not be rejected: %v", err)
	}

	hr := &helmv2.HelmRelease{}
	if err := fc.Get(ctx, client.ObjectKey{Namespace: "tenant-test", Name: "site-router-gw"}, hr); err != nil {
		t.Fatalf("fetch created HelmRelease: %v", err)
	}
	var stored map[string]interface{}
	if err := json.Unmarshal(hr.Spec.Values.Raw, &stored); err != nil {
		t.Fatalf("decode stored values: %v", err)
	}
	if _, present := stored["logSerialConsole"]; !present {
		t.Fatalf("expected the un-prefixed key to reach spec.values, got %v; if the API now prunes it, this test's premise is stale and the chart-side assertion is doing all the work", stored)
	}
	if _, present := stored["_logSerialConsole"]; present {
		t.Errorf("an un-prefixed tenant value must never be promoted to the reserved key, got %v", stored)
	}
}
