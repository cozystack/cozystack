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
	"maps"
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
		{name: "api types the gate sees nothing in", setup: demoAPI, files: map[string]string{"api/apps/v1alpha1/types.go": "package v1alpha1\n"}},
		{name: "pkg/apis is not api/", files: map[string]string{"pkg/apis/apps/types.go": "package apps\n"}},
		{name: "a CRD added under crds/", files: map[string]string{"packages/system/x/charts/x/crds/a.yaml": crdDoc("as.x.io", "a") + "\n"}, want: "a CRD"},
		{name: "a ConfigMap under crds/ is not a CRD", files: map[string]string{"packages/system/x/charts/x/crds/a.yaml": "kind: ConfigMap\n"}},
		{
			name:  "a DaemonSet edit next to an unchanged CRD",
			setup: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(crdDoc("as.x.io", "a"), daemonSet("1"))},
			files: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(crdDoc("as.x.io", "a"), daemonSet("2"))},
		},
		{
			name:  "a real CRD edit in a manifest with other documents",
			setup: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(crdDoc("as.x.io", "a"), daemonSet("1"))},
			files: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(crdDoc("as.x.io", "b"), daemonSet("1"))},
			want:  "a CRD",
		},
		{
			name:  "a CRD renamed",
			setup: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(crdDoc("as.x.io", "a"), daemonSet("1"))},
			files: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(crdDoc("bs.x.io", "a"), daemonSet("1"))},
			want:  "a CRD",
		},
		{name: "a CRD in a fixture under tests/", files: map[string]string{"packages/system/x/tests/render_test.yaml": crdDoc("as.x.io", "a")}},
		{name: "a CRD in a fixture under testdata/", files: map[string]string{"hack/testdata/crd.yaml": crdDoc("as.x.io", "a")}},
		{
			name:  "a templated CRD edited, which cannot be read",
			setup: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(templatedCRD("a"), daemonSet("1"))},
			files: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(templatedCRD("b"), daemonSet("1"))},
			want:  "a CRD",
		},
		{
			name:  "a DaemonSet edit next to an unchanged templated CRD",
			setup: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(templatedCRD("a"), daemonSet("1"))},
			files: map[string]string{"packages/system/x/templates/m.yaml": multiDoc(templatedCRD("a"), daemonSet("2"))},
		},
		{
			name:  "an unparseable file that names the kind, changed",
			setup: map[string]string{"x/broken.yaml": "kind: CustomResourceDefinition\nspec: [unclosed\n"},
			files: map[string]string{"x/broken.yaml": "kind: CustomResourceDefinition\nspec: [still unclosed\n"},
			want:  "a CRD",
		},
		{
			name:  "a lookup that names the kind in a Helm helper",
			setup: map[string]string{"packages/system/x/templates/_helpers.tpl": "{{- $crd := lookup \"apiextensions.k8s.io/v1\" \"CustomResourceDefinition\" \"\" .crd -}}\n{{- if $crd }}a{{ end }}\n"},
			files: map[string]string{"packages/system/x/templates/_helpers.tpl": "{{- $crd := lookup \"apiextensions.k8s.io/v1\" \"CustomResourceDefinition\" \"\" .crd -}}\n{{- if $crd }}b{{ end }}\n"},
		},
		{
			name:  "a CRD example in documentation",
			setup: map[string]string{"docs/crds.md": "Example:\n\n```yaml\nkind: CustomResourceDefinition\nmetadata:\n  name: as.x.io\n```\n"},
			files: map[string]string{"docs/crds.md": "An example:\n\n```yaml\nkind: CustomResourceDefinition\nmetadata:\n  name: bs.x.io\n```\n"},
		},
		{name: "a CRD in a template without a YAML extension", files: map[string]string{"packages/system/x/templates/crd.tmpl": crdDoc("as.x.io", "a")}, want: "a CRD"},
		{name: "a CRD added as JSON", files: map[string]string{"x/crd.json": `{"apiVersion": "apiextensions.k8s.io/v1",` + "\n" + `  "kind": "CustomResourceDefinition",` + "\n" + `  "metadata": {"name": "as.x.io"}}` + "\n"}, want: "a CRD"},
		{name: "a List of CRDs", files: map[string]string{"x/list.yaml": "kind: List\nitems:\n- kind: CustomResourceDefinition\n  metadata:\n    name: as.x.io\n"}, want: "a CRD"},
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
		{name: "a CRD as compact JSON", files: map[string]string{"x/crd.json": `{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":"as.x.io"}}` + "\n"}, want: "a CRD"},
		{name: "a CRD under a quoted kind key", files: map[string]string{"x/crd.yaml": "'kind': CustomResourceDefinition\nmetadata:\n  name: as.x.io\n"}, want: "a CRD"},
		{
			name:  "a templated CRD under a quoted kind key, edited",
			setup: map[string]string{"packages/system/x/templates/m.yaml": strings.Replace(templatedCRD("a"), "kind:", "'kind':", 1)},
			files: map[string]string{"packages/system/x/templates/m.yaml": strings.Replace(templatedCRD("b"), "kind:", "'kind':", 1)},
			want:  "a CRD",
		},
		{name: "a ConfigMap as compact JSON naming the kind", files: map[string]string{"x/cm.json": `{"kind":"ConfigMap","data":{"note":"kind: CustomResourceDefinition"}}` + "\n"}},
		{name: "a CRD mentioned in prose", files: map[string]string{"x/README.md": "Set kind: CustomResourceDefinition here.\n"}},
		{name: "a file named like an option declaring a CRD", files: map[string]string{"-e.yaml": "kind: CustomResourceDefinition\n"}, want: "a CRD"},
		// As pathspec magic, :(icase)... would match a file of that name in
		// another case, and there is none.
		{name: "a file named like pathspec magic declaring a CRD", files: map[string]string{":(icase)zz-crd.yaml": "kind: CustomResourceDefinition\n"}, want: "a CRD"},
		{
			name:  "a migration and a sizeable API change together",
			setup: demoAPI,
			files: map[string]string{migrationsDir + "61": "x\n", "api/x.go": "package x\n", demoRD: cozyrd("demo", `{"type":"object","properties":{}}`)},
			want:  "platform migrations, a sizeable API change (breaking-change: [v1alpha1] spec: field \"size\" was removed)",
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			setup := maps.Clone(apiBase)
			maps.Copy(setup, tc.setup)
			f.squash(1000, "chore: setup", "", setup)
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

// A line branched and not yet released is one the batch runs for, and rule 6
// holds each older line to the next newer one.
func TestNewerLineInTheRCWindow(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.release("release-1.2")
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	for _, tc := range []struct{ line, reason string }{
		{"release-1.2", ""},
		{"release-1.1", "not on release-1.2 yet"},
		{"release-1.0", "not on release-1.1 yet"},
	} {
		v := verdictOn(t, f, nil, tc.line, 5)
		if v.reason != tc.reason || (v.rule == 0) != (tc.reason == "") {
			t.Errorf("#5 on %s = rule %d (%s), want %q", tc.line, v.rule, v.reason, tc.reason)
		}
	}
}

func TestOnlySupportedLines(t *testing.T) {
	f := newFixture(t)
	f.release("release-0.9")
	f.lines()
	code, _, stderr := f.exec(nil, "check", "--line", "release-0.9")
	if code != 2 || !strings.Contains(stderr, "not a line the batch runs for") {
		t.Errorf("check on release-0.9 = %d (%s), want 2 and a refusal", code, stderr)
	}
}

// demoRD is an ApplicationDefinition where apigate looks for one, and demoAPI
// the tree the API cases start from.
const demoRD = "packages/system/demo-rd/cozyrds/demo.yaml"

// apiBase is the least API a tree holds: apigate refuses a tree with none.
var apiBase = map[string]string{"packages/system/keep-rd/cozyrds/keep.yaml": cozyrd("keep", `{"type":"object"}`)}

var demoAPI = map[string]string{
	demoRD:                            cozyrd("demo", `{"type":"object","properties":{"size":{"type":"string","description":"Size."}}}`),
	"api/apps/v1alpha1/demo/types.go": "package demo\n\n// Size of it.\ntype Spec struct{ Size string }\n",
}

func cozyrd(name, schema string) string {
	return "apiVersion: cozystack.io/v1alpha1\nkind: ApplicationDefinition\nmetadata:\n  name: " + name +
		"\nspec:\n  application:\n    kind: " + strings.ToUpper(name[:1]) + name[1:] + "\n    plural: " + name + "s\n    singular: " + name +
		"\n    openAPISchema: '" + schema + "'\n"
}

// Rule 4 holds back a change to the API only when the API review gate calls it
// sizeable: a new or removed resource or group, or a breaking schema change.
func TestAPIGateRule(t *testing.T) {
	typesGo := "api/apps/v1alpha1/demo/types.go"
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "a description reworded", files: map[string]string{
			demoRD:  cozyrd("demo", `{"type":"object","properties":{"size":{"type":"string","description":"The size."}}}`),
			typesGo: "package demo\n\n// The size.\ntype Spec struct{ Size string }\n",
		}},
		{name: "an optional field added", files: map[string]string{
			demoRD:  cozyrd("demo", `{"type":"object","properties":{"size":{"type":"string"},"zone":{"type":"string"}}}`),
			typesGo: "package demo\n\ntype Spec struct{ Size, Zone string }\n",
		}},
		{name: "a field removed", files: map[string]string{
			demoRD:  cozyrd("demo", `{"type":"object","properties":{}}`),
			typesGo: "package demo\n\ntype Spec struct{}\n",
		}, want: "breaking-change"},
		{name: "a field's type changed", files: map[string]string{
			demoRD:  cozyrd("demo", `{"type":"object","properties":{"size":{"type":"integer"}}}`),
			typesGo: "package demo\n\ntype Spec struct{ Size int }\n",
		}, want: "breaking-change"},
		{name: "a field made required", files: map[string]string{
			demoRD:  cozyrd("demo", `{"type":"object","required":["size"],"properties":{"size":{"type":"string"}}}`),
			typesGo: "package demo\n\n// +required\ntype Spec struct{ Size string }\n",
		}, want: "breaking-change"},
		{name: "a resource added", files: map[string]string{
			"packages/system/other-rd/cozyrds/other.yaml": cozyrd("other", `{"type":"object"}`),
			"api/apps/v1alpha1/other/types.go":            "package other\n",
		}, want: "new-resource"},
		{name: "a resource removed", files: map[string]string{
			demoRD:  "",
			typesGo: "",
		}, want: "removed"},
		{name: "a breaking schema change that does not touch api/", files: map[string]string{
			demoRD: cozyrd("demo", `{"type":"object","properties":{}}`),
		}, want: "breaking-change"},
		{name: "an aggregated group registered in a chart", files: map[string]string{
			"packages/system/extra/templates/apiservice.yaml": apiService("extra.cozystack.io"),
		}, want: "new-api-group"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			setup := maps.Clone(apiBase)
			maps.Copy(setup, demoAPI)
			f.squash(1000, "chore: setup", "", setup)
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
			if (got != "") != (tc.want != "") || !strings.Contains(got, tc.want) {
				t.Errorf("touchesRule = %q, want %q", got, tc.want)
			}
		})
	}
}

