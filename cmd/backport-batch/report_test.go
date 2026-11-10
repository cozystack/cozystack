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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
)

// reportsPosted lists the report IDs the comments carry, in the order they
// were posted, whoever wrote them.
func reportsPosted(gh *fakeGH) []string {
	var ids []string
	for _, c := range gh.issueComments {
		if m := reportMarkerRE.FindStringSubmatch(c.Body); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// bodyReportOf is the report a body on release-1.1, or on the line given,
// carries, failing the test without one.
func bodyReportOf(t *testing.T, body string, on ...line) storedReport {
	t.Helper()
	l := notifyLine
	if len(on) > 0 {
		l = on[0]
	}
	s, err := bodyReport(body, l)
	if err != nil || s == nil {
		t.Fatalf("the body carries no readable report: %v\n%s", err, body)
	}
	return *s
}

// refusePushes makes the remote refuse every push, as a lost connection or
// a protected branch would, until it is called with false.
func (f *fixture) refusePushes(on bool) {
	f.t.Helper()
	hook := filepath.Join(f.remote, "hooks", "pre-receive")
	if !on {
		if err := os.Remove(hook); err != nil {
			f.t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func mustRun(t *testing.T, f *fixture, gh *fakeGH, args ...string) string {
	t.Helper()
	code, stdout, stderr := f.exec(gh, args...)
	if code != 0 {
		t.Fatalf("%q = %d\n%s", args, code, stderr)
	}
	f.git("switch", "-q", "main")
	return stdout
}

// A report comment keeps each classification: one the next run replaced in
// the body is still read back by its ID, also once the batch merged, which
// nothing writes to.
func TestReportSurvivesContinuation(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): a line-only five", map[string]string{"app/five.txt": "line\n"})
	mc5 := f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	r1 := bodyReportOf(t, gh.prs[0].Body).doc.ID
	f.onLine("release-1.1", "fix(app): five, by hand\n\n(cherry picked from commit "+mc5+")", map[string]string{"app/five.txt": "5\n"})
	f.now = f.now.AddDate(0, 0, 1)
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
	r2 := bodyReportOf(t, gh.find(90).Body).doc.ID
	if r1 == r2 || !slices.Equal(reportsPosted(gh), []string{r1, r2}) {
		t.Fatalf("reports %q; want R1 %.12s, then R2 %.12s", reportsPosted(gh), r1, r2)
	}
	read := func(id, want string) {
		t.Helper()
		code, stdout, stderr := f.exec(gh, "report", "--pr", "90", "--id", id)
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("report --id %.12s = %d, want %q:\n%s%s", id, code, want, stdout, stderr)
		}
	}
	for _, merged := range []bool{false, true} {
		if merged {
			mergeBatch(f, gh)
		}
		read(r1, "#5 deferred: conflict at ")
		read(r2, "#5 skipped.on-line")
		read(r2, "#6 picked")
	}
}

// Two runs that find the same post one report, and the body carries the one
// kept, whatever time and job the second ran in. A look-alike someone else
// wrote is no report, and a kept report edited since stops the reader.
func TestReportDedupAndTrust(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "cozystack/cozystack")
	t.Setenv("GITHUB_RUN_ID", "100")

	// The report the next run makes, read off a run that publishes nothing,
	// planted by someone the batch does not trust.
	preview := bodyReportOf(t, mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue"))
	planted, err := reportComment(preview)
	if err != nil {
		t.Fatal(err)
	}
	look := issueComment{ID: 7, Body: planted}
	look.User.Login, look.User.Type = "someone", "User"
	gh.issueComments = append(gh.issueComments, look)

	f.now = f.now.AddDate(0, 0, 1)
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
	kept := bodyReportOf(t, gh.find(90).Body)
	if kept.doc.ID != preview.doc.ID {
		t.Fatalf("the run made %.12s, the preview %.12s", kept.doc.ID, preview.doc.ID)
	}
	f.now = f.now.AddDate(0, 0, 1)
	t.Setenv("GITHUB_RUN_ID", "101")
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
	posted := reportsPosted(gh)
	if n := len(slices.DeleteFunc(slices.Clone(posted), func(id string) bool { return id != kept.doc.ID })); n != 2 || len(posted) != 3 {
		t.Errorf("reports %q; want the first, the look-alike and the run's own once", posted)
	}
	body := gh.find(90).Body
	again := bodyReportOf(t, body)
	rec, err := parseRecord(body, notifyLine)
	if err != nil || again.raw != kept.raw || rec.Report != again.doc.ID || !strings.Contains(again.raw, `"run":"https://github.com/cozystack/cozystack/actions/runs/100"`) {
		t.Errorf("the second run's body carries %s with record report %v (%v); want the kept bytes of run 100", again.raw, rec, err)
	}

	// The reader takes the run's own comment, not the look-alike.
	if code, stdout, stderr := f.exec(gh, "report", "--pr", "90", "--id", kept.doc.ID); code != 0 || !strings.Contains(stdout, "#6 picked") {
		t.Errorf("report = %d:\n%s%s", code, stdout, stderr)
	}
	for i := range gh.issueComments {
		c := &gh.issueComments[i]
		if c.User.Login == "maintainer" && strings.Contains(c.Body, kept.doc.ID) {
			c.Body = strings.Replace(c.Body, `"picked":[5,6]`, `"picked":[5]`, 1)
		}
	}
	if code, stdout, stderr := f.exec(gh, "report", "--pr", "90", "--id", kept.doc.ID); code != 2 || !strings.Contains(stderr, "does not match") {
		t.Errorf("report of an edited comment = %d, want 2:\n%s%s", code, stdout, stderr)
	}
}

// The report holds no pull request number, so a new batch's report is whole
// before GitHub numbers the pull request, and the reader finds it on
// whatever number it got.
func TestFirstReportIDIsKnownBeforeTheNumber(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{nextPR: 4242}
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--push")
	inBody := bodyReportOf(t, gh.bodies[0])
	// Every list is one, empty or not, so equal reports are equal bytes:
	// main here has no commit that merged no pull request.
	if !strings.Contains(inBody.raw, `"other":[]`) {
		t.Errorf("the report writes its empty list of other commits as something else:\n%s", inBody.raw)
	}
	if !slices.Equal(gh.commented, []int{4242}) || !slices.Equal(reportsPosted(gh), []string{inBody.doc.ID}) {
		t.Fatalf("commented on %v with %q, want #4242 with %.12s", gh.commented, reportsPosted(gh), inBody.doc.ID)
	}
	found, bad, err := findReports(gh, 4242, notifyLine)
	if err != nil || len(bad) != 0 || found[inBody.doc.ID].raw != inBody.raw {
		t.Errorf("findReports = %v, %v, %v; want the body's report", found, bad, err)
	}
	if code, stdout, stderr := f.exec(gh, "report", "--pr", "4242"); code != 0 || !strings.Contains(stdout, "report "+inBody.doc.ID) || !strings.Contains(stdout, "#4242's body carries this report") {
		t.Errorf("report --pr 4242 = %d:\n%s%s", code, stdout, stderr)
	}
}

// Every original main merged since the line branched is in exactly one
// category of the report, a commit that merged no pull request is counted
// apart, and the report says so whatever the run had to do.
func TestReportPartition(t *testing.T) {
	f := newFixture(t)
	f.lines()
	gh := &fakeGH{labels: map[string][]int{labelSkip: {9}}}
	// onNewer puts an original on release-1.1, as the newer line's batch
	// would, so rule 6 lets it onto release-1.0.
	onNewer := func(mc string, files map[string]string) {
		f.onLine("release-1.1", "fix: by the newer batch\n\n(cherry picked from commit "+mc+")", files)
	}
	pr := func(n int, title string, files map[string]string, newer bool) {
		mc := f.pr(n, title, "", files)
		if newer {
			onNewer(mc, files)
		}
	}
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	f.pr(4, "fix(app): four, before the start", "", map[string]string{"app/four.txt": "4\n"})
	os.Unsetenv("GIT_COMMITTER_DATE")
	f.setPolicy("release-1.0: 2026-06-01\n")
	f.onLine("release-1.0", "fix(app): a line-only eleven", map[string]string{"app/eleven.txt": "line\n"})
	pr(5, "fix(app): five", map[string]string{"app/five.txt": "5\n"}, true)
	pr(6, "fix(app): six, not on the newer line", map[string]string{"app/six.txt": "6\n"}, false)
	pr(7, "fix(app)!: seven", map[string]string{"app/seven.txt": "7\n"}, true)
	pr(8, "fix(platform): eight", map[string]string{migrationsDir + "42": "#!/bin/sh\n"}, true)
	pr(9, "fix(app): nine", map[string]string{"app/nine.txt": "9\n"}, true)
	mc10 := f.pr(10, "fix(app): ten", "", map[string]string{"app/ten.txt": "10\n"})
	onNewer(mc10, map[string]string{"app/ten.txt": "10\n"})
	f.onLine("release-1.0", "fix(app): ten\n\n(cherry picked from commit "+mc10+")", map[string]string{"app/ten.txt": "10\n"})
	pr(11, "fix(app): eleven", map[string]string{"app/eleven.txt": "11\n"}, true)
	pr(12, "fix(ci): twelve", map[string]string{".github/workflows/twelve.yaml": "on: push\n"}, true)
	pr(13, "fix(app): thirteen", map[string]string{"app/thirteen.txt": "13\n"}, true)
	f.git("switch", "-q", "main")
	f.write("app/direct.txt", "pushed\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "chore: a direct push")
	direct := f.git("rev-parse", "HEAD")
	f.git("push", "-q", "origin", "main")

	stdout := mustRun(t, f, gh, "run", "--line", "release-1.0", "--no-workflow-files", "--push")
	const branch = "backport-batch/release-1.0-20261026"
	gh.prs = []pullRequest{f.batchPR(90, "release-1.0", branch, stdout)}
	pr(14, "fix(app): fourteen, after the batch", map[string]string{"app/fourteen.txt": "14\n"}, true)
	stdout = mustRun(t, f, gh, "run", "--line", "release-1.0", "--defer", "13", "--reason", "fails the e2e", "--push")
	// An active drop, of #5, which the batch carries, and one of a feat,
	// which rule 2 keeps out first.
	gh.prs = []pullRequest{f.batchPR(90, "release-1.0", branch, stdout)}
	stdout = mustRun(t, f, gh, "run", "--line", "release-1.0", "--drop", "5,1", "--reason", "not for 1.0", "--push")

	l, _ := parseLine("release-1.0")
	s := bodyReportOf(t, stdout, l)
	got, err := s.doc.categories()
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "skipped.type", 4: "skipped.before-start", 5: "skipped.dropped", 6: "skipped.newer", 7: "skipped.breaking",
		8: "skipped.touches", 9: "skipped.skip-label", 10: "skipped.on-line", 11: "deferred", 12: "deferred", 13: "deferred",
		14: "missing", 9000: "skipped.type"}
	for n, category := range want {
		if got[n] != category {
			t.Errorf("#%d is %q, want %q", n, got[n], category)
		}
		delete(got, n)
	}
	if len(got) != 0 {
		t.Errorf("the report also names %v", got)
	}
	if gh.find(90).State != "closed" || !strings.Contains(stdout, `"mode":"close"`) {
		t.Errorf("dropping the batch's last pick left #90 %s; want it closed", gh.find(90).State)
	}
	if !slices.Equal(droppedNumbers(s.doc.Overlays.Dropped), []int{1, 5}) || len(s.doc.Primary.Picked) != 0 {
		t.Errorf("the report drops %v and picks %v; want #1 and #5 dropped, nothing picked", s.doc.Overlays.Dropped, s.doc.Primary.Picked)
	}
	if !slices.Contains(s.doc.Other, direct) {
		t.Errorf("the report does not count the direct push %.12s: %v", direct, s.doc.Other)
	}
	kinds := map[int]string{}
	for _, e := range s.doc.Primary.Deferred {
		kinds[e.Original] = fmt.Sprintf("commit=%v reason=%q workflows=%v", e.Commit != "", e.Reason, e.Workflows)
	}
	if kinds[11] != `commit=true reason="" workflows=false` || kinds[12] != `commit=false reason="" workflows=true` || kinds[13] != `commit=false reason="fails the e2e" workflows=false` {
		t.Errorf("the deferred entries are %v", kinds)
	}
}

// A report too big for a comment is refused before anything is written: no
// push, no comment, no edit, and no replay of the earlier report either.
func TestReportTooLargeIsRefusedBeforePush(t *testing.T) {
	f, gh, head := bigWaits(t, 7)
	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--no-workflow-files", "--push")
	if code != 2 || !strings.Contains(stderr, "the report comment is") {
		t.Errorf("run --push = %d, want a refusal of the report:\n%s", code, stderr)
	}
	if f.remoteHead(batchBranch1) != head || len(gh.issueComments) != 0 || len(gh.edited) != 0 {
		t.Errorf("the refused run moved the batch to %.12s, wrote %d comments and edited %q", f.remoteHead(batchBranch1), len(gh.issueComments), gh.edited)
	}
}

// bigWaits opens a batch whose report comment failed, so its body keeps a
// report no comment does, and then merges n originals that each wait on 50
// workflow files with long names, which both the record and the report list
// in full.
func bigWaits(t *testing.T, n int) (*fixture, *fakeGH, string) {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{fail: map[string]error{"createComment": errString("HTTP 502")}}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 2 || len(gh.created) != 1 {
		t.Fatalf("the first run = %d, want its comment to fail after the pull request opened\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	for i := range n {
		files := map[string]string{}
		for j := range filesKept {
			files[fmt.Sprintf(".github/workflows/%s-%02d-%02d.yaml", strings.Repeat("w", 170), i, j)] = "on: push\n"
		}
		f.pr(10+i, fmt.Sprintf("fix(ci): wait %d", i), "", files)
	}
	f.now = f.now.AddDate(0, 0, 1)
	return f, gh, f.remoteHead(batchBranch1)
}

// A failure between any two writes leaves what was written readable, and the
// next run, whatever changed meanwhile, replays a report the body kept but no
// comment did, byte for byte, and posts nothing twice. A batch closed in
// between is never written again.
func TestReportReplayAfterEachFailure(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *fakeGH) {
		f := newFixture(t)
		f.lines()
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		return f, &fakeGH{}
	}
	// change moves a label, main, the clock and the run's flags between
	// the failure and the retry, so the retry's report is another one.
	change := func(f *fixture, gh *fakeGH) []string {
		gh.labels = map[string][]int{labelSkip: {6}}
		f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		f.now = f.now.AddDate(0, 0, 1)
		return []string{"run", "--line", "release-1.1", "--continue", "--no-workflow-files", "--push"}
	}
	once := func(t *testing.T, gh *fakeGH, ids ...string) {
		t.Helper()
		posted := reportsPosted(gh)
		for _, id := range ids {
			if n := len(slices.DeleteFunc(slices.Clone(posted), func(p string) bool { return p != id })); n != 1 {
				t.Errorf("report %.12s is posted %d times: %q", id, n, posted)
			}
		}
	}

	t.Run("a new batch whose pull request was not opened", func(t *testing.T) {
		f, gh := setup(t)
		gh.fail = map[string]error{"createPull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		if len(gh.prs) != 0 || len(gh.issueComments) != 0 || f.remoteHead(batchBranch1) == "" {
			t.Errorf("after the failure: %d pull requests, %d comments, branch %q; want the branch alone", len(gh.prs), len(gh.issueComments), f.remoteHead(batchBranch1))
		}
		mustRun(t, f, gh, change(f, gh)...)
		if len(gh.prs) != 1 || len(reportsPosted(gh)) != 1 || bodyReportOf(t, gh.prs[0].Body).doc.ID != reportsPosted(gh)[0] {
			t.Errorf("the retry opened %d pull requests with reports %q", len(gh.prs), reportsPosted(gh))
		}
	})
	t.Run("a new batch whose report comment failed", func(t *testing.T) {
		f, gh := setup(t)
		gh.fail = map[string]error{"createComment": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 2 || !strings.Contains(stderr, "the body keeps it") {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		first := bodyReportOf(t, gh.find(90).Body)
		mustRun(t, f, gh, change(f, gh)...)
		second := bodyReportOf(t, gh.find(90).Body)
		want, err := reportComment(first)
		if err != nil || len(gh.issueComments) != 2 || gh.issueComments[0].Body != want || reportsPosted(gh)[1] != second.doc.ID || first.doc.ID == second.doc.ID {
			t.Errorf("comments %q; want the first report replayed as it was, then the retry's", reportsPosted(gh))
		}
	})
	t.Run("a continuation whose push failed", func(t *testing.T) {
		f, gh := setup(t)
		openBatch(t, f, gh)
		first := reportsPosted(gh)[0]
		f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
		f.refusePushes(true)
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		f.refusePushes(false)
		attempted := reportsPosted(gh)[1]
		if bodyReportOf(t, gh.find(90).Body).doc.ID != first {
			t.Error("the body moved on although the push failed")
		}
		mustRun(t, f, gh, change(f, gh)...)
		once(t, gh, first, attempted)
		if len(reportsPosted(gh)) != 3 {
			t.Errorf("reports %q; want the first, the attempted and the retry's", reportsPosted(gh))
		}
	})
	t.Run("a continuation whose body edit failed", func(t *testing.T) {
		f, gh := setup(t)
		openBatch(t, f, gh)
		first := reportsPosted(gh)[0]
		before := f.remoteHead(batchBranch1)
		f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
		gh.fail = map[string]error{"editPull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		attempted := reportsPosted(gh)[1]
		if f.remoteHead(batchBranch1) == before || bodyReportOf(t, gh.find(90).Body).doc.ID != first {
			t.Error("the push or the body is not where the failure left them")
		}
		mustRun(t, f, gh, change(f, gh)...)
		once(t, gh, first, attempted)
		if last := reportsPosted(gh)[len(reportsPosted(gh))-1]; bodyReportOf(t, gh.find(90).Body).doc.ID != last || len(reportsPosted(gh)) != 3 {
			t.Errorf("reports %q; want the body to carry the retry's, posted last", reportsPosted(gh))
		}
	})
	t.Run("a batch closed while the run writes to it", func(t *testing.T) {
		f, gh := setup(t)
		openBatch(t, f, gh)
		f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
		gh.after = map[string]func(){"createComment": func() { gh.find(90).State = "closed" }}
		code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
		if code != 2 || !strings.Contains(stderr, "batch pull request #90 is closed now; nothing is written to it") || len(gh.edited) != 0 {
			t.Errorf("run = %d, edited %q; want 2 and no edit of the closed pull request:\n%s", code, gh.edited, stderr)
		}
	})
	t.Run("a batch closed before its report was posted", func(t *testing.T) {
		f, gh := setup(t)
		gh.fail = map[string]error{"createComment": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		kept := bodyReportOf(t, gh.find(90).Body)
		gh.find(90).State = "closed"
		gh.nextPR = 91
		mustRun(t, f, gh, change(f, gh)...)
		if slices.Contains(gh.commented, 90) || slices.Contains(reportsPosted(gh), kept.doc.ID) {
			t.Errorf("commented on %v, posting %q; want nothing on the closed #90", gh.commented, reportsPosted(gh))
		}
		code, stdout, stderr := f.exec(gh, "report", "--pr", "90")
		if code != 0 || !strings.Contains(stdout, "report "+kept.doc.ID) || !strings.Contains(stdout, "kept in the body only") {
			t.Errorf("report --pr 90 = %d:\n%s%s", code, stdout, stderr)
		}
	})
	// Every comment is posted only after GitHub says the batch is still
	// open, the replay's included, so a close between the two stops the
	// second.
	t.Run("a batch closed between the replay and the new report", func(t *testing.T) {
		f, gh := setup(t)
		gh.fail = map[string]error{"createComment": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		kept := bodyReportOf(t, gh.find(90).Body)
		f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
		gh.after = map[string]func(){"createComment": func() { gh.find(90).State = "closed" }}
		code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
		if code != 2 || !strings.Contains(stderr, "batch pull request #90 is closed now; nothing is written to it") || !slices.Equal(reportsPosted(gh), []string{kept.doc.ID}) || len(gh.edited) != 0 {
			t.Errorf("run = %d, posted %q, edited %q; want 2 after the replay alone:\n%s", code, reportsPosted(gh), gh.edited, stderr)
		}
	})
}

// The reader says how a report stands against the pull request as GitHub
// shows it now, and claims publication only when the head or the body shows
// it: a failed push, a push whose answer was lost, a body that was not
// updated and a rebuild since all read as they are.
func TestReportPublicationStatus(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *fakeGH, string) {
		f := newFixture(t)
		f.lines()
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		gh := &fakeGH{}
		openBatch(t, f, gh)
		return f, gh, reportsPosted(gh)[0]
	}
	status := func(t *testing.T, f *fixture, gh *fakeGH, id, want string) {
		t.Helper()
		code, stdout, stderr := f.exec(gh, "report", "--pr", "90", "--id", id)
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("report --id %.12s = %d, want %q:\n%s%s", id, code, want, stdout, stderr)
		}
	}
	t.Run("a push the remote refused, and one it took whose answer was lost", func(t *testing.T) {
		f, gh, first := setup(t)
		f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		f.refusePushes(true)
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.refusePushes(false)
		attempted := reportsPosted(gh)[1]
		status(t, f, gh, attempted, "publication unconfirmed: #90's head is ")
		status(t, f, gh, attempted, "its body carries report "+first[:12])
		// The same head reaching the remote is what a push GitHub took looks
		// like when its answer never came back.
		f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
		f.git("switch", "-q", "main")
		status(t, f, gh, attempted, "the batch this report describes, but its body carries report "+first[:12])
	})
	t.Run("a push followed by a failed body edit", func(t *testing.T) {
		f, gh, first := setup(t)
		f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		gh.fail = map[string]error{"editPull": errString("HTTP 502")}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 2 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		status(t, f, gh, reportsPosted(gh)[1], "the batch this report describes, but its body carries report "+first[:12])
		status(t, f, gh, first, "#90's body carries this report")
	})
	t.Run("a rebuild since", func(t *testing.T) {
		f, gh, first := setup(t)
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "x", "--push")
		second := reportsPosted(gh)[1]
		status(t, f, gh, first, "publication unconfirmed: #90's head is ")
		status(t, f, gh, second, "#90's body carries this report")
	})
}

// forged is a report on release-1.1 with what change sets, under an ID that
// matches it: only checking what it says stands in its way.
func forged(change func(d *reportDoc)) storedReport {
	sha := strings.Repeat("a", 40)
	d := reportDoc{Format: reportFormat, Line: "release-1.1", Diag: map[string][]string{}, Other: []string{sha},
		Built: builtFrom{Main: sha, Line: sha, Policy: sha, Batch: sha, At: "2026-10-26T06:00:00Z", Options: runOptions{Mode: modeNew}}}
	d.Primary.Picked = []int{5}
	change(&d)
	d.ID = reportID(d)
	return storedReport{doc: d, raw: reportJSON(d)}
}

// reportRun runs report with gh faked; it reads nothing but GitHub.
func reportRun(gh *fakeGH, args ...string) (int, string, string) {
	var stdout, stderr strings.Builder
	a := &app{gh: gh, stdout: &stdout, stderr: &stderr, now: func() time.Time { return time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC) }}
	code := a.main(append([]string{"report"}, args...))
	return code, stdout.String(), stderr.String()
}

// A report is read only when everything it says is what a run writes, and
// it is for the line of the pull request it sits on: an ID that matches its
// content proves only that nobody changed it since it was hashed.
func TestReportRefusesWhatItCannotVouchFor(t *testing.T) {
	for name, change := range map[string]func(d *reportDoc){
		"a line that is no release line": func(d *reportDoc) { d.Line = "release-1.1\n\x1b[2Jpicked" },
		"another line":                   func(d *reportDoc) { d.Line = "release-1.0" },
		"a main that is no commit":       func(d *reportDoc) { d.Built.Main = "main\n#5 picked" },
		"a time that is none":            func(d *reportDoc) { d.Built.At = "2026-10-26T<b>" },
		"an unknown operation":           func(d *reportDoc) { d.Built.Options.Mode = "new\x1b[2J" },
		"a run that is no Actions run":   func(d *reportDoc) { d.Built.Run = "javascript:alert(1)" },
		"a tool that is no commit":       func(d *reportDoc) { d.Built.Tool = "v1\n" },
		"a commit on main that is none":  func(d *reportDoc) { d.Other = []string{"HEAD\x1b]0;x\x07"} },
		"a deferred commit that is none": func(d *reportDoc) {
			d.Primary.Deferred = []deferredEntry{{Original: 6, Commit: "-x", Files: []string{"a"}}}
		},
		"an original named twice":     func(d *reportDoc) { d.Primary.Missing = []int{5} },
		"an original that is none":    func(d *reportDoc) { d.Primary.Skipped.Type = []int{-1} },
		"a diagnostic of two lines":   func(d *reportDoc) { d.Diag = map[string][]string{"5": {"incomplete\n\x1b[2Jforged"}} },
		"a diagnostic of no original": func(d *reportDoc) { d.Diag = map[string][]string{"five": {"incomplete"}} },
		// Fields that speak of one another have to agree.
		"a drop category with no drop": func(d *reportDoc) { d.Primary.Skipped.Dropped = []int{6} },
		"a stale original it has missing": func(d *reportDoc) {
			d.Primary.Missing, d.Overlays.Stale = []int{7}, []int{7}
		},
		"a stale original it puts nowhere": func(d *reportDoc) { d.Overlays.Stale = []int{8} },
		"a changed original it defers": func(d *reportDoc) {
			d.Primary.Deferred = []deferredEntry{{Original: 9, Files: []string{}, Reason: "fails the e2e"}}
			d.Overlays.Changed = []int{9}
		},
		"a skip label on a picked original":   func(d *reportDoc) { d.Overrides.Skip = []int{5} },
		"a skip-label category with no label": func(d *reportDoc) { d.Primary.Skipped.SkipLabel = []int{10} },
		"an include label on a rule 2 skip": func(d *reportDoc) {
			d.Primary.Skipped.Type, d.Overrides.Include = []int{11}, []int{11}
		},
		"both labels on one original": func(d *reportDoc) {
			d.Primary.Skipped.SkipLabel, d.Overrides.Include, d.Overrides.Skip = []int{12}, []int{12}, []int{12}
		},
		"a label on no original":                      func(d *reportDoc) { d.Overrides.Include = []int{13} },
		"a diagnostic of an original it puts nowhere": func(d *reportDoc) { d.Diag = map[string][]string{"14": {"incomplete"}} },
		"a workflow wait without the flag": func(d *reportDoc) {
			d.Primary.Deferred = []deferredEntry{{Original: 15, Files: []string{".github/workflows/ci.yaml"}, Workflows: true}}
		},
		"a close that still picks": func(d *reportDoc) { d.Built.Options.Mode = modeClose },
		"a park with no merge": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Kind: parkPartial, Files: []string{"a"}}}
		},
		"a parked original it picks": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 5, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"a"}}}
		},
		"a park of an unknown kind": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: "later", Files: []string{"a"}}}
		},
		"a partial park that needs an original": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"a"}, Needs: []int{5}}}
		},
		"a partial park of unsorted files": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"b", "a"}}}
		},
		"a partial park of a file that drives a terminal": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"a\x1b[2J"}}}
		},
		"a park announced in the report": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"a"}, Announced: true}}
		},
		"a park for an original it picks": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkDependency, Needs: []int{5}}}
		},
		"a park that needs itself": func(d *reportDoc) {
			d.Primary.Skipped.Type = []int{17}
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkDependency, Needs: []int{16, 17}}}
		},
		"a park for a commit that merged a pull request": func(d *reportDoc) {
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkDependency, Commits: []string{strings.Repeat("c", 40)}}}
		},
		"a partial unit not on the batch": func(d *reportDoc) { d.Overlays.Partial = []int{5} },
		"format 1 with a park": func(d *reportDoc) {
			d.Format = 1
			d.Primary.Parked = []parkedEntry{{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"a"}}}
		},
	} {
		s := forged(change)
		c, _ := reportComment(storedReport{doc: forged(func(*reportDoc) {}).doc, raw: s.raw})
		comment := issueComment{ID: 1, Body: strings.Replace(c, forged(func(*reportDoc) {}).doc.ID, s.doc.ID, 1)}
		comment.User.Login, comment.User.Type = "maintainer", "User"
		for where, gh := range map[string]*fakeGH{
			"in the body":  {prs: []pullRequest{notifyPR("20261026", "text\n\n"+reportBlock(s.raw)+"\n")}},
			"in a comment": {prs: []pullRequest{notifyPR("20261026", "text")}, issueComments: []issueComment{comment}},
		} {
			if code, stdout, stderr := reportRun(gh, "--pr", "90", "--id", s.doc.ID); code != 2 || stdout != "" {
				t.Errorf("%s, %s: report = %d, want 2 and nothing printed:\n%s%s", name, where, code, stdout, stderr)
			}
		}
	}
	if code, stdout, stderr := reportRun(&fakeGH{prs: []pullRequest{notifyPR("20261026", "text\n\n"+reportBlock(forged(func(*reportDoc) {}).raw)+"\n")}}, "--pr", "90"); code != 0 || !strings.Contains(stdout, "#5 picked") {
		t.Errorf("report of a sound one = %d:\n%s%s", code, stdout, stderr)
	}
	// What a run wrote before anything was parked reads as it was written.
	format1 := forged(func(d *reportDoc) { d.Format, d.Primary.Parked = 1, []parkedEntry{} })
	if code, stdout, stderr := reportRun(&fakeGH{prs: []pullRequest{notifyPR("20261026", "text\n\n"+reportBlock(format1.raw)+"\n")}}, "--pr", "90"); code != 0 || !strings.Contains(stdout, "#5 picked") {
		t.Errorf("report of a sound format 1 one = %d:\n%s%s", code, stdout, stderr)
	}
	parked := forged(func(d *reportDoc) {
		d.Primary.Skipped.Type = []int{17}
		d.Other = append(d.Other, strings.Repeat("c", 40))
		d.Primary.Parked = []parkedEntry{
			{Original: 16, Merge: strings.Repeat("b", 40), Kind: parkPartial, Files: []string{"chart/templates/policy.yaml"}},
			{Original: 18, Merge: strings.Repeat("b", 40), Kind: parkDependency, Needs: []int{16, 17}, Commits: []string{strings.Repeat("c", 40)}},
		}
	})
	if code, stdout, stderr := reportRun(&fakeGH{prs: []pullRequest{notifyPR("20261026", "text\n\n"+reportBlock(parked.raw)+"\n")}}, "--pr", "90"); code != 0 || !strings.Contains(stdout, "#16 parked: only part of it applies") || !strings.Contains(stdout, "#18 parked: builds on #16, #17, cccccccccccc") {
		t.Errorf("report of sound parks = %d:\n%s%s", code, stdout, stderr)
	}
}

