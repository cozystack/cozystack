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
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// Rule numbers as docs/release/backports.md numbers them. Rule 5, a clean
// pick, is decided while the batch is assembled.
const (
	ruleOnLine   = 1
	ruleType     = 2
	ruleBreaking = 3
	ruleTouches  = 4
	ruleNewer    = 6
	// Not a rule of the policy: the backport/skip override.
	ruleSkipLabel = 100
	// Rule 1 for a line that started under the policy after it branched:
	// merged before that start.
	ruleBeforeStart = 101
)

// Rule 4 as paths in this tree.
const (
	// The numbered scripts run-migrations.sh walks; hack/check-migrations-target.sh
	// reads the same directory.
	migrationsDir = "packages/core/platform/images/migrations/migrations/"
	// Where migrations.targetVersion is set; hack/check-migrations-target.sh
	// reads it from here.
	platformValues = "packages/core/platform/values.yaml"
	// A change under api/ is held back only when the API review gate calls it
	// sizeable (apisurface.go).
	apiDir = "api/"
	// Only a file holding this text at either end of the change can hold a
	// CRD document; crdDocs decides document by document.
	crdKind = "CustomResourceDefinition"
)

// The title grammar and the breaking footer are pr-labeler.yaml's, so that a
// pull request labelled kind/bug and kind/breaking-change reads the same here.
var (
	backportPrefixRE = regexp.MustCompile(`^(?:\[Backport [^\]]+\]\s+)+`)
	conventionalRE   = regexp.MustCompile(`^([a-z]+)(?:\(([^)]+)\))?(!)?:\s*\S`)
	breakingFooterRE = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE:`)
	htmlCommentRE    = regexp.MustCompile(`(?s)<!--.*?-->`)
	targetVersionRE  = regexp.MustCompile(`(?m)^[+-][ \t]*targetVersion[ \t]*:`)
)

// title is a parsed Conventional Commits title.
type title struct {
	typ    string
	scopes []string
	bang   bool
}

func parseTitle(s string) (title, bool) {
	m := conventionalRE.FindStringSubmatch(backportPrefixRE.ReplaceAllString(s, ""))
	if m == nil {
		return title{}, false
	}
	t := title{typ: m[1], bang: m[3] == "!"}
	for scope := range strings.SplitSeq(m[2], ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			t.scopes = append(t.scopes, scope)
		}
	}
	return t, true
}

// typeRule is rule 2 together with the policy's "never in a patch release":
// dependency bumps, scope deps, stay out whatever their type.
func typeRule(s string) string {
	t, ok := parseTitle(s)
	switch {
	case !ok:
		return "the title is not a Conventional Commits title"
	case !slices.Contains(takenTypes, t.typ):
		return "type " + t.typ
	case slices.ContainsFunc(t.scopes, func(s string) bool { return slices.Contains(excludedScopes, s) }):
		return "an excluded scope"
	}
	return ""
}

// breakingRule is rule 3. The PR template quotes the footer inside an HTML
// comment, which a body merged with the template left in carries.
func breakingRule(s, body string) string {
	if t, ok := parseTitle(s); ok && t.bang {
		return "! in the title"
	}
	if breakingFooterRE.MatchString(htmlCommentRE.ReplaceAllString(body, "")) {
		return "BREAKING CHANGE footer"
	}
	return ""
}

// fileChange is one path a merge changes against its first parent.
type fileChange struct {
	status byte
	path   string
}

func changedFiles(from, to string) ([]fileChange, error) {
	out, err := git("diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--no-renames", "--end-of-options", from, to)
	if err != nil {
		return nil, err
	}
	f := splitNUL(out)
	if len(f)%2 != 0 {
		return nil, fmt.Errorf("unreadable diff of %s..%s", from, to)
	}
	var changes []fileChange
	for i := 0; i < len(f); i += 2 {
		changes = append(changes, fileChange{status: f[i][0], path: f[i+1]})
	}
	return changes, nil
}

func paths(changes []fileChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.path)
	}
	return out
}

// Rule 4's reasons, in the order they are reported.
const (
	touchesMigrations    = "platform migrations"
	touchesTargetVersion = "targetVersion"
	touchesAPI           = "a sizeable API change"
	touchesCRD           = "a CRD"
)

// touchesRule is rule 4: what of migrations, targetVersion, api/ and CRDs the
// merge touches, or "" when it touches none of them.
func touchesRule(o *original, changes []fileChange) (string, error) {
	hit := map[string]bool{}
	underAPI := false
	for _, c := range changes {
		p := c.path
		switch {
		case strings.HasPrefix(p, migrationsDir):
			hit[touchesMigrations] = true
		case p == platformValues:
			diff, err := git("--literal-pathspecs", "diff", "-U0", "--no-ext-diff", "--no-textconv", "--end-of-options", o.merge+"^1", o.merge, "--", platformValues)
			if err != nil {
				return "", err
			}
			hit[touchesTargetVersion] = targetVersionRE.MatchString(diff)
		case strings.HasPrefix(p, apiDir):
			underAPI = true
		}
	}
	var err error
	if hit[touchesCRD], err = changesCRD(o.merge+"^1", o.merge, changes); err != nil {
		return "", err
	}
	apiDetail := ""
	if underAPI {
		if apiDetail, err = sizeableAPIChange(o); err != nil {
			return "", err
		}
		hit[touchesAPI] = apiDetail != ""
	}
	var why []string
	for _, r := range []string{touchesMigrations, touchesTargetVersion, touchesAPI, touchesCRD} {
		switch {
		case !hit[r]:
		case r == touchesAPI:
			why = append(why, r+" ("+apiDetail+")")
		default:
			why = append(why, r)
		}
	}
	return strings.Join(why, ", "), nil
}

// changesCRD reports whether the change adds, removes or modifies a
// CustomResourceDefinition document. It looks document by document: an edit to
// the DaemonSet beside a CRD in one manifest is not a CRD change, and a CRD in
// a test fixture changes nothing a cluster runs.
func changesCRD(parent, merge string, changes []fileChange) (bool, error) {
	var files []string
	for _, c := range changes {
		if manifest(c.path) && !testFixture(c.path) {
			files = append(files, c.path)
		}
	}
	candidates := map[string]bool{}
	for _, rev := range []string{parent, merge} {
		found, err := mentionsCRD(rev, files)
		if err != nil {
			return false, err
		}
		for _, f := range found {
			candidates[f] = true
		}
	}
	for _, c := range changes {
		if !candidates[c.path] {
			continue
		}
		var before, after map[string]string
		if c.status != 'A' {
			content, err := git("cat-file", "blob", parent+":"+c.path)
			if err != nil {
				return false, err
			}
			before = crdDocs(content)
		}
		if c.status != 'D' {
			content, err := git("cat-file", "blob", merge+":"+c.path)
			if err != nil {
				return false, err
			}
			after = crdDocs(content)
		}
		if !maps.Equal(before, after) {
			return true, nil
		}
	}
	return false, nil
}

// manifest reports whether path is a file a chart or a cluster can apply: YAML,
// JSON or a Helm template. A CRD quoted in documentation or code applies
// nothing.
func manifest(path string) bool {
	switch filepath.Ext(path) {
	case ".yaml", ".yml", ".json", ".tpl":
		return true
	}
	dirs := strings.Split(path, "/")
	return slices.ContainsFunc(dirs[:len(dirs)-1], func(d string) bool { return d == "templates" || d == "crds" })
}

// testFixture reports whether path sits under a tests/ or testdata/ directory.
func testFixture(path string) bool {
	dirs := strings.Split(path, "/")
	return slices.ContainsFunc(dirs[:len(dirs)-1], func(d string) bool { return d == "tests" || d == "testdata" })
}

// mentionsCRD lists which of files at rev hold the text of the CRD kind. Paths
// are passed literally, so a name such as ":(exclude)x" means only that file.
func mentionsCRD(rev string, files []string) ([]string, error) {
	const chunk = 500
	var found []string
	for len(files) > 0 {
		n := min(chunk, len(files))
		args := append([]string{"--literal-pathspecs", "grep", "-l", "-z", "-I", "-F", "-e", crdKind, "--end-of-options", rev, "--"}, files[:n]...)
		stdout, stderr, code, err := gitStatus(args...)
		switch {
		case err != nil:
			return nil, err
		case code != 0 && code != 1:
			return nil, fmt.Errorf("git grep at %s: exit %d: %s", rev, code, strings.TrimSpace(stderr))
		}
		for _, hit := range splitNUL(stdout) {
			if path, ok := strings.CutPrefix(hit, rev+":"); ok {
				found = append(found, path)
			}
		}
		files = files[n:]
	}
	return found, nil
}

var (
	docSeparatorRE = regexp.MustCompile(`(?m)^---[ \t]*(?:#.*)?$`)
	// A document declares the kind on a line of its own, in YAML or in JSON;
	// a sentence or a lookup call that names it declares nothing.
	crdKindLineRE = regexp.MustCompile(`(?m)^[ \t]*(?:-[ \t]+)?(?:kind|"kind")[ \t]*:[ \t]*["']?CustomResourceDefinition["']?[ \t]*,?[ \t]*(?:#.*)?$`)
)

// crdDocs maps the CRD documents of a file to their text, keyed by
// metadata.name, so a CRD added, removed or edited changes the map. A document
// that declares the kind but cannot be read as an object, a CRD behind a Helm
// template guard for one, is kept under its own text, so that any change to it
// counts: the check fails closed. So does a list holding a CRD.
func crdDocs(content string) map[string]string {
	docs := map[string]string{}
	for _, doc := range docSeparatorRE.Split(content, -1) {
		if !crdKindLineRE.MatchString(doc) {
			continue
		}
		var head struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Items []struct {
				Kind string `json:"kind"`
			} `json:"items"`
		}
		key := ""
		switch err := yaml.Unmarshal([]byte(doc), &head); {
		case err != nil:
			key = "unreadable:" + doc
		case head.Kind == crdKind && head.Metadata.Name != "":
			key = "crd:" + head.Metadata.Name
		case head.Kind == crdKind:
			key = "unnamed:" + doc
		case strings.HasSuffix(head.Kind, "List") && slices.ContainsFunc(head.Items, func(i struct {
			Kind string `json:"kind"`
		}) bool {
			return i.Kind == crdKind
		}):
			key = "list:" + doc
		default:
			continue
		}
		docs[key] += doc
	}
	return docs
}
