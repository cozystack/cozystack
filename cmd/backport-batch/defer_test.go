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
// before it keeps its commits, what came after is picked again in its own
// shape, a resolution pushed by hand survives as the merge it was, and the
// body records the reason.
func TestDeferRebuildsWithoutTheOriginal(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): a line-only seven", map[string]string{"app/seven.txt": "line\n"})
	f.prCommits(5, "fix(app): five", "",
		change{files: map[string]string{"app/five.txt": "5\n"}},
		change{files: map[string]string{"app/five.txt": "5, done\n"}})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	mc7 := f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	l, _ := parseLine("release-1.1")
	if d, err := parseDeferred(gh.prs[0].Body, l); err != nil || len(d[7].Files) == 0 {
		t.Fatalf("#7 is not a deferred conflict: %+v, %v", d, err)
	}
	// A resolution of #7, the way fold accepts one: its commit picked onto the
	// batch head with the conflict resolved, under a merge that names it.
	f.git("switch", "-q", "--detach", head)
	f.write("app/seven.txt", "7, resolved\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "fix(app): seven\n\n(cherry picked from commit "+f.git("rev-parse", mc7+"^2")+")")
	pick := f.git("rev-parse", "HEAD")
	f.git("switch", "-q", "--detach", head)
	f.git("merge", "-q", "--no-ff", "-m", "fix(app): seven (#7)\n\n"+batchTrailer+": "+mc7, pick)
	resolution := f.git("rev-parse", "HEAD")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, gh.prs[0].Body)}
	if code, out, stderr := f.exec(gh, "fold", "--line", "release-1.1", "--original", "7", "--commit", resolution); code != 1 || !strings.Contains(out, "already carries #7") {
		t.Fatalf("fold of the pushed resolution = %d, %q, %s; want it seen as carried", code, out, stderr)
	}
	first := f.git("rev-parse", head+"~1")
	// A commit picked again gets a new committer date, so a new name.
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-27T06:00:00Z")

	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "fails the app e2e", "--push")
	if code != 0 {
		t.Fatalf("run --defer = %d\n%s", code, stderr)
	}
	pushed := f.remoteHead(batchBranch1)
	want := []string{"fix(app): five (#5)", "fix(app): seven (#7)"}
	if got := f.subjects("origin/release-1.1", pushed); !slices.Equal(got, want) {
		t.Errorf("the rebuilt batch carries %q, want %q", got, want)
	}
	if got := f.git("rev-parse", pushed+"~1"); got != first {
		t.Errorf("the merge of #5 became %.12s, want it kept as %.12s", got, first)
	}
	if parents := strings.Fields(f.git("rev-list", "--parents", "-n", "1", pushed)); len(parents) != 3 {
		t.Errorf("#7's resolution came back as %v, want a merge", parents)
	}
	if !strings.HasSuffix(f.git("log", "-1", "--format=%B", pushed), batchTrailer+": "+mc7) {
		t.Error("#7's merge lost its trailer")
	}
	if got := f.git("show", pushed+":app/seven.txt"); got != "7, resolved" {
		t.Errorf("the resolution was lost: app/seven.txt is %q", got)
	}
	if want := "`fix(app): seven`, 1 commit, changed in the pick: `" + f.git("rev-parse", pushed+"^2")[:12] + "`"; !strings.Contains(stdout, want) {
		t.Errorf("the body does not say the resolution differs from #7's commit (%s):\n%s", want, stdout)
	}
	if strings.Contains(stdout, "`fix(app): five`, 2 commits, changed") {
		t.Errorf("the body calls #5's clean picks changed:\n%s", stdout)
	}
	if code, out, stderr := f.exec(gh, "verify", "--line", "release-1.1", "--head", pushed); code != 0 {
		t.Errorf("verify of the rebuilt batch = %d\n%s\n%s", code, out, stderr)
	}
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
	if code != 2 || !strings.Contains(stderr, ", of #6, no longer applies (app/x.txt); defer it as well") {
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

	pr := f.batchPR(90, "release-1.1", batchBranch1, gh.prs[0].Body)
	a := &app{gh: gh}
	b := &batch{line: l, lineHead: lineHead, pr: &pr, prHead: head, src: recordSource{carrier: &pr, open: true, code: &pr}}
	err := a.publish(b, publication{branch: batchBranch1, head: rebuilt, title: "title", body: "body"}, true)
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
	if pushed := f.remoteHead(batchBranch1); !descends(t, head, pushed) || pushed == head {
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

// A batch pull request whose body backport-batch did not write is never
// rewritten, nor its branch pushed: it is a maintainer's.
func TestPublishLeavesAHandWrittenBatch(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	gh.prs[0].Body = "Picked by hand: #5."
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	for _, args := range [][]string{
		{"--continue", "--push"},
		{"--defer", "5", "--reason", "x", "--push"},
	} {
		code, _, stderr := f.exec(gh, append([]string{"run", "--line", "release-1.1"}, args...)...)
		if code != 2 || !strings.Contains(stderr, "backport-batch did not write it") {
			t.Errorf("run %q on a hand-written batch = %d, want a refusal:\n%s", args, code, stderr)
		}
	}
	if got := f.remoteHead(batchBranch1); got != head || len(gh.edited) != 0 {
		t.Errorf("the batch moved to %.12s and %q were edited, want it left alone", got, gh.edited)
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

// closeWorld is a batch carrying #5 alone, opened and pushed.
func closeWorld(t *testing.T) (*fixture, *fakeGH, string) {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	h1 := openBatch(t, f, gh)
	return f, gh, h1
}

// Deferring everything a batch carries closes it: the record says why and
// what it froze while the pull request is still open, then the pull request
// is closed and its branch deleted, leased on that head. The next batch
// tries what was deferred again.
func TestDeferEverythingClosesTheBatch(t *testing.T) {
	f, gh, h1 := closeWorld(t)
	var log []string
	gh.after = map[string]func(){
		"createComment": func() { log = append(log, "comment") },
		"editPull":      func() { log = append(log, "edit") },
		"closePull":     func() { log = append(log, "close, branch at "+f.remoteHead(batchBranch1)) },
	}
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "fails the app e2e", "--push")
	if code != 0 || !strings.Contains(stderr, "closed https://github.com/cozystack/cozystack/pull/90: without what is deferred it carries nothing beyond release-1.1; the next batch tries them again") {
		t.Fatalf("run --defer of the last pick = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	if want := []string{"comment", "edit", "close, branch at " + h1}; !slices.Equal(log, want) || f.remoteHead(batchBranch1) != "" || gh.find(90).State != "closed" {
		t.Errorf("writes %q, branch at %q, #90 %s; want %q, the branch deleted and #90 closed", log, f.remoteHead(batchBranch1), gh.find(90).State, want)
	}
	rec, err := parseRecord(gh.find(90).Body, notifyLine)
	if err != nil || rec.Closing == nil || *rec.Closing != (closing{PR: 90, Branch: batchBranch1, Head: h1}) || rec.Built.Options.Mode != modeClose || rec.deferred()[5].Reason != "fails the app e2e" {
		t.Errorf("the closed batch's record is %+v, %v; want #5 deferred and the close it froze", rec, err)
	}
	if !strings.Contains(stdout, `"closing":{"pr":90,"branch":"`+batchBranch1+`","head":"`+h1+`"}`) {
		t.Errorf("the body printed does not say what the close froze:\n%s", stdout)
	}
	code, _, stderr = f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stderr, "warning: deferred on #90 (closed) to the next batch: https://github.com/cozystack/cozystack/pull/5 ") {
		t.Errorf("check = %d, want 0 with #5 deferred:\n%s", code, stderr)
	}
	gh.nextPR = 91
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): five (#5)"}) || gh.find(91) == nil {
		t.Errorf("the next batch carries %q, want #5 tried again on a new pull request", got)
	}
}

// A close that stopped part way is finished by run --push, and by nothing
// else, and never past a head it did not freeze; nothing is ever written to
// the closed pull request.
func TestCloseRecovery(t *testing.T) {
	deferAll := []string{"run", "--line", "release-1.1", "--defer", "5", "--reason", "fails the app e2e", "--push"}
	push := []string{"run", "--line", "release-1.1", "--continue", "--push"}
	// moveBranch pushes a commit onto the batch branch, or recreates it, as
	// someone else would.
	moveBranch := func(f *fixture, base string) string {
		f.git("switch", "-q", "--detach", base)
		f.git("commit", "-q", "--allow-empty", "-m", "pushed since")
		h2 := f.git("rev-parse", "HEAD")
		f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
		f.git("switch", "-q", "main")
		return h2
	}
	writes := func(gh *fakeGH) int { return len(gh.issueComments) + len(gh.edited) }

	t.Run("stopped after the record, then the branch moved", func(t *testing.T) {
		f, gh, h1 := closeWorld(t)
		gh.after = map[string]func(){"editPull": func() { gh.fail = map[string]error{"pull": errString("HTTP 502")} }}
		if code, _, stderr := f.exec(gh, deferAll...); code != 2 || !strings.Contains(stderr, "HTTP 502") {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		h2 := moveBranch(f, h1)
		before := writes(gh)
		if code, _, stderr := f.exec(gh, push...); code != 2 || !strings.Contains(stderr, h2[:12]) {
			t.Errorf("run --push over a moved branch = %d, want 2 naming it:\n%s", code, stderr)
		}
		if gh.find(90).State != "open" || f.remoteHead(batchBranch1) != h2 || writes(gh) != before {
			t.Error("the run acted on a branch that moved since the close froze it")
		}
	})
	// GitHub and the remote are read apart, and either may be the first to
	// show that someone pushed while the record was written.
	for _, seen := range []string{"by the remote", "by GitHub"} {
		t.Run("the branch moved while the record was written, seen "+seen, func(t *testing.T) {
			f, gh, h1 := closeWorld(t)
			gh.after = map[string]func(){"editPull": func() {
				gh.remoteHead = func(string) string { return "" }
				if seen == "by GitHub" {
					gh.find(90).Head.SHA = strings.Repeat("b", 40)
				} else {
					moveBranch(f, h1)
				}
			}}
			code, _, stderr := f.exec(gh, deferAll...)
			f.git("switch", "-q", "main")
			if code != 2 || !strings.Contains(stderr, "which its record froze") || gh.find(90).State != "open" || f.remoteHead(batchBranch1) == "" {
				t.Errorf("run = %d, #90 %s, branch %q; want 2, #90 open and the branch kept:\n%s", code, gh.find(90).State, f.remoteHead(batchBranch1), stderr)
			}
		})
	}
	t.Run("the close failed", func(t *testing.T) {
		f, gh, _ := closeWorld(t)
		gh.fail = map[string]error{"closePull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, deferAll...); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		before := writes(gh)
		// Anything but run --push leaves it as it is.
		for _, args := range [][]string{
			{"run", "--line", "release-1.1", "--continue"},
			{"check", "--line", "release-1.1"},
			{"fold", "--line", "release-1.1", "--original", "5", "--commit", strings.Repeat("a", 40)},
			{"notify", "--line", "release-1.1", "--push"},
		} {
			if code, _, stderr := f.exec(gh, args...); code != 2 || !strings.Contains(stderr, "being closed") {
				t.Errorf("%q = %d, want 2:\n%s", args, code, stderr)
			}
			f.git("switch", "-q", "main")
		}
		if writes(gh) != before || gh.find(90).State != "open" {
			t.Error("a command other than run --push wrote to the batch")
		}
		code, _, stderr := f.exec(gh, push...)
		if code != 0 || gh.find(90).State != "closed" || f.remoteHead(batchBranch1) != "" || writes(gh) != before {
			t.Errorf("run --push = %d, #90 %s, branch %q, %d writes; want the close finished and nothing else\n%s", code, gh.find(90).State, f.remoteHead(batchBranch1), writes(gh)-before, stderr)
		}
	})
	// A run that finishes a close stops there: the change it was asked for
	// needs an open batch pull request, and none is left to make it on.
	t.Run("a change asked for while the close was left", func(t *testing.T) {
		f, gh, _ := closeWorld(t)
		gh.fail = map[string]error{"closePull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, deferAll...); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		code, _, stderr := f.exec(gh, deferAll...)
		f.git("switch", "-q", "main")
		if code != 2 || !strings.Contains(stderr, "finished the close of #90 and made no other change") || gh.find(90).State != "closed" || f.remoteHead(batchBranch1) != "" {
			t.Errorf("run --defer = %d, #90 %s, branch %q; want 2, the close finished and nothing deferred:\n%s", code, gh.find(90).State, f.remoteHead(batchBranch1), stderr)
		}
	})
	t.Run("closed by hand while the close writes", func(t *testing.T) {
		f, gh, h1 := closeWorld(t)
		gh.after = map[string]func(){"createComment": func() { gh.find(90).State = "closed" }}
		code, _, stderr := f.exec(gh, deferAll...)
		f.git("switch", "-q", "main")
		if code != 2 || !strings.Contains(stderr, "batch pull request #90 is closed now; nothing is written to it") || len(gh.edited) != 0 || f.remoteHead(batchBranch1) != h1 {
			t.Errorf("run = %d, edited %q, branch %q; want 2, no edit and the branch kept:\n%s", code, gh.edited, f.remoteHead(batchBranch1), stderr)
		}
	})
	t.Run("the delete failed", func(t *testing.T) {
		f, gh, h1 := closeWorld(t)
		gh.after = map[string]func(){"closePull": func() { f.refusePushes(true) }}
		if code, _, stderr := f.exec(gh, deferAll...); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		f.refusePushes(false)
		code, _, stderr := f.exec(gh, "check", "--line", "release-1.1")
		if code != 0 || !strings.Contains(stderr, "warning: batch pull request #90 is closed, but its branch "+batchBranch1+" is still at "+h1[:12]) {
			t.Errorf("check = %d, want 0 naming the branch:\n%s", code, stderr)
		}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue"); code != 2 {
			t.Errorf("run without --push = %d, want 2:\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		if code, _, stderr := f.exec(gh, push...); code != 0 || f.remoteHead(batchBranch1) != "" {
			t.Errorf("run --push = %d, branch %q; want it deleted\n%s", code, f.remoteHead(batchBranch1), stderr)
		}
		mustRun(t, f, gh, "check", "--line", "release-1.1")
	})
	// A batch pull request a person opens on the branch a close left owns
	// it: neither recovery nor check offers to delete it, moved or not. One
	// on another branch does not stop the delete.
	for _, tc := range []struct {
		name   string
		branch string
		moved  bool
	}{
		{"a batch opened on the branch the close left", batchBranch1, false},
		{"a batch opened on the branch the close left, moved since", batchBranch1, true},
		{"a batch opened on another branch", "backport-batch/release-1.1-20261027", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, gh, h1 := closeWorld(t)
			gh.after = map[string]func(){"closePull": func() { f.refusePushes(true) }}
			if code, _, stderr := f.exec(gh, deferAll...); code != 2 {
				t.Fatalf("run = %d\n%s", code, stderr)
			}
			f.git("switch", "-q", "main")
			f.refusePushes(false)
			gh.after = nil
			if tc.branch != batchBranch1 {
				f.git("push", "-q", "origin", h1+":refs/heads/"+tc.branch)
			}
			left := h1
			if tc.moved {
				left = moveBranch(f, h1)
			}
			gh.prs = append(gh.prs, f.batchPR(91, "release-1.1", tc.branch, "a maintainer's batch"))
			taken := tc.branch == batchBranch1
			_, _, stderr := f.exec(gh, "check", "--line", "release-1.1")
			f.git("switch", "-q", "main")
			if named := strings.Contains(stderr, "batch pull request #91 is open on "+batchBranch1); named != taken || taken == strings.Contains(stderr, "--push deletes it") || taken && strings.Contains(stderr, "git push origin --delete") {
				t.Errorf("check names the branch as #91's: %v, want %v, and offers no delete of it:\n%s", named, taken, stderr)
			}
			code, _, stderr := f.exec(gh, push...)
			f.git("switch", "-q", "main")
			if taken && (code != 2 || !strings.Contains(stderr, "batch pull request #91 is open on "+batchBranch1) || strings.Contains(stderr, "git push origin --delete") || f.remoteHead(batchBranch1) != left || gh.find(91).State != "open") {
				t.Errorf("run --push = %d, branch %q; want 2, no delete offered and #91's branch kept:\n%s", code, f.remoteHead(batchBranch1), stderr)
			}
			if !taken && (code != 0 || f.remoteHead(batchBranch1) != "") {
				t.Errorf("run --push = %d, branch %q; want the left branch deleted:\n%s", code, f.remoteHead(batchBranch1), stderr)
			}
		})
	}
	t.Run("the branch moved after the close", func(t *testing.T) {
		f, gh, h1 := closeWorld(t)
		var h2 string
		gh.after = map[string]func(){"closePull": func() { h2 = moveBranch(f, h1) }}
		if code, _, stderr := f.exec(gh, deferAll...); code != 2 || !strings.Contains(stderr, "is at "+h2[:12]+" now") {
			t.Fatalf("run = %d, want 2 naming where the branch moved\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		// What was pushed onto the branch may be work the next run cannot
		// pick again, so the refusal promises nothing about deleting it.
		if code, _, stderr := f.exec(gh, push...); code != 2 || !strings.Contains(stderr, h1[:12]) || !strings.Contains(stderr, h2[:12]) || !strings.Contains(stderr, "#90") || strings.Contains(stderr, "nothing on it is lost") || !strings.Contains(stderr, "check what "+h2[:12]+" carries") {
			t.Errorf("run --push = %d, want 2 naming both heads and #90, and what to check before deleting:\n%s", code, stderr)
		}
		if f.remoteHead(batchBranch1) != h2 {
			t.Error("the run moved a branch it did not freeze")
		}
		if _, err := git(deleteArgs(batchBranch1, h1)...); err == nil || f.remoteHead(batchBranch1) != h2 {
			t.Error("the remote took a delete whose lease names a head it no longer has")
		}
	})
	t.Run("a batch of the same day that opened no pull request", func(t *testing.T) {
		f, gh, _ := closeWorld(t)
		if code, _, stderr := f.exec(gh, deferAll...); code != 0 || f.remoteHead(batchBranch1) != "" {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		// The same day, a new batch reuses the branch name, pushes, and
		// fails to open its pull request.
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		gh.fail = map[string]error{"createPull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, push...); code != 2 {
			t.Fatalf("the same-day run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		h2 := f.remoteHead(batchBranch1)
		for _, args := range [][]string{push, {"run", "--line", "release-1.1", "--onto", h2, "--push"}} {
			if code, _, stderr := f.exec(gh, args...); code != 2 || !strings.Contains(stderr, "git push origin --delete "+batchBranch1) {
				t.Errorf("%q = %d, want 2 naming the branch to delete:\n%s", args, code, stderr)
			}
			f.git("switch", "-q", "main")
		}
		if f.remoteHead(batchBranch1) != h2 || len(gh.created) != 1 {
			t.Errorf("the branch moved to %.12s or a pull request was opened: %q", f.remoteHead(batchBranch1), gh.created)
		}
		f.git("push", "-q", "origin", "--delete", batchBranch1)
		gh.nextPR = 91
		mustRun(t, f, gh, push...)
		if gh.find(91) == nil {
			t.Error("after the branch was deleted the next run opened no batch")
		}
	})
	t.Run("a preview", func(t *testing.T) {
		f, gh, _ := closeWorld(t)
		gh.fail = map[string]error{"closePull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, deferAll...); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		before := writes(gh)
		code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "x")
		if code != 2 || !strings.Contains(stderr, "run --line release-1.1 --push finishes") || writes(gh) != before || gh.find(90).State != "open" {
			t.Errorf("a preview = %d, want 2 naming the step left and nothing written:\n%s", code, stderr)
		}
	})
}

// A batch that merged on GitHub and left its branch behind is named when a
// run of the same day refuses to push over that branch.
func TestRefusalNamesTheMergedBatchsBranch(t *testing.T) {
	f, gh, _ := closeWorld(t)
	head := f.remoteHead(batchBranch1)
	mergeBatch(f, gh)
	// A commit pushed onto the batch after it merged is left on its branch.
	f.git("switch", "-q", "--detach", head)
	f.git("commit", "-q", "--allow-empty", "-m", "pushed after the merge")
	pushed := f.git("rev-parse", "HEAD")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
	if code != 2 || !strings.Contains(stderr, "it is the branch of batch pull request #90, which is merged") || strings.Contains(stderr, "--onto") || !strings.Contains(stderr, "check what "+pushed[:12]+" carries") {
		t.Errorf("run --push = %d, want a refusal naming #90:\n%s", code, stderr)
	}
}
