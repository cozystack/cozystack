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

package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cozystack/cozystack/internal/apigate"
)

// Rule 4's API clause is the API review gate's own verdict: the same
// apigate.LoadSnapshot and apigate.Classify that cmd/api-gate runs in
// api-review-gate.yaml, on the trees of the original's first parent and of its
// merge, so a change the gate passes as additive or descriptive is no reason to
// hold a fix back.

// apiInputs are what apigate.LoadSnapshot reads of a checkout: the
// ApplicationDefinitions and every first-party CRD or APIService, all YAML
// under packages/ and internal/, and the apiserver's storage registrations.
var apiInputs = []string{"packages", "internal", "pkg/apiserver/apiserver.go"}

const (
	apiserverGo = "pkg/apiserver/apiserver.go"
	// apigate skips a YAML file larger than this; reading it would be wasted.
	apiFileLimit = 8 << 20
)

var apiSnapshots = map[string]apigate.Snapshot{}

// apiSnapshot is apigate's snapshot of commit, built from a temporary tree
// holding only what it reads. The blobs come from ls-tree and cat-file rather
// than a checkout or git archive: no attribute filters or export rules apply,
// and a symlink is never followed out of the tree.
func apiSnapshot(commit string) (apigate.Snapshot, error) {
	if snap, ok := apiSnapshots[commit]; ok {
		return snap, nil
	}
	out, err := git(append([]string{"ls-tree", "-r", "-z", "-l", "--full-tree", "--end-of-options", commit, "--"}, apiInputs...)...)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	var blobs []string
	for _, entry := range splitNUL(out) {
		meta, name, ok := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 {
			return nil, fmt.Errorf("ls-tree %.12s: unreadable entry %q", commit, entry)
		}
		mode, kind, blob := f[0], f[1], f[2]
		size, _ := strconv.Atoi(f[3])
		ext := path.Ext(name)
		switch {
		case kind != "blob" || (mode != "100644" && mode != "100755"):
		case name != apiserverGo && ext != ".yaml" && ext != ".yml":
		case size > apiFileLimit:
		case checkSHA(blob) != nil || !filepath.IsLocal(filepath.FromSlash(name)):
			return nil, fmt.Errorf("ls-tree %.12s: unreadable entry %q", commit, entry)
		default:
			paths[name] = blob
			blobs = append(blobs, blob)
		}
	}
	contents, err := readObjects(dedupe(blobs), "blob")
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "backport-batch-api-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, blob := range paths {
		file := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, contents[blob], 0o600); err != nil {
			return nil, err
		}
	}
	snap, err := apigate.LoadSnapshot(dir)
	if err != nil {
		return nil, fmt.Errorf("the API at %.12s: %w", commit, err)
	}
	apiSnapshots[commit] = snap
	return snap, nil
}

// sizeableAPIChange says why the API review gate would call the original's
// change to the API sizeable, or "" when it would not.
func sizeableAPIChange(o *original) (string, error) {
	parent, err := resolve(o.merge + "^1")
	if err != nil {
		return "", err
	}
	base, err := apiSnapshot(parent)
	if err != nil {
		return "", err
	}
	head, err := apiSnapshot(o.merge)
	if err != nil {
		return "", err
	}
	findings := apigate.Classify(base, head)
	if len(findings) == 0 {
		return "", nil
	}
	why := fmt.Sprintf("%s: %s", findings[0].Category, findings[0].Detail)
	if len(findings) > 1 {
		why += fmt.Sprintf(", and %d more", len(findings)-1)
	}
	return why, nil
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
