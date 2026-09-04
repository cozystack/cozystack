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
