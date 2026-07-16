/*
Copyright 2026 The Cozystack Authors.

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

package cacert

import (
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// postgresApplicationDefinitionPath is the SHIPPED postgres ApplicationDefinition,
// relative to this package. The test loads it rather than hand-building a copy so
// that the wiring and the controller cannot drift apart silently: if the caCert
// declaration or the secrets selector in the YAML changes, this test sees it.
var postgresApplicationDefinitionPath = filepath.Join(
	"..", "..", "..", "packages", "system", "postgres-rd", "cozyrds", "postgres.yaml")

func loadPostgresApplicationDefinition(t *testing.T) *cozyv1alpha1.ApplicationDefinition {
	t.Helper()
	raw, err := os.ReadFile(postgresApplicationDefinitionPath)
	if err != nil {
		t.Fatalf("read shipped postgres ApplicationDefinition: %v", err)
	}
	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := yaml.Unmarshal(raw, def); err != nil {
		t.Fatalf("decode shipped postgres ApplicationDefinition: %v", err)
	}
	return def
}

// TestPostgresWiring_EndToEnd is the acceptance test for the first converged
// engine, driving the SHIPPED ApplicationDefinition rather than a fixture.
//
// postgres is the validation engine on purpose: CNPG creates its CA Secret
// itself — <release>-ca, holding ca.crt next to the CA PRIVATE KEY — offers no
// way to label it, and mints it asynchronously. That is the hardest input the
// controller has to handle, so proving it end to end here proves the mechanism
// for the simpler engines too.
//
// Both halves of "a tenant can read ca.crt" are asserted: the controller
// produces the key-free <release>.tenant-ca projection, AND the shipped
// spec.secrets selector matches it — which is what makes the lineage webhook
// stamp tenantresource=true and the tenant able to read it.
func TestPostgresWiring_EndToEnd(t *testing.T) {
	def := loadPostgresApplicationDefinition(t)

	// The declaration the controller depends on must actually be in the shipped
	// YAML, and must name CNPG's operator-created CA Secret.
	if def.Spec.CACert == nil {
		t.Fatal("the shipped postgres ApplicationDefinition declares no caCert; the name-driven leg has nothing to resolve")
	}
	if def.Spec.CACert.SourceSecretName != "{{ .release }}-ca" {
		t.Errorf("caCert.sourceSecretName = %q, want CNPG's CA Secret \"{{ .release }}-ca\"", def.Spec.CACert.SourceSecretName)
	}

	// CNPG's operator-created CA: ca.crt next to the CA private key, no publish
	// label, named after the release ({{ .release }}-ca = postgres-mydb-ca).
	cnpgCA := operatorSecret(testOperatorCA, map[string][]byte{
		caCertKey: []byte(testCA),
		caKeyKey:  []byte(testKey),
	})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), def, cnpgCA,
	).Build()

	mustReconcile(t, c)

	// Half one: the projection exists, holds ca.crt alone, and leaks no key.
	proj := mustProjection(t, c)
	assertKeyFree(t, proj, testCA)

	// Half two: the shipped secrets selector actually selects that projection.
	// Without a matching include, the lineage webhook stamps tenantresource=false
	// and the projection, though correct, is never readable by the tenant.
	if !selectedForTenant(t, def, proj) {
		t.Errorf("the shipped postgres secrets selector does not select the trust anchor %q (labels %v); a tenant could not read ca.crt",
			proj.Name, proj.Labels)
	}

	// The CNPG source is left byte-for-byte as the operator wrote it — key and all.
	src, ok := getSecret(t, c, testOperatorCA)
	if !ok {
		t.Fatal("the operator's CA Secret must be kept")
	}
	if len(src.Data) != 2 || string(src.Data[caKeyKey]) != testKey {
		t.Errorf("the operator's CA Secret must not be modified, got %v", keysOf(src.Data))
	}
}

// selectedForTenant reports whether any spec.secrets include selector in the
// definition matches the projection's labels — the same question the lineage
// webhook answers when it decides the tenantresource verdict.
//
// A resourceNames-only include carries an empty LabelSelector, which
// LabelSelectorAsSelector turns into "matches everything"; that would make the
// check pass vacuously, so empty selectors are skipped and only a real label
// match counts.
func selectedForTenant(t *testing.T, def *cozyv1alpha1.ApplicationDefinition, proj *corev1.Secret) bool {
	t.Helper()
	for _, inc := range def.Spec.Secrets.Include {
		sel, err := metav1.LabelSelectorAsSelector(&inc.LabelSelector)
		if err != nil {
			t.Fatalf("bad include selector in the shipped definition: %v", err)
		}
		if !sel.Empty() && sel.Matches(labels.Set(proj.Labels)) {
			return true
		}
	}
	return false
}
