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
	"os"
	"path/filepath"
	"strings"
)

// cozyRDGlob matches every ApplicationDefinition backing the apps.cozystack.io
// aggregated API.
const cozyRDGlob = "packages/system/*-rd/cozyrds/*.yaml"

// crdRoots are the trees walked to discover checked-in CustomResourceDefinition
// manifests. Within them, only files under a directory named in crdDirNames are
// parsed, and only CRDs whose group belongs to the cozystack.io family are
// kept. Discovering CRDs by location + content (rather than an explicit file
// list) means a first-party CRD that moves — e.g. into a chart's crds/ dir — is
// still covered here rather than slipping through until an incident.
var crdRoots = []string{"packages", "internal"}

// crdDirNames are the directory names that hold raw (non-templated) first-party
// CRD manifests. Helm's crds/ convention and this repo's definitions/ and
// manifests/ dirs all hold rendered CRDs; templated CRDs live under templates/
// and are intentionally excluded so we never try to parse Helm markup.
var crdDirNames = map[string]struct{}{
	"crds":        {},
	"definitions": {},
	"manifests":   {},
}

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
// (typed groups), and the apiserver storage registrations (static Go groups).
// Files that are absent are skipped so the loader works against historical
// checkouts predating a given path.
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

	// Typed groups: CRD manifests discovered by location + content.
	crdFiles, err := discoverCRDFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, path := range crdFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		resources, err := ParseCRDs(rel(dir, path), data)
		if err != nil {
			// A file living in a crds/ or definitions/ dir that does not parse
			// as YAML is almost always an upstream/templated artifact, not a
			// first-party CRD; skip it rather than failing the whole gate.
			continue
		}
		for _, res := range resources {
			if isFirstPartyGroup(res.Group) {
				add(res)
			}
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

// discoverCRDFiles walks the crdRoots under dir and returns the paths of files
// that sit in a CRD directory (crdDirNames). Content filtering to first-party
// CRDs happens at parse time in LoadSnapshot.
func discoverCRDFiles(dir string) ([]string, error) {
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
			if _, ok := crdDirNames[filepath.Base(filepath.Dir(path))]; !ok {
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
