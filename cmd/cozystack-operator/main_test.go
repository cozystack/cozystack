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
	"strings"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	expectedVariants := []string{"default", "isp-full", "isp-hosted", "isp-full-generic", "isp-slim", "isp-slim-generic", "isp-hosted-slim"}
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
	k8sClient := fake.NewClientBuilder().WithScheme(s).Build()
	ctx := context.Background()

	// The first apply establishes the operator as the owner of the object.
	if err := installPlatformPackageSource(ctx, k8sClient, "cozystack-platform", "OCIRepository"); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Re-applying as the same owner is a legitimate update, so it must not
	// conflict even though Force is no longer set. (A conflict would only arise
	// against a different manager, which the refusal tests cover.)
	if err := installPlatformPackageSource(ctx, k8sClient, "cozystack-platform", "OCIRepository"); err != nil {
		t.Fatalf("re-apply for the operator's own object must not conflict: %v", err)
	}

	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "cozystack.cozystack-platform"}, ps); err != nil {
		t.Fatalf("PackageSource not found: %v", err)
	}
	if ps.Spec.SourceRef.Name != "cozystack-platform" {
		t.Errorf("expected sourceRef.name %q, got %q", "cozystack-platform", ps.Spec.SourceRef.Name)
	}
	if len(ps.Spec.Variants) != 7 {
		t.Errorf("expected 7 variants, got %d", len(ps.Spec.Variants))
	}
}

// TestInstallPlatformPackageSource_RefusesTapOwned asserts that when a
// marketplace tap has materialized a PackageSource under a name the platform
// later ships, the platform refuses to overwrite it, names the conflict, and
// leaves the object untouched.
func TestInstallPlatformPackageSource_RefusesTapOwned(t *testing.T) {
	s := newTestScheme()
	tapOwned := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cozystack.cozystack-platform",
			Labels:      map[string]string{tapconst.Label: "true"},
			Annotations: map[string]string{tapconst.SourceAnnotation: "tap-acme-platform"},
		},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "tap-acme-platform", Namespace: "cozy-system"},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(s).WithObjects(tapOwned).Build()

	err := installPlatformPackageSource(context.Background(), k8sClient, "cozystack-platform", "OCIRepository")
	if err == nil || !strings.Contains(err.Error(), "owned by a marketplace tap") {
		t.Fatalf("expected refusal naming the tap owner, got %v", err)
	}
	// The tapped object is left exactly as it was.
	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cozystack.cozystack-platform"}, ps); err != nil {
		t.Fatal(err)
	}
	if ps.Spec.SourceRef.Name != "tap-acme-platform" {
		t.Errorf("the tapped PackageSource must be left untouched, sourceRef=%q", ps.Spec.SourceRef.Name)
	}
}

// TestInstallPlatformPackageSource_RefusesForeignOwner asserts that a
// PackageSource owned by any other field manager (no tap label) is not
// force-overwritten: the server-side apply conflict surfaces as an error and the
// object is left untouched.
func TestInstallPlatformPackageSource_RefusesForeignOwner(t *testing.T) {
	s := newTestScheme()
	// WithObjects seeds an object owned by a foreign manager (before-first-apply)
	// with a conflicting sourceRef and no tap label.
	foreign := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack.cozystack-platform"},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: "old-name", Namespace: "cozy-system"},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(s).WithObjects(foreign).Build()

	err := installPlatformPackageSource(context.Background(), k8sClient, "cozystack-platform", "OCIRepository")
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected a field-ownership conflict error, got %v", err)
	}
	ps := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "cozystack.cozystack-platform"}, ps); err != nil {
		t.Fatal(err)
	}
	if ps.Spec.SourceRef.Name != "old-name" {
		t.Errorf("a foreign-owned PackageSource must be left untouched, sourceRef=%q", ps.Spec.SourceRef.Name)
	}
}

func TestParsePlatformSourceURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantType string
		wantURL  string
		wantErr  bool
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
		"isp-slim":         {"values.yaml", "values-isp-slim.yaml"},
		"isp-slim-generic": {"values.yaml", "values-isp-slim-generic.yaml"},
		"isp-hosted-slim":  {"values.yaml", "values-isp-hosted-slim.yaml"},
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
	obj, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", refMap, "", "")
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
	if obj.Spec.SecretRef != nil {
		t.Errorf("expected SecretRef to be nil when secretName is empty, got %+v", obj.Spec.SecretRef)
	}
	if obj.Spec.CertSecretRef != nil {
		t.Errorf("expected CertSecretRef to be nil when certSecretName is empty, got %+v", obj.Spec.CertSecretRef)
	}
}

