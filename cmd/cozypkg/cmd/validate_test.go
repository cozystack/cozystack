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

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// validRepo builds a minimal but complete External-Apps repository:
// one PackageSource with a default variant that has a library, an app
// component that uses the library, and a paired -rd component whose chart
// carries a matching ApplicationDefinition.
func validRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	writeFile(t, root, "packages/core/platform/sources/foo.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.foo-application
spec:
  sourceRef:
    kind: OCIRepository
    name: example-packages
    namespace: cozy-system
    path: /
  variants:
    - name: default
      dependsOn:
        - cozystack.networking
      libraries:
        - name: cozy-lib
          path: library/cozy-lib
      components:
        - name: foo
          path: apps/foo
          libraries: ["cozy-lib"]
        - name: foo-rd
          path: system/foo-rd
          install:
            namespace: cozy-system
`)

	writeFile(t, root, "packages/apps/foo/Chart.yaml", "apiVersion: v2\nname: foo\nversion: 0.1.0\n")
	writeFile(t, root, "packages/system/foo-rd/Chart.yaml", "apiVersion: v2\nname: foo-rd\nversion: 0.1.0\n")
	writeFile(t, root, "packages/library/cozy-lib/Chart.yaml", "apiVersion: v2\nname: cozy-lib\ntype: library\nversion: 0.1.0\n")

	writeFile(t, root, "packages/system/foo-rd/cozyrds/foo.yaml", `apiVersion: cozystack.io/v1alpha1
kind: ApplicationDefinition
metadata:
  name: foo
spec:
  application:
    kind: Foo
    openAPISchema: ""
    plural: foos
    singular: foo
  release:
    chartRef:
      kind: ExternalArtifact
      name: example-foo-application-default-foo
      namespace: cozy-system
    prefix: foo-
`)

	return root
}

func codeCounts(r *Report) map[string]int {
	m := map[string]int{}
	for _, f := range r.Findings {
		m[f.Code]++
	}
	return m
}

func TestValidateRepo_Valid(t *testing.T) {
	root := validRepo(t)
	rep, err := ValidateRepo(ValidateOptions{RepoRoot: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("expected no errors, got findings: %+v", rep.Findings)
	}
	codes := codeCounts(rep)
	// cozystack.networking dependsOn resolves to a shipped-source info, not a warning.
	if codes["dependson-shipped"] != 1 {
		t.Errorf("expected 1 dependson-shipped info, got %d (%+v)", codes["dependson-shipped"], rep.Findings)
	}
	if codes["dependson-unresolved"] != 0 {
		t.Errorf("expected no unresolved dependsOn, got %d", codes["dependson-unresolved"])
	}
	if codes["appdef-dangling"] != 0 {
		t.Errorf("expected no dangling appdef, got %d", codes["appdef-dangling"])
	}
}

func TestValidateRepo_NotADirectory(t *testing.T) {
	_, err := ValidateRepo(ValidateOptions{RepoRoot: filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestValidateRepo_NoPackageSource(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "packages/apps/foo/Chart.yaml", "apiVersion: v2\nname: foo\nversion: 0.1.0\n")
	rep, err := ValidateRepo(ValidateOptions{RepoRoot: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if codeCounts(rep)["no-packagesource"] != 1 {
		t.Fatalf("expected no-packagesource error, got %+v", rep.Findings)
	}
}

func TestValidateRepo_MissingComponentChart(t *testing.T) {
	root := validRepo(t)
	if err := os.RemoveAll(filepath.Join(root, "packages/apps/foo")); err != nil {
		t.Fatal(err)
	}
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if !rep.HasErrors() {
		t.Fatal("expected errors after removing component chart")
	}
	if codeCounts(rep)["comp-missing"] != 1 {
		t.Fatalf("expected comp-missing error, got %+v", rep.Findings)
	}
}

func TestValidateRepo_ChartYAMLMissing(t *testing.T) {
	root := validRepo(t)
	if err := os.Remove(filepath.Join(root, "packages/apps/foo/Chart.yaml")); err != nil {
		t.Fatal(err)
	}
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if codeCounts(rep)["chart-yaml"] != 1 {
		t.Fatalf("expected chart-yaml error, got %+v", rep.Findings)
	}
}

func TestValidateRepo_Privileged(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.priv
spec:
  variants:
    - name: default
      components:
        - name: risky
          path: apps/risky
          install:
            privileged: true
`)
	writeFile(t, root, "packages/apps/risky/Chart.yaml", "apiVersion: v2\nname: risky\nversion: 0.1.0\n")
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if rep.HasErrors() {
		t.Fatalf("privileged alone must not be an error: %+v", rep.Findings)
	}
	if codeCounts(rep)["privileged"] != 1 {
		t.Fatalf("expected privileged warning, got %+v", rep.Findings)
	}
}

