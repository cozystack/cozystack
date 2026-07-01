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

package apigate

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleCozyRD = `apiVersion: cozystack.io/v1alpha1
kind: ApplicationDefinition
metadata:
  name: postgres
spec:
  application:
    kind: Postgres
    singular: postgres
    plural: postgreses
    openAPISchema: |-
      {"type":"object","properties":{"replicas":{"type":"integer","default":2}}}
`

const sampleCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.cozystack.io
spec:
  group: example.cozystack.io
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              size:
                type: string
`

func TestParseCozyRD(t *testing.T) {
	r, ok, err := ParseCozyRD("postgres.yaml", []byte(sampleCozyRD))
	if err != nil || !ok {
		t.Fatalf("ParseCozyRD failed: ok=%v err=%v", ok, err)
	}
	if r.Group != "apps.cozystack.io" || r.Kind != "Postgres" || r.Plural != "postgreses" {
		t.Fatalf("unexpected identity: %+v", r)
	}
	if _, ok := r.Versions["v1alpha1"]; !ok {
		t.Fatalf("expected v1alpha1 schema, got versions %v", r.Versions)
	}
}

func TestParseCRDs(t *testing.T) {
	rs, err := ParseCRDs("widget.yaml", []byte(sampleCRD))
	if err != nil {
		t.Fatalf("ParseCRDs failed: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("expected 1 CRD resource, got %d", len(rs))
	}
	r := rs[0]
	if r.Group != "example.cozystack.io" || r.Kind != "Widget" || r.Plural != "widgets" {
		t.Fatalf("unexpected identity: %+v", r)
	}
	if _, ok := r.Versions["v1alpha1"]; !ok {
		t.Fatalf("expected v1alpha1 schema")
	}
}

func TestParseAPIServerStorages(t *testing.T) {
	src := `
	coreV1alpha1Storage["tenantsecrets"] = cozyregistry.RESTInPeace(x)
	sdnV1alpha1Storage["securitygroups"] = cozyregistry.RESTInPeace(y)
	appsV1alpha1Storage[resConfig.Application.Plural] = cozyregistry.RESTInPeace(z)
	`
	rs := ParseAPIServerStorages("apiserver.go", []byte(src))
	got := map[string]string{}
	for _, r := range rs {
		got[r.Plural] = r.Group
	}
	if got["tenantsecrets"] != "core.cozystack.io" {
		t.Fatalf("expected core tenantsecrets, got %v", got)
	}
	if got["securitygroups"] != "sdn.cozystack.io" {
		t.Fatalf("expected sdn securitygroups, got %v", got)
	}
	if _, leaked := got["Plural"]; leaked {
		t.Fatalf("apps dynamic registration must not be captured: %v", got)
	}
}

// TestLoadSnapshotEndToEnd builds two minimal checkouts on disk and verifies
// the loader + classifier wire together across all three sources.
func TestLoadSnapshotEndToEnd(t *testing.T) {
	base := t.TempDir()
	head := t.TempDir()

	write := func(root, rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Base has just Postgres.
	write(base, "packages/system/postgres-rd/cozyrds/postgres.yaml", sampleCozyRD)
	// Head keeps Postgres and adds a brand-new CRD group.
	write(head, "packages/system/postgres-rd/cozyrds/postgres.yaml", sampleCozyRD)
	write(head, "internal/crdinstall/manifests/example.yaml", sampleCRD)

	baseSnap, err := LoadSnapshot(base)
	if err != nil {
		t.Fatal(err)
	}
	headSnap, err := LoadSnapshot(head)
	if err != nil {
		t.Fatal(err)
	}
	findings := Classify(baseSnap, headSnap)
	if countCategory(findings, NewGroup) != 1 {
		t.Fatalf("expected 1 new-group finding, got %v", findings)
	}
}
