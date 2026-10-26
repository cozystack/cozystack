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
	"regexp"
	"slices"
	"strings"
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
)

// Rule 4 as paths in this tree.
const (
	// The numbered scripts run-migrations.sh walks; hack/check-migrations-target.sh
	// reads the same directory.
	migrationsDir = "packages/core/platform/images/migrations/migrations/"
	// Where migrations.targetVersion is set; hack/check-migrations-target.sh
	// reads it from here.
	platformValues = "packages/core/platform/values.yaml"
	apiDir         = "api/"
	// A file declares a CRD when a line of it is this, before or after the
	// change. ERE for git grep.
	crdPattern = `^[[:space:]]*kind:[[:space:]]*["']?CustomResourceDefinition["']?[[:space:]]*(#.*)?$`
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
	case t.typ != "fix" && t.typ != "security":
		return "type " + t.typ
	case slices.Contains(t.scopes, "deps"):
		return "scope deps"
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
	touchesAPI           = "api/"
	touchesCRD           = "a CRD"
)

// touchesRule is rule 4: what of migrations, targetVersion, api/ and CRDs the
// merge touches, or "" when it touches none of them.
func touchesRule(o *original, changes []fileChange) (string, error) {
	hit := map[string]bool{}
	var after, before []string
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
			hit[touchesAPI] = true
		case strings.Contains("/"+p, "/crds/"):
			hit[touchesCRD] = true
		}
		if c.status != 'D' {
			after = append(after, p)
		}
		if c.status != 'A' {
			before = append(before, p)
		}
	}
	for _, side := range []struct {
		rev   string
		files []string
	}{{o.merge, after}, {o.merge + "^1", before}} {
		if hit[touchesCRD] {
			break
		}
		found, err := declaresCRD(side.rev, side.files)
		if err != nil {
			return "", err
		}
		hit[touchesCRD] = found
	}
	var why []string
	for _, r := range []string{touchesMigrations, touchesTargetVersion, touchesAPI, touchesCRD} {
		if hit[r] {
			why = append(why, r)
		}
	}
	return strings.Join(why, ", "), nil
}

// declaresCRD reports whether any of files at rev declares a CRD. Paths are
// passed literally, so a name such as ":(exclude)x" means only that file.
func declaresCRD(rev string, files []string) (bool, error) {
	const chunk = 500
	for len(files) > 0 {
		n := min(chunk, len(files))
		args := append([]string{"--literal-pathspecs", "grep", "-q", "-I", "-E", "-e", crdPattern, "--end-of-options", rev, "--"}, files[:n]...)
		_, stderr, code, err := gitStatus(args...)
		switch {
		case err != nil:
			return false, err
		case code == 0:
			return true, nil
		case code != 1:
			return false, fmt.Errorf("git grep at %s: exit %d: %s", rev, code, strings.TrimSpace(stderr))
		}
		files = files[n:]
	}
	return false, nil
}