func TestGenerateOCIRepository_NoRef(t *testing.T) {
	obj, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", map[string]string{}, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.Reference != nil {
		t.Error("expected Reference to be nil for empty refMap")
	}
}

func TestGenerateOCIRepository_InvalidRef(t *testing.T) {
	_, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", map[string]string{"branch": "main"}, "", "")
	if err == nil {
		t.Fatal("expected error for invalid OCI ref key, got nil")
	}
}

func TestGenerateOCIRepository_WithSecret(t *testing.T) {
	obj, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", map[string]string{"tag": "v1.0"}, "my-registry-creds", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.SecretRef == nil {
		t.Fatal("expected SecretRef to be set when secretName is non-empty")
	}
	if obj.Spec.SecretRef.Name != "my-registry-creds" {
		t.Errorf("expected SecretRef.Name %q, got %q", "my-registry-creds", obj.Spec.SecretRef.Name)
	}
}

func TestGenerateOCIRepository_WithCertSecret(t *testing.T) {
	obj, err := generateOCIRepository("my-repo", "oci://registry.example.com/repo", map[string]string{"tag": "v1.0"}, "", "registry-ca")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.CertSecretRef == nil {
		t.Fatal("expected CertSecretRef to be set when certSecretName is non-empty")
	}
	if obj.Spec.CertSecretRef.Name != "registry-ca" {
		t.Errorf("expected CertSecretRef.Name %q, got %q", "registry-ca", obj.Spec.CertSecretRef.Name)
	}
	if obj.Spec.SecretRef != nil {
		t.Errorf("expected SecretRef to stay nil when only certSecretName is set, got %+v", obj.Spec.SecretRef)
	}
}

func newSourceTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := newTestScheme()
	if err := sourcev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add source scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// The operator rewrites the whole platform OCIRepository on every start, so a
// certSecretRef patched onto the live object by hand is lost on the next
// restart and source-controller then fails TLS against a registry behind a
// private CA. The CA has to come from the operator's own arguments and survive
// that update path, including a rename of the Secret when the CA is rotated.
func TestInstallPlatformSourceResource_UpdateKeepsCertSecret(t *testing.T) {
	existing := &sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack-platform", Namespace: "cozy-system"},
		Spec:       sourcev1.OCIRepositorySpec{URL: "oci://ghcr.io/cozystack/cozystack/cozystack-packages"},
	}
	k8sClient := newSourceTestClient(t, existing)

	for _, certSecret := range []string{"registry-ca", "registry-ca-next"} {
		if err := installPlatformSourceResource(context.Background(), k8sClient,
			"oci://registry.internal:8083/cozystack/cozystack/cozystack-packages", "cozystack-platform",
			"digest=sha256:abc123", "registry-creds", certSecret); err != nil {
			t.Fatalf("installPlatformSourceResource: %v", err)
		}
	}

	got := &sourcev1.OCIRepository{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "cozy-system", Name: "cozystack-platform"}, got); err != nil {
		t.Fatalf("get OCIRepository: %v", err)
	}
	if got.Spec.CertSecretRef == nil || got.Spec.CertSecretRef.Name != "registry-ca-next" {
		t.Errorf("expected CertSecretRef registry-ca-next after update, got %+v", got.Spec.CertSecretRef)
	}
	if got.Spec.SecretRef == nil || got.Spec.SecretRef.Name != "registry-creds" {
		t.Errorf("expected SecretRef registry-creds after update, got %+v", got.Spec.SecretRef)
	}
	if got.Spec.URL != "oci://registry.internal:8083/cozystack/cozystack/cozystack-packages" {
		t.Errorf("expected URL to be repointed, got %q", got.Spec.URL)
	}
}

