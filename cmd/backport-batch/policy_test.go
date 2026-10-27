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
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParsePolicy(t *testing.T) {
	got, err := parsePolicy("text\n\n" + policyFence + "\n# comment\n\nrelease-1.6: 2026-10-26\nrelease-1.5:2026-01-02\nbatch-only: 2026-10-20\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.starts) != 2 || !got.starts["release-1.6"].Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("parsePolicy = %v", got)
	}
	if !got.batchOnly.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the batch-only day = %v, want 2026-10-20", got.batchOnly)
	}
	if got, err := parsePolicy(policyFence + "\nbatch-only: 2026-10-20\n```\n"); err != nil || len(got.starts) != 0 {
		t.Errorf("a block with no line = %v, %v; want no starts", got, err)
	}
	for name, page := range map[string]string{
		"no batch-only day":    policyFence + "\nrelease-1.6: 2026-10-26\n```\n",
		"two batch-only days":  policyFence + "\nbatch-only: 2026-10-26\nbatch-only: 2026-10-27\n```\n",
		"a bad batch-only day": policyFence + "\nbatch-only: soon\n```\n",
		"no block":             "## Backports\n",
		"two blocks":           policyFence + "\n```\n" + policyFence + "\n```\n",
		"unclosed":             policyFence + "\nrelease-1.6: 2026-10-26\n",
		"a bad line":           policyFence + "\nrelease 1.6 on 2026-10-26\n```\n",
		"a bad date":           policyFence + "\nrelease-1.6: 2026-13-40\n```\n",
		"a padded line":        policyFence + "\nrelease-01.6: 2026-10-26\n```\n",
		"listed twice":         policyFence + "\nrelease-1.6: 2026-10-26\nrelease-1.6: 2026-10-27\n```\n",
		"not a line":           policyFence + "\nmain: 2026-10-26\n```\n",
		"trailing words":       policyFence + "\nrelease-1.6: 2026-10-26 or so\n```\n",
	} {
		if _, err := parsePolicy(page); err == nil {
			t.Errorf("%s: parsePolicy accepted it", name)
		}
	}
}

// A line listed in the policy takes nothing merged before its start, unless a
// maintainer lets an original in; any other line starts at its fork point.
func TestPolicyStart(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "feat(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{labels: map[string][]int{labelInclude: {6, 7}}}

	verdicts := func() map[int]*verdict {
		t.Helper()
		f.git("fetch", "-q", "origin")
		l, _ := parseLine("release-1.1")
		b, err := (&app{gh: gh, stderr: &strings.Builder{}}).prepare(l)
		if err != nil {
			t.Fatal(err)
		}
		return map[int]*verdict{5: b.find(5), 6: b.find(6), 7: b.find(7)}
	}
	if v := verdicts(); v[5].rule != 0 || v[6].rule != 0 || v[7].rule != 0 {
		t.Errorf("with no start every original qualifies: %d %d %d", v[5].rule, v[6].rule, v[7].rule)
	}

	f.setPolicy("release-1.1: 2099-01-01\n")
	v := verdicts()
	if v[5].rule != ruleBeforeStart {
		t.Errorf("#5 = rule %d (%s), want merged before the start", v[5].rule, v[5].reason)
	}
	if v[6].rule != 0 || v[7].rule != 0 {
		t.Errorf("backport/include lifts the start: #6 = rule %d, #7 = rule %d", v[6].rule, v[7].rule)
	}

	f.setPolicy("release-1.0: 2099-01-01\n")
	if v := verdicts(); v[5].rule != 0 {
		t.Errorf("a start for another line kept #5 out of release-1.1: rule %d", v[5].rule)
	}

	f.setPolicy("release-1.1: 2000-01-01\n")
	if v := verdicts(); v[5].rule != 0 {
		t.Errorf("a start before the fork point kept #5 out: rule %d", v[5].rule)
	}
}

