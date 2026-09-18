// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package tap

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

func TestArtifactName(t *testing.T) {
	if got := artifactName("cozystack.postgres-application", "default", "postgres"); got != "cozystack-postgres-application-default-postgres" {
		t.Fatalf("artifactName = %q", got)
	}
}

func appDef(name, kind, chartRef string, dash *cozyv1alpha1.ApplicationDefinitionDashboard) cozyv1alpha1.ApplicationDefinition {
	return cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: kind},
			Release:     cozyv1alpha1.ApplicationDefinitionRelease{ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: chartRef}},
			Dashboard:   dash,
		},
	}
}

func TestIndexAppDefsByChartRef(t *testing.T) {
	ads := []cozyv1alpha1.ApplicationDefinition{
		appDef("foo", "Foo", "community-org-repo-default-foo", nil),
		appDef("noref", "NoRef", "", nil),
	}
	idx := indexAppDefsByChartRef(ads)
	if len(idx) != 1 {
		t.Fatalf("expected 1 indexed entry (empty chartRef skipped), got %d", len(idx))
	}
	if _, ok := idx["community-org-repo-default-foo"]; !ok {
		t.Fatalf("expected foo indexed by its chartRef name")
	}
}

func TestBuildTap(t *testing.T) {
	ps := cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "acme.repo",
			Labels: map[string]string{tapconst.Label: "true"},
		},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "tap-acme-repo", Namespace: "cozy-system"},
			Variants: []cozyv1alpha1.Variant{
				{
					Name: "default",
					Components: []cozyv1alpha1.Component{
						{Name: "foo", Path: "apps/foo", Install: &cozyv1alpha1.ComponentInstall{Privileged: true}},
						{Name: "foo-rd", Path: "system/foo-rd"},
					},
				},
				{
					Name: "big",
					// A second variant whose foo has no matching ApplicationDefinition
					// (its artifact name embeds "big"); it must not add a package.
					Components: []cozyv1alpha1.Component{
						{Name: "foo", Path: "apps/foo"},
					},
				},
			},
		},
		Status: cozyv1alpha1.PackageSourceStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Message: "all good"}},
		},
	}
	// Only the app component "foo" (default variant) has a matching ApplicationDefinition.
	idx := indexAppDefsByChartRef([]cozyv1alpha1.ApplicationDefinition{
		appDef("foo", "Foo", artifactName("acme.repo", "default", "foo"),
			&cozyv1alpha1.ApplicationDefinitionDashboard{Description: "A foo", Category: "Storage", Tags: []string{"x"}, Icon: "data:svg"}),
	})

	tap := buildTap(ps, idx, true)

	if !tap.Spec.Community {
		t.Errorf("expected Community=true for a labeled tap source")
	}
	if !tap.Spec.Ready || tap.Spec.Message != "all good" {
		t.Errorf("ready/message not read from status: %+v", tap.Spec)
	}
	if tap.Spec.Source.Kind != "OCIRepository" || tap.Spec.Source.Name != "tap-acme-repo" {
		t.Errorf("source not set: %+v", tap.Spec.Source)
	}
	if len(tap.Spec.Packages) != 1 {
		t.Fatalf("expected exactly 1 package (only default/foo has an ApplicationDefinition; -rd and big/foo excluded), got %d: %+v", len(tap.Spec.Packages), tap.Spec.Packages)
	}
	p := tap.Spec.Packages[0]
	if p.Name != "foo" || p.Kind != "Foo" || p.Component != "foo" {
		t.Errorf("package identity wrong: %+v", p)
	}
	if p.Description != "A foo" || p.Category != "Storage" || p.Icon != "data:svg" || len(p.Tags) != 1 {
		t.Errorf("dashboard metadata not carried: %+v", p)
	}
	// default/foo declares install.privileged; the matched package must surface it.
	if !p.Privileged {
		t.Errorf("privileged must be taken from the matched component: %+v", p)
	}
}

func TestBuildTapNoMatchingAppDef(t *testing.T) {
	ps := cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack.core"},
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{{Name: "default", Components: []cozyv1alpha1.Component{{Name: "x", Path: "apps/x"}}}},
		},
	}
	tap := buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, true)
	if tap.Spec.Community {
		t.Errorf("a source without the tap label must not be flagged community")
	}
	if len(tap.Spec.Packages) != 0 {
		t.Errorf("expected no packages without matching ApplicationDefinitions, got %+v", tap.Spec.Packages)
	}
}