// GitRepository has no certSecretRef: Flux reads a Git CA from the ca.crt key
// of the secretRef Secret. Accepting the flag for a Git source would silently
// drop the CA, so it must fail before anything is written.
func TestInstallPlatformSourceResource_GitRejectsCertSecret(t *testing.T) {
	k8sClient := newSourceTestClient(t)

	err := installPlatformSourceResource(context.Background(), k8sClient,
		"https://git.internal/cozystack/cozystack", "cozystack-platform", "branch=main", "git-creds", "git-ca")
	if err == nil {
		t.Fatal("expected an error for a cert secret on a Git platform source, got nil")
	}
	if !strings.Contains(err.Error(), "ca.crt") {
		t.Errorf("expected the error to point at the ca.crt key of the source secret, got: %v", err)
	}

	list := &sourcev1.GitRepositoryList{}
	if err := k8sClient.List(context.Background(), list); err != nil {
		t.Fatalf("list GitRepositories: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected no GitRepository to be written, got %d", len(list.Items))
	}
}

// The Git guard runs on every start; one that misfires without a cert secret
// would crash-loop the operator for every Git platform source.
func TestInstallPlatformSourceResource_GitWithoutCertSecret(t *testing.T) {
	for _, sourceURL := range []string{
		"https://git.internal/cozystack/cozystack",
		"ssh://git@git.internal/cozystack/cozystack",
	} {
		t.Run(sourceURL, func(t *testing.T) {
			k8sClient := newSourceTestClient(t)

			if err := installPlatformSourceResource(context.Background(), k8sClient,
				sourceURL, "cozystack-platform", "branch=main", "git-creds", ""); err != nil {
				t.Fatalf("installPlatformSourceResource: %v", err)
			}

			got := &sourcev1.GitRepository{}
			if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "cozy-system", Name: "cozystack-platform"}, got); err != nil {
				t.Fatalf("get GitRepository: %v", err)
			}
			if got.Spec.URL != sourceURL {
				t.Errorf("expected URL %q, got %q", sourceURL, got.Spec.URL)
			}
			if got.Spec.SecretRef == nil || got.Spec.SecretRef.Name != "git-creds" {
				t.Errorf("expected SecretRef git-creds, got %+v", got.Spec.SecretRef)
			}
		})
	}
}

// Checked right after flag parsing: CRDs and Flux are installed before the
// platform source, so a combination rejected only there would exit after a
// partial install.
func TestValidatePlatformSourceCertSecret(t *testing.T) {
	tests := []struct {
		name       string
		sourceURL  string
		certSecret string
		wantErr    bool
	}{
		{name: "oci with cert secret", sourceURL: "oci://registry.internal:8083/cozystack-packages", certSecret: "registry-ca"},
		{name: "oci without cert secret", sourceURL: "oci://registry.internal:8083/cozystack-packages"},
		{name: "https git without cert secret", sourceURL: "https://git.internal/cozystack/cozystack"},
		{name: "ssh git without cert secret", sourceURL: "ssh://git@git.internal/cozystack/cozystack"},
		{name: "no source url", certSecret: "registry-ca"},
		{name: "https git with cert secret", sourceURL: "https://git.internal/cozystack/cozystack", certSecret: "git-ca", wantErr: true},
		{name: "ssh git with cert secret", sourceURL: "ssh://git@git.internal/cozystack/cozystack", certSecret: "git-ca", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePlatformSourceCertSecret(tt.sourceURL, tt.certSecret)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePlatformSourceCertSecret(%q, %q) error = %v, wantErr %v", tt.sourceURL, tt.certSecret, err, tt.wantErr)
			}
		})
	}
}

func TestGenerateGitRepository(t *testing.T) {
	refMap := map[string]string{"branch": "main", "commit": "abc1234def5678"}
	obj, err := generateGitRepository("my-repo", "https://github.com/user/repo", refMap, "")
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
	if obj.Spec.SecretRef != nil {
		t.Errorf("expected SecretRef to be nil when secretName is empty, got %+v", obj.Spec.SecretRef)
	}
}

