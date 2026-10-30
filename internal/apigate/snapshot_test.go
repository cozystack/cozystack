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

// The checkout LoadSnapshot reads is a PR head, so what a symlink in it may
// lead to is attacker-controlled. Helm follows links when it packages a chart,
// which makes a link a way to ship a manifest the detector does not read.

func writeTree(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkTree makes root/rel a symlink whose text is target, as given to symlink(2).
func linkTree(t *testing.T, root, rel, target string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

var widgetsKey = resourceKey{Group: "example.cozystack.io", Plural: "widgets"}

func TestLoadSnapshotFollowsFileLinkInsideCheckout(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "packages/chart/resource.txt", sampleCRD)
	linkTree(t, root, "packages/chart/crds/resource.yaml", "../resource.txt")

	snap, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap[widgetsKey]; !ok {
		t.Fatalf("a CRD behind an in-checkout link was not discovered: %v", snap)
	}
}

func TestLoadSnapshotRejectsFileLinkLeavingCheckout(t *testing.T) {
	const marker = "SECRET-MARKER"
	for name, build := range map[string]func(root, outside string){
		"crd manifest": func(root, outside string) {
			linkTree(t, root, "packages/chart/crds/resource.yaml", filepath.Join(outside, "secret.yaml"))
		},
		"chain through the checkout": func(root, outside string) {
			linkTree(t, root, "packages/chart/hop", filepath.Join(outside, "secret.yaml"))
			linkTree(t, root, "packages/chart/crds/resource.yaml", "../hop")
		},
		"cozyrd": func(root, outside string) {
			linkTree(t, root, "packages/system/widget-rd/cozyrds/widget.yaml", filepath.Join(outside, "secret.yaml"))
		},
		"apiserver registrations": func(root, outside string) {
			writeTree(t, root, "packages/chart/crds/resource.yaml", sampleCRD)
			linkTree(t, root, "pkg/apiserver/apiserver.go", filepath.Join(outside, "secret.yaml"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			writeTree(t, outside, "secret.yaml", sampleCRD+"# "+marker+"\n")
			build(root, outside)

			_, err := LoadSnapshot(root)
			if err == nil {
				t.Fatal("a link leaving the checkout was read")
			}
			if !strings.Contains(err.Error(), "resolves outside the checkout") {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), outside) {
				t.Fatalf("the error names what the link leads to: %v", err)
			}
		})
	}
}

func TestLoadSnapshotRejectsDirLinkOutsideScannedRoots(t *testing.T) {
	for name, target := range map[string]func(root, outside string) string{
		// Inside the checkout but not under packages/ or internal/: the walk
		// never reaches the directory by its own path, Helm does by the link.
		"elsewhere in the checkout": func(root, _ string) string { return "../../docs/hidden" },
		"outside the checkout":      func(_, outside string) string { return outside },
	} {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			writeTree(t, root, "docs/hidden/resource.yaml", sampleCRD)
			writeTree(t, outside, "resource.yaml", sampleCRD)
			writeTree(t, root, "packages/chart/Chart.yaml", "apiVersion: v2\nname: example\nversion: 0.1.0\n")
			// Something the load can find, so that a silent pass is not masked by
			// the empty-snapshot guard.
			writeTree(t, root, "packages/system/widget-rd/cozyrds/widget.yaml", sampleCozyRD)
			linkTree(t, root, "packages/chart/crds", target(root, outside))

			_, err := LoadSnapshot(root)
			if err == nil {
				t.Fatal("a directory link the walk does not cover was accepted")
			}
			if !strings.Contains(err.Error(), "outside the scanned roots") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotAcceptsDirLinkUnderScannedRoots(t *testing.T) {
	root := t.TempDir()
	// The shape the tree has today: every app's charts/cozy-lib.
	writeTree(t, root, "packages/library/lib/crds/resource.yaml", sampleCRD)
	linkTree(t, root, "packages/apps/foo/charts/lib", "../../../library/lib")
	linkTree(t, root, "internal/vendored", "../packages/library/lib")

	snap, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := snap[widgetsKey]
	if !ok {
		t.Fatalf("CRD behind a directory link was not discovered: %v", snap)
	}
	if want := "packages/library/lib/crds/resource.yaml"; res.Origin != want {
		t.Fatalf("origin = %q, want the canonical path %q, once", res.Origin, want)
	}
}

func TestLoadSnapshotDanglingLinks(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "packages/chart/crds/resource.yaml", sampleCRD)

	// Nothing to read or walk: not worth failing every PR over.
	linkTree(t, root, "packages/chart/charts/gone", "../../nowhere")
	if _, err := LoadSnapshot(root); err != nil {
		t.Fatalf("a dangling link that is not a manifest failed the load: %v", err)
	}

	// A manifest that cannot be read is an error, as it always was.
	linkTree(t, root, "packages/chart/crds/gone.yaml", "../../nowhere.yaml")
	if _, err := LoadSnapshot(root); err == nil {
		t.Fatal("a dangling manifest link was skipped")
	}
}

func TestLoadSnapshotThroughSymlinkedCheckoutRoot(t *testing.T) {
	target := t.TempDir()
	writeTree(t, target, "packages/chart/crds/resource.yaml", sampleCRD)
	linkTo := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(target, linkTo); err != nil {
		t.Fatal(err)
	}

	snap, err := LoadSnapshot(linkTo)
	if err != nil {
		t.Fatalf("a checkout reached through a link was refused: %v", err)
	}
	if _, ok := snap[widgetsKey]; !ok {
		t.Fatalf("CRD not discovered: %v", snap)
	}
}

func TestLoadSnapshotSkipsOversizedFileBehindLink(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "packages/system/widget-rd/cozyrds/widget.yaml", sampleCozyRD)
	writeTree(t, root, "packages/chart/big.txt", sampleCRD+"# "+strings.Repeat("x", maxYAMLFileSize)+"\n")
	linkTree(t, root, "packages/chart/crds/resource.yaml", "../big.txt")

	snap, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap[widgetsKey]; ok {
		t.Fatal("the size cap measured the link, not the file behind it")
	}
}
