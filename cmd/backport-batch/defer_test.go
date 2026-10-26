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

// openBatch pushes a batch for the fixes merged so far and opens its pull
// request on the fake, with the body the run printed.
func openBatch(t *testing.T, f *fixture, gh *fakeGH) string {
	t.Helper()
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push")
	if code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, stdout)}
	return f.remoteHead(batchBranch1)
}

// reopen points the fake's batch pull request at what the remote now has and
// at the body the tool last wrote.
func reopen(f *fixture, gh *fakeGH) {
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, gh.bodies[len(gh.bodies)-1])}
}

// --defer takes an original out of the open batch and nothing else: what came
// before it keeps its commits, what came after is picked again, a resolution
// pushed by hand survives, and the body records the reason.
func TestDeferRebuildsWithoutTheOriginal(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	mc7 := f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	// A fold for #7, as a maintainer would push it over a conflict.
	f.git("switch", "-q", "--detach", head)
	f.write("app/seven.txt", "7, resolved\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "fix(app): seven (#7), resolved\n\n(cherry picked from commit "+mc7+")")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, gh.prs[0].Body)}
	first := f.git("rev-parse", head+"~2")
	// A commit picked again gets a new committer date, so a new name.
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-27T06:00:00Z")

	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "fails the app e2e", "--push")
	if code != 0 {
		t.Fatalf("run --defer = %d\n%s", code, stderr)
	}
	pushed := f.remoteHead(batchBranch1)
	want := []string{"fix(app): five (#5)", "fix(app): seven (#7)", "fix(app): seven (#7), resolved"}
	if got := f.subjects("origin/release-1.1", pushed); !slices.Equal(got, want) {
		t.Errorf("the rebuilt batch carries %q, want %q", got, want)
	}
	if got := f.git("rev-parse", pushed+"~2"); got != first {
		t.Errorf("the pick of #5 became %.12s, want it kept as %.12s", got, first)
	}
	if got := f.git("show", pushed+":app/seven.txt"); got != "7, resolved" {
		t.Errorf("the resolution was lost: app/seven.txt is %q", got)
	}
	l, _ := parseLine("release-1.1")
	deferred, err := parseDeferred(stdout, l)
	if err != nil || len(deferred) != 1 || deferred[6].Reason != "fails the app e2e" {
		t.Errorf("deferred = %+v, %v; want #6 for its reason", deferred, err)
	}
	if !strings.Contains(stdout, "### Deferred to the next batch") || !strings.Contains(stdout, "`fails the app e2e`") {
		t.Errorf("the body does not show what is deferred and why:\n%s", stdout)
	}
	if !slices.Equal(gh.edited, []string{"#90 fix(backport): batch for release-1.1, 2026-10-26"}) || len(gh.created) != 1 {
		t.Errorf("edited %q and created %q, want the open pull request edited", gh.edited, gh.created)
	}

	// check warns about it instead of failing.
	reopen(f, gh)
	code, _, stderr = f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stderr, "deferred on #90 to the next batch: https://github.com/cozystack/cozystack/pull/6 fix(app): six (fails the app e2e)") {
		t.Errorf("check = %d, want 0 with a warning about #6:\n%s", code, stderr)
	}
}

// The batch that held an original out keeps it out when a run continues it,
// and the next batch picks it again.
func TestDeferHoldsUntilTheNextBatch(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "#6", "--reason", "fails the app e2e", "--push"); code != 0 {
		t.Fatalf("run --defer = %d\n%s", code, stderr)
	}
	reopen(f, gh)
	f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
	f.now = f.now.AddDate(0, 0, 3)

	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
	if code != 0 {
		t.Fatalf("run --continue = %d\n%s", code, stderr)
	}
	want := []string{"fix(app): five (#5)", "fix(app): eight (#8)"}
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, want) {
		t.Errorf("the continued batch carries %q, want %q", got, want)
	}
	l, _ := parseLine("release-1.1")
	if deferred, err := parseDeferred(stdout, l); err != nil || deferred[6].Reason != "fails the app e2e" {
		t.Errorf("the continued body defers %+v, %v; want #6 still held", deferred, err)
	}

	// The batch merges; the next one starts from the line and tries #6 again.
	f.git("push", "-q", "origin", f.remoteHead(batchBranch1)+":refs/heads/release-1.1")
	gh.prs = nil
	code, stdout, stderr = f.exec(gh, "run", "--line", "release-1.1", "--continue")
	if code != 0 {
		t.Fatalf("the next run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("the next batch carries %q, want #6 tried again", got)
	}
	if strings.Contains(stdout, "Deferred to the next batch") {
		t.Errorf("the next batch still holds something out:\n%s", stdout)
	}
}

// A rebuild picks the rest of the batch again onto a line that moved since,
// and refuses when dropping an original leaves a later commit that no longer
// applies, without pushing anything.
func TestDeferOnAMovedLine(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/x.txt": "five\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/x.txt": "five and six\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	moved := f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})

	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "x", "--push")
	if code != 2 || !strings.Contains(stderr, "which picks #6, no longer applies (app/x.txt); defer it as well") {
		t.Errorf("run --defer 5 = %d, want a refusal naming #6:\n%s", code, stderr)
	}
	if got := f.remoteHead(batchBranch1); got != head {
		t.Errorf("the refused rebuild moved the batch to %.12s", got)
	}

	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5,6", "--reason", "x", "--push"); code != 0 {
		t.Fatalf("run --defer 5,6 = %d\n%s", code, stderr)
	}
	pushed := f.remoteHead(batchBranch1)
	if got := f.git("rev-parse", pushed+"^"); got != moved {
		t.Errorf("the rebuild sits on %.12s, want the line's head %.12s", got, moved)
	}
	if got := f.subjects("origin/release-1.1", pushed); !slices.Equal(got, []string{"fix(app): seven (#7)"}) {
		t.Errorf("the rebuilt batch carries %q, want #7", got)
	}
}

