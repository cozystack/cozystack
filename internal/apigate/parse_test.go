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
	"strings"
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
    served: true
    storage: true
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

// TestParseCRDsSkipsUnservedVersions covers B7(a): a version flipped to
// served:false is not live API surface and must be excluded, so the standard
// CRD deprecation step surfaces later as a removed served version.
func TestParseCRDsSkipsUnservedVersions(t *testing.T) {
	const crd = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.cozystack.io
spec:
  group: example.cozystack.io
  names: {kind: Widget, plural: widgets}
  versions:
  - name: v1alpha1
    served: false
    schema: {openAPIV3Schema: {type: object}}
  - name: v1beta1
    served: true
    schema: {openAPIV3Schema: {type: object}}
`
	rs, err := ParseCRDs("widget.yaml", []byte(crd))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(rs))
	}
	if _, ok := rs[0].Versions["v1alpha1"]; ok {
		t.Fatalf("unserved v1alpha1 must be excluded, got versions %v", rs[0].Versions)
	}
	if _, ok := rs[0].Versions["v1beta1"]; !ok {
		t.Fatalf("served v1beta1 must be present, got versions %v", rs[0].Versions)
	}
}

// TestParseCRDsToleratesTemplatedNeighborDocument reproduces the cozyplane
// bug: a real CRD living in a chart's templates/ dir, guarded by a
// `{{- if }}` block that is not valid YAML on its own. The malformed
// neighbor document must not take the real CRD down with it.
func TestParseCRDsToleratesTemplatedNeighborDocument(t *testing.T) {
	const doc = `{{- /* CRDs are installed only in CRD mode */ -}}
{{- if not .Values.apiserver.enabled }}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: vpcs.sdn.cozystack.io
spec:
  group: sdn.cozystack.io
  names: {kind: VPC, plural: vpcs}
  versions:
  - name: v1alpha1
    served: true
    schema: {openAPIV3Schema: {type: object}}
{{- end }}
`
	rs, err := ParseCRDs("templates/crds.yaml", []byte(doc))
	if err != nil {
		t.Fatalf("ParseCRDs failed: %v", err)
	}
	if len(rs) != 1 || rs[0].Plural != "vpcs" {
		t.Fatalf("expected the CRD sharing the file with templated markup to survive, got %v", rs)
	}
}

// TestParseAPIServices covers the new APIService source: a first-party group
// registration is surfaced as a schema-less resource, and a non-APIService or
// incomplete document is ignored.
func TestParseAPIServices(t *testing.T) {
	const doc = `apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1alpha1.sdn.cozystack.io
spec:
  group: sdn.cozystack.io
  version: v1alpha1
  groupPriorityMinimum: 1000
  versionPriority: 10
---
apiVersion: v1
kind: Service
metadata:
  name: cozyplane-apiserver
`
	rs := ParseAPIServices("apiserver.yaml", []byte(doc))
	if len(rs) != 1 {
		t.Fatalf("expected exactly 1 APIService resource, got %v", rs)
	}
	r := rs[0]
	if r.Group != "sdn.cozystack.io" || r.Source != SourceAPIService || r.Plural != apiServicePlural {
		t.Fatalf("unexpected APIService resource: %+v", r)
	}
	if _, ok := r.Versions["v1alpha1"]; !ok {
		t.Fatalf("expected v1alpha1 in versions, got %v", r.Versions)
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

// TestParseAPIServerStoragesSurfacesUnknownPrefix covers the drift guard: a new
// aggregated group registered under an unlisted *Storage variable must not be
// silently dropped, or it would bypass the gate entirely.
func TestParseAPIServerStoragesSurfacesUnknownPrefix(t *testing.T) {
	src := `newgroupV1alpha1Storage["foos"] = cozyregistry.RESTInPeace(x)`
	rs := ParseAPIServerStorages("apiserver.go", []byte(src))
	if len(rs) != 1 {
		t.Fatalf("expected the unknown-prefix resource to be surfaced, got %v", rs)
	}
	if !strings.HasPrefix(rs[0].Group, unmappedStoragePrefix) {
		t.Fatalf("unknown prefix must surface under the unmapped group, got %q", rs[0].Group)
	}
}

// TestLoadSnapshotDiscoversCRDsUnderChartsCrds covers B6: a first-party CRD that
// lives in a chart's crds/ directory (rather than a definitions/ dir) is still
// discovered.
func TestLoadSnapshotDiscoversCRDsUnderChartsCrds(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("packages/system/foo/charts/foo/crds/example.cozystack.io_widgets.yaml", sampleCRD)

	snap, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap[resourceKey{Group: "example.cozystack.io", Plural: "widgets"}]; !ok {
		t.Fatalf("CRD under charts/*/crds/ was not discovered: %v", snap)
	}
}

// TestLoadSnapshotDiscoversCRDUnderTemplatesDir reproduces the real gap found
// in the cozyplane PR: a raw CRD document guarded by `{{- if }}`, sitting in a
// chart's templates/ dir (not crds/), must still be discovered — directory
// name is no longer the discovery signal, file content is.
func TestLoadSnapshotDiscoversCRDUnderTemplatesDir(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("packages/system/cozyplane/templates/crds.yaml", `{{- if not .Values.apiserver.enabled }}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: vpcs.sdn.cozystack.io
spec:
  group: sdn.cozystack.io
  names: {kind: VPC, plural: vpcs}
  versions:
  - name: v1alpha1
    served: true
    schema: {openAPIV3Schema: {type: object}}
{{- end }}
`)

	snap, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap[resourceKey{Group: "sdn.cozystack.io", Plural: "vpcs"}]; !ok {
		t.Fatalf("CRD under templates/ was not discovered: %v", snap)
	}
}

// TestLoadSnapshotDiscoversAPIServiceAndTripsNewGroup covers the other half of
// the cozyplane gap: a group served only by an aggregated apiserver (no CRD,
// no cozyrd, no apiserver.go entry) — its schema lives entirely outside this
// repo, but the group's first appearance must still require review. Versions
// registered via separate sibling APIService objects (one per version, the
// real-world shape) must merge onto the same resource.
func TestLoadSnapshotDiscoversAPIServiceAndTripsNewGroup(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("packages/system/cozyplane/templates/apiserver-v1alpha1.yaml", `apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata: {name: v1alpha1.sdn.cozystack.io}
spec: {group: sdn.cozystack.io, version: v1alpha1}
`)
	write("packages/system/cozyplane/templates/apiserver-v1beta1.yaml", `apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata: {name: v1beta1.sdn.cozystack.io}
spec: {group: sdn.cozystack.io, version: v1beta1}
`)

	// A minimal but non-empty base, unrelated to sdn.cozystack.io.
	baseRoot := t.TempDir()
	writeBase := func(rel, content string) {
		p := filepath.Join(baseRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeBase("packages/system/postgres-rd/cozyrds/postgres.yaml", sampleCozyRD)

	base, err := LoadSnapshot(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	head, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}

	key := resourceKey{Group: "sdn.cozystack.io", Plural: apiServicePlural}
	res, ok := head[key]
	if !ok {
		t.Fatalf("APIService group was not discovered: %v", head)
	}
	if _, ok := res.Versions["v1alpha1"]; !ok {
		t.Fatalf("expected v1alpha1 merged in, got %v", res.Versions)
	}
	if _, ok := res.Versions["v1beta1"]; !ok {
		t.Fatalf("expected v1beta1 from the sibling APIService object merged in, got %v", res.Versions)
	}

	if countCategory(Classify(base, head), NewGroup) != 1 {
		t.Fatalf("expected the APIService-only group to trip NewGroup, got %v", Classify(base, head))
	}
}

// TestLoadSnapshotIgnoresThirdPartyManifestsUnderTemplates guards against the
// content-based walk becoming too permissive: a vendored CRD and APIService
// for a non-cozystack.io group, sitting in the exact same templates/ shape,
// must still be filtered out.
func TestLoadSnapshotIgnoresThirdPartyManifestsUnderTemplates(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("packages/system/cert-manager/templates/crds.yaml", `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: certificates.cert-manager.io}
spec:
  group: cert-manager.io
  names: {kind: Certificate, plural: certificates}
  versions:
  - name: v1
    served: true
    schema: {openAPIV3Schema: {type: object}}
