/*
Copyright 2025 The Cozystack Authors.

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

package cmd

import (
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

func TestParseOCIRef(t *testing.T) {
	cases := []struct {
		in                                  string
		wantURL, wantOrg, wantRepo, wantTag string
		wantErr                             bool
	}{
		{"oci://ghcr.io/foo/bar:v1", "oci://ghcr.io/foo/bar", "foo", "bar", "v1", false},
		{"oci://ghcr.io/foo/bar", "oci://ghcr.io/foo/bar", "foo", "bar", "latest", false},
		{"oci://ghcr.io/foo/group/bar:1.2.3", "oci://ghcr.io/foo/group/bar", "group", "bar", "1.2.3", false},
		{"oci://registry.example.com/solo:tag", "oci://registry.example.com/solo", "", "solo", "tag", false},
		{"https://ghcr.io/foo/bar", "", "", "", "", true},
		{"oci://ghcr.io/foo/bar@sha256:abc", "", "", "", "", true},
		{"oci://ghcr.io", "", "", "", "", true},
	}
	for _, c := range cases {
		got, err := parseOCIRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseOCIRef(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOCIRef(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got.URL != c.wantURL || got.Org != c.wantOrg || got.Repo != c.wantRepo || got.Tag != c.wantTag {
			t.Errorf("parseOCIRef(%q) = %+v, want url=%q org=%q repo=%q tag=%q", c.in, got, c.wantURL, c.wantOrg, c.wantRepo, c.wantTag)
		}
	}
}

func TestFluxSourceName(t *testing.T) {
	if got := fluxSourceName(ociRef{Org: "foo", Repo: "bar"}); got != "community-foo-bar" {
		t.Errorf("fluxSourceName = %q", got)
	}
	if got := fluxSourceName(ociRef{Repo: "solo"}); got != "community-solo" {
		t.Errorf("fluxSourceName (no org) = %q", got)
	}
	if got := fluxSourceName(ociRef{Org: "Foo_X", Repo: "Bar.Y"}); got != "community-foo-x-bar-y" {
		t.Errorf("fluxSourceName sanitized = %q", got)
	}
}

func TestTapPackageSourceName(t *testing.T) {
	r := ociRef{Org: "foo", Repo: "bar"}
	if got := tapPackageSourceName(r, "example.hello", true); got != "community.foo.bar" {
		t.Errorf("single = %q", got)
	}
	if got := tapPackageSourceName(r, "example.hello", false); got != "community.foo.bar.example.hello" {
		t.Errorf("multiple = %q", got)
	}
	// An already community-prefixed original name must not double the prefix.
	if got := tapPackageSourceName(r, "community.hello", false); got != "community.foo.bar.hello" {
		t.Errorf("multiple with community-prefixed original = %q", got)
	}
	if got := tapPackageSourceName(ociRef{Repo: "solo"}, "x", true); got != "community.solo" {
		t.Errorf("no org = %q", got)
	}
}

func TestBuildTapOCIRepository(t *testing.T) {
	r := ociRef{URL: "oci://ghcr.io/foo/bar", Tag: "v1"}
	obj := buildTapOCIRepository("community-foo-bar", r, "")
	if obj.Namespace != "cozy-system" || obj.Spec.URL != "oci://ghcr.io/foo/bar" {
		t.Fatalf("unexpected OCIRepository: %+v", obj.Spec)
	}
	if obj.Spec.Reference == nil || obj.Spec.Reference.Tag != "v1" {
		t.Fatalf("expected tag v1, got %+v", obj.Spec.Reference)
	}
	if obj.Spec.Interval.Duration.Minutes() != 5 {
		t.Fatalf("expected 5m interval, got %v", obj.Spec.Interval)
	}
	if obj.Spec.SecretRef != nil {
		t.Fatalf("expected no secretRef, got %+v", obj.Spec.SecretRef)
	}
	withSecret := buildTapOCIRepository("n", r, "pull-creds")
	if withSecret.Spec.SecretRef == nil || withSecret.Spec.SecretRef.Name != "pull-creds" {
		t.Fatalf("expected secretRef pull-creds, got %+v", withSecret.Spec.SecretRef)
	}
}

func TestRewritePackageSourceForTap(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{}
	ps.SetName("example.hello")
	ps.SetResourceVersion("12345")
	ps.SetUID("some-uid")
	ps.Spec.SourceRef = &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "hello-packages", Namespace: "cozy-system", Path: "/"}
	ps.Spec.Variants = []cozyv1alpha1.Variant{{Name: "default", Components: []cozyv1alpha1.Component{{Name: "hello", Path: "apps/hello"}}}}

	rewritePackageSourceForTap(ps, "community.foo.bar", "community-foo-bar", "/")

	if ps.GetName() != "community.foo.bar" {
		t.Errorf("name = %q", ps.GetName())
	}
	if ps.GetResourceVersion() != "" || ps.GetUID() != "" {
		t.Errorf("resourceVersion/uid must be cleared for a fresh apply")
	}
	if ps.Spec.SourceRef.Name != "community-foo-bar" || ps.Spec.SourceRef.Namespace != "cozy-system" || ps.Spec.SourceRef.Kind != "OCIRepository" {
		t.Errorf("sourceRef not repointed: %+v", ps.Spec.SourceRef)
	}
	if len(ps.Spec.Variants) != 1 || ps.Spec.Variants[0].Components[0].Path != "apps/hello" {
		t.Errorf("variants/components must be preserved: %+v", ps.Spec.Variants)
	}
	// An empty source path defaults to "/".
	ps2 := &cozyv1alpha1.PackageSource{}
	rewritePackageSourceForTap(ps2, "community.x", "src", "")
	if ps2.Spec.SourceRef.Path != "/" {
		t.Errorf("empty path should default to /, got %q", ps2.Spec.SourceRef.Path)
	}
}
