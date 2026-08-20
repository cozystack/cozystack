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
	"testing"
)

func sampleIndex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "foo-bar.yaml", `name: foo.bar
ociRef: oci://ghcr.io/foo/bar
description: A SQL database operator
maintainer: Foo <foo@example.com>
homepage: https://example.com/foo
tags:
  - database
  - sql
signing:
  identity: https://github.com/foo/bar/.github/workflows/release.yaml@refs/heads/main
  issuer: https://token.actions.githubusercontent.com
`)
	writeFile(t, dir, "baz-qux.yaml", `name: baz.qux
ociRef: oci://ghcr.io/baz/qux
description: A message queue
tags: [messaging]
`)
	writeFile(t, dir, "README.md", "# index\nnot an entry\n")
	return dir
}

func TestLoadIndex(t *testing.T) {
	entries, err := loadIndex(sampleIndex(t))
	if err != nil {
		t.Fatalf("loadIndex: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	// Sorted by name: baz.qux before foo.bar.
	if entries[0].Name != "baz.qux" || entries[1].Name != "foo.bar" {
		t.Fatalf("entries not sorted by name: %v", []string{entries[0].Name, entries[1].Name})
	}
	if entries[1].Signing == nil || entries[1].Signing.Issuer == "" {
		t.Errorf("expected signing identity on foo.bar")
	}
}

func TestLoadIndexRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.yaml", "name: x\nociRef: oci://r/x\nbogusUnknown: true\n")
	if _, err := loadIndex(dir); err == nil {
		t.Fatal("expected strict-decode error for unknown field")
	}

	dir2 := t.TempDir()
	writeFile(t, dir2, "missing.yaml", "description: no name or ref\n")
	if _, err := loadIndex(dir2); err == nil {
		t.Fatal("expected error for entry missing name/ociRef")
	}
}

func TestFilterEntries(t *testing.T) {
	entries, _ := loadIndex(sampleIndex(t))
	if got := filterEntries(entries, ""); len(got) != 2 {
		t.Errorf("empty term should match all, got %d", len(got))
	}
	if got := filterEntries(entries, "sql"); len(got) != 1 || got[0].Name != "foo.bar" {
		t.Errorf("term 'sql' should match foo.bar only, got %+v", got)
	}
	if got := filterEntries(entries, "MESSAGING"); len(got) != 1 || got[0].Name != "baz.qux" {
		t.Errorf("case-insensitive tag match failed, got %+v", got)
	}
	if got := filterEntries(entries, "nonexistent"); len(got) != 0 {
		t.Errorf("no match expected, got %+v", got)
	}
}

func TestResolveTapTarget(t *testing.T) {
	// oci:// passes through untouched, no index needed.
	if got, err := resolveTapTarget("oci://ghcr.io/a/b:v1", ""); err != nil || got != "oci://ghcr.io/a/b:v1" {
		t.Fatalf("oci passthrough failed: got %q err %v", got, err)
	}

	dir := sampleIndex(t)
	got, err := resolveTapTarget("foo.bar", dir)
	if err != nil {
		t.Fatalf("resolveTapTarget: %v", err)
	}
	if got != "oci://ghcr.io/foo/bar" {
		t.Fatalf("resolved ref = %q", got)
	}

	if _, err := resolveTapTarget("does.not.exist", dir); err == nil {
		t.Fatal("expected error for unknown short name")
	}
	if _, err := resolveTapTarget("foo.bar", ""); err == nil {
		t.Fatal("expected error when short name given but no index configured")
	}
}

func TestDefaultIndexRef(t *testing.T) {
	if got := defaultIndexRef("/explicit"); got != "/explicit" {
		t.Errorf("flag should win, got %q", got)
	}
	t.Setenv("COZYPKG_INDEX", "/from/env")
	if got := defaultIndexRef(""); got != "/from/env" {
		t.Errorf("env fallback failed, got %q", got)
	}
	if got := defaultIndexRef("/explicit"); got != "/explicit" {
		t.Errorf("flag should still win over env, got %q", got)
	}
}