`)
	write("packages/system/metrics/templates/apiservice.yaml", `apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata: {name: v1beta1.metrics.k8s.io}
spec: {group: metrics.k8s.io, version: v1beta1}
`)
	// Give the checkout a first-party resource so LoadSnapshot's
	// empty-snapshot guard doesn't fire for an unrelated reason.
	write("packages/system/postgres-rd/cozyrds/postgres.yaml", sampleCozyRD)

	snap, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap[resourceKey{Group: "cert-manager.io", Plural: "certificates"}]; ok {
		t.Fatalf("vendored third-party CRD must not be discovered: %v", snap)
	}
	if _, ok := snap[resourceKey{Group: "metrics.k8s.io", Plural: apiServicePlural}]; ok {
		t.Fatalf("vendored third-party APIService must not be discovered: %v", snap)
	}
}

// TestLoadSnapshotEmptyErrors covers the silent-bypass guard: an empty result
// (e.g. wrong directory) must be an error, not a clean pass.
func TestLoadSnapshotEmptyErrors(t *testing.T) {
	if _, err := LoadSnapshot(t.TempDir()); err == nil {
		t.Fatal("expected an error for a directory with no API resources")
	}
}

// TestIsFirstPartyGroup covers the subdomain check: only cozystack.io and its
// dot-delimited subdomains are first-party; a lookalike suffix is not.
func TestIsFirstPartyGroup(t *testing.T) {
	cases := map[string]bool{
		"cozystack.io":                  true,
		"apps.cozystack.io":             true,
		"strategy.backups.cozystack.io": true,
		"fakecozystack.io":              false,
		"cozystack.io.evil.com":         false,
		"cert-manager.io":               false,
	}
	for group, want := range cases {
		if got := isFirstPartyGroup(group); got != want {
			t.Errorf("isFirstPartyGroup(%q) = %v, want %v", group, got, want)
		}
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