// What --defer cannot do is refused before anything is touched.
func TestDeferRefuses(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "feat(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "x"); code != 2 || !strings.Contains(stderr, "no batch pull request is open") {
		t.Errorf("--defer with no batch open = %d (%s), want a refusal", code, stderr)
	}
	head := openBatch(t, f, gh)
	for _, args := range [][]string{
		{"--defer", "5"},
		{"--defer", "5", "--reason", " \n "},
		{"--reason", "x"},
		{"--defer", "five", "--reason", "x"},
		{"--defer", "5,-1", "--reason", "x"},
		{"--defer", "5", "--reason", "x", "--onto", head},
		{"--defer", "5", "--reason", "x", "--continue"},
		{"--defer", "5", "--reason", "x", "--date", "2026-10-26"},
		{"--continue", "--onto", head},
		{"--defer", "6", "--reason", "x"},
		{"--defer", "4", "--reason", "x"},
	} {
		code, _, stderr := f.exec(gh, append([]string{"run", "--line", "release-1.1"}, args...)...)
		if code != 2 {
			t.Errorf("run %q = %d (%s), want 2", args, code, stderr)
		}
	}
	if got := f.remoteHead(batchBranch1); got != head {
		t.Errorf("a refused run moved the batch to %.12s", got)
	}
}

// A rebuild replaces the head it started from and nothing else: one pushed
// since is refused by the tool, and by the remote through the lease.
func TestDeferLeasesTheHeadItRebuilt(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	l, _ := parseLine("release-1.1")
	lineHead := f.git("rev-parse", "origin/release-1.1")
	rebuilt := f.git("rev-parse", head+"~1")
	// Someone pushes on top of the batch after the rebuild read its head.
	f.git("switch", "-q", "--detach", head)
	f.git("commit", "-q", "--allow-empty", "-m", "pushed since")
	since := f.git("rev-parse", "HEAD")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")

	pr := f.batchPR(90, "release-1.1", batchBranch1, "")
	a := &app{gh: gh}
	b := &batch{line: l, lineHead: lineHead, pr: &pr, prHead: head}
	err := a.publish(b, batchBranch1, rebuilt, "title", "body", true)
	if err == nil || !strings.Contains(err.Error(), "which the rebuild started from") {
		t.Errorf("publish over a head pushed since = %v, want a refusal", err)
	}
	if _, err := git(pushArgs(batchBranch1, rebuilt, head)...); err == nil {
		t.Error("the remote took a push whose lease names a head it no longer has")
	}
	if got := f.remoteHead(batchBranch1); got != since {
		t.Errorf("the batch moved to %.12s, want it left at %.12s", got, since)
	}
	// The lease that names the head the remote has is taken, over a
	// non-fast-forward; a plain push of the same is not.
	if _, err := git(pushArgs(batchBranch1, rebuilt, "")...); err == nil {
		t.Error("a push without a lease replaced the batch")
	}
	if _, err := git(pushArgs(batchBranch1, rebuilt, since)...); err != nil {
		t.Errorf("a push leased on the remote's head = %v", err)
	}
	if got := f.remoteHead(batchBranch1); got != rebuilt {
		t.Errorf("the batch is at %.12s, want %.12s", got, rebuilt)
	}
}

// A plain run never replaces the batch, deferred or not: it stays a
// fast-forward.
func TestContinueIsAFastForward(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 0 {
		t.Fatalf("run --continue = %d\n%s", code, stderr)
	}
	if pushed := f.remoteHead(batchBranch1); !isAncestor(head, pushed) || pushed == head {
		t.Errorf("the batch moved from %.12s to %.12s, want a fast-forward", head, pushed)
	}
	if !slices.Equal(gh.edited, []string{"#90 fix(backport): batch for release-1.1, 2026-10-26"}) {
		t.Errorf("edited %q, want #90", gh.edited)
	}
}

// A batch that carries a merge commit is not rebuilt: picking it again would
// lose whatever the merge resolved.
func TestDeferRefusesAMerge(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})
	f.git("switch", "-q", "--detach", head)
	f.git("merge", "-q", "--no-ff", "-m", "Merge release-1.1 into the batch", "origin/release-1.1")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	reopen(f, gh)
	gh.prs[0].Body = gh.bodies[0]

	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "x", "--push")
	if code != 2 || !strings.Contains(stderr, "carries merge commit") {
		t.Errorf("run --defer over a merge = %d, want a refusal:\n%s", code, stderr)
	}
}

// backport/skip added after the batch picked an original keeps the commit
// on the branch until --defer takes it out; the body says so meanwhile.
func TestSkipAfterThePick(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	gh.labels = map[string][]int{labelSkip: {6}}

	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue")
	if code != 0 || !strings.Contains(stdout, "`backport/skip`, still on this branch") {
		t.Errorf("run --continue = %d, want the body to say #6 is still on the branch:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "labelled backport/skip", "--push"); code != 0 {
		t.Fatalf("run --defer of a skipped original = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): five (#5)"}) {
		t.Errorf("the batch carries %q, want #6 taken out", got)
	}
	reopen(f, gh)
	if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 {
		t.Errorf("check = %d\n%s", code, stderr)
	}
}
