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
	"time"

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

func TestBuildHelmReleaseSpec(t *testing.T) {
	r := &PackageReconciler{
		HelmReleaseInterval:       42 * time.Second,
		HelmReleaseRetryInterval:  17 * time.Second,
		HelmReleaseInstallTimeout: 11 * time.Minute,
		HelmReleaseUpgradeTimeout: 13 * time.Minute,
		HelmReleaseMaxHistory:     7,
	}
	componentInstall := &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Skip"}

	spec := r.buildHelmReleaseSpec(componentInstall, "ps-variant-component")

	if spec.Interval.Duration != 42*time.Second {
		t.Errorf("Interval = %v, want 42s", spec.Interval.Duration)
	}
	if spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil, want pointer to 7")
	}
	if *spec.MaxHistory != 7 {
		t.Errorf("MaxHistory = %d, want 7", *spec.MaxHistory)
	}

	if spec.ChartRef == nil {
		t.Fatal("ChartRef is nil")
	}
	if spec.ChartRef.Kind != "ExternalArtifact" {
		t.Errorf("ChartRef.Kind = %q, want ExternalArtifact", spec.ChartRef.Kind)
	}
	if spec.ChartRef.Name != "ps-variant-component" {
		t.Errorf("ChartRef.Name = %q, want ps-variant-component", spec.ChartRef.Name)
	}
	if spec.ChartRef.Namespace != "cozy-system" {
		t.Errorf("ChartRef.Namespace = %q, want cozy-system", spec.ChartRef.Namespace)
	}

	if spec.Install == nil {
		t.Fatal("Install is nil")
	}
	if spec.Install.Timeout == nil || spec.Install.Timeout.Duration != 11*time.Minute {
		t.Errorf("Install.Timeout = %v, want 11m", spec.Install.Timeout)
	}
	if spec.Install.Strategy == nil {
		t.Fatal("Install.Strategy is nil")
	}
	if spec.Install.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Install.Strategy.Name = %q, want %q", spec.Install.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if spec.Install.Strategy.RetryInterval == nil || spec.Install.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Install.Strategy.RetryInterval = %v, want 17s", spec.Install.Strategy.RetryInterval)
	}

	if spec.Upgrade == nil {
		t.Fatal("Upgrade is nil")
	}
	if spec.Upgrade.Timeout == nil || spec.Upgrade.Timeout.Duration != 13*time.Minute {
		t.Errorf("Upgrade.Timeout = %v, want 13m", spec.Upgrade.Timeout)
	}
	if spec.Upgrade.Strategy == nil {
		t.Fatal("Upgrade.Strategy is nil")
	}
	if spec.Upgrade.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Upgrade.Strategy.Name = %q, want %q", spec.Upgrade.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if spec.Upgrade.Strategy.RetryInterval == nil || spec.Upgrade.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Upgrade.Strategy.RetryInterval = %v, want 17s", spec.Upgrade.Strategy.RetryInterval)
	}
	if spec.Upgrade.CRDs != helmv2.Skip {
		t.Errorf("Upgrade.CRDs = %q, want Skip", spec.Upgrade.CRDs)
	}
}

// TestBuildHelmReleaseSpecZeroMaxHistory pins that MaxHistory=0 (unlimited
// history per Helm semantics) survives the spec build — i.e. is set as a
// non-nil pointer to 0 rather than dropped or replaced with a default.
func TestBuildHelmReleaseSpecZeroMaxHistory(t *testing.T) {
	r := &PackageReconciler{HelmReleaseMaxHistory: 0}
	spec := r.buildHelmReleaseSpec(nil, "x")
	if spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil for HelmReleaseMaxHistory=0; want pointer to 0")
	}
	if *spec.MaxHistory != 0 {
		t.Errorf("MaxHistory = %d, want 0", *spec.MaxHistory)
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
