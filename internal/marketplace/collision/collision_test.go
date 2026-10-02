// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package collision

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := cozyv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPackageSourceName(t *testing.T) {
	s := scheme(t)

	// A foreign (official) PackageSource of the same name blocks the tap.
	official := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: "acme.hello"}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(official).Build()
	if err := PackageSourceName(context.Background(), cl, "acme.hello", "tap-acme-hello"); err == nil {
		t.Error("expected a collision error against a foreign PackageSource of the same name")
	}

	// This tap's own PackageSource (same label + source annotation) is not a
	// collision: a re-tap must be idempotent.
	own := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{
		Name:        "acme.hello",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "tap-acme-hello"},
	}}
	clOwn := fake.NewClientBuilder().WithScheme(s).WithObjects(own).Build()
	if err := PackageSourceName(context.Background(), clOwn, "acme.hello", "tap-acme-hello"); err != nil {
		t.Errorf("a re-tap of the same source must not collide, got %v", err)
	}

	// A tap from a different source claiming the same name is a collision.
	if err := PackageSourceName(context.Background(), clOwn, "acme.hello", "tap-other-source"); err == nil {
		t.Error("expected a collision error when a different source claims a name owned by another tap")
	}

	// No existing PackageSource: no collision.
	clEmpty := fake.NewClientBuilder().WithScheme(s).Build()
	if err := PackageSourceName(context.Background(), clEmpty, "acme.hello", "tap-acme-hello"); err != nil {
		t.Errorf("no existing PackageSource must not collide, got %v", err)
	}
}

func TestOwnsEmptySourceNeverMatchesLabelOnly(t *testing.T) {
	// A PackageSource with no SourceRef yields sourceName "". A label-only object
	// (no source annotation, which also reads as "") must NOT be reported as owned,
	// or a delete/adopt keyed on Owns would hit an object this source never created.
	labelOnly := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{
		Name:   "x",
		Labels: map[string]string{tapconst.Label: "true"},
	}}
	if Owns(labelOnly, "") {
		t.Error("Owns(labelOnly, \"\") must be false")
	}
	// A properly annotated object with a real source is still owned.
	owned := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{
		Name:        "x",
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "tap-a"},
	}}
	if !Owns(owned, "tap-a") {
		t.Error("Owns(owned, \"tap-a\") must be true")
	}
}

func TestManagedRegistration(t *testing.T) {
	auto := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{
		Labels:      map[string]string{tapconst.Label: "true"},
		Annotations: map[string]string{tapconst.SourceAnnotation: "tap-a"},
	}}
	if !ManagedRegistration(auto, "tap-a", auto.Spec.Variant) {
		t.Error("owned + empty variant must be a managed registration")
	}
	pinned := auto.DeepCopy()
	pinned.Spec.Variant = "full"
	if ManagedRegistration(pinned, "tap-a", pinned.Spec.Variant) {
		t.Error("a pinned (non-empty variant) Package is not managed")
	}
	foreign := auto.DeepCopy()
	foreign.Annotations[tapconst.SourceAnnotation] = "tap-other"
	if ManagedRegistration(foreign, "tap-a", foreign.Spec.Variant) {
		t.Error("a Package owned by another source is not managed")
	}
	if ManagedRegistration(auto, "", auto.Spec.Variant) {
		t.Error("empty sourceName must never match")
	}
}
