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

func TestReadYAMLObjects(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantErr   bool
	}{
		{
			name: "single document",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`,
			wantCount: 1,
		},
		{
			name: "multiple documents",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: test2
`,
			wantCount: 2,
		},
		{
			name:      "empty input",
			input:     "",
			wantCount: 0,
		},
		{
			name: "document without kind returns error",
			input: `apiVersion: v1
metadata:
  name: test
`,
			wantErr: true,
		},
		{
			name: "whitespace-only document between separators is skipped",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test1
---

---
apiVersion: v1
kind: ConfigMap
metadata:
  name: test2
`,
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects, err := readYAMLObjects(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("readYAMLObjects() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(objects) != tt.wantCount {
				t.Errorf("readYAMLObjects() returned %d objects, want %d", len(objects), tt.wantCount)
			}
		})
	}
}

func TestReadYAMLObjects_preservesFields(t *testing.T) {
	input := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: packages.cozystack.io
spec:
  group: cozystack.io
`
	objects, err := readYAMLObjects(strings.NewReader(input))
	if err != nil {
		t.Fatalf("readYAMLObjects() error = %v", err)
	}
	if len(objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects))
	}

	obj := objects[0]
	if obj.GetKind() != "CustomResourceDefinition" {
		t.Errorf("kind = %q, want %q", obj.GetKind(), "CustomResourceDefinition")
	}
	if obj.GetName() != "packages.cozystack.io" {
		t.Errorf("name = %q, want %q", obj.GetName(), "packages.cozystack.io")
	}
	if obj.GetAPIVersion() != "apiextensions.k8s.io/v1" {
		t.Errorf("apiVersion = %q, want %q", obj.GetAPIVersion(), "apiextensions.k8s.io/v1")
	}
}

func TestParseManifests(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "test.yaml")

	content := `apiVersion: v1
kind: ConfigMap
metadata:
  name: cm1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm2
`
	if err := os.WriteFile(manifestPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test manifest: %v", err)
	}

	objects, err := parseManifests(manifestPath)
	if err != nil {
		t.Fatalf("parseManifests() error = %v", err)
	}
	if len(objects) != 2 {
		t.Errorf("parseManifests() returned %d objects, want 2", len(objects))
	}
}

func TestParseManifests_fileNotFound(t *testing.T) {
	_, err := parseManifests("/nonexistent/path/test.yaml")
	if err == nil {
		t.Error("parseManifests() expected error for nonexistent file, got nil")
	}
}

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

	// Verify files are non-empty and contain valid YAML
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

func TestInstall_appliesAllCRDs(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apiextensions to scheme: %v", err)
	}

	// Intercept Get calls to simulate CRDs becoming Established
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
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

	// Write two CRD manifests
	writeManifests := func(dir string) error {
		crd1 := `apiVersion: apiextensions.k8s.io/v1
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
		crd2 := `apiVersion: apiextensions.k8s.io/v1
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
		if err := os.WriteFile(filepath.Join(dir, "crd1.yaml"), []byte(crd1), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "crd2.yaml"), []byte(crd2), 0600)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, writeManifests)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
}

func TestInstall_noManifests(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	writeEmpty := func(dir string) error {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, writeEmpty)
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

	writeFail := func(dir string) error {
		return os.ErrPermission
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, log.FromContext(context.Background()))

	err := Install(ctx, fakeClient, writeFail)
	if err == nil {
		t.Error("Install() expected error when writeManifests fails, got nil")
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
