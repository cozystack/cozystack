// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package tap

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

func TestAnyOtherReferences(t *testing.T) {
	pss := []cozyv1alpha1.PackageSource{
		{ObjectMeta: metav1.ObjectMeta{Name: "community.a.b"}, Spec: cozyv1alpha1.PackageSourceSpec{SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src1"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "community.c.d"}, Spec: cozyv1alpha1.PackageSourceSpec{SourceRef: &cozyv1alpha1.PackageSourceRef{Name: "src2"}}},
	}
	if anyOtherReferences(pss, "src1", "community.a.b") {
		t.Error("src1 is referenced only by the excluded PS; expected false")
	}
	if !anyOtherReferences(pss, "src2", "community.a.b") {
		t.Error("src2 is referenced by another PS; expected true")
	}
}

func psObj(name, srcName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cozystack.io/v1alpha1",
		"kind":       "PackageSource",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"sourceRef": map[string]interface{}{"kind": "OCIRepository", "name": srcName, "namespace": "cozy-system"},
		},
	}}
}

// tapPsObj is a materialized tap's PackageSource: like psObj but stamped with
// the marketplace-tap marker label, so the label-based disconnect guard permits
// it.
func tapPsObj(name, srcName string) *unstructured.Unstructured {
	u := psObj(name, srcName)
	u.SetLabels(map[string]string{"apps.cozystack.io/marketplace-tap": "true"})
	return u
}

func ociRepoObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "OCIRepository",
		"metadata":   map[string]interface{}{"name": name, "namespace": "cozy-system"},
		"spec":       map[string]interface{}{"url": "oci://ghcr.io/a/b"},
	}}
}

func fakeREST(objs ...runtime.Object) *REST {
	scheme := runtime.NewScheme()
	gvrToKind := map[schema.GroupVersionResource]string{
		gvrPackageSources: "PackageSourceList",
		gvrAppDefs:        "ApplicationDefinitionList",
		gvrOCIRepos:       "OCIRepositoryList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToKind, objs...)
	return NewREST(dyn)
}

func TestDeleteRefusesOfficialTap(t *testing.T) {
	// An existing PackageSource without the tap marker label is official and
	// protected from disconnect.
	r := fakeREST(psObj("cozystack.postgres-application", "cozystack-source"))
	_, _, err := r.Delete(context.Background(), "cozystack.postgres-application", nil, nil)
	if err == nil || !apierrors.IsForbidden(err) {
		t.Fatalf("expected Forbidden deleting an official (unlabeled) source, got %v", err)
	}
}

func TestDeleteCommunityTapRemovesSource(t *testing.T) {
	r := fakeREST(tapPsObj("a.b", "tap-a-b"), ociRepoObj("tap-a-b"))
	obj, ok, err := r.Delete(context.Background(), "a.b", nil, nil)
	if err != nil || !ok || obj == nil {
		t.Fatalf("delete failed: ok=%v err=%v", ok, err)
	}
	// PackageSource is gone.
	if _, err := r.dyn.Resource(gvrPackageSources).Get(context.Background(), "a.b", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected PackageSource deleted, got err=%v", err)
	}
	// The unreferenced OCIRepository is gone too.
	if _, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "tap-a-b", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected OCIRepository deleted, got err=%v", err)
	}
}

