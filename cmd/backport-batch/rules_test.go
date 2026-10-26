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
	"slices"
	"strings"
	"testing"
)

func TestTypeRule(t *testing.T) {
	cases := []struct {
		title string
		taken bool
	}{
		{"fix(postgres): keep the replica count on resize", true},
		{"fix: typo in monitoring-agents values.yaml", true},
		{"security(dashboard): escape the tenant name", true},
		{"fix(ci): retry the flaky e2e step", true},
		{"[Backport release-1.6] fix(linstor): x", true},
		{"[Backport release-1.5] [Backport release-1.6] fix(linstor): x", true},
		{"fix(platform, dashboard): two scopes", true},
		{"feat(postgres): add a field", false},
		{"chore(release): promote", false},
		{"docs(release): split the page", false},
		{"refactor(api): move code", false},
		{"fix(deps): bump golang.org/x/net", false},
		{"fix(platform, deps): bump and fix", false},
		{"Fix(postgres): capitalised type", false},
		{"fix postgres without a colon", false},
		{"[postgres] bracket form", false},
		{"fixes(postgres): not the fix type", false},
		{"", false},
	}
	for _, tc := range cases {
		reason := typeRule(tc.title)
		if (reason == "") != tc.taken {
			t.Errorf("typeRule(%q) = %q, want taken=%v", tc.title, reason, tc.taken)
		}
	}
}

func TestBreakingRule(t *testing.T) {
	cases := []struct {
		name, title, body string
		breaking          bool
	}{
		{"plain fix", "fix(x): y", "## What this PR does\n\nFixes it.", false},
		{"bang", "fix(x)!: y", "", true},
		{"bang without scope", "fix!: y", "", true},
		{"bang behind a backport prefix", "[Backport release-1.6] fix(x)!: y", "", true},
		{"footer", "fix(x): y", "Body.\n\nBREAKING CHANGE: the field is gone", true},
		{"hyphenated footer", "fix(x): y", "BREAKING-CHANGE: the field is gone", true},
		{"footer quoted mid-line", "fix(x): y", "This is not a BREAKING CHANGE: nothing changes.", false},
		{"footer inside the template comment", "fix(x): y", "<!--\nBREAKING CHANGE: example\n-->\nBody.", false},
		{"lower-case footer", "fix(x): y", "breaking change: no", false},
	}
	for _, tc := range cases {
		reason := breakingRule(tc.title, tc.body)
		if (reason != "") != tc.breaking {
			t.Errorf("%s: breakingRule = %q, want breaking=%v", tc.name, reason, tc.breaking)
		}
	}
}

