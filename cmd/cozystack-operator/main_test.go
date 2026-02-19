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

package main

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(s)
	return s
}

func TestInstallPlatformPackageSource_Creates(t *testing.T) {
	s := newTestScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(s).Build()

	err := installPlatformPackageSource(context.Background(), k8sClient, "cozystack-platform", "OCIRepository")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cozystack.cozystack-platform"}, ps); err != nil {
		t.Fatalf("PackageSource not found: %v", err)
	}

	// Verify name
	if ps.Name != "cozystack.cozystack-platform" {
		t.Errorf("expected name %q, got %q", "cozystack.cozystack-platform", ps.Name)
	}

	// Verify annotation
	if ps.Annotations["operator.cozystack.io/skip-cozystack-values"] != "true" {
		t.Errorf("expected skip-cozystack-values annotation to be 'true', got %q", ps.Annotations["operator.cozystack.io/skip-cozystack-values"])
	}

	// Verify sourceRef
	if ps.Spec.SourceRef == nil {
		t.Fatal("expected SourceRef to be set")
	}
	if ps.Spec.SourceRef.Kind != "OCIRepository" {
		t.Errorf("expected sourceRef.kind %q, got %q", "OCIRepository", ps.Spec.SourceRef.Kind)
	}
	if ps.Spec.SourceRef.Name != "cozystack-platform" {
		t.Errorf("expected sourceRef.name %q, got %q", "cozystack-platform", ps.Spec.SourceRef.Name)
	}
	if ps.Spec.SourceRef.Namespace != "cozy-system" {
		t.Errorf("expected sourceRef.namespace %q, got %q", "cozy-system", ps.Spec.SourceRef.Namespace)
	}
	if ps.Spec.SourceRef.Path != "/" {
		t.Errorf("expected sourceRef.path %q, got %q", "/", ps.Spec.SourceRef.Path)
	}

	// Verify variants
	expectedVariants := []string{"default", "isp-full", "isp-hosted", "isp-full-generic"}
	if len(ps.Spec.Variants) != len(expectedVariants) {
		t.Fatalf("expected %d variants, got %d", len(expectedVariants), len(ps.Spec.Variants))
	}
	for i, name := range expectedVariants {
		if ps.Spec.Variants[i].Name != name {
			t.Errorf("expected variant[%d].name %q, got %q", i, name, ps.Spec.Variants[i].Name)
		}
		if len(ps.Spec.Variants[i].Components) != 1 {
			t.Errorf("expected variant[%d] to have 1 component, got %d", i, len(ps.Spec.Variants[i].Components))
		}
	}
}

func TestInstallPlatformPackageSource_Updates(t *testing.T) {
	s := newTestScheme()

	existing := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "cozystack.cozystack-platform",
			ResourceVersion: "1",
		},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{
				Kind:      "OCIRepository",
				Name:      "old-name",
				Namespace: "cozy-system",
			},
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(s).WithObjects(existing).Build()

	err := installPlatformPackageSource(context.Background(), k8sClient, "cozystack-platform", "OCIRepository")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cozystack.cozystack-platform"}, ps); err != nil {
		t.Fatalf("PackageSource not found: %v", err)
	}

	// Verify sourceRef was updated
	if ps.Spec.SourceRef.Name != "cozystack-platform" {
		t.Errorf("expected updated sourceRef.name %q, got %q", "cozystack-platform", ps.Spec.SourceRef.Name)
	}

	// Verify all 4 variants are present after update
	if len(ps.Spec.Variants) != 4 {
		t.Errorf("expected 4 variants after update, got %d", len(ps.Spec.Variants))
	}
}

func TestInstallPlatformPackageSource_GitRepository(t *testing.T) {
	s := newTestScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(s).Build()

	err := installPlatformPackageSource(context.Background(), k8sClient, "my-source", "GitRepository")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cozystack.my-source"}, ps); err != nil {
		t.Fatalf("PackageSource not found: %v", err)
	}

	if ps.Spec.SourceRef.Kind != "GitRepository" {
		t.Errorf("expected sourceRef.kind %q, got %q", "GitRepository", ps.Spec.SourceRef.Kind)
	}
	if ps.Spec.SourceRef.Name != "my-source" {
		t.Errorf("expected sourceRef.name %q, got %q", "my-source", ps.Spec.SourceRef.Name)
	}
}
