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

// verdictOn prepares the line and returns the verdict on one original.
func verdictOn(t *testing.T, f *fixture, gh *fakeGH, lineName string, n int) *verdict {
	t.Helper()
	f.git("fetch", "-q", "origin")
	l, _ := parseLine(lineName)
	if gh == nil {
		gh = &fakeGH{}
	}
	b, err := (&app{gh: gh, stderr: &strings.Builder{}}).prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	v := b.find(n)
	if v == nil {
		t.Fatalf("#%d is not an original for %s", n, lineName)
	}
	return v
}

func TestPresenceOnTheLine(t *testing.T) {
	const fix = "fix(app): the change"
	files := map[string]string{"app/x.txt": "fixed\n", "app/y.txt": "also\n"}
	cases := []struct {
		name string
		// land puts something on release-1.1 for the original merged as mc,
		// whose own commit is pr.
		land    func(f *fixture, mc, pr string)
		carried string
	}{
		{
			name: "nothing on the line",
			land: func(*fixture, string, string) {},
		},
		{
			name: "an -x line naming the merge commit",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", fix+"\n\n(cherry picked from commit "+mc+")", map[string]string{"app/x.txt": "fixed\n"})
			},
			carried: "picked in",
		},
		{
			name: "an -x line naming the pull request's only commit",
			land: func(f *fixture, _, pr string) {
				f.onLine("release-1.1", "change\n\n(cherry picked from commit "+pr+")", map[string]string{"app/x.txt": "fixed\n"})
			},
			carried: "its 1 commit picked",
		},
		{
			name: "an -x line among other trailers",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", "port\n\nSigned-off-by: A <a@example.invalid>\n(cherry picked from commit "+mc+")\nSigned-off-by: B <b@example.invalid>", map[string]string{"app/x.txt": "fixed\n"})
			},
			carried: "picked in",
		},
		{
			name: "an abbreviated -x line",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", "port\n\n(cherry picked from commit "+mc[:12]+")", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "an -x line in the body, not in the trailers",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", "port\n\n(cherry picked from commit "+mc+")\n\nThe rest of it is still to come.", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "an -x line inside a prose paragraph at the end",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", "port\n\nThis is not (cherry picked from commit "+mc+")\nbut a sentence.", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "a squash-merged hand port that says Backport of, from before the batch-only day",
			land: func(f *fixture, _, _ string) {
				f.t.Setenv("GIT_COMMITTER_DATE", "2026-10-01T00:00:00Z")
				f.onLine("release-1.1", "[Backport release-1.1] fix(app): the change (#77)\n\n# Description\nBackport of #5 to `release-1.1`.", map[string]string{"app/x.txt": "fixed\n"})
			},
			carried: `named by "Backport of"`,
		},
		{
			name: "the same after the batch-only day",
			land: func(f *fixture, _, _ string) {
				f.t.Setenv("GIT_COMMITTER_DATE", "2026-10-27T00:00:00Z")
				f.onLine("release-1.1", "[Backport release-1.1] fix(app): the change (#77)\n\n# Description\nBackport of #5 to `release-1.1`.", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "an -x line followed by prose in its paragraph",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", "port\n\n(cherry picked from commit "+mc+")\nand more words", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "an -x line in a paragraph git reads as trailers that holds prose too",
			land: func(f *fixture, mc, _ string) {
				f.onLine("release-1.1", "port\n\nFixes the thing.\nSigned-off-by: A <a@example.invalid>\n(cherry picked from commit "+mc+")\nSigned-off-by: B <b@example.invalid>", map[string]string{"app/x.txt": "fixed\n"})
			},
			carried: "picked in",
		},
		{
			name: "Backport of another repository's number",
			land: func(f *fixture, _, _ string) {
				f.onLine("release-1.1", "port\n\nBackport of other/repo#5.", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "the same change under another name",
			land: func(f *fixture, _, _ string) {
				f.onLine("release-1.1", "fix(app): unrelated title", files)
			},
			carried: "a pick would change nothing",
		},
		{
			name: "half of the change, named nowhere",
			land: func(f *fixture, _, _ string) {
				f.onLine("release-1.1", "fix(app): half", map[string]string{"app/x.txt": "fixed\n"})
			},
		},
		{
			name: "an -x line naming some other commit",
			land: func(f *fixture, _, _ string) {
				f.onLine("release-1.1", "port\n\n(cherry picked from commit 0123456789abcdef0123456789abcdef01234567)", map[string]string{"app/z.txt": "z\n"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.lines()
			mc := f.pr(5, fix, "", files)
			pr := f.git("rev-parse", mc+"^2")
			tc.land(f, mc, pr)
			v := verdictOn(t, f, nil, "release-1.1", 5)
			switch {
			case tc.carried == "" && v.rule != 0:
				t.Errorf("verdict = rule %d (%s), want it to qualify", v.rule, v.reason)
			case tc.carried != "" && (v.rule != ruleOnLine || !strings.HasPrefix(v.reason, tc.carried)):
				t.Errorf("verdict = rule %d (%s), want rule 1 with %q", v.rule, v.reason, tc.carried)
			}
		})
	}
}

// History the line shares with main is not evidence: a commit from before
// the line branched can say "Backport of #5" without the line carrying #5.
func TestSharedHistoryIsNotEvidence(t *testing.T) {
	f := newFixture(t)
	f.release("release-1.0")
	f.pr(1, "docs(app): plan", "Backport of #5 is planned.", map[string]string{"app/plan.txt": "p\n"})
	f.release("release-1.1")
	f.pr(5, "fix(app): the change", "", map[string]string{"app/x.txt": "fixed\n"})
	if v := verdictOn(t, f, nil, "release-1.1", 5); v.rule != 0 {
		t.Errorf("verdict = rule %d (%s), want it to qualify", v.rule, v.reason)
	}
}

// Only a batch pull request opened from this repository by someone with write
// access, or by GitHub Actions' bot, speaks for the batch.
func TestCarriedByTheBatch(t *testing.T) {
	cases := []struct {
		name     string
		edit     func(pr *pullRequest, gh *fakeGH)
		carried  bool
		notAsked bool // the author's permission must not be looked up
	}{
		{name: "a maintainer with write access", carried: true},
		{name: "an admin", edit: func(_ *pullRequest, gh *fakeGH) { gh.perms = map[string]string{"maintainer": "admin"} }, carried: true},
		{name: "a private member reported as a contributor, with write access",
			edit: func(pr *pullRequest, _ *fakeGH) { pr.AuthorAssociation = "CONTRIBUTOR" }, carried: true},
		{name: "an organization member with read access",
			edit: func(pr *pullRequest, gh *fakeGH) { gh.perms = map[string]string{"maintainer": "read"} }},
		{name: "an invited collaborator with read access", edit: func(pr *pullRequest, gh *fakeGH) {
			pr.AuthorAssociation = "COLLABORATOR"
			gh.perms = map[string]string{"maintainer": "read"}
		}},
		{name: "no access", edit: func(_ *pullRequest, gh *fakeGH) { gh.perms = map[string]string{"maintainer": "none"} }},
		{name: "a permission GitHub cannot be asked for", edit: func(_ *pullRequest, gh *fakeGH) { gh.permErr = errString("HTTP 502") }},
		{name: "GitHub Actions' bot", edit: func(pr *pullRequest, _ *fakeGH) {
			pr.User.Login, pr.User.Type, pr.AuthorAssociation = "github-actions[bot]", "Bot", "NONE"
		}, carried: true, notAsked: true},
		{name: "another bot", edit: func(pr *pullRequest, _ *fakeGH) { pr.User.Login, pr.User.Type = "renovate[bot]", "Bot" }, notAsked: true},
		{name: "a user named like the bot", edit: func(pr *pullRequest, _ *fakeGH) {
			pr.User.Login, pr.User.Type = "github-actions[bot]", "User"
		}, notAsked: true},
		{name: "a login shaped like a path", edit: func(pr *pullRequest, _ *fakeGH) { pr.User.Login = "maintainer/../../orgs/x" }, notAsked: true},
		{name: "a fork whose branch is named like a batch", edit: func(pr *pullRequest, _ *fakeGH) { pr.Head.Repo.FullName = "someone/cozystack" }, notAsked: true},
		{name: "a deleted fork", edit: func(pr *pullRequest, _ *fakeGH) { pr.Head.Repo = nil }, notAsked: true},
		{name: "a branch for another line", edit: func(pr *pullRequest, _ *fakeGH) { pr.Head.Ref = "backport-batch/release-1.0-20261026" }, notAsked: true},
		{name: "a branch not named for a batch", edit: func(pr *pullRequest, _ *fakeGH) { pr.Head.Ref = "my-backports" }, notAsked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.lines()
			mc := f.pr(5, "fix(app): the change", "", map[string]string{"app/x.txt": "fixed\n"})
			f.git("switch", "-q", "-c", "batch", "origin/release-1.1")
			f.git("cherry-pick", "-x", "-m", "1", mc)
			f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
			f.git("switch", "-q", "main")
			pr := f.batchPR(90, "release-1.1", batchBranch1, "")
			gh := &fakeGH{}
			if tc.edit != nil {
				tc.edit(&pr, gh)
			}
			gh.prs = []pullRequest{pr}
			v := verdictOn(t, f, gh, "release-1.1", 5)
			if (v.onBatch != "") != tc.carried {
				t.Errorf("onBatch = %q, want carried=%v", v.onBatch, tc.carried)
			}
			if tc.notAsked && len(gh.asked) > 0 {
				t.Errorf("asked GitHub for the permission of %q", gh.asked)
			}
		})
	}
}

// The batch head is the one GitHub reports: a branch that moved since is read
// again, and one that does not match even then is refused.
func TestBatchHeadMustBeGitHubs(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): the change", "", map[string]string{"app/x.txt": "fixed\n"})
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	pr := f.batchPR(90, "release-1.1", batchBranch1, "")
	pr.Head.SHA = strings.Repeat("a", 40)
	f.git("fetch", "-q", "origin")
	l, _ := parseLine("release-1.1")
	_, err := (&app{gh: &fakeGH{prs: []pullRequest{pr}}, stderr: &strings.Builder{}}).prepare(l)
	if err == nil || !strings.Contains(err.Error(), "as GitHub reports") {
		t.Errorf("prepare = %v, want a refusal of the moved head", err)
	}
}

func TestTwoBatchPullRequestsAreRefused(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/backport-batch/release-1.1-20261022")
	gh := &fakeGH{prs: []pullRequest{
		f.batchPR(90, "release-1.1", batchBranch1, ""),
		f.batchPR(91, "release-1.1", "backport-batch/release-1.1-20261022", ""),
	}}
	code, _, stderr := f.exec(gh, "check", "--line", "release-1.1")
	if code != 2 || !strings.Contains(stderr, "more than one open batch") {
		t.Errorf("check = %d (%s), want 2", code, stderr)
	}
}

func TestBodyOrigins(t *testing.T) {
	cases := []struct {
		body string
		want []int
	}{
		{"Backport of #4253 to `release-1.6`.", []int{4253}},
		{"Backport of #1, #2, and #3", []int{1, 2, 3}},
		{"Backport of #4253 to release-1.6, together with #3460", []int{4253, 3460}},
		{"Backport of #10, #20 is not included", []int{10}},
		{"Backport of cozystack/cozystack#7.", []int{7}},
		{"Backport of other/repo#7.", nil},
		{"Fixes #12; see #13", nil},
	}
	for _, tc := range cases {
		if got := bodyOrigins(tc.body, upstreamRepo); !slices.Equal(got, tc.want) {
			t.Errorf("bodyOrigins(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