func apiService(group string) string {
	return "apiVersion: apiregistration.k8s.io/v1\nkind: APIService\nmetadata:\n  name: v1alpha1." + group +
		"\nspec:\n  group: " + group + "\n  version: v1alpha1\n"
}

// The API review gate is asked about exactly the changes that reach what
// apigate parses at either end of the merge.
func TestReadsAPI(t *testing.T) {
	crd := crdDoc("as.x.io", "a")
	cases := []struct {
		name  string
		setup map[string]string
		files map[string]string
		want  bool
	}{
		{name: "the apiserver's registrations", files: map[string]string{apiserverGo: "package apiserver\n"}, want: true},
		{name: "an ApplicationDefinition, which carries no marker", files: map[string]string{demoRD: "kind: ApplicationDefinition\n"}, want: true},
		{name: "a CRD in a chart", files: map[string]string{"packages/system/x/crds/a.yaml": crd}, want: true},
		{name: "an APIService under internal/", files: map[string]string{"internal/x/apiservice.yml": apiService("x.cozystack.io")}, want: true},
		{
			name:  "a CRD turned into something else",
			setup: map[string]string{"packages/system/x/crds/a.yaml": crd},
			files: map[string]string{"packages/system/x/crds/a.yaml": "kind: ConfigMap\n"},
			want:  true,
		},
		{name: "a binary file with a marker", files: map[string]string{"packages/system/x/a.yaml": "\x00kind: APIService\n"}, want: true},
		{name: "chart values", files: map[string]string{"packages/system/x/values.yaml": "replicas: 2\n"}},
		{name: "Go types under api/", files: map[string]string{"api/apps/v1alpha1/demo/types.go": "package demo\n"}},
		{name: "a CRD outside the roots apigate walks", files: map[string]string{"hack/crds/a.yaml": crd}},
		{name: "a CRD in a template without a YAML extension", files: map[string]string{"packages/system/x/templates/crd.tpl": crd}},
		{name: "a file beside the ApplicationDefinitions", files: map[string]string{"packages/system/demo-rd/cozyrds/sub/demo.yaml": "kind: ApplicationDefinition\n"}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			setup := maps.Clone(apiBase)
			maps.Copy(setup, tc.setup)
			f.squash(1000, "chore: setup", "", setup)
			mc := f.pr(10+i, "fix(x): y", "", tc.files)
			changes, err := changedFiles(mc+"^1", mc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readsAPI(mc+"^1", mc, changes)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("readsAPI = %v, want %v", got, tc.want)
			}
		})
	}
}

func crdDoc(name, version string) string {
	return "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: " + name +
		"\nspec:\n  group: x.io\n  versions:\n  - name: v1" + version + "\n"
}

func daemonSet(replicas string) string {
	return "apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: agent\nspec:\n  template:\n    spec:\n      priority: {{ .Values.priority | default " + replicas + " }}\n"
}

func templatedCRD(version string) string {
	return "{{- if .Values.crds }}\n" + crdDoc("ts.x.io", version) + "{{- end }}\n"
}

func multiDoc(docs ...string) string { return "---\n" + strings.Join(docs, "---\n") }
