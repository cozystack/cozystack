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

// SiteRouter deny-set admission — tests written FIRST (T09 Phase A).
//
// These pin the SiteRouter-specific validating admission check the apps
// aggregated apiserver runs on SiteRouter app instances (DECISIONS.md D9/D10):
// a remoteCIDR that is malformed or overlaps a cluster-owned network is rejected
// synchronously with a Forbidden naming the offending CIDR and the colliding
// network, a disjoint one is accepted, and NON-SiteRouter kinds are untouched
// (the generic app-instance admission and the Ready/WorkloadsReady conversion
// are a shared code path that must not change). The deny-set decision itself is
// the pure internal/siterouter/denyset.Validate helper shared with the
// controller; the check here only sources the cluster CIDRs from the
// cozy-system/cozystack ConfigMap and shapes the rejection.
//
// Phase B wires (a) a SiteRouter-scoped REST method
// r.validateSiteRouterRemoteCIDRs(ctx, app) that returns nil for a non-SiteRouter
// kind / disjoint CIDRs and a Forbidden otherwise, and (b) its call sites in
// Create and Update ahead of conversion. Until then these tests are red.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

// siteRouterKindName is the Application.Kind string this admission check gates
// on. Kept a local literal so the test does not depend on where Phase B homes
// the kind constant; the check must fire for exactly this kind and no other.
const siteRouterKindName = "SiteRouter"

// cozystackCM builds the cluster ConfigMap the admission check reads the
// pod/service/join CIDRs from. Passing an empty map yields a ConfigMap with no
// data (the check then falls back to the platform-values defaults). Passing nil
// (via the helper below) omits the ConfigMap entirely.
func cozystackCM(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "cozy-system"},
		Data:       data,
	}
}

// defaultClusterCIDRs mirrors the cozy-system/cozystack ConfigMap the platform
// ships (packages/core/platform/values.yaml networking.*).
func defaultClusterCIDRs() map[string]string {
	return map[string]string{
		"ipv4-pod-cidr":  "10.244.0.0/16",
		"ipv4-svc-cidr":  "10.96.0.0/16",
		"ipv4-join-cidr": "100.64.0.0/16",
	}
}

// newSiteRouterREST builds a REST for the given kind wired to a fake client
// (corev1 scheme) preloaded with objs. The watch client is intentionally nil so
// the admission check exercises its cached-client read path — the same fallback
// production takes when the direct reader is absent (as in unit tests).
func newSiteRouterREST(t *testing.T, kind string, objs ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &REST{
		c: fc,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "siterouters",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    kind,
		},
		kindName:      kind,
		releaseConfig: config.ReleaseConfig{Prefix: "site-router-"},
	}
}

// siteRouterApp builds a SiteRouter (or other-kind) Application whose spec.values
// declare the given remoteCIDRs — the authoritative tunnel input the deny-set
// validates.
func siteRouterApp(t *testing.T, kind, name string, remoteCIDRs ...string) *appsv1alpha1.Application {
	t.Helper()
	cidrs := make([]interface{}, len(remoteCIDRs))
	for i := range remoteCIDRs {
		cidrs[i] = remoteCIDRs[i]
	}
	return &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       kind,
		},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-test"},
		Spec:       jsonSpec(t, map[string]interface{}{"remoteCIDRs": cidrs}),
	}
}

func jsonSpec(t *testing.T, v map[string]interface{}) *apiextv1.JSON {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal spec values: %v", err)
	}
	return &apiextv1.JSON{Raw: raw}
}

// -----------------------------------------------------------------------------
// Deny-set decision (the SiteRouter-scoped REST method)
// -----------------------------------------------------------------------------

