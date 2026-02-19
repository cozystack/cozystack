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
			Labels: map[string]string{
				"custom-label": "should-be-preserved",
			},
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

	// Verify that labels set by other controllers are preserved (SSA does not overwrite unmanaged fields)
	if ps.Labels["custom-label"] != "should-be-preserved" {
		t.Errorf("expected custom-label to be preserved, got %q", ps.Labels["custom-label"])
	}
}

func TestParsePlatformSourceURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantType   string
		wantURL    string
		wantErr    bool
	}{
		{
			name:     "OCI URL",
			url:      "oci://ghcr.io/cozystack/cozystack/cozystack-packages",
			wantType: "oci",
			wantURL:  "oci://ghcr.io/cozystack/cozystack/cozystack-packages",
		},
		{
			name:     "HTTPS URL",
			url:      "https://github.com/cozystack/cozystack",
			wantType: "git",
			wantURL:  "https://github.com/cozystack/cozystack",
		},
		{
			name:     "SSH URL",
			url:      "ssh://git@github.com/cozystack/cozystack",
			wantType: "git",
			wantURL:  "ssh://git@github.com/cozystack/cozystack",
		},
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
		},
		{
			name:    "unsupported scheme",
			url:     "ftp://example.com/repo",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceType, repoURL, err := parsePlatformSourceURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for URL %q, got nil", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sourceType != tt.wantType {
				t.Errorf("expected type %q, got %q", tt.wantType, sourceType)
			}
			if repoURL != tt.wantURL {
				t.Errorf("expected URL %q, got %q", tt.wantURL, repoURL)
			}
		})
	}
}

func TestInstallPlatformPackageSource_VariantValuesFiles(t *testing.T) {
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

	expectedValuesFiles := map[string][]string{
		"default":          {"values.yaml"},
		"isp-full":         {"values.yaml", "values-isp-full.yaml"},
		"isp-hosted":       {"values.yaml", "values-isp-hosted.yaml"},
		"isp-full-generic": {"values.yaml", "values-isp-full-generic.yaml"},
	}

	for _, v := range ps.Spec.Variants {
		expected, ok := expectedValuesFiles[v.Name]
		if !ok {
			t.Errorf("unexpected variant %q", v.Name)
			continue
		}

		if len(v.Components) != 1 {
			t.Errorf("variant %q: expected 1 component, got %d", v.Name, len(v.Components))
			continue
		}

		comp := v.Components[0]
		if comp.Name != "platform" {
			t.Errorf("variant %q: expected component name %q, got %q", v.Name, "platform", comp.Name)
		}
		if comp.Path != "core/platform" {
			t.Errorf("variant %q: expected component path %q, got %q", v.Name, "core/platform", comp.Path)
		}
		if comp.Install == nil {
			t.Errorf("variant %q: expected Install to be set", v.Name)
		} else {
			if comp.Install.Namespace != "cozy-system" {
				t.Errorf("variant %q: expected install namespace %q, got %q", v.Name, "cozy-system", comp.Install.Namespace)
			}
			if comp.Install.ReleaseName != "cozystack-platform" {
				t.Errorf("variant %q: expected install releaseName %q, got %q", v.Name, "cozystack-platform", comp.Install.ReleaseName)
			}
		}

		if len(comp.ValuesFiles) != len(expected) {
			t.Errorf("variant %q: expected %d valuesFiles, got %d", v.Name, len(expected), len(comp.ValuesFiles))
			continue
		}
		for i, f := range expected {
			if comp.ValuesFiles[i] != f {
				t.Errorf("variant %q: expected valuesFiles[%d] %q, got %q", v.Name, i, f, comp.ValuesFiles[i])
			}
		}
	}
}

func TestInstallPlatformPackageSource_CustomName(t *testing.T) {
	s := newTestScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(s).Build()

	err := installPlatformPackageSource(context.Background(), k8sClient, "custom-source", "OCIRepository")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cozystack.custom-source"}, ps); err != nil {
		t.Fatalf("PackageSource not found: %v", err)
	}

	if ps.Name != "cozystack.custom-source" {
		t.Errorf("expected name %q, got %q", "cozystack.custom-source", ps.Name)
	}
	if ps.Spec.SourceRef.Name != "custom-source" {
		t.Errorf("expected sourceRef.name %q, got %q", "custom-source", ps.Spec.SourceRef.Name)
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

func TestParseRefSpec(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  map[string]string{},
		},
		{
			name:  "single key-value",
			input: "tag=v1.0",
			want:  map[string]string{"tag": "v1.0"},
		},
		{
			name:  "multiple key-values",
			input: "digest=sha256:abc123,tag=v1.0",
			want:  map[string]string{"digest": "sha256:abc123", "tag": "v1.0"},
		},
		{
			name:  "whitespace around pairs",
			input: " tag=v1.0 , branch=main ",
			want:  map[string]string{"tag": "v1.0", "branch": "main"},
		},
		{
			name:  "equals sign in value",
			input: "digest=sha256:abc=123",
			want:  map[string]string{"digest": "sha256:abc=123"},
		},
		{
			name:    "missing equals sign",
			input:   "tag",
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   "=value",
			wantErr: true,
		},
		{
			name:    "empty value",
			input:   "tag=",
			wantErr: true,
		},
		{
			name:  "trailing comma",
			input: "tag=v1.0,",
			want:  map[string]string{"tag": "v1.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRefSpec(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("expected %d entries, got %d: %v", len(tt.want), len(got), got)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("expected %q=%q, got %q=%q", k, v, k, got[k])
				}
			}
		})
	}
}

