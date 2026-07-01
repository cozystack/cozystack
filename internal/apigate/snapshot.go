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
	"os"
	"path/filepath"
)

// cozyRDGlob matches every ApplicationDefinition backing the apps.cozystack.io
// aggregated API.
const cozyRDGlob = "packages/system/*-rd/cozyrds/*.yaml"

// crdGlobs enumerates the directories holding checked-in CustomResourceDefinition
// manifests for the typed API groups. Kept explicit (rather than a repo-wide
// scan) so the gate's surface is auditable and a new manifest home is a
// deliberate one-line addition here.
var crdGlobs = []string{
	"internal/crdinstall/manifests/*.yaml",
	"packages/system/application-definition-crd/definition/*.yaml",
	"packages/system/backup-controller/definitions/*.yaml",
	"packages/system/backupstrategy-controller/definitions/*.yaml",
	"packages/system/cozystack-controller/definitions/*.yaml",
}

// apiserverGoFile is the source of the static Go-backed aggregated registrations.
const apiserverGoFile = "pkg/apiserver/apiserver.go"

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

	// Typed groups: CRD manifests.
	for _, glob := range crdGlobs {
		matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(glob)))
		if err != nil {
			return nil, err
		}
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			resources, err := ParseCRDs(rel(dir, path), data)
			if err != nil {
				return nil, err
			}
			for _, res := range resources {
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

	return snap, nil
}

// rel renders a discovered path repo-relative for reporting, falling back to
// the absolute path if it lies outside dir.
func rel(dir, path string) string {
	if r, err := filepath.Rel(dir, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}