func TestValidateRepo_DependsOnUnresolvedVsKnown(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.app
spec:
  variants:
    - name: default
      dependsOn:
        - third-party.thing
      components:
        - name: app
          path: apps/app
`)
	writeFile(t, root, "packages/apps/app/Chart.yaml", "apiVersion: v2\nname: app\nversion: 0.1.0\n")

	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if codeCounts(rep)["dependson-unresolved"] != 1 {
		t.Fatalf("expected unresolved dependsOn warning, got %+v", rep.Findings)
	}

	repKnown, _ := ValidateRepo(ValidateOptions{RepoRoot: root, KnownSources: []string{"third-party.thing"}})
	if codeCounts(repKnown)["dependson-unresolved"] != 0 {
		t.Fatalf("known-source should silence dependsOn warning, got %+v", repKnown.Findings)
	}
}

func TestValidateRepo_DanglingAppDefRef(t *testing.T) {
	root := validRepo(t)
	// Point the ApplicationDefinition at a non-existent component artifact.
	writeFile(t, root, "packages/system/foo-rd/cozyrds/foo.yaml", `apiVersion: cozystack.io/v1alpha1
kind: ApplicationDefinition
metadata:
  name: foo
spec:
  application:
    kind: Foo
    openAPISchema: ""
    plural: foos
    singular: foo
  release:
    chartRef:
      kind: ExternalArtifact
      name: example-foo-application-default-nonexistent
      namespace: cozy-system
    prefix: foo-
`)
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if codeCounts(rep)["appdef-dangling"] != 1 {
		t.Fatalf("expected dangling appdef warning, got %+v", rep.Findings)
	}
}

func TestValidateRepo_StrictSchemaRejectsUnknownField(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.bad
spec:
  bogusUnknownField: true
  variants:
    - name: default
      components:
        - name: app
          path: apps/app
`)
	writeFile(t, root, "packages/apps/app/Chart.yaml", "apiVersion: v2\nname: app\nversion: 0.1.0\n")
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if codeCounts(rep)["schema"] != 1 {
		t.Fatalf("expected schema error for unknown field, got %+v", rep.Findings)
	}
}

func TestValidateRepo_UndeclaredComponentLibrary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.app
spec:
  variants:
    - name: default
      components:
        - name: app
          path: apps/app
          libraries: ["missing-lib"]
`)
	writeFile(t, root, "packages/apps/app/Chart.yaml", "apiVersion: v2\nname: app\nversion: 0.1.0\n")
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if codeCounts(rep)["comp-lib-undeclared"] != 1 {
		t.Fatalf("expected comp-lib-undeclared error, got %+v", rep.Findings)
	}
}

func TestValidateRepo_BlockScalarNotFractured(t *testing.T) {
	root := t.TempDir()
	// The annotation's block scalar contains a separator-looking line; a naive
	// split would fracture the document and fail the strict decode.
	writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.app
  annotations:
    note: |
      line one
      ---
      line two
spec:
  variants:
    - name: default
      components:
        - name: app
          path: apps/app
`)
	writeFile(t, root, "packages/apps/app/Chart.yaml", "apiVersion: v2\nname: app\nversion: 0.1.0\n")
	rep, err := ValidateRepo(ValidateOptions{RepoRoot: root})
	if err != nil {
		t.Fatalf("ValidateRepo: %v", err)
	}
	if codeCounts(rep)["schema"] != 0 || codeCounts(rep)["no-packagesource"] != 0 {
		t.Fatalf("block scalar fractured the document: %+v", rep.Findings)
	}
}