func TestValidateOCIRef(t *testing.T) {
	tests := []struct {
		name    string
		refMap  map[string]string
		wantErr bool
	}{
		{
			name:   "valid tag",
			refMap: map[string]string{"tag": "v1.0"},
		},
		{
			name:   "valid digest",
			refMap: map[string]string{"digest": "sha256:abc123def456"},
		},
		{
			name:   "valid semver",
			refMap: map[string]string{"semver": ">=1.0.0"},
		},
		{
			name:   "multiple valid keys",
			refMap: map[string]string{"tag": "v1.0", "digest": "sha256:abc"},
		},
		{
			name:   "empty map",
			refMap: map[string]string{},
		},
		{
			name:    "invalid key",
			refMap:  map[string]string{"branch": "main"},
			wantErr: true,
		},
		{
			name:    "invalid digest format",
			refMap:  map[string]string{"digest": "md5:abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOCIRef(tt.refMap)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateGitRef(t *testing.T) {
	tests := []struct {
		name    string
		refMap  map[string]string
		wantErr bool
	}{
		{
			name:   "valid branch",
			refMap: map[string]string{"branch": "main"},
		},
		{
			name:   "valid commit",
			refMap: map[string]string{"commit": "abc1234"},
		},
		{
			name:   "valid tag and branch",
			refMap: map[string]string{"tag": "v1.0", "branch": "release"},
		},
		{
			name:   "empty map",
			refMap: map[string]string{},
		},
		{
			name:    "invalid key",
			refMap:  map[string]string{"digest": "sha256:abc"},
			wantErr: true,
		},
		{
			name:    "commit too short",
			refMap:  map[string]string{"commit": "abc"},
			wantErr: true,
		},
		{
			name:    "commit not hex",
			refMap:  map[string]string{"commit": "zzzzzzz"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitRef(tt.refMap)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGenerateOCIRepository(t *testing.T) {
	refMap := map[string]string{"tag": "v1.0", "digest": "sha256:abc123"}
	obj, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", refMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if obj.Name != "my-repo" {
		t.Errorf("expected name %q, got %q", "my-repo", obj.Name)
	}
	if obj.Namespace != "cozy-system" {
		t.Errorf("expected namespace %q, got %q", "cozy-system", obj.Namespace)
	}
	if obj.Spec.URL != "oci://registry.example.com/repo" {
		t.Errorf("expected URL %q, got %q", "oci://registry.example.com/repo", obj.Spec.URL)
	}
	if obj.Spec.Reference == nil {
		t.Fatal("expected Reference to be set")
	}
	if obj.Spec.Reference.Tag != "v1.0" {
		t.Errorf("expected tag %q, got %q", "v1.0", obj.Spec.Reference.Tag)
	}
	if obj.Spec.Reference.Digest != "sha256:abc123" {
		t.Errorf("expected digest %q, got %q", "sha256:abc123", obj.Spec.Reference.Digest)
	}
}

func TestGenerateOCIRepository_NoRef(t *testing.T) {
	obj, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.Reference != nil {
		t.Error("expected Reference to be nil for empty refMap")
	}
}

func TestGenerateOCIRepository_InvalidRef(t *testing.T) {
	_, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", map[string]string{"branch": "main"})
	if err == nil {
		t.Fatal("expected error for invalid OCI ref key, got nil")
	}
}

func TestGenerateGitRepository(t *testing.T) {
	refMap := map[string]string{"branch": "main", "commit": "abc1234def5678"}
	obj, err := generateGitRepository("my-repo", "https://github.com/user/repo", refMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if obj.Name != "my-repo" {
		t.Errorf("expected name %q, got %q", "my-repo", obj.Name)
	}
	if obj.Namespace != "cozy-system" {
		t.Errorf("expected namespace %q, got %q", "cozy-system", obj.Namespace)
	}
	if obj.Spec.URL != "https://github.com/user/repo" {
		t.Errorf("expected URL %q, got %q", "https://github.com/user/repo", obj.Spec.URL)
	}
	if obj.Spec.Reference == nil {
		t.Fatal("expected Reference to be set")
	}
	if obj.Spec.Reference.Branch != "main" {
		t.Errorf("expected branch %q, got %q", "main", obj.Spec.Reference.Branch)
	}
	if obj.Spec.Reference.Commit != "abc1234def5678" {
		t.Errorf("expected commit %q, got %q", "abc1234def5678", obj.Spec.Reference.Commit)
	}
}

func TestGenerateGitRepository_NoRef(t *testing.T) {
	obj, err := generateGitRepository("my-repo", "https://github.com/user/repo", map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.Reference != nil {
		t.Error("expected Reference to be nil for empty refMap")
	}
}

func TestGenerateGitRepository_InvalidRef(t *testing.T) {
	_, err := generateGitRepository("my-repo", "https://github.com/user/repo", map[string]string{"digest": "sha256:abc"})
	if err == nil {
		t.Fatal("expected error for invalid Git ref key, got nil")
	}
}