func TestDeleteKeepsSharedSource(t *testing.T) {
	// Two PackageSources reference the same Flux source; deleting one must keep it.
	r := fakeREST(
		tapPsObj("a.b", "shared-src"),
		tapPsObj("c.d", "shared-src"),
		ociRepoObj("shared-src"),
	)
	if _, ok, err := r.Delete(context.Background(), "a.b", nil, nil); err != nil || !ok {
		t.Fatalf("delete failed: ok=%v err=%v", ok, err)
	}
	if _, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "shared-src", metav1.GetOptions{}); err != nil {
		t.Errorf("shared OCIRepository must be kept, got err=%v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	r := fakeREST()
	_, _, err := r.Delete(context.Background(), "missing.x", nil, nil)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// orphanOciRepo is a tap OCIRepository with no materialized PackageSource yet
// (a pending or failed connect). Its tap-name annotation records its own object
// name, which is the tap's identity before materialization.
func orphanOciRepo(fluxName string) *unstructured.Unstructured {
	u := ociRepoObj(fluxName)
	u.SetLabels(map[string]string{"apps.cozystack.io/marketplace-tap": "true"})
	u.SetAnnotations(map[string]string{"apps.cozystack.io/tap-name": fluxName})
	return u
}

func TestDeleteOrphanTapSource(t *testing.T) {
	// No PackageSource exists, only the labeled OCIRepository from a pending connect.
	r := fakeREST(orphanOciRepo("tap-a-b"))
	obj, ok, err := r.Delete(context.Background(), "tap-a-b", nil, nil)
	if err != nil || !ok || obj == nil {
		t.Fatalf("expected orphan tap source removed: ok=%v err=%v", ok, err)
	}
	if _, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "tap-a-b", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected orphan OCIRepository deleted, got %v", err)
	}
}

func TestDeleteOrphanNoMatch(t *testing.T) {
	// A labeled source exists but for a different tap name -> still NotFound.
	r := fakeREST(orphanOciRepo("tap-a-b"))
	if _, _, err := r.Delete(context.Background(), "tap-other-x", nil, nil); !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound for a non-matching orphan, got %v", err)
	}
}

// TestGetPendingTap covers a collision-blocked or connecting tap: its
// OCIRepository exists but no PackageSource does. Get must surface it as a
// pending Tap (mirroring List), not 404, so a dashboard detail view can show the
// block reason.
func TestGetPendingTap(t *testing.T) {
	repo := orphanOciRepo("tap-a-b")
	repo.SetAnnotations(map[string]string{
		"apps.cozystack.io/tap-name":  "tap-a-b",
		"apps.cozystack.io/tap-error": `a PackageSource named "acme.demo" already exists`,
	})
	r := fakeREST(repo)

	obj, err := r.Get(context.Background(), "tap-a-b", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get on a pending tap must not 404: %v", err)
	}
	tap := obj.(*corev1alpha1.Tap)
	if tap.Name != "tap-a-b" || tap.Spec.Ready || !tap.Spec.Community {
		t.Errorf("unexpected pending tap from Get: %+v", tap.Spec)
	}
	if tap.Spec.Message != `a PackageSource named "acme.demo" already exists` {
		t.Errorf("Get did not surface the collision message: %q", tap.Spec.Message)
	}

	// A name with neither a PackageSource nor a matching tap source is NotFound.
	if _, err := r.Get(context.Background(), "tap-missing", &metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound for an unknown name, got %v", err)
	}
}