// What report prints stays one line per field and carries nothing a
// terminal acts on, whatever reached it.
func TestReportPrintsNothingThatDrivesATerminal(t *testing.T) {
	const evil = "\n\x1b[2Jfake: #9 picked\u202e"
	s := forged(func(d *reportDoc) {
		d.Line, d.Built.At, d.Built.Options.Mode = "release-1.1"+evil, "2026-10-26"+evil, "new"+evil
		d.Built.Main, d.Built.Tool, d.Built.Run, d.Built.Newer = "main"+evil, "tool"+evil, "run"+evil, "newer"+evil
		d.Other = []string{"other" + evil}
		d.Primary.Deferred = []deferredEntry{{Original: 6, Reason: "why" + evil}}
	})
	var stdout strings.Builder
	a := &app{stdout: &stdout}
	pr := notifyPR("20261026", "")
	a.printReport(&pr, s, nil, true)
	for _, ln := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(ln, "fake") || strings.ContainsFunc(ln, func(r rune) bool { return r != '\t' && unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			t.Errorf("report printed %q", ln)
		}
	}
}

// The comment is made of what was checked: a line, a time and an ID that
// carry no markup into the summary GitHub renders.
func TestReportCommentIsBuiltFromCheckedFields(t *testing.T) {
	for name, change := range map[string]func(d *reportDoc){
		"markup in the line": func(d *reportDoc) { d.Line = "release-1.1</summary><img src=x onerror=alert(1)>" },
		"markup in the time": func(d *reportDoc) { d.Built.At = "<b>2026-10-26</b>T06:00:00Z" },
		"a mention":          func(d *reportDoc) { d.Line = "release-1.1 @cozystack/maintainers" },
	} {
		if c, err := reportComment(forged(change)); err == nil {
			t.Errorf("%s: reportComment made\n%s", name, c)
		}
	}
	if _, err := reportComment(forged(func(*reportDoc) {})); err != nil {
		t.Errorf("reportComment of a sound report: %v", err)
	}
}

