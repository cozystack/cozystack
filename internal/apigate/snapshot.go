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
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// cozyRDGlob matches every ApplicationDefinition backing the apps.cozystack.io
// aggregated API.
const cozyRDGlob = "packages/system/*-rd/cozyrds/*.yaml"

// crdRoots are the trees walked to discover checked-in CustomResourceDefinition
// and APIService manifests. Every .yaml/.yml file under them is a candidate;
// filtering to the resources we actually care about is content-based (does
// this document parse, and does it have the right kind and a first-party
// group?) rather than directory-based. A directory-name allowlist (crds/,
// definitions/, manifests/, ...) was tried first and missed a real CRD that
// shipped inside a chart's templates/ dir behind a `{{- if }}` guard — the
// convention a contributor uses for a chart layout is not a reliable signal,
// so content sniffing replaces it entirely.
var crdRoots = []string{"packages", "internal"}

// apiserverGoFile is the source of the static Go-backed aggregated registrations.
const apiserverGoFile = "pkg/apiserver/apiserver.go"

// crdGroupBase is the first-party API group family. A discovered CRD is gated
// only when its group is exactly this or a dot-delimited subdomain of it, so
// vendored upstream CRDs (cert-manager, flux, …) sitting in a crds/ directory
// are not gated — and a lookalike such as "fakecozystack.io" is not mistaken
// for first-party.
const crdGroupBase = "cozystack.io"

// isFirstPartyGroup reports whether group belongs to the cozystack.io family.
func isFirstPartyGroup(group string) bool {
	return group == crdGroupBase || strings.HasSuffix(group, "."+crdGroupBase)
}

// LoadSnapshot walks a repository checkout rooted at dir and builds the full
// API Snapshot from every source of truth: cozyrds (apps API), CRD manifests
// (typed groups), the apiserver storage registrations (static Go groups), and
// APIService manifests (groups delegated to an aggregated apiserver, possibly
// built outside this repo entirely). Files that are absent are skipped so the
// loader works against historical checkouts predating a given path.
func LoadSnapshot(dir string) (Snapshot, error) {
	snap := Snapshot{}
	add := func(r Resource) {
		snap[r.key()] = r
	}

	// Apps API: one Resource per cozyrd.
	cozyrds, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(cozyRDGlob)))
	if err != nil {
		return nil, err
	}
	for _, path := range cozyrds {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		res, ok, err := ParseCozyRD(rel(dir, path), data)
		if err != nil {
			return nil, err
		}
		if ok {
			add(res)
		}
	}

	// Typed groups (CRD manifests) and APIService-backed groups: both are
	// discovered by content from the same file walk, splitting each file's
	// documents exactly once and probing each document's kind exactly once
	// (docKind) before deciding which of the two shapes to try — the walk
	// now scans every YAML file under packages/ and internal/ rather than a
	// curated directory allowlist, so most documents are neither and this
	// keeps the common case cheap. APIService resources are accumulated
	// separately and merged by group below, since a group's versions are
	// commonly registered as several sibling APIService objects (one per
	// version) rather than a single document listing all of them.
	yamlFiles, err := discoverYAMLFiles(dir)
	if err != nil {
		return nil, err
	}
	apiServices := map[resourceKey]Resource{}
	for _, path := range yamlFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !mightBeAPIManifest(data) {
			// Whole file contains neither marker substring anywhere, so no
			// document within it can be a CRD or an APIService either;
			// skip the `---` split (a regexp pass over the full file) too.
			continue
		}
		origin := rel(dir, path)

		for i, docBytes := range splitYAMLDocs(data) {
			if len(strings.TrimSpace(string(docBytes))) == 0 {
				continue
			}
			switch docKind(docBytes) {
			case "CustomResourceDefinition":
				res, ok, err := crdFromDoc(origin, i, docBytes)
				if err != nil {
					// A document unambiguously claiming to be a first-party
					// CRD but structurally incomplete; skip just this
					// document rather than losing any other resource that
					// shares its file.
					continue
				}
				if ok && isFirstPartyGroup(res.Group) {
					add(res)
				}
			case "APIService":
				res, ok := apiServiceFromDoc(origin, docBytes)
				if !ok || !isFirstPartyGroup(res.Group) {
					continue
				}
				key := resourceKey{Group: res.Group, Plural: res.Plural}
				existing, ok := apiServices[key]
				if !ok {
					apiServices[key] = res
					continue
				}
				maps.Copy(existing.Versions, res.Versions)
				if existing.Origin != res.Origin {
					existing.Origin += ", " + res.Origin
				}
				apiServices[key] = existing
			}
		}
	}
	for key, res := range apiServices {
		// Do not overwrite a richer cozyrd/CRD/apiserver-storage resource
		// that shares the same key; apiServicePlural is reserved and cannot
		// collide with a real plural, so in practice this only guards
		// against processing the same merged group twice.
		if _, exists := snap[key]; !exists {
			add(res)
		}
	}

	// Static Go-backed aggregated groups: apiserver registrations.
	apiserverPath := filepath.Join(dir, filepath.FromSlash(apiserverGoFile))
	if data, err := os.ReadFile(apiserverPath); err == nil {
		for _, res := range ParseAPIServerStorages(apiserverGoFile, data) {
			// Do not overwrite a richer cozyrd/CRD resource that shares the
			// same (group, plural); the apiserver entry has no schema.
			if _, exists := snap[res.key()]; !exists {
				add(res)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", apiserverPath, err)
	}

	// A zero-resource snapshot means the loader ran against the wrong directory
	// (or the layout moved out from under every discovery rule). Failing here
	// prevents the gate from silently passing — "no resources found" must never
	// be mistaken for "no sizeable change".
	if len(snap) == 0 {
		return nil, fmt.Errorf("no API resources discovered under %s; verify the checkout path and directory layout", dir)
	}

	return snap, nil
}

// maxYAMLFileSize bounds how large a single discovered file is read and
// parsed. Discovery is now content-based across every YAML file in the tree
// (no directory allowlist — see crdRoots), so this walk runs against
// untrusted PR head checkouts with no other size guard; real chart
// CRD/APIService manifests in this repo are well under a megabyte, so this
// is generous headroom, not a tight fit, and only bounds a pathological or
// adversarial file.
const maxYAMLFileSize = 8 << 20 // 8 MiB

// discoverYAMLFiles walks the crdRoots under dir and returns the path of
// every .yaml/.yml file found, up to maxYAMLFileSize. Filtering to the CRD /
// APIService documents we actually care about happens at parse time in
// LoadSnapshot.
func discoverYAMLFiles(dir string) ([]string, error) {
	var out []string
	for _, root := range crdRoots {
		base := filepath.Join(dir, root)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > maxYAMLFileSize {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rel renders a discovered path repo-relative for reporting, falling back to
// the absolute path if it lies outside dir.
func rel(dir, path string) string {
	if r, err := filepath.Rel(dir, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}
