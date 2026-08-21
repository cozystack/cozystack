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
	r := fakeREST()
	_, _, err := r.Delete(context.Background(), "cozystack.postgres-application", nil, nil)
	if err == nil || !apierrors.IsForbidden(err) {
		t.Fatalf("expected Forbidden deleting an official tap, got %v", err)
	}
}

func TestDeleteCommunityTapRemovesSource(t *testing.T) {
	r := fakeREST(psObj("community.a.b", "community-a-b"), ociRepoObj("community-a-b"))
	obj, ok, err := r.Delete(context.Background(), "community.a.b", nil, nil)
	if err != nil || !ok || obj == nil {
		t.Fatalf("delete failed: ok=%v err=%v", ok, err)
	}
	// PackageSource is gone.
	if _, err := r.dyn.Resource(gvrPackageSources).Get(context.Background(), "community.a.b", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected PackageSource deleted, got err=%v", err)
	}
	// The unreferenced OCIRepository is gone too.
	if _, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "community-a-b", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected OCIRepository deleted, got err=%v", err)
	}
}

func TestDeleteKeepsSharedSource(t *testing.T) {
	// Two PackageSources reference the same Flux source; deleting one must keep it.
	r := fakeREST(
		psObj("community.a.b", "shared-src"),
		psObj("community.c.d", "shared-src"),
		ociRepoObj("shared-src"),
	)
	if _, ok, err := r.Delete(context.Background(), "community.a.b", nil, nil); err != nil || !ok {
		t.Fatalf("delete failed: ok=%v err=%v", ok, err)
	}
	if _, err := r.dyn.Resource(gvrOCIRepos).Namespace("cozy-system").Get(context.Background(), "shared-src", metav1.GetOptions{}); err != nil {
		t.Errorf("shared OCIRepository must be kept, got err=%v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	r := fakeREST()
	_, _, err := r.Delete(context.Background(), "community.missing.x", nil, nil)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}