// Anyone can comment on a public pull request: the reader takes a report
// from a comment only when the batch trusts its author, whatever ID it
// carries.
func TestReportReaderTakesOnlyTrustedComments(t *testing.T) {
	s := forged(func(*reportDoc) {})
	c, err := reportComment(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		login, kind string
		read        bool
	}{
		{"someone", "User", false},
		{"renovate[bot]", "Bot", false},
		{"github-actions[bot]", "Bot", false},
		{"maintainer", "User", true},
		{defaultBatchBot, "Bot", true},
	} {
		comment := issueComment{ID: 1, Body: c}
		comment.User.Login, comment.User.Type = tc.login, tc.kind
		gh := &fakeGH{prs: []pullRequest{notifyPR("20261026", "text")}, issueComments: []issueComment{comment}, bot: defaultBatchBot}
		code, stdout, stderr := reportRun(gh, "--pr", "90", "--id", s.doc.ID)
		if read := code == 0 && strings.Contains(stdout, "#5 picked"); read != tc.read || !tc.read && (code != 2 || !strings.Contains(stderr, "keeps no report")) {
			t.Errorf("a report by %s: report = %d, read %v, want %v:\n%s%s", tc.login, code, read, tc.read, stdout, stderr)
		}
	}
}

// A report whose ID is not one says so without repeating it as written.
func TestReportErrorsRepeatNothingRaw(t *testing.T) {
	s := forged(func(*reportDoc) {})
	// Short enough that a cut to 12 characters keeps the forged line.
	raw := strings.Replace(s.raw, `"id":"`+s.doc.ID+`"`, `"id":"\nforged"`, 1)
	code, _, stderr := reportRun(&fakeGH{prs: []pullRequest{notifyPR("20261026", "text\n\n"+reportBlock(raw)+"\n")}}, "--pr", "90")
	for _, ln := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(ln, "forged") {
			t.Errorf("report = %d, printed the forged line %q", code, ln)
		}
	}
	if code != 2 {
		t.Errorf("report = %d, want 2:\n%s", code, stderr)
	}
}