// TestSiteRouterAdmission_RejectsClusterOverlap is the T09 Acceptance core: a
// remoteCIDR overlapping a cluster network is rejected synchronously (Forbidden)
// with a message naming the offending CIDR and the colliding network.
func TestSiteRouterAdmission_RejectsClusterOverlap(t *testing.T) {
	r := newSiteRouterREST(t, siteRouterKindName, cozystackCM(defaultClusterCIDRs()))
	app := siteRouterApp(t, siteRouterKindName, "gw", "10.244.7.0/24") // inside pod 10.244.0.0/16

	err := r.validateSiteRouterRemoteCIDRs(context.Background(), app)
	if err == nil {
		t.Fatalf("expected a cluster-overlapping remoteCIDR to be rejected, got nil")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected a Forbidden status error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "10.244.7.0/24") {
		t.Errorf("rejection %q should name the offending CIDR 10.244.7.0/24", err.Error())
	}
	if !strings.Contains(err.Error(), "10.244.0.0/16") {
		t.Errorf("rejection %q should name the colliding cluster network 10.244.0.0/16", err.Error())
	}
}

// TestSiteRouterAdmission_RejectsMalformed proves a malformed remoteCIDR is
// rejected synchronously too (belt-and-suspenders behind the T03 schema shape
// check), naming the offending value.
func TestSiteRouterAdmission_RejectsMalformed(t *testing.T) {
	r := newSiteRouterREST(t, siteRouterKindName, cozystackCM(defaultClusterCIDRs()))
	app := siteRouterApp(t, siteRouterKindName, "gw", "not-a-cidr")

	err := r.validateSiteRouterRemoteCIDRs(context.Background(), app)
	if err == nil {
		t.Fatalf("expected a malformed remoteCIDR to be rejected, got nil")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected a Forbidden status error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "not-a-cidr") {
		t.Errorf("rejection %q should name the malformed value", err.Error())
	}
}

// TestSiteRouterAdmission_AcceptsDisjoint proves a remoteCIDR disjoint from every
// cluster network is accepted (no error) — the positive counterpart that guards
// against the deny-set rejecting valid tunnels.
func TestSiteRouterAdmission_AcceptsDisjoint(t *testing.T) {
	r := newSiteRouterREST(t, siteRouterKindName, cozystackCM(defaultClusterCIDRs()))
	app := siteRouterApp(t, siteRouterKindName, "gw", "172.31.0.0/16", "10.10.0.0/16")

	if err := r.validateSiteRouterRemoteCIDRs(context.Background(), app); err != nil {
		t.Fatalf("expected cluster-disjoint remoteCIDRs to be accepted, got %v", err)
	}
}

// TestSiteRouterAdmission_NonSiteRouterKindSkipped proves the check is
// SiteRouter-specific: an Application of another kind carrying the very same
// cluster-overlapping value is NOT touched by the deny-set check, so generic
// app-instance admission is unchanged (D9 — shared code path).
func TestSiteRouterAdmission_NonSiteRouterKindSkipped(t *testing.T) {
	r := newSiteRouterREST(t, "MySQL", cozystackCM(defaultClusterCIDRs()))
	app := siteRouterApp(t, "MySQL", "db", "10.244.7.0/24") // would overlap if this were a SiteRouter

	if err := r.validateSiteRouterRemoteCIDRs(context.Background(), app); err != nil {
		t.Fatalf("deny-set check must not fire for a non-SiteRouter kind, got %v", err)
	}
}

// TestSiteRouterAdmission_MissingConfigMapUsesDefaults proves the check still
// enforces the platform-values default cluster networks when the cozystack
// ConfigMap is absent (fail-safe, matching the controller's clusterNetworks
// fallback): a value overlapping the default pod CIDR is rejected with no
// ConfigMap present.
func TestSiteRouterAdmission_MissingConfigMapUsesDefaults(t *testing.T) {
	r := newSiteRouterREST(t, siteRouterKindName) // no ConfigMap loaded
	app := siteRouterApp(t, siteRouterKindName, "gw", "10.244.7.0/24")

	err := r.validateSiteRouterRemoteCIDRs(context.Background(), app)
	if err == nil {
		t.Fatalf("expected default cluster CIDRs to be enforced when the ConfigMap is absent, got nil")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected a Forbidden status error, got %T: %v", err, err)
	}
}

