// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package tap

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
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
		ObjectMeta: metav1.ObjectMeta{Name: "community.org.repo"},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "community-org-repo", Namespace: "cozy-system"},
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
		appDef("foo", "Foo", artifactName("community.org.repo", "default", "foo"),
			&cozyv1alpha1.ApplicationDefinitionDashboard{Description: "A foo", Category: "Storage", Tags: []string{"x"}, Icon: "data:svg"}),
	})

	tap := buildTap(ps, idx)

	if !tap.Spec.Community {
		t.Errorf("expected Community=true for community.* source")
	}
	if !tap.Spec.Ready || tap.Spec.Message != "all good" {
		t.Errorf("ready/message not read from status: %+v", tap.Spec)
	}
	if tap.Spec.Source.Kind != "OCIRepository" || tap.Spec.Source.Name != "community-org-repo" {
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
	tap := buildTap(ps, map[string]cozyv1alpha1.ApplicationDefinition{})
	if tap.Spec.Community {
		t.Errorf("cozystack.* must not be flagged community")
	}
	if len(tap.Spec.Packages) != 0 {
		t.Errorf("expected no packages without matching ApplicationDefinitions, got %+v", tap.Spec.Packages)
	}
}