func TestGenerateGitRepository_NoRef(t *testing.T) {
	obj, err := generateGitRepository("my-repo", "https://github.com/user/repo", map[string]string{}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.Reference != nil {
		t.Error("expected Reference to be nil for empty refMap")
	}
}

func TestGenerateGitRepository_InvalidRef(t *testing.T) {
	_, err := generateGitRepository("my-repo", "https://github.com/user/repo", map[string]string{"digest": "sha256:abc"}, "")
	if err == nil {
		t.Fatal("expected error for invalid Git ref key, got nil")
	}
}

func TestGenerateGitRepository_WithSecret(t *testing.T) {
	obj, err := generateGitRepository("my-repo", "https://github.com/user/repo", map[string]string{"branch": "main"}, "my-git-creds")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Spec.SecretRef == nil {
		t.Fatal("expected SecretRef to be set when secretName is non-empty")
	}
	if obj.Spec.SecretRef.Name != "my-git-creds" {
		t.Errorf("expected SecretRef.Name %q, got %q", "my-git-creds", obj.Spec.SecretRef.Name)
	}
}

// The system namespace memory flags are the only ones on this binary whose bad values do
// not surface here at all: a limit and a request that parse but contradict each other
// produce a LimitRange the API server rejects, once per system namespace, on every Package
// reconcile, with nothing in the error pointing back at a flag. Each branch below is a way
// that has happened or could.
func TestParseSystemNamespaceMemory(t *testing.T) {
	tests := []struct {
		name        string
		rawLimit    string
		rawRequest  string
		wantErr     bool
		wantLimit   string
		wantRequest string
	}{
		{
			// Inputs follow the constants registered as flag defaults; literal
			// expectations pin the shipped contract so changing either constant
			// requires an intentional test update.
			name:        "defaults",
			rawLimit:    DefaultSystemNamespaceMemoryLimit,
			rawRequest:  DefaultSystemNamespaceMemoryRequest,
			wantLimit:   "32Gi",
			wantRequest: "32Mi",
		},
		{
			// Empty is the documented off switch, not a missing value: the chart
			// leaves both keys empty unless an operator sets them.
			name:        "empty disables both",
			rawLimit:    "",
			rawRequest:  "",
			wantLimit:   "0",
			wantRequest: "0",
		},
		{
			// An explicit 0 limit is the other way to disable, and it must not
			// trip the pair check against a request that is still set.
			name:        "zero limit disables and ignores the request",
			rawLimit:    "0",
			rawRequest:  "32Mi",
			wantLimit:   "0",
			wantRequest: "32Mi",
		},
		{
			// Supported on purpose: the container still gets memory.max, the
			// scheduler reserves nothing. See the flag help.
			name:        "zero request against a real limit is accepted",
			rawLimit:    "4Gi",
			rawRequest:  "0",
			wantLimit:   "4Gi",
			wantRequest: "0",
		},
		{
			name:     "malformed limit",
			rawLimit: "4GB",
			wantErr:  true,
		},
		{
			name:       "malformed request",
			rawLimit:   "4Gi",
			rawRequest: "thirty-two",
			wantErr:    true,
		},
		{
			// Parses as a Quantity and is meaningless as memory.
			name:     "negative limit",
			rawLimit: "-1Gi",
			wantErr:  true,
		},
		{
			name:       "negative request",
			rawLimit:   "4Gi",
			rawRequest: "-32Mi",
			wantErr:    true,
		},
		{
			// The one the API server would otherwise reject per namespace, per
			// reconcile, forever.
			name:       "request above limit",
			rawLimit:   "1Gi",
			rawRequest: "2Gi",
			wantErr:    true,
		},
		{
			// Equal is the boundary and is legal: defaultRequest may equal default.
			name:        "request equal to limit",
			rawLimit:    "1Gi",
			rawRequest:  "1Gi",
			wantLimit:   "1Gi",
			wantRequest: "1Gi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, request, err := parseSystemNamespaceMemory(tt.rawLimit, tt.rawRequest)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSystemNamespaceMemory(%q, %q) = %s/%s, want an error",
						tt.rawLimit, tt.rawRequest, limit.String(), request.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSystemNamespaceMemory(%q, %q): %v", tt.rawLimit, tt.rawRequest, err)
			}
			if want := resource.MustParse(tt.wantLimit); limit.Cmp(want) != 0 {
				t.Errorf("limit = %s, want %s", limit.String(), want.String())
			}
			if want := resource.MustParse(tt.wantRequest); request.Cmp(want) != 0 {
				t.Errorf("request = %s, want %s", request.String(), want.String())
			}
		})
	}
}
