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
	"os"
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/cozystack/cozystack/"

// readTestInputs returns the entries of test-inputs.txt, the list CI reads to
// decide whether a pull request runs these tests.
func readTestInputs(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("test-inputs.txt")
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
	}
	if len(entries) == 0 {
		t.Fatal("test-inputs.txt lists no path")
	}
	return entries
}

// covered reports whether a change to path matches an entry the way the plan
// job matches it: a trailing slash is a directory prefix, anything else is
// one file.
func covered(entries []string, path string) bool {
	for _, e := range entries {
		if strings.HasSuffix(e, "/") && strings.HasPrefix(path, e) || path == e {
			return true
		}
	}
	return false
}

// A pull request that changes an input of these tests has to run them, and CI
// knows the inputs only from test-inputs.txt. An import added here, or added to
// a package this tool imports, lands in a pull request that touches a listed
// path, so it runs this test, which fails until the new package is listed.
func TestInputsListed(t *testing.T) {
	entries := readTestInputs(t)

	out, err := exec.Command("go", "list", "-deps", "-test", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var deps int
	for _, pkg := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// A test variant prints as "pkg [pkg.test]"; the synthesized main
		// package as "pkg.test". Both are this directory.
		pkg, _, _ = strings.Cut(pkg, " ")
		pkg = strings.TrimSuffix(pkg, ".test")
		rel, ok := strings.CutPrefix(pkg, modulePath)
		if !ok {
			continue
		}
		deps++
		if !covered(entries, rel+"/x.go") {
			t.Errorf("these tests import %s, which test-inputs.txt does not list: add %s/", pkg, rel)
		}
	}
	if deps == 0 {
		t.Fatalf("go list named no package of %s, so nothing was checked:\n%s", modulePath, out)
	}

	for _, page := range []string{policyPage, "docs/release/backport-tasks/x.md"} {
		if !covered(entries, page) {
			t.Errorf("the tests read %s, which test-inputs.txt does not cover", page)
		}
	}
	for _, f := range []string{"go.mod", "go.sum"} {
		if !covered(entries, f) {
			t.Errorf("test-inputs.txt does not list %s", f)
		}
	}
}

// A listed path that no longer exists makes the list read as broader than it
// is, and usually means a dependency moved and its new home is missing.
func TestInputsExist(t *testing.T) {
	for _, e := range readTestInputs(t) {
		if _, err := os.Stat("../../" + e); err != nil {
			t.Errorf("test-inputs.txt lists %s: %v", e, err)
		}
	}
}
