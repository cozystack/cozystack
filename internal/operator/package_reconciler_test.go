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

package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestParseCRDPolicy(t *testing.T) {
	tests := []struct {
		name    string
		install *cozyv1alpha1.ComponentInstall
		want    helmv2.CRDsPolicy
	}{
		{
			name:    "nil install leaves flux default",
			install: nil,
			want:    "",
		},
		{
			name:    "empty upgradeCRDs leaves flux default",
			install: &cozyv1alpha1.ComponentInstall{},
			want:    "",
		},
		{
			name:    "Skip is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Skip"},
			want:    helmv2.Skip,
		},
		{
			name:    "Create is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Create"},
			want:    helmv2.Create,
		},
		{
			name:    "CreateReplace is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "CreateReplace"},
			want:    helmv2.CreateReplace,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCRDPolicy(tc.install)
			if got != tc.want {
				t.Errorf("parseCRDPolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPackageSourceCRDHasUpgradeCRDsEnum guards the generated CRD schema: the
// invalid-value case from the spec is enforced at the API server via a
// kubebuilder enum marker, not in the reconciler. If someone drops the marker
// and forgets to regenerate, this test catches it.
func TestPackageSourceCRDHasUpgradeCRDsEnum(t *testing.T) {
	path := filepath.Join("..", "crdinstall", "manifests", "cozystack.io_packagesources.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}

	var field *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}
		variants, ok := spec.Properties["variants"]
		if !ok || variants.Items == nil || variants.Items.Schema == nil {
			continue
		}
		components, ok := variants.Items.Schema.Properties["components"]
		if !ok || components.Items == nil || components.Items.Schema == nil {
			continue
		}
		install, ok := components.Items.Schema.Properties["install"]
		if !ok {
			continue
		}
		f, ok := install.Properties["upgradeCRDs"]
		if !ok {
			continue
		}
		field = &f
		break
	}

	if field == nil {
		t.Fatal("upgradeCRDs field not found in PackageSource CRD schema")
	}

	got := map[string]bool{}
	for _, e := range field.Enum {
		var s string
		if err := json.Unmarshal(e.Raw, &s); err != nil {
			t.Fatalf("unmarshal enum value %q: %v", e.Raw, err)
		}
		got[s] = true
	}

	for _, want := range []string{"Skip", "Create", "CreateReplace"} {
		if !got[want] {
			t.Errorf("enum value %q missing from upgradeCRDs; got %v", want, got)
		}
	}
}