// Rule 4 reads the merge itself, so each case is a pull request merged into a
// real repository.
func TestTouchesRule(t *testing.T) {
	cases := []struct {
		name  string
		setup map[string]string // committed on main before the pull request
		files map[string]string
		want  string
	}{
		{name: "a chart change", files: map[string]string{"chart/values.yaml": "replicas: 2\nimage: app:1\n"}},
		{name: "a migration script", files: map[string]string{migrationsDir + "61": "#!/bin/sh\n"}, want: "platform migrations"},
		{name: "a migration library file", files: map[string]string{migrationsDir + "lib/common.sh": "x\n"}, want: "platform migrations"},
		{name: "the migration runner is not a migration", files: map[string]string{"packages/core/platform/images/migrations/run-migrations.sh": "x\n"}},
		{
			name:  "targetVersion moves",
			setup: map[string]string{platformValues: "migrations:\n  enabled: false\n  targetVersion: 60\n"},
			files: map[string]string{platformValues: "migrations:\n  enabled: false\n  targetVersion: 61\n"},
			want:  "targetVersion",
		},
		{
			name:  "another key of the platform values",
			setup: map[string]string{platformValues: "migrations:\n  enabled: false\n  targetVersion: 60\n"},
			files: map[string]string{platformValues: "migrations:\n  enabled: true\n  targetVersion: 60\n"},
		},
		{name: "api types", files: map[string]string{"api/apps/v1alpha1/types.go": "package v1alpha1\n"}, want: "api/"},
		{name: "pkg/apis is not api/", files: map[string]string{"pkg/apis/apps/types.go": "package apps\n"}},
		{name: "a crds directory", files: map[string]string{"packages/system/x/charts/x/crds/a.yaml": "kind: ConfigMap\n"}, want: "a CRD"},
		{name: "a template declaring a CRD", files: map[string]string{"packages/system/x/templates/crd.yaml": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n"}, want: "a CRD"},
		{name: "a quoted declaration", files: map[string]string{"x/crd.yaml": "  kind: \"CustomResourceDefinition\"  # yes\n"}, want: "a CRD"},
		{
			name:  "a CRD changed in place",
			setup: map[string]string{"x/crd.yaml": "kind: CustomResourceDefinition\nspec: {}\n"},
			files: map[string]string{"x/crd.yaml": "kind: CustomResourceDefinition\nspec: {a: 1}\n"},
			want:  "a CRD",
		},
		{
			name:  "a CRD deleted",
			setup: map[string]string{"x/crd.yaml": "kind: CustomResourceDefinition\n"},
			files: map[string]string{"x/crd.yaml": ""},
			want:  "a CRD",
		},
		{
			name:  "a CRD turned into something else",
			setup: map[string]string{"x/crd.yaml": "kind: CustomResourceDefinition\n"},
			files: map[string]string{"x/crd.yaml": "kind: ConfigMap\n"},
			want:  "a CRD",
		},
		{name: "a CRD list is not a CRD", files: map[string]string{"x/list.yaml": "kind: CustomResourceDefinitionList\n"}},
		{name: "a CRD mentioned in prose", files: map[string]string{"x/README.md": "Set kind: CustomResourceDefinition here.\n"}},
		{name: "a file named like an option declaring a CRD", files: map[string]string{"-e": "kind: CustomResourceDefinition\n"}, want: "a CRD"},
		// As pathspec magic, :(icase)... would match a file of that name in
		// another case, and there is none.
		{name: "a file named like pathspec magic declaring a CRD", files: map[string]string{":(icase)zz-crd.yaml": "kind: CustomResourceDefinition\n"}, want: "a CRD"},
		{
			name:  "migration and api together",
			files: map[string]string{migrationsDir + "61": "x\n", "api/x.go": "package x\n"},
			want:  "platform migrations, api/",
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.setup != nil {
				f.squash(1000, "chore: setup", "", tc.setup)
			}
			mc := f.pr(10+i, "fix(x): y", "", tc.files)
			o := &original{number: 10 + i, merge: mc, parents: 2}
			changes, err := changedFiles(mc+"^1", mc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := touchesRule(o, changes)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("touchesRule = %q, want %q", got, tc.want)
			}
		})
	}
}

// Rule 6: an older line takes only what the newer line already carries, and
// rule 1: neither line takes what it carries already.
func TestNewerLineAndOnLine(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(2, "fix(app): before nothing", "", map[string]string{"app/a.txt": "a\n"})
	f.pr(3, "fix(app): picked onto 1.1", "", map[string]string{"app/b.txt": "b\n"})
	mc3 := f.git("rev-parse", "HEAD")
	f.pr(4, "fix(app): only on main", "", map[string]string{"app/c.txt": "c\n"})
	f.onLine("release-1.1", "fix(app): picked onto 1.1 (#3)\n\n(cherry picked from commit "+mc3+")", map[string]string{"app/b.txt": "b\n"})

	for _, tc := range []struct {
		line      string
		qualified []int
		skipped   map[int]int
	}{
		{line: "release-1.1", qualified: []int{2, 4}, skipped: map[int]int{3: ruleOnLine}},
		// #1 is a feature; #2 and #4 are not on release-1.1 yet; #3 is.
		{line: "release-1.0", qualified: []int{3}, skipped: map[int]int{1: ruleType, 2: ruleNewer, 4: ruleNewer}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			f.git("fetch", "-q", "origin")
			l, _ := parseLine(tc.line)
			b, err := (&app{gh: &fakeGH{}, stderr: &strings.Builder{}}).prepare(l)
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, v := range b.qualifying() {
				got = append(got, v.o.number)
			}
			if !slices.Equal(got, tc.qualified) {
				t.Errorf("qualifying = %v, want %v", got, tc.qualified)
			}
			for n, rule := range tc.skipped {
				if v := b.find(n); v == nil || v.rule != rule {
					t.Errorf("#%d: verdict %+v, want rule %d", n, v, rule)
				}
			}
		})
	}
}

// An original merged before the newer line branched is on it, even when the
// newer line has since changed the same lines and a pick would conflict.
func TestNewerLineCarriesWhatItBranchedWith(t *testing.T) {
	f := newFixture(t)
	f.release("release-1.0")
	f.pr(2, "fix(app): two", "", map[string]string{"app/a.txt": "two\n"})
	f.release("release-1.1")
	mc3 := f.pr(3, "fix(app): three", "", map[string]string{"app/a.txt": "three\n"})
	f.onLine("release-1.1", "fix(app): three (#3)\n\n(cherry picked from commit "+mc3+")", map[string]string{"app/a.txt": "three\n"})
	if v := verdictOn(t, f, nil, "release-1.0", 2); v.rule != 0 {
		t.Errorf("#2 on release-1.0 = rule %d (%s), want it to qualify", v.rule, v.reason)
	}
}

func TestOnlySupportedLines(t *testing.T) {
	f := newFixture(t)
	f.release("release-0.9")
	f.lines()
	code, _, stderr := f.exec(nil, "check", "--line", "release-0.9")
	if code != 2 || !strings.Contains(stderr, "not a supported line") {
		t.Errorf("check on release-0.9 = %d (%s), want 2 and a refusal", code, stderr)
	}
}