func TestValidateRepo_ReservedNamePrefix(t *testing.T) {
	for _, name := range []string{"cozystack.evil", "community.acme.demo"} {
		root := t.TempDir()
		writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: `+name+`
spec:
  variants:
    - name: default
      components:
        - name: app
          path: apps/app
`)
		writeFile(t, root, "packages/apps/app/Chart.yaml", "apiVersion: v2\nname: app\nversion: 0.1.0\n")

		rep, err := ValidateRepo(ValidateOptions{RepoRoot: root})
		if err != nil {
			t.Fatalf("ValidateRepo(%s): %v", name, err)
		}
		if codeCounts(rep)["ps-name-reserved"] != 1 {
			t.Errorf("expected ps-name-reserved for %q, got %+v", name, rep.Findings)
		}
		// The escape hatch clears it (caller-side only; the gate never sets it).
		rep2, _ := ValidateRepo(ValidateOptions{RepoRoot: root, AllowReservedNames: true})
		if codeCounts(rep2)["ps-name-reserved"] != 0 {
			t.Errorf("AllowReservedNames should clear the finding for %q", name)
		}
	}
}

func TestValidateRepo_ArtifactNameCollision(t *testing.T) {
	root := t.TempDir()
	// variant "v1.0"/comp "c" and variant "v1"/comp "0-c" both normalise to
	// <ps>-v1-0-c after dot-to-dash.
	writeFile(t, root, "packages/core/platform/sources/p.yaml", `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.app
spec:
  variants:
    - name: "v1.0"
      components:
        - name: c
          path: apps/c
    - name: v1
      components:
        - name: 0-c
          path: apps/0-c
`)
	writeFile(t, root, "packages/apps/c/Chart.yaml", "apiVersion: v2\nname: c\nversion: 0.1.0\n")
	writeFile(t, root, "packages/apps/0-c/Chart.yaml", "apiVersion: v2\nname: c\nversion: 0.1.0\n")
	rep, _ := ValidateRepo(ValidateOptions{RepoRoot: root})
	if codeCounts(rep)["artifact-name-collision"] != 1 {
		t.Fatalf("expected an artifact-name-collision error, got %+v", rep.Findings)
	}
}

func TestArtifactName(t *testing.T) {
	got := artifactName("cozystack.postgres-application", "default", "postgres")
	want := "cozystack-postgres-application-default-postgres"
	if got != want {
		t.Fatalf("artifactName = %q, want %q", got, want)
	}
}

func TestSplitYAMLDocuments(t *testing.T) {
	docs := splitYAMLDocuments([]byte("---\na: 1\n---\nb: 2\n---\n"))
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
}

func TestVerifyCosignSignatureGuards(t *testing.T) {
	// Errors regardless of whether cosign is installed: absent -> missing-binary
	// error; present -> missing identity/issuer error. Either way, no silent pass.
	if _, err := verifyCosignSignature("oci://ghcr.io/foo/bar:v1", "", ""); err == nil {
		t.Fatal("expected an error when identity/issuer are missing or cosign is absent")
	}
}

func TestParseVerifiedDigest(t *testing.T) {
	a := "sha256:" + strings.Repeat("a", 64)
	b := "sha256:" + strings.Repeat("b", 64)
	if got := parseVerifiedDigest([]byte(`[{"critical":{"image":{"docker-manifest-digest":"` + a + `"}}}]`)); got != a {
		t.Errorf("parseVerifiedDigest = %q", got)
	}
	// Fallback: digest present anywhere in non-JSON output.
	if got := parseVerifiedDigest([]byte("verified " + b)); got != b {
		t.Errorf("fallback parseVerifiedDigest = %q", got)
	}
	if got := parseVerifiedDigest([]byte("no digest here")); got != "" {
		t.Errorf("expected empty digest, got %q", got)
	}
}

func TestArtifactRoot_FallsBackWhenNoPackagesDir(t *testing.T) {
	root := t.TempDir()
	if got := artifactRoot(root); got != root {
		t.Fatalf("expected fallback to root %q, got %q", root, got)
	}
	writeFile(t, root, "packages/keep.txt", "x")
	if got := artifactRoot(root); got != filepath.Join(root, "packages") {
		t.Fatalf("expected packages subdir, got %q", got)
	}
}
