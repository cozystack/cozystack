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

package server

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

func appDef(name, kind, group string) cozyv1alpha1.ApplicationDefinition {
	return cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{
				Group:    group,
				Kind:     kind,
				Singular: name,
				Plural:   name + "s",
			},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				Prefix:   name + "-",
				ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "HelmRepository", Name: name, Namespace: "cozy-public"},
			},
		},
	}
}

func groupDef(group string) cozyv1alpha1.ApplicationGroupDefinition {
	return cozyv1alpha1.ApplicationGroupDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: group},
		Spec:       cozyv1alpha1.ApplicationGroupDefinitionSpec{Group: group},
	}
}

// Pins the back-compat guarantee: an ApplicationDefinition that sets no group
// is served in apps.cozystack.io exactly as before ApplicationGroupDefinitions
// existed, with or without group registrations present.
func TestBuildResourceConfig_DefaultGroupUnchanged(t *testing.T) {
	appDefs := []cozyv1alpha1.ApplicationDefinition{appDef("bucket", "Bucket", "")}
	for _, groupDefs := range [][]cozyv1alpha1.ApplicationGroupDefinition{
		nil,
		{groupDef("apps.example.com")},
	} {
		rc, err := buildResourceConfig(appDefs, groupDefs, helmReleaseFlagValues{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rc.Resources) != 1 {
			t.Fatalf("expected 1 resource, got %d", len(rc.Resources))
		}
		if got := rc.Resources[0].Application.Group; got != "apps.cozystack.io" {
			t.Errorf("expected default group apps.cozystack.io, got %q", got)
		}
	}
}

// A definition selecting a registered custom group is served in that group;
// one naming the default group explicitly is served without a registration.
func TestBuildResourceConfig_CustomGroupServed(t *testing.T) {
	rc, err := buildResourceConfig(
		[]cozyv1alpha1.ApplicationDefinition{
			appDef("widget", "Widget", "apps.example.com"),
			appDef("bucket", "Bucket", "apps.cozystack.io"),
		},
		[]cozyv1alpha1.ApplicationGroupDefinition{groupDef("apps.example.com")},
		helmReleaseFlagValues{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rc.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(rc.Resources))
	}
	byKind := map[string]string{}
	for _, r := range rc.Resources {
		byKind[r.Application.Kind] = r.Application.Group
	}
	if byKind["Widget"] != "apps.example.com" {
		t.Errorf("Widget group = %q, want apps.example.com", byKind["Widget"])
	}
	if byKind["Bucket"] != "apps.cozystack.io" {
		t.Errorf("Bucket group = %q, want apps.cozystack.io", byKind["Bucket"])
	}
}

// A definition referencing a group with no ApplicationGroupDefinition (the
// dangling-reference case, e.g. the registration was deleted) is skipped, and
// definitions in healthy groups are unaffected.
func TestBuildResourceConfig_DanglingGroupSkipped(t *testing.T) {
	rc, err := buildResourceConfig(
		[]cozyv1alpha1.ApplicationDefinition{
			appDef("widget", "Widget", "apps.example.com"),
			appDef("bucket", "Bucket", ""),
		},
		nil,
		helmReleaseFlagValues{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rc.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(rc.Resources))
	}
	if rc.Resources[0].Application.Kind != "Bucket" {
		t.Errorf("expected only Bucket to survive, got %q", rc.Resources[0].Application.Kind)
	}
}

// Kinds must be unique across groups because the published OpenAPI schema
// refs are keyed by kind alone: the second group claiming a kind is skipped.
func TestBuildResourceConfig_CrossGroupKindCollisionSkipped(t *testing.T) {
	rc, err := buildResourceConfig(
		[]cozyv1alpha1.ApplicationDefinition{
			appDef("bucket", "Bucket", ""),
			appDef("bucket2", "Bucket", "apps.example.com"),
		},
		[]cozyv1alpha1.ApplicationGroupDefinition{groupDef("apps.example.com")},
		helmReleaseFlagValues{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rc.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(rc.Resources))
	}
	if got := rc.Resources[0].Application.Group; got != "apps.cozystack.io" {
		t.Errorf("surviving Bucket group = %q, want apps.cozystack.io", got)
	}
}

// A registration whose group is malformed or reserved is ignored server-side
// even if it somehow bypassed CRD validation, so definitions referencing it
// stay unserved rather than shadowing platform or Kubernetes groups.
func TestServedGroups_RejectsInvalidRegistrations(t *testing.T) {
	groups := servedGroups([]cozyv1alpha1.ApplicationGroupDefinition{
		groupDef("apps.example.com"),  // valid
		groupDef("apps"),              // no dot: could shadow the built-in apps group
		groupDef("Bad.Example.Com"),   // uppercase
		groupDef("evil.cozystack.io"), // reserved platform namespace
		groupDef("networking.k8s.io"), // reserved Kubernetes namespace
	})
	want := map[string]bool{"apps.cozystack.io": true, "apps.example.com": true}
	if len(groups) != len(want) {
		t.Fatalf("served groups = %v, want %v", groups, want)
	}
	for g := range want {
		if !groups[g] {
			t.Errorf("expected group %q to be served", g)
		}
	}
}

func TestValidateApplicationGroup(t *testing.T) {
	valid := []string{"apps.example.com", "example.io", "a.b"}
	for _, g := range valid {
		if err := cozyv1alpha1.ValidateApplicationGroup(g); err != nil {
			t.Errorf("ValidateApplicationGroup(%q) = %v, want nil", g, err)
		}
	}
	invalid := []string{
		"", "apps", "-bad.example.com", "bad-.example.com", "Bad.example.com",
		"cozystack.io", "apps.cozystack.io", "x.cozystack.io", "k8s.io", "apps.k8s.io",
	}
	for _, g := range invalid {
		if err := cozyv1alpha1.ValidateApplicationGroup(g); err == nil {
			t.Errorf("ValidateApplicationGroup(%q) = nil, want error", g)
		}
	}
}
