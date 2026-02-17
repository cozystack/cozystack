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

package manifestutil

import (
	"context"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCollectCRDNames(t *testing.T) {
	objects := []*unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]interface{}{"name": "test-ns"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "packages.cozystack.io"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "test-deploy"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "packagesources.cozystack.io"},
		}},
	}

	names := CollectCRDNames(objects)
	if len(names) != 2 {
		t.Fatalf("CollectCRDNames() returned %d names, want 2", len(names))
	}
	if names[0] != "packages.cozystack.io" {
		t.Errorf("names[0] = %q, want %q", names[0], "packages.cozystack.io")
	}
	if names[1] != "packagesources.cozystack.io" {
		t.Errorf("names[1] = %q, want %q", names[1], "packagesources.cozystack.io")
	}
}

func TestCollectCRDNames_noCRDs(t *testing.T) {
	objects := []*unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]interface{}{"name": "test"},
		}},
	}

	names := CollectCRDNames(objects)
	if len(names) != 0 {
		t.Errorf("CollectCRDNames() returned %d names, want 0", len(names))
	}
}

func TestWaitForCRDsEstablished_success(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	// Create a CRD object in the fake client
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "packages.cozystack.io"},
	}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crd).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				u, ok := obj.(*unstructured.Unstructured)
				if !ok {
					return nil
				}
				if u.GetKind() == "CustomResourceDefinition" {
					_ = unstructured.SetNestedSlice(u.Object, []interface{}{
						map[string]interface{}{
							"type":   "Established",
							"status": "True",
						},
					}, "status", "conditions")
				}
				return nil
			},
		}).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := WaitForCRDsEstablished(ctx, fakeClient, []string{"packages.cozystack.io"})
	if err != nil {
		t.Fatalf("WaitForCRDsEstablished() error = %v", err)
	}
}

func TestWaitForCRDsEstablished_timeout(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	// CRD exists but never gets Established condition
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "packages.cozystack.io"},
	}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(crd).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := WaitForCRDsEstablished(ctx, fakeClient, []string{"packages.cozystack.io"})
	if err == nil {
		t.Fatal("WaitForCRDsEstablished() expected error on timeout, got nil")
	}
	if !contains(err.Error(), "packages.cozystack.io") {
		t.Errorf("error should mention stuck CRD name, got: %v", err)
	}
}

func TestWaitForCRDsEstablished_empty(t *testing.T) {
	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx := context.Background()
	err := WaitForCRDsEstablished(ctx, fakeClient, nil)
	if err != nil {
		t.Fatalf("WaitForCRDsEstablished() with empty names should return nil, got: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
