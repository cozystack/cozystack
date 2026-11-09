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
	"slices"
	"strconv"
	"strings"

	"github.com/cozystack/cozystack/internal/apigate"
)

// Rule 4's API clause starts from the API review gate's own verdict: the same
// apigate.LoadSnapshot and apigate.Classify that cmd/api-gate runs in
// api-review-gate.yaml, on the trees of the original's first parent and of its
// merge, so a change the gate passes as descriptive is no reason to hold a fix
// back. Of what the gate passes as additive, a new field still is one, which
// addedAPIField reads off the same snapshots.

const (
	apiserverGo = "pkg/apiserver/apiserver.go"
	// apigate reads an ApplicationDefinition wherever this matches, whatever
	// the file holds.
	cozyRDGlob = "packages/system/*-rd/cozyrds/*.yaml"
	// apigate skips a YAML file larger than this; reading it would be wasted.
	apiFileLimit = 8 << 20
)

// apiYAMLRoots hold all YAML apigate.LoadSnapshot parses: the
// ApplicationDefinitions and every first-party CRD or APIService.
var apiYAMLRoots = []string{"packages", "internal"}

// apiInputs are what apigate.LoadSnapshot reads of a checkout: the YAML roots
// and the apiserver's storage registrations.
var apiInputs = append(slices.Clone(apiYAMLRoots), apiserverGo)

// apiMarkers are mightBeAPIManifest's: apigate parses no other YAML file that
// holds neither.
var apiMarkers = []string{"-e", "CustomResourceDefinition", "-e", "APIService"}

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

// readsAPI reports whether the change reaches anything apigate.LoadSnapshot
// parses at either end. A change to anything else leaves both snapshots alike,
// so the gate is not asked. A binary file is searched as well, since apigate
// reads one like any other; a file over its size limit is still asked about,
// which costs a snapshot and changes no verdict.
func readsAPI(parent, merge string, changes []fileChange) (bool, error) {
	var yamls []string
	for _, c := range changes {
		p := c.path
		if p == apiserverGo {
			return true, nil
		}
		if ext := path.Ext(p); ext != ".yaml" && ext != ".yml" {
			continue
		}
		if !slices.ContainsFunc(apiYAMLRoots, func(root string) bool { return strings.HasPrefix(p, root+"/") }) {
			continue
		}
		if rd, _ := path.Match(cozyRDGlob, p); rd {
			return true, nil
		}
		yamls = append(yamls, p)
	}
	for _, rev := range []string{parent, merge} {
		found, err := grepFiles(rev, yamls, apiMarkers...)
		if err != nil || len(found) > 0 {
			return len(found) > 0, err
		}
	}
	return false, nil
}

// apiEnds are apigate's snapshots of the original's first parent and of its
// merge.
func apiEnds(o *original) (base, head apigate.Snapshot, err error) {
	parent, err := resolve(o.merge + "^1")
	if err != nil {
		return nil, nil, err
	}
	if base, err = apiSnapshot(parent); err != nil {
		return nil, nil, err
	}
	head, err = apiSnapshot(o.merge)
	return base, head, err
}

// sizeableAPIChange says why the API review gate would call the original's
// change to the API sizeable, or "" when it would not.
func sizeableAPIChange(o *original) (string, error) {
	base, head, err := apiEnds(o)
	if err != nil {
		return "", err
	}
	var why []string
	for _, f := range apigate.Classify(base, head) {
		why = append(why, fmt.Sprintf("%s: %s", f.Category, f.Detail))
	}
	return firstOf(why), nil
}

// addedAPIField says which field the original adds to a schema apigate reads,
// or "" when it adds none. The API review gate passes a new optional field as
// additive; a patch release takes none.
func addedAPIField(o *original) (string, error) {
	base, head, err := apiEnds(o)
	if err != nil {
		return "", err
	}
	var added []string
	for k, res := range head {
		// A resource new to head is the gate's finding.
		prev, ok := base[k]
		if !ok {
			continue
		}
		// An ApplicationDefinition's schema is its spec, which apigate names
		// spec; a CRD's is the whole object, whose spec is a field like any
		// other.
		root := "spec"
		if res.Source == apigate.SourceCRD {
			root = ""
		}
		for v, schema := range res.Versions {
			had, now := map[string]string{}, map[string]string{}
			declaredFields(root, "", prev.Versions[v], had)
			declaredFields(root, "", schema, now)
			for key, field := range now {
				if _, ok := had[key]; !ok {
					added = append(added, fmt.Sprintf("[%s] %s was added", v, field))
				}
			}
		}
	}
	slices.Sort(added)
	return firstOf(added), nil
}

// declaredFields maps each field schema declares, at every depth apigate's
// diff walks, to how a report names it. A key quotes each name, so that a
// field called "a.b" is not field b of a.
func declaredFields(path, key string, schema map[string]any, out map[string]string) {
	if schema == nil {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	for name, sub := range props {
		k := key + "." + strconv.Quote(name)
		child, field := name, fmt.Sprintf("field %q", name)
		if path != "" {
			child, field = path+"."+name, path+": "+field
		}
		out[k] = field
		sub, _ := sub.(map[string]any)
		declaredFields(child, k, sub, out)
	}
	items, _ := schema["items"].(map[string]any)
	declaredFields(path+"[]", key+"[]", items, out)
	values, _ := schema["additionalProperties"].(map[string]any)
	declaredFields(path+"{}", key+"{}", values, out)
}

// firstOf is the first of reasons and how many more follow it, or "" for none.
func firstOf(reasons []string) string {
	switch len(reasons) {
	case 0:
		return ""
	case 1:
		return reasons[0]
	}
	return fmt.Sprintf("%s, and %d more", reasons[0], len(reasons)-1)
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
