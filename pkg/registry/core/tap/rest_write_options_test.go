// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package tap

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// The fake dynamic client's tracker ignores DryRun and persists regardless, so
// asserting that an object is absent afterwards would pass with or without the
// fix. What distinguishes them is the options the storage hands to the backing
// client, which the fake records on the action.

func recordedActions(t *testing.T, r *REST) []k8stesting.Action {
	t.Helper()
	f, ok := r.dyn.(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatalf("expected a fake dynamic client, got %T", r.dyn)
	}
	return f.Actions()
}

func createDryRunFor(t *testing.T, r *REST, gvr schema.GroupVersionResource) []string {
	t.Helper()
	for _, a := range recordedActions(t, r) {
		c, ok := a.(k8stesting.CreateActionImpl)
		if ok && a.GetVerb() == "create" && a.GetResource() == gvr {
			return c.CreateOptions.DryRun
		}
	}
	t.Fatalf("no create action recorded for %q", gvr.Resource)
	return nil
}

func updateDryRunFor(t *testing.T, r *REST, gvr schema.GroupVersionResource) []string {
	t.Helper()
	for _, a := range recordedActions(t, r) {
		u, ok := a.(k8stesting.UpdateActionImpl)
		if ok && a.GetVerb() == "update" && a.GetResource() == gvr {
			return u.UpdateOptions.DryRun
		}
	}
	t.Fatalf("no update action recorded for %q", gvr.Resource)
	return nil
}

func deleteDryRunFor(t *testing.T, r *REST, gvr schema.GroupVersionResource) []string {
	t.Helper()
	for _, a := range recordedActions(t, r) {
		d, ok := a.(k8stesting.DeleteActionImpl)
		if ok && a.GetVerb() == "delete" && a.GetResource() == gvr {
			return d.DeleteOptions.DryRun
		}
	}
	t.Fatalf("no delete action recorded for %q", gvr.Resource)
	return nil
}

func wantDryRunAll(t *testing.T, what string, got []string) {
	t.Helper()
	if len(got) != 1 || got[0] != metav1.DryRunAll {
		t.Errorf("%s dropped the caller's dry-run: got %v, want [%s]", what, got, metav1.DryRunAll)
	}
}

func connectTap() *corev1alpha1.Tap {
	return &corev1alpha1.Tap{
		ObjectMeta: metav1.ObjectMeta{Name: "a.b"},
		Spec:       corev1alpha1.TapSpec{URL: "oci://ghcr.io/a/b", Tag: "v1"},
	}
}

// orphanOciRepoObj is a tap whose OCIRepository exists but whose PackageSource
// was never materialized, which is the fallback branch of Delete.
func orphanOciRepoObj(srcName, tapName string) *unstructured.Unstructured {
	u := ociRepoObj(srcName)
	u.SetLabels(map[string]string{tapconst.Label: "true"})
	u.SetAnnotations(map[string]string{tapconst.NameAnnotation: tapName})
	return u
}

func TestCreateForwardsDryRunToBackingSource(t *testing.T) {
	r := fakeREST()
	opts := &metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
	if _, err := r.Create(context.Background(), connectTap(), nil, opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	wantDryRunAll(t, "the backing OCIRepository create", createDryRunFor(t, r, gvrOCIRepos))
}

func TestCreateForwardsDryRunWhenSourceAlreadyExists(t *testing.T) {
	// A repeat connect at the same URL takes the update branch.
	r := fakeREST(ociRepoObj("tap-a-b"))
	opts := &metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
	if _, err := r.Create(context.Background(), connectTap(), nil, opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	wantDryRunAll(t, "the backing OCIRepository update", updateDryRunFor(t, r, gvrOCIRepos))
}

func TestDeleteForwardsDryRunToBackingDeletes(t *testing.T) {
	r := fakeREST(tapPsObj("a.b", "tap-a-b"), ociRepoObj("tap-a-b"))
	opts := &metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}}
	if _, _, err := r.Delete(context.Background(), "a.b", nil, opts); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantDryRunAll(t, "the backing PackageSource delete", deleteDryRunFor(t, r, gvrPackageSources))
	wantDryRunAll(t, "the backing OCIRepository delete", deleteDryRunFor(t, r, gvrOCIRepos))
}

func TestDeleteOrphanTapSourceForwardsDryRun(t *testing.T) {
	r := fakeREST(orphanOciRepoObj("tap-a-b", "a.b"))
	opts := &metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}}
	if _, _, err := r.Delete(context.Background(), "a.b", nil, opts); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantDryRunAll(t, "the orphan OCIRepository delete", deleteDryRunFor(t, r, gvrOCIRepos))
}

func TestWritesWithoutOptionsDoNotSynthesiseDryRun(t *testing.T) {
	// Callers that pass no options at all must keep writing for real.
	r := fakeREST(tapPsObj("a.b", "tap-a-b"), ociRepoObj("tap-a-b"))
	if _, _, err := r.Delete(context.Background(), "a.b", nil, nil); err != nil {
		t.Fatalf("delete with nil options: %v", err)
	}
	if got := deleteDryRunFor(t, r, gvrPackageSources); len(got) != 0 {
		t.Errorf("nil options must not produce a dry-run: got %v", got)
	}
}
