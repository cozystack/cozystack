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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// tarGz builds an in-memory gzipped tarball from name->content entries and
// returns the bytes plus their Flux-style digest.
func tarGz(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, "sha256:" + hex.EncodeToString(sum[:])
}

const samplePS = `apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: example.hello
spec:
  variants:
    - name: default
      components:
        - name: hello
          path: apps/hello
`

func TestVerifyAndExtractAndParse(t *testing.T) {
	data, digest := tarGz(t, map[string]string{
		"packages/core/platform/sources/hello.yaml": samplePS,
		"packages/apps/hello/Chart.yaml":            "apiVersion: v2\nname: hello\nversion: 0.1.0\n",
	})
	dir := t.TempDir()
	if err := verifyAndExtract(data, digest, dir); err != nil {
		t.Fatalf("verifyAndExtract: %v", err)
	}
	pss, err := parsePackageSourcesFromTree(dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pss) != 1 || pss[0].Name != "example.hello" {
		t.Fatalf("expected 1 PackageSource example.hello, got %+v", pss)
	}
}

func TestVerifyAndExtractDigestMismatch(t *testing.T) {
	data, _ := tarGz(t, map[string]string{"packages/x.yaml": "a: b\n"})
	if err := verifyAndExtract(data, "sha256:"+hex.EncodeToString(make([]byte, 32)), t.TempDir()); err == nil {
		t.Fatal("expected digest mismatch error")
	}
}

func TestVerifyAndExtractRejectsTraversal(t *testing.T) {
	data, digest := tarGz(t, map[string]string{"../escape.yaml": "a: b\n"})
	if err := verifyAndExtract(data, digest, t.TempDir()); err == nil {
		t.Fatal("expected path-traversal rejection")
	}
}

func TestParsePackageSourcesRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	data, digest := tarGz(t, map[string]string{
		"bad.yaml": "apiVersion: cozystack.io/v1alpha1\nkind: PackageSource\nmetadata:\n  name: x\nspec:\n  bogusUnknown: true\n",
	})
	if err := verifyAndExtract(data, digest, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := parsePackageSourcesFromTree(dir); err == nil {
		t.Fatal("expected strict-decode error for unknown field")
	}
}

func TestMaterializedName(t *testing.T) {
	if got := materializedName("community.org.repo", "example.hello", true); got != "community.org.repo" {
		t.Errorf("single = %q", got)
	}
	if got := materializedName("community.org.repo", "community.hello", false); got != "community.org.repo.hello" {
		t.Errorf("multiple = %q", got)
	}
}

func TestCommunityBaseFromURL(t *testing.T) {
	cases := map[string]string{
		"oci://ghcr.io/foo/bar":           "community.foo.bar",
		"oci://ghcr.io/foo/bar:v1":        "community.foo.bar",
		"oci://registry.example.com/solo": "community.solo",
		"oci://ghcr.io/a/b/c:2.0":         "community.b.c",
	}
	for in, want := range cases {
		if got := communityBaseFromURL(in); got != want {
			t.Errorf("communityBaseFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
