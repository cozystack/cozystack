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
	"context"
	"errors"
	"testing"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
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

// fluxSourceName names the OCIRepository (a Flux source object in cozy-system),
// not the app-facing PackageSource. It carries a neutral "tap-" prefix, not
// "community-": tapped repositories keep their own declared PackageSource names.
func TestFluxSourceName(t *testing.T) {
	if got := fluxSourceName(ociRef{Org: "foo", Repo: "bar"}); got != "tap-foo-bar" {
		t.Errorf("fluxSourceName = %q", got)
	}
	if got := fluxSourceName(ociRef{Repo: "solo"}); got != "tap-solo" {
		t.Errorf("fluxSourceName (no org) = %q", got)
	}
	if got := fluxSourceName(ociRef{Org: "Foo_X", Repo: "Bar.Y"}); got != "tap-foo-x-bar-y" {
		t.Errorf("fluxSourceName sanitized = %q", got)
	}
}

func TestObjectExists(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sourcev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &sourcev1.OCIRepository{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "cozy-system"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	if !objectExists(context.Background(), cl, existing) {
		t.Error("expected an existing object to be reported as existing (would be spared on rollback)")
	}
	absent := &sourcev1.OCIRepository{ObjectMeta: metav1.ObjectMeta{Name: "y", Namespace: "cozy-system"}}
	if objectExists(context.Background(), cl, absent) {
		t.Error("expected an absent object to be reported as not existing (eligible for rollback)")
	}

	// Fail-safe: a non-NotFound Get error (RBAC/throttling/timeout) must report
	// "exists" so rollback never deletes a possibly-pre-existing object.
	errCl := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return apierrors.NewInternalError(errors.New("apiserver unavailable"))
		},
	}).Build()
	if !objectExists(context.Background(), errCl, absent) {
		t.Error("a non-NotFound Get error must be treated as possibly-existing (spared from rollback)")
	}
}

func TestBuildTapOCIRepository(t *testing.T) {
	r := ociRef{URL: "oci://ghcr.io/foo/bar", Org: "foo", Repo: "bar", Tag: "v1"}
	obj := buildTapOCIRepository("tap-foo-bar", r, "")
	if obj.Namespace != "cozy-system" || obj.Spec.URL != "oci://ghcr.io/foo/bar" {
		t.Fatalf("unexpected OCIRepository: %+v", obj.Spec)
	}
	if obj.Spec.Reference == nil || obj.Spec.Reference.Tag != "v1" {
		t.Fatalf("expected tag v1, got %+v", obj.Spec.Reference)
	}
	if obj.Labels[tapconst.Label] != "true" {
		t.Errorf("expected marketplace-tap label, got %v", obj.Labels)
	}
	// The tap-name annotation records the source's own object name (used by the
	// dashboard orphan-disconnect), not a derived community.* PackageSource name.
	if obj.Annotations[tapconst.NameAnnotation] != "tap-foo-bar" {
		t.Errorf("expected tap-name annotation tap-foo-bar, got %v", obj.Annotations)
	}
	if obj.Spec.Interval.Minutes() != 5 {
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

// rewritePackageSourceForTap must NOT rename the PackageSource: a tapped
// repository keeps its own declared name. It only clears server-set metadata,
// stamps the tap marker (label + source annotation), and repoints the sourceRef.
func TestRewritePackageSourceForTap(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{}
	ps.SetName("acme.hello")
	ps.SetResourceVersion("12345")
	ps.SetUID("some-uid")
	ps.Spec.SourceRef = &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "hello-packages", Namespace: "cozy-system", Path: "/"}
	ps.Spec.Variants = []cozyv1alpha1.Variant{{Name: "default", Components: []cozyv1alpha1.Component{{Name: "hello", Path: "apps/hello"}}}}

	rewritePackageSourceForTap(ps, "tap-acme-hello", "/")

	if ps.GetName() != "acme.hello" {
		t.Errorf("name must be preserved (no community rename), got %q", ps.GetName())
	}
	if ps.GetResourceVersion() != "" || ps.GetUID() != "" {
		t.Errorf("resourceVersion/uid must be cleared for a fresh apply")
	}
	if ps.GetLabels()[tapconst.Label] != "true" {
		t.Errorf("tap label must be stamped so the source is identifiable without a name prefix, got %v", ps.GetLabels())
	}
	if ps.GetAnnotations()[tapconst.SourceAnnotation] != "tap-acme-hello" {
		t.Errorf("source annotation must record the owning OCIRepository, got %v", ps.GetAnnotations())
	}
	if ps.Spec.SourceRef.Name != "tap-acme-hello" || ps.Spec.SourceRef.Namespace != "cozy-system" || ps.Spec.SourceRef.Kind != "OCIRepository" {
		t.Errorf("sourceRef not repointed: %+v", ps.Spec.SourceRef)
	}
	if len(ps.Spec.Variants) != 1 || ps.Spec.Variants[0].Components[0].Path != "apps/hello" {
		t.Errorf("variants/components must be preserved: %+v", ps.Spec.Variants)
	}
	// An empty source path defaults to "/".
	ps2 := &cozyv1alpha1.PackageSource{}
	rewritePackageSourceForTap(ps2, "src", "")
	if ps2.Spec.SourceRef.Path != "/" {
		t.Errorf("empty path should default to /, got %q", ps2.Spec.SourceRef.Path)
	}
}