func TestBuildTapReflectsMissingRegistration(t *testing.T) {
	// The PackageSource reports Ready, but its registration (ApplicationDefinition)
	// is gone — e.g. its registration Package was deleted out-of-band. The Tap must
	// NOT echo the PackageSource's "ready" while the catalog is empty.
	ps := cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "demo.gitea", Labels: map[string]string{tapconst.Label: "true"}},
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{{Name: "default", Components: []cozyv1alpha1.Component{{Name: "gitea", Path: "apps/gitea"}}}},
		},
		Status: cozyv1alpha1.PackageSourceStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Message: "reconciliation succeeded"},
		}},
	}
	tap := buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, true) // no AppDefs resolve
	if tap.Spec.Ready {
		t.Error("Tap must not report Ready when the repository declares apps but none are registered")
	}
	if tap.Spec.Message == "reconciliation succeeded" {
		t.Error("Tap must not echo the PackageSource's Ready message when the catalog is empty")
	}

	// A recorded skip reason (e.g. privileged) is surfaced verbatim.
	ps.Annotations = map[string]string{tapconst.RegistrationStateAnnotation: "not auto-registered: privileged"}
	tap = buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, true)
	if tap.Spec.Ready || tap.Spec.Message != "not auto-registered: privileged" {
		t.Errorf("expected the recorded registration reason as the message, got %+v", tap.Spec)
	}
	ps.Annotations = nil

	// A NON-authoritative index (an ApplicationDefinition list failed) must not
	// flip a healthy tap to not-ready.
	tap = buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, false)
	if !tap.Spec.Ready {
		t.Error("a non-authoritative (failed) AppDef list must not override Ready")
	}
}

// TestBuildTapOfficialSourceStaysReady: the Tap list also carries official
// (non-tap) PackageSources whose default variant installs a system component with
// no user-facing ApplicationDefinition (e.g. cozystack.reloader). Their catalog is
// empty by design, and the truthful-status override must NOT flip them to
// not-ready — only tap-managed (Community) sources are subject to it.
func TestBuildTapOfficialSourceStaysReady(t *testing.T) {
	ps := cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack.reloader"}, // NO tap label
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{{Name: "default", Components: []cozyv1alpha1.Component{
				{Name: "reloader", Path: "system/reloader", Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-system"}},
			}}},
		},
		Status: cozyv1alpha1.PackageSourceStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Message: "reconciliation succeeded"},
		}},
	}
	tap := buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, true)
	if tap.Spec.Community {
		t.Fatal("an official source must not be flagged Community")
	}
	if !tap.Spec.Ready || tap.Spec.Message != "reconciliation succeeded" {
		t.Errorf("an official source's Ready must be preserved, got %+v", tap.Spec)
	}
}

// TestBuildTapNoDefaultVariantSurfacesReason: a tapped repo with no "default"
// variant carries the operator's recorded skip reason, which the Tap must surface
// even though defaultVariantHasComponents is false.
func TestBuildTapNoDefaultVariantSurfacesReason(t *testing.T) {
	ps := cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "demo.only-full",
			Labels:      map[string]string{tapconst.Label: "true"},
			Annotations: map[string]string{tapconst.RegistrationStateAnnotation: `not auto-registered: it declares no "default" variant`},
		},
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{{Name: "full", Components: []cozyv1alpha1.Component{{Name: "x", Path: "apps/x"}}}},
		},
		Status: cozyv1alpha1.PackageSourceStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Message: "reconciliation succeeded"},
		}},
	}
	tap := buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, true)
	if tap.Spec.Ready || tap.Spec.Message != `not auto-registered: it declares no "default" variant` {
		t.Errorf("a no-default tap must surface its recorded reason, got %+v", tap.Spec)
	}
}

// TestBuildTapEmptyDefaultVariantStaysReady: a repo whose default variant declares
// no components registers nothing; that empty catalog is correct, not a drift.
func TestBuildTapEmptyDefaultVariantStaysReady(t *testing.T) {
	ps := cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "demo.x", Labels: map[string]string{tapconst.Label: "true"}},
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{
				{Name: "default", Components: nil},
				{Name: "full", Components: []cozyv1alpha1.Component{{Name: "x", Path: "apps/x"}}},
			},
		},
		Status: cozyv1alpha1.PackageSourceStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Message: "ok"},
		}},
	}
	tap := buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{}, true)
	if !tap.Spec.Ready {
		t.Error("an empty default variant registers nothing; the tap must not be flipped to not-ready")
	}
}

func TestBuildPendingTap(t *testing.T) {
	// No error: a connecting message, not ready, community, identified by source.
	p := buildPendingTap("tap-acme-repo", "")
	if p.Name != "tap-acme-repo" || !p.Spec.Community || p.Spec.Ready {
		t.Errorf("unexpected pending tap: %+v", p)
	}
	if p.Spec.Source.Kind != "OCIRepository" || p.Spec.Source.Name != "tap-acme-repo" {
		t.Errorf("pending tap source not set: %+v", p.Spec.Source)
	}
	if p.Spec.Message == "" {
		t.Error("pending tap must carry a connecting message")
	}
	// A materialization error surfaces as the message.
	e := buildPendingTap("tap-acme-repo", `a PackageSource named "acme.repo" already exists`)
	if e.Spec.Ready || e.Spec.Message != `a PackageSource named "acme.repo" already exists` {
		t.Errorf("expected the error surfaced as the message, got %+v", e.Spec)
	}
}