// The policy is read from main as the run pins it, or from the ref --policy
// names, and a run without a readable policy stops.
func TestPolicyRef(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.git("switch", "-q", "-c", "proposal")
	f.write(policyPage, policyWith("release-1.1: 2099-01-01\n"))
	f.git("commit", "-q", "-am", "docs: start release-1.1 later")
	f.git("switch", "-q", "main")

	code, stdout, stderr := f.exec(nil, "check", "--line", "release-1.1")
	if code != 1 || !strings.Contains(stdout, "/pull/5 ") {
		t.Errorf("check against main's policy = %d, %q, %s; want #5 missing", code, stdout, stderr)
	}
	code, stdout, stderr = f.exec(nil, "check", "--line", "release-1.1", "--policy", "proposal")
	if code != 0 {
		t.Errorf("check against the proposal's policy = %d, %q, %s; want #5 before the start", code, stdout, stderr)
	}
	if code, _, _ := f.exec(nil, "check", "--line", "release-1.1", "--policy", "--upload-pack=x"); code != 2 {
		t.Errorf("--policy with an option = %d, want 2", code)
	}

	f.squash(9001, "docs: drop the page", "", map[string]string{policyPage: ""})
	if code, _, stderr := f.exec(nil, "check", "--line", "release-1.1"); code != 2 || !strings.Contains(stderr, "reading the policy") {
		t.Errorf("check without a policy page = %d (%s), want 2", code, stderr)
	}
}

// The policy page and the code say the same thing: the start block parses as
// the batch reads it, rule 2 names the types the code takes, the page names
// the scopes the code keeps out, and both override labels.
func TestPolicyPageMatchesCode(t *testing.T) {
	data, err := os.ReadFile("../../" + policyPage)
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	pol, err := parsePolicy(page)
	if err != nil {
		t.Fatalf("the page's policy block does not parse: %v", err)
	}
	if day, ok := pol.starts["release-1.6"]; !ok || day.Format(time.DateOnly) != "2026-10-26" {
		t.Errorf("release-1.6 starts %v, want 2026-10-26", pol.starts["release-1.6"])
	}
	if day := pol.batchOnly.Format(time.DateOnly); day != "2026-10-26" {
		t.Errorf("the batch-only day is %s, want 2026-10-26", day)
	}

	rule2 := regexp.MustCompile("(?m)^2\\. Its title type is (.*)$").FindStringSubmatch(page)
	if rule2 == nil {
		t.Fatal("the page has no rule 2")
	}
	var types []string
	for _, m := range regexp.MustCompile("`([a-z]+)`").FindAllStringSubmatch(rule2[1], -1) {
		types = append(types, m[1])
	}
	if !slices.Equal(types, takenTypes) {
		t.Errorf("rule 2 on the page takes %v, the code %v", types, takenTypes)
	}
	never := regexp.MustCompile("(?m)^### What never goes into a patch release\\n\\n(.*)$").FindStringSubmatch(page)
	if never == nil {
		t.Fatal("the page has no list of what never goes into a patch release")
	}
	for _, scope := range excludedScopes {
		if !strings.Contains(never[1], "(scope `"+scope+"`)") {
			t.Errorf("the page does not keep scope %s out", scope)
		}
	}
	if scopes := regexp.MustCompile("scope `([a-z]+)`").FindAllStringSubmatch(never[1], -1); len(scopes) != len(excludedScopes) {
		t.Errorf("the page keeps %d scopes out, the code %d", len(scopes), len(excludedScopes))
	}
	for i := 1; i <= 6; i++ {
		if !regexp.MustCompile("(?m)^" + string(rune('0'+i)) + "\\. ").MatchString(page) {
			t.Errorf("the page has no rule %d", i)
		}
	}
	for _, label := range []string{labelInclude, labelSkip} {
		if !strings.Contains(page, "`"+label+"`") {
			t.Errorf("the page does not describe %s", label)
		}
	}
}