func TestParseConnectURL(t *testing.T) {
	got, err := parseConnectURL("oci://ghcr.io/foo/bar:v2", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.URL != "oci://ghcr.io/foo/bar" || got.Tag != "v2" || got.FluxSourceName != "tap-foo-bar" {
		t.Fatalf("unexpected parse: %+v", got)
	}
	// tag override wins over the URL tag.
	if o, _ := parseConnectURL("oci://ghcr.io/foo/bar:v2", "v9"); o.Tag != "v9" {
		t.Errorf("tag override = %q", o.Tag)
	}
	// no tag defaults to latest.
	if o, _ := parseConnectURL("oci://ghcr.io/foo/bar", ""); o.Tag != "latest" {
		t.Errorf("default tag = %q", o.Tag)
	}
	if _, err := parseConnectURL("ghcr.io/foo/bar", ""); err == nil {
		t.Error("expected error for non-oci url")
	}
	if _, err := parseConnectURL("oci://ghcr.io/foo/bar@sha256:abc", ""); err == nil {
		t.Error("expected error for digest ref")
	}
}

func TestCreateGuards(t *testing.T) {
	r := fakeREST()
	if _, err := r.Create(context.Background(), &corev1alpha1.Tap{}, nil, nil); !apierrors.IsBadRequest(err) {
		t.Errorf("missing url should be BadRequest, got %v", err)
	}
	if _, err := r.Create(context.Background(), &corev1alpha1.Tap{Spec: corev1alpha1.TapSpec{URL: "ghcr.io/x/y"}}, nil, nil); !apierrors.IsBadRequest(err) {
		t.Errorf("non-oci url should be BadRequest, got %v", err)
	}
}

func TestCreateRepeatPreservesFinalizerAndRevision(t *testing.T) {
	// A source already connected and materialized carries the operator's
	// finalizer and materialized-revision annotation; a repeat connect must
	// update the tag without stripping them.
	existing := ociRepoObj("tap-foo-bar")
	// Same repository, connected earlier at a different tag.
	_ = unstructured.SetNestedField(existing.Object, "oci://ghcr.io/foo/bar", "spec", "url")
	existing.SetFinalizers([]string{"apps.cozystack.io/tap-materializer"})
	existing.SetAnnotations(map[string]string{"apps.cozystack.io/materialized-revision": "rev-1"})
	r := fakeREST(existing)

	in := &corev1alpha1.Tap{Spec: corev1alpha1.TapSpec{URL: "oci://ghcr.io/foo/bar:v2"}}
	if _, err := r.Create(context.Background(), in, nil, nil); err != nil {
		t.Fatalf("repeat create: %v", err)
	}
	u, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "tap-foo-bar", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(u.GetFinalizers()) == 0 {
		t.Error("repeat connect stripped the operator finalizer")
	}
	if u.GetAnnotations()["apps.cozystack.io/materialized-revision"] != "rev-1" {
		t.Error("repeat connect stripped the materialized-revision annotation")
	}
	if tag, _, _ := unstructured.NestedString(u.Object, "spec", "ref", "tag"); tag != "v2" {
		t.Errorf("repeat connect did not update the tag, got %q", tag)
	}
	if u.GetAnnotations()["apps.cozystack.io/tap-name"] != "tap-foo-bar" {
		t.Error("repeat connect did not set the tap-name annotation")
	}
}

func TestCreateRefusesConflictingURL(t *testing.T) {
	// A source with the same derived name but a different registry host must not
	// be silently retargeted.
	existing := ociRepoObj("tap-foo-bar")
	_ = unstructured.SetNestedField(existing.Object, "oci://other.host/foo/bar", "spec", "url")
	r := fakeREST(existing)

	in := &corev1alpha1.Tap{Spec: corev1alpha1.TapSpec{URL: "oci://ghcr.io/foo/bar:v1"}}
	_, err := r.Create(context.Background(), in, nil, nil)
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected Conflict for a different repository at the same name, got %v", err)
	}
	// The existing source keeps its original URL.
	u, _ := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "tap-foo-bar", metav1.GetOptions{})
	if url, _, _ := unstructured.NestedString(u.Object, "spec", "url"); url != "oci://other.host/foo/bar" {
		t.Errorf("existing source URL was overwritten: %q", url)
	}
}

func TestCreateMakesLabeledSource(t *testing.T) {
	r := fakeREST()
	in := &corev1alpha1.Tap{Spec: corev1alpha1.TapSpec{URL: "oci://ghcr.io/foo/bar:v1", SecretRef: "pull-creds"}}
	out, err := r.Create(context.Background(), in, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tap := out.(*corev1alpha1.Tap)
	if tap.Name != "tap-foo-bar" || tap.Spec.Ready || !tap.Spec.Community {
		t.Fatalf("unexpected returned tap: %+v", tap.Spec)
	}
	// The returned object must never echo the url or secret back.
	if tap.Spec.URL != "" || tap.Spec.SecretRef != "" {
		t.Errorf("create response leaked connect inputs: %+v", tap.Spec)
	}
	u, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "tap-foo-bar", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("OCIRepository not created: %v", err)
	}
	if u.GetLabels()["apps.cozystack.io/marketplace-tap"] != "true" {
		t.Errorf("OCIRepository missing tap label: %v", u.GetLabels())
	}
	if u.GetAnnotations()["apps.cozystack.io/tap-name"] != "tap-foo-bar" {
		t.Errorf("OCIRepository missing tap-name annotation: %v", u.GetAnnotations())
	}
	secretName, _, _ := unstructured.NestedString(u.Object, "spec", "secretRef", "name")
	if secretName != "pull-creds" {
		t.Errorf("secretRef not set on OCIRepository: got %q", secretName)
	}
}
