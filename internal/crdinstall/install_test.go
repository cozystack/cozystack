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

package crdinstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteEmbeddedManifests(t *testing.T) {
	tmpDir := t.TempDir()

	if err := WriteEmbeddedManifests(tmpDir); err != nil {
		t.Fatalf("WriteEmbeddedManifests() error = %v", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}

	var yamlFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			yamlFiles = append(yamlFiles, e.Name())
		}
	}

	if len(yamlFiles) == 0 {
		t.Error("WriteEmbeddedManifests() produced no YAML files")
	}

	expectedFiles := []string{
		"cozystack.io_packages.yaml",
		"cozystack.io_packagesources.yaml",
	}
	for _, expected := range expectedFiles {
		found := false
		for _, actual := range yamlFiles {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected file %q not found in output, got %v", expected, yamlFiles)
		}
	}

	// Verify files are non-empty
	for _, f := range yamlFiles {
		data, err := os.ReadFile(filepath.Join(tmpDir, f))
		if err != nil {
			t.Errorf("failed to read %s: %v", f, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("file %s is empty", f)
		}
	}
}

func TestWriteEmbeddedManifests_filePermissions(t *testing.T) {
	tmpDir := t.TempDir()

	if err := WriteEmbeddedManifests(tmpDir); err != nil {
		t.Fatalf("WriteEmbeddedManifests() error = %v", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Errorf("failed to get info for %s: %v", e.Name(), err)
			continue
		}
		perm := info.Mode().Perm()
		if perm&0o077 != 0 {
			t.Errorf("file %s has overly permissive mode %o, expected no group/other access", e.Name(), perm)
		}
	}
}

// newCRDManifestWriter returns a function that writes test CRD YAML files.
func newCRDManifestWriter(crds ...string) func(string) error {
	return func(dir string) error {
		for i, crd := range crds {
			filename := filepath.Join(dir, fmt.Sprintf("crd%d.yaml", i+1))
			if err := os.WriteFile(filename, []byte(crd), 0600); err != nil {
				return err
			}
		}
		return nil
	}
}

var testCRD1 = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: packages.cozystack.io
spec:
  group: cozystack.io
  names:
    kind: Package
    plural: packages
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

var testCRD2 = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: packagesources.cozystack.io
spec:
  group: cozystack.io
  names:
    kind: PackageSource
    plural: packagesources
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

// establishedInterceptor simulates CRDs becoming Established in the API server.
func establishedInterceptor() interceptor.Funcs {
	return interceptor.Funcs{
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
	}
}

func TestInstall_appliesAllCRDs(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(establishedInterceptor()).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, newCRDManifestWriter(testCRD1, testCRD2))
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
}

func TestInstall_noManifests(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, func(string) error { return nil })
	if err == nil {
		t.Error("Install() expected error for empty manifests, got nil")
	}
	if !strings.Contains(err.Error(), "no YAML manifest files found") {
		t.Errorf("Install() error = %v, want error containing 'no YAML manifest files found'", err)
	}
}

func TestInstall_writeManifestsFails(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, func(string) error { return os.ErrPermission })
	if err == nil {
		t.Error("Install() expected error when writeManifests fails, got nil")
	}
}

func TestInstall_rejectsNonCRDObjects(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	nonCRD := `apiVersion: v1
kind: Namespace
metadata:
  name: should-not-be-applied
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, newCRDManifestWriter(nonCRD))
	if err == nil {
		t.Fatal("Install() expected error for non-CRD object, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected object") {
		t.Errorf("Install() error = %v, want error containing 'unexpected object'", err)
	}
}

func TestInstall_crdNotEstablished(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	// No interceptor: CRDs will never get Established condition
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, newCRDManifestWriter(testCRD1))
	if err == nil {
		t.Fatal("Install() expected error when CRDs never become established, got nil")
	}
	if !strings.Contains(err.Error(), "CRDs not established") {
		t.Errorf("Install() error = %v, want error containing 'CRDs not established'", err)
	}
}