// TestSiteRouterAdmission_ConfigMapOverridesDefaults proves the check reads the
// cluster CIDRs from the ConfigMap rather than only the compiled defaults: a
// value that overlaps a NON-default pod CIDR declared in the ConfigMap (but not
// the compiled default) is rejected.
func TestSiteRouterAdmission_ConfigMapOverridesDefaults(t *testing.T) {
	custom := map[string]string{
		"ipv4-pod-cidr":  "192.168.0.0/16",
		"ipv4-svc-cidr":  "10.96.0.0/16",
		"ipv4-join-cidr": "100.64.0.0/16",
	}
	r := newSiteRouterREST(t, siteRouterKindName, cozystackCM(custom))
	app := siteRouterApp(t, siteRouterKindName, "gw", "192.168.5.0/24") // overlaps the CM pod CIDR only

	err := r.validateSiteRouterRemoteCIDRs(context.Background(), app)
	if err == nil {
		t.Fatalf("expected a value overlapping the ConfigMap pod CIDR to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "192.168.0.0/16") {
		t.Errorf("rejection %q should name the ConfigMap-sourced pod network 192.168.0.0/16", err.Error())
	}
}

// -----------------------------------------------------------------------------
// Admission-chain wiring (Create / Update call sites)
// -----------------------------------------------------------------------------

// TestCreate_SiteRouter_RejectsOverlapViaAdmission pins that REST.Create runs the
// SiteRouter deny-set check ahead of conversion, so an overlapping remoteCIDR is
// rejected at apply time with a Forbidden naming the CIDR — the synchronous UX
// the tenant sees on save.
func TestCreate_SiteRouter_RejectsOverlapViaAdmission(t *testing.T) {
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
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cozystackCM(defaultClusterCIDRs())).Build()
	r := &REST{
		c: fc,
		gvr: schema.GroupVersionResource{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "siterouters",
		},
		gvk: schema.GroupVersionKind{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: siteRouterKindName,
		},
		kindName:      siteRouterKindName,
		releaseConfig: config.ReleaseConfig{Prefix: "site-router-"},
	}

	app := siteRouterApp(t, siteRouterKindName, "gw", "10.244.7.0/24")
	ctx := request.WithNamespace(context.Background(), "tenant-test")

	_, err := r.Create(ctx, app, nil, &metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected Create to reject an overlapping remoteCIDR at admission, got nil")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected a Forbidden status error from Create, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "10.244.7.0/24") {
		t.Errorf("Create rejection %q should name the offending CIDR", err.Error())
	}
}

// TestUpdate_SiteRouter_RejectsOverlapViaAdmission pins the same check on the
// Update call site: editing a SiteRouter to add an overlapping remoteCIDR is
// rejected synchronously the same way a create is.
func TestUpdate_SiteRouter_RejectsOverlapViaAdmission(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register cozystack scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{{Application: config.ApplicationConfig{Kind: siteRouterKindName}}},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}

	// Pre-populate the HelmRelease so Update takes the update path (not the
	// create fall-through) and reaches the deny-set check.
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
	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(existing, cozystackCM(defaultClusterCIDRs())).Build()
	r := &REST{
		c: fc,
		gvr: schema.GroupVersionResource{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "siterouters",
		},
		gvk: schema.GroupVersionKind{
			Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: siteRouterKindName,
		},
		kindName:      siteRouterKindName,
		releaseConfig: config.ReleaseConfig{Prefix: "site-router-"},
	}

	app := siteRouterApp(t, siteRouterKindName, "gw", "10.244.7.0/24")
	ctx := request.WithNamespace(context.Background(), "tenant-test")

	_, _, err := r.Update(ctx, "gw", newDefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{})
	if err == nil {
		t.Fatalf("expected Update to reject an overlapping remoteCIDR at admission, got nil")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected a Forbidden status error from Update, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "10.244.7.0/24") {
		t.Errorf("Update rejection %q should name the offending CIDR", err.Error())
	}
}
