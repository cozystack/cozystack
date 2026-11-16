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
	"slices"
	"strings"
	"testing"
)

// recordOf is the record the open batch #90 carries now.
func recordOf(t *testing.T, gh *fakeGH) *lineRecord {
	t.Helper()
	rec, err := parseRecord(gh.find(90).Body, notifyLine)
	if err != nil || rec == nil {
		t.Fatalf("#90 carries no readable record: %v", err)
	}
	return rec
}

func dropped(rec *lineRecord) []int {
	var out []int
	for _, d := range rec.Dropped {
		out = append(out, d.Original)
	}
	return out
}

// A drop takes a picked original off the batch and keeps it off the line:
// a run that continues the batch does not pick it, and nor does the next
// batch, which carries the drop over from the merged one.
func TestDropTakesAnOriginalOffTheLine(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	mc6 := f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	stdout := mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "6", "--reason", "needs the rework of #4", "--push")
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): five (#5)"}) {
		t.Errorf("after the drop the batch carries %q, want #5 alone", got)
	}
	rec := recordOf(t, gh)
	if want := (droppedEntry{Original: 6, Merge: mc6, Reason: "needs the rework of #4", On: 90, Date: "2026-10-26"}); len(rec.Dropped) != 1 || rec.Dropped[0] != want || rec.Built.Options.Mode != modeDrop {
		t.Errorf("the record drops %+v (%s), want %+v", rec.Dropped, rec.Built.Options.Mode, want)
	}
	if s := bodyReportOf(t, stdout); !slices.Equal(s.doc.Primary.Skipped.Dropped, []int{6}) || !slices.Equal(droppedNumbers(s.doc.Overlays.Dropped), []int{6}) {
		t.Errorf("the report has %v dropped and %v as the decision", s.doc.Primary.Skipped.Dropped, s.doc.Overlays.Dropped)
	}
	if !strings.Contains(stdout, "<details><summary>Dropped from `release-1.1` by a maintainer</summary>") {
		t.Errorf("the body does not list #6 as dropped:\n%s", stdout)
	}
	code, _, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut")
	if code != 1 || !strings.Contains(stderr, "note: #6 is dropped from release-1.1 on #90 (needs the rework of #4): https://github.com/cozystack/cozystack/pull/6 ") {
		t.Errorf("check --cut = %d, want #5 to fail it and a note on #6:\n%s", code, stderr)
	}

	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): five (#5)", "fix(app): seven (#7)"}) || !slices.Equal(dropped(recordOf(t, gh)), []int{6}) {
		t.Errorf("the continued batch carries %q and drops %v", got, dropped(recordOf(t, gh)))
	}

	mergeBatch(f, gh)
	f.now = f.now.AddDate(0, 0, 3)
	f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
	gh.nextPR = 91
	stdout = mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
	rec, err := parseRecord(stdout, notifyLine)
	if got := f.subjects("origin/release-1.1", f.remoteHead("backport-batch/release-1.1-20261029")); err != nil || !slices.Equal(got, []string{"fix(app): eight (#8)"}) || !slices.Equal(dropped(rec), []int{6}) {
		t.Errorf("the next batch carries %q and drops %v (%v); want #8 alone, #6 still dropped", got, rec, err)
	}
}

// Dropping an original the batch does not carry changes only the record:
// the batch is not rebuilt, so a line that moved since does not move it.
func TestDropIsRecordOnlyWhenTheLineMoved(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): a line-only seven", map[string]string{"app/seven.txt": "line\n"})
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	if recordOf(t, gh).deferred()[7].Commit == "" {
		t.Fatal("#7 is not a recorded conflict")
	}
	f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "7", "--reason", "needs #4", "--push")
	rec := recordOf(t, gh)
	if f.remoteHead(batchBranch1) != head || !slices.Equal(dropped(rec), []int{7}) || len(rec.deferred()) != 0 || len(gh.edited) != 1 {
		t.Errorf("the batch moved to %.12s (from %.12s), the record drops %v and defers %v; want only the record changed", f.remoteHead(batchBranch1), head, dropped(rec), rec.deferred())
	}
}

// A drop is written to the open batch's record, and to no other: with no
// batch open, or none ever, it is refused and nothing is written.
func TestDropNeedsAnOpenBatch(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): a line-only seven", map[string]string{"app/seven.txt": "line\n"})
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--drop", "5", "--reason", "r", "--push"); code != 2 || !strings.Contains(stderr, "no open batch pull request on release-1.1 carries a record; a drop is recorded on one") {
		t.Errorf("a drop on a line that never had a batch = %d:\n%s", code, stderr)
	}
	openBatch(t, f, gh)
	mergeBatch(f, gh)
	written := len(gh.edited) + len(gh.issueComments)
	for _, args := range [][]string{{"--drop", "7", "--reason", "r", "--push"}, {"--undrop", "7", "--push"}} {
		if code, _, stderr := f.exec(gh, append([]string{"run", "--line", "release-1.1"}, args...)...); code != 2 || !strings.Contains(stderr, "carries a record; a drop is recorded on one") {
			t.Errorf("%q after the batch merged = %d:\n%s", args, code, stderr)
		}
	}
	if len(gh.edited)+len(gh.issueComments) != written {
		t.Error("a refused drop wrote to GitHub")
	}
}

// A drop is a decision kept beside whatever else decides an original: the
// rule, label or carry that applies first is its category, and the drop
// stays on record. backport/include does not lift it.
func TestDropOverlay(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *fakeGH) {
		f := newFixture(t)
		f.lines()
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		f.pr(7, "feat(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		gh := &fakeGH{labels: map[string][]int{labelInclude: {7}}}
		openBatch(t, f, gh)
		return f, gh
	}
	category := func(t *testing.T, gh *fakeGH, n int) string {
		t.Helper()
		s := bodyReportOf(t, gh.find(90).Body)
		where, err := s.doc.categories()
		if err != nil {
			t.Fatal(err)
		}
		return where[n]
	}
	merge := func(f *fixture, n int) string {
		mc, _, _ := strings.Cut(f.git("log", "--first-parent", "--format=%H", "--grep", fmt.Sprintf("(#%d)$", n), "main"), "\n")
		return mc
	}
	drop := func(t *testing.T, f *fixture, gh *fakeGH, n string) {
		t.Helper()
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", n, "--reason", "not for 1.1", "--push")
	}
	t.Run("then backport/skip", func(t *testing.T) {
		f, gh := setup(t)
		drop(t, f, gh, "6")
		gh.labels[labelSkip] = []int{6}
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
		if got := category(t, gh, 6); got != "skipped.skip-label" || !slices.Equal(dropped(recordOf(t, gh)), []int{6}) {
			t.Errorf("#6 is %q, dropped %v", got, dropped(recordOf(t, gh)))
		}
	})
	t.Run("then picked onto the line by hand", func(t *testing.T) {
		f, gh := setup(t)
		drop(t, f, gh, "6")
		pickOnLine(f, 6, map[string]string{"app/six.txt": "6\n"})
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
		if got := category(t, gh, 6); got != "skipped.on-line" || !slices.Equal(dropped(recordOf(t, gh)), []int{6}) {
			t.Errorf("#6 is %q, dropped %v", got, dropped(recordOf(t, gh)))
		}
	})
	t.Run("a feat backport/include let in", func(t *testing.T) {
		f, gh := setup(t)
		drop(t, f, gh, "7")
		if got := category(t, gh, 7); got != "skipped.dropped" || slices.Contains(f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)), "feat(app): seven (#7)") {
			t.Errorf("#7 is %q; want it dropped and off the batch", got)
		}
		if !strings.Contains(gh.find(90).Body, "`backport/include`, skipped by a maintainer's drop") {
			t.Errorf("the overrides do not say what became of #7:\n%s", gh.find(90).Body)
		}
	})
	t.Run("undropped", func(t *testing.T) {
		f, gh := setup(t)
		drop(t, f, gh, "6")
		head := f.remoteHead(batchBranch1)
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--undrop", "6", "--push")
		if got := category(t, gh, 6); got != "missing" || len(recordOf(t, gh).Dropped) != 0 || f.remoteHead(batchBranch1) != head || recordOf(t, gh).Built.Options.Mode != modeUndrop {
			t.Errorf("#6 is %q, dropped %v; want it missing until the next run, the batch where it was", got, dropped(recordOf(t, gh)))
		}
		if code, stdout, _ := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/6 ") {
			t.Errorf("check = %d, want #6 missing:\n%s", code, stdout)
		}
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
		if got := category(t, gh, 6); got != "picked" {
			t.Errorf("after the next run #6 is %q, want it picked again", got)
		}
	})
	t.Run("a new original during a change to the record alone", func(t *testing.T) {
		f, gh := setup(t)
		f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
		drop(t, f, gh, "7")
		mustRun(t, f, gh, "run", "--line", "release-1.1", "--undrop", "7", "--push")
		if got := category(t, gh, 8); got != "missing" {
			t.Errorf("#8 is %q, want missing until a run tries it", got)
		}
	})
	// A stored drop is bound to the merge it names: one that names another
	// original's, or one of no merge of it, is refused; one of an original
	// merged before the line branched is kept and decides nothing.
	t.Run("a stored drop of an earlier commit that names the original", func(t *testing.T) {
		// Before release-1.1 branched, a commit on main already ended in
		// (#6); the drop has to name the merge of #6 the line can take.
		f := newFixture(t)
		f.release("release-1.0")
		f.tag("v1.0.0")
		early := f.squash(6, "docs: what six will change", "", map[string]string{"docs/six.md": "6\n"})
		f.release("release-1.1")
		f.tag("v1.1.0")
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		gh := &fakeGH{}
		openBatch(t, f, gh)
		drop(t, f, gh, "6")
		gh.find(90).Body = strings.Replace(gh.find(90).Body, `{"original":6,"merge":"`+merge(f, 6)+`"`, `{"original":6,"merge":"`+early+`"`, 1)
		if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 2 || !strings.Contains(stderr, "but #6 merged as") {
			t.Errorf("check = %d, want 2 naming the merge of #6:\n%s", code, stderr)
		}
	})
	t.Run("a stored drop of a commit inside a pull request", func(t *testing.T) {
		// A commit of a pull request's own branch can end in its number too,
		// but only the merge on main's first-parent line is the original.
		f := newFixture(t)
		f.release("release-1.0")
		f.tag("v1.0.0")
		mc3 := f.prCommits(3, "feat(app): three", "", change{files: map[string]string{"app/three.txt": "3\n"}, message: "feat(app): the part of three (#3)"})
		inner := f.git("rev-parse", mc3+"^2")
		f.release("release-1.1")
		f.tag("v1.1.0")
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		gh := &fakeGH{}
		openBatch(t, f, gh)
		drop(t, f, gh, "6")
		gh.find(90).Body = strings.Replace(gh.find(90).Body, `{"original":6,"merge":"`+merge(f, 6)+`"`, `{"original":3,"merge":"`+inner+`"`, 1)
		if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 2 || !strings.Contains(stderr, "which is no merge of it on main") {
			t.Errorf("check = %d, want 2:\n%s", code, stderr)
		}
	})
	for _, tc := range []struct {
		name     string
		original func(f *fixture) (int, string)
		refused  bool
	}{
		{"another original's merge", func(f *fixture) (int, string) { return 6, merge(f, 5) }, true},
		{"a commit that merged no pull request", func(f *fixture) (int, string) { return 6, f.git("rev-list", "--max-parents=0", "main") }, true},
		{"a merge from before the line branched, of another pull request", func(f *fixture) (int, string) { return 2, merge(f, 1) }, true},
		{"a merge from before the line branched", func(f *fixture) (int, string) { return 1, merge(f, 1) }, false},
	} {
		t.Run("a stored drop of "+tc.name, func(t *testing.T) {
			f, gh := setup(t)
			drop(t, f, gh, "6")
			n, mc := tc.original(f)
			gh.find(90).Body = strings.Replace(gh.find(90).Body, `{"original":6,"merge":"`+merge(f, 6)+`"`, fmt.Sprintf(`{"original":%d,"merge":"%s"`, n, mc), 1)
			code, _, stderr := f.exec(gh, "check", "--line", "release-1.1")
			switch {
			case tc.refused && (code != 2 || !strings.Contains(stderr, fmt.Sprintf("drops #%d by %.12s", n, mc))):
				t.Errorf("check = %d, want 2 naming the drop:\n%s", code, stderr)
			case !tc.refused && code == 2:
				t.Errorf("check = %d, want the drop kept:\n%s", code, stderr)
			case !tc.refused:
				mustRun(t, f, gh, "run", "--line", "release-1.1", "--continue", "--push")
				if !slices.Equal(dropped(recordOf(t, gh)), []int{1}) || category(t, gh, 6) != "picked" {
					t.Errorf("the next run drops %v and puts #6 in %q; want #1's drop carried on and #6 picked", dropped(recordOf(t, gh)), category(t, gh, 6))
				}
			}
		})
	}
}

// What a drop cannot do is refused before anything is touched.
func TestDropRefuses(t *testing.T) {
	f := newFixture(t)
	f.lines()
	mc5 := f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{labels: map[string][]int{labelSkip: {7, 9}}}
	head := openBatch(t, f, gh)
	f.onLine("release-1.1", "fix(app): five\n\n(cherry picked from commit "+mc5+")", map[string]string{"app/five.txt": "5\n"})
	// The line carries these too, by hand, while backport/skip and rule 2
	// decide them before the line is looked at.
	mc7 := f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	mc8 := f.pr(8, "feat(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
	f.onLine("release-1.1", "fix(app): seven\n\n(cherry picked from commit "+mc7+")", map[string]string{"app/seven.txt": "7\n"})
	f.onLine("release-1.1", "feat(app): eight\n\n(cherry picked from commit "+mc8+")", map[string]string{"app/eight.txt": "8\n"})
	// #9 is on the line only through a pick of its own commit, which a later
	// line change has since moved away from what main has.
	mc9 := f.pr(9, "fix(app): nine", "", map[string]string{"app/nine.txt": "9\n"})
	f.onLine("release-1.1", "fix(app): nine\n\n(cherry picked from commit "+f.git("rev-parse", mc9+"^2")+")", map[string]string{"app/nine.txt": "9\n"})
	f.onLine("release-1.1", "fix(app): nine, as the line needs it", map[string]string{"app/nine.txt": "nine on release-1.1\n"})
	for _, tc := range []struct {
		args []string
		why  string
	}{
		{[]string{"--drop", "4242", "--reason", "r"}, "#4242 did not merge into main"},
		{[]string{"--drop", "5", "--reason", "r"}, "already on release-1.1; a drop cannot remove code the line has"},
		{[]string{"--drop", "7", "--reason", "r"}, "#7 is already on release-1.1; a drop cannot remove code the line has"},
		{[]string{"--drop", "8", "--reason", "r"}, "#8 is already on release-1.1; a drop cannot remove code the line has"},
		{[]string{"--drop", "9", "--reason", "r"}, "#9 is already on release-1.1; a drop cannot remove code the line has"},
		{[]string{"--drop", "6"}, "--drop needs a --reason"},
		{[]string{"--drop", "6", "--reason", "r", "--defer", "6"}, "give one"},
		{[]string{"--drop", "6", "--reason", "r", "--continue"}, "takes no --onto, --continue or --date"},
		{[]string{"--drop", "6", "--reason", "r", "--onto", head}, "takes no --onto, --continue or --date"},
		{[]string{"--drop", "6", "--reason", "r", "--date", "2026-10-26"}, "takes no --onto, --continue or --date"},
		{[]string{"--undrop", "6", "--reason", "r"}, "--undrop takes no --reason"},
		{[]string{"--undrop", "6"}, "#6 is not dropped from release-1.1"},
		{[]string{"--drop", "6", "--undrop", "6", "--reason", "r"}, "give one"},
		{[]string{"--drop", "six", "--reason", "r"}, "not a list of pull request numbers"},
	} {
		code, _, stderr := f.exec(gh, append(append([]string{"run", "--line", "release-1.1"}, tc.args...), "--push")...)
		f.git("switch", "-q", "main")
		if code != 2 || !strings.Contains(stderr, tc.why) {
			t.Errorf("run %q = %d, want 2 with %q:\n%s", tc.args, code, tc.why, stderr)
		}
	}
	if f.remoteHead(batchBranch1) != head || len(gh.edited) != 0 || len(reportsPosted(gh)) != 1 {
		t.Errorf("a refused drop moved the batch to %.12s or wrote %q", f.remoteHead(batchBranch1), gh.edited)
	}
}

// A drop or an undrop asked for while a close was left changes nothing: the
// run finishes the close, and no open batch pull request is left to record
// it on.
func TestDropDuringAnUnfinishedClose(t *testing.T) {
	deferAll := []string{"run", "--line", "release-1.1", "--defer", "5", "--reason", "fails the app e2e", "--push"}
	for _, tc := range []struct {
		name    string
		stop    func(f *fixture, gh *fakeGH)
		refused bool
		args    []string
	}{
		{"a drop after the close failed", func(f *fixture, gh *fakeGH) { gh.fail = map[string]error{"closePull": errString("HTTP 502")} }, false, []string{"--drop", "6", "--reason", "not for 1.1"}},
		{"an undrop after the delete failed", func(f *fixture, gh *fakeGH) {
			gh.after = map[string]func(){"closePull": func() { f.refusePushes(true) }}
		}, true, []string{"--undrop", "6"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, gh, _ := closeWorld(t)
			tc.stop(f, gh)
			if code, _, stderr := f.exec(gh, deferAll...); code != 2 {
				t.Fatalf("run = %d\n%s", code, stderr)
			}
			f.git("switch", "-q", "main")
			if tc.refused {
				f.refusePushes(false)
			}
			gh.after = nil
			f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
			code, _, stderr := f.exec(gh, append([]string{"run", "--line", "release-1.1", "--push"}, tc.args...)...)
			f.git("switch", "-q", "main")
			rec, err := parseRecord(gh.find(90).Body, notifyLine)
			if code != 2 || !strings.Contains(stderr, "finished the close of #90 and made no other change") || gh.find(90).State != "closed" || f.remoteHead(batchBranch1) != "" || err != nil || len(rec.Dropped) != 0 {
				t.Errorf("run %q = %d, #90 %s, branch %q, record %+v, %v; want 2, the close finished and nothing dropped:\n%s", tc.args, code, gh.find(90).State, f.remoteHead(batchBranch1), rec, err, stderr)
			}
		})
	}
}

// The batch carries an original whose first commit the line already had, so
// its pick came out empty and its unit holds the rest: a drop still takes
// that unit off.
func TestDropTakesOffAUnitWithAnEmptyPick(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): the first part, on its own", map[string]string{"app/already.txt": "first\n"})
	f.prCommits(5, "fix(app): a fix in two parts", "",
		change{files: map[string]string{"app/already.txt": "first\n"}},
		change{files: map[string]string{"app/new.txt": "second\n"}},
	)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "5", "--reason", "not for 1.1", "--push")
	head := f.remoteHead(batchBranch1)
	if got := f.subjects("origin/release-1.1", head); !slices.Equal(got, []string{"fix(app): six (#6)"}) || f.git("ls-tree", "--name-only", head, "--", "app/new.txt") != "" {
		t.Errorf("after the drop the batch carries %q, want #6 alone and nothing of #5", got)
	}
}

// A report keeps each drop the record keeps, with the merge commit it is
// bound to, its reason and the batch pull request that recorded it, so an
// earlier report still says why after the drop is withdrawn.
func TestReportKeepsEachDrop(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	mc6 := f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	stdout := mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "6", "--reason", "needs the rework of #4", "--push")
	dropped := bodyReportOf(t, stdout)
	want := `"overlays":{"dropped":[{"original":6,"merge":"` + mc6 + `","reason":"needs the rework of #4","on":90,"date":"2026-10-26"}]`
	if !strings.Contains(dropped.raw, want) {
		t.Errorf("the report does not keep the drop with its merge and reason:\n%s", dropped.raw)
	}
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--undrop", "6", "--push")
	if raw := bodyReportOf(t, gh.find(90).Body).raw; !strings.Contains(raw, `"overlays":{"dropped":[],`) {
		t.Errorf("the report after the undrop still keeps a drop:\n%s", raw)
	}
	code, out, stderr := f.exec(gh, "report", "--pr", "90", "--id", dropped.doc.ID)
	if code != 0 || !strings.Contains(out, "dropped: #6, merged as "+mc6[:12]+", on #90: needs the rework of #4\n") {
		t.Errorf("report --id of the earlier report = %d, want it to show the drop:\n%s%s", code, out, stderr)
	}
}

// A report's drop is held to the record's rules and bound to main as the
// record's drops are: a reason a run does not write, another original's
// merge, or an earlier commit on main that names the same number is
// refused, however the ID was made to match, in the body and in a trusted
// report comment alike.
func TestReportRefusesAForgedDrop(t *testing.T) {
	f := newFixture(t)
	f.release("release-1.0")
	f.tag("v1.0.0")
	early := f.squash(6, "docs: what six will change", "", map[string]string{"docs/six.md": "6\n"})
	f.release("release-1.1")
	f.tag("v1.1.0")
	mc5 := f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	mc6 := f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "6", "--reason", "not for 1.1", "--push")
	body, comments := gh.find(90).Body, slices.Clone(gh.issueComments)
	s := bodyReportOf(t, body)
	for _, tc := range []struct{ name, old, new, why string }{
		{"a reason of two lines", `"reason":"not for 1.1"`, `"reason":"not for 1.1\nwarning: forged"`, "which is no reason a run writes"},
		{"another original's merge", `"merge":"` + mc6 + `"`, `"merge":"` + mc5 + `"`, "which merged #5"},
		{"an earlier commit that names the same number", `"merge":"` + mc6 + `"`, `"merge":"` + early + `"`, "but #6 merged as " + mc6[:12]},
	} {
		raw := strings.Replace(s.raw, tc.old, tc.new, 1)
		if raw == s.raw {
			t.Fatalf("the report holds no %s to change:\n%s", tc.old, s.raw)
		}
		var d reportDoc
		if err := decodeStrict(raw, &d); err != nil {
			t.Fatalf("the changed report does not decode: %v", err)
		}
		d.ID = reportID(d)
		forged := storedReport{doc: d, raw: reportJSON(d)}
		refused := func(t *testing.T, args ...string) {
			t.Helper()
			code, out, stderr := f.exec(gh, append([]string{"report", "--pr", "90"}, args...)...)
			if code != 2 || !strings.Contains(stderr, tc.why) || strings.Contains(out+stderr, "\nwarning: forged") {
				t.Errorf("report of a forged drop = %d, want 2 with %q:\n%s%s", code, tc.why, out, stderr)
			}
		}
		t.Run(tc.name+", in the body", func(t *testing.T) {
			gh.find(90).Body = strings.Replace(body, reportBlock(s.raw), reportBlock(forged.raw), 1)
			defer func() { gh.find(90).Body = body }()
			refused(t)
		})
		t.Run(tc.name+", in a comment", func(t *testing.T) {
			text, err := reportComment(forged)
			if err != nil {
				t.Fatal(err)
			}
			c := issueComment{ID: 9000, Body: text}
			c.User.Login, c.User.Type = "maintainer", "User"
			gh.issueComments = append(slices.Clone(comments), c)
			defer func() { gh.issueComments = comments }()
			refused(t, "--id", d.ID)
		})
	}
}

// droppedNumbers are the originals a report's drops name.
func droppedNumbers(es []droppedEntry) []int {
	var out []int
	for _, e := range es {
		out = append(out, e.Original)
	}
	return out
}

// dropWorld has #3 merged before release-1.1 branched, an earlier commit on
// main that names #6, and a batch with #5 and #6 from which #6 is dropped.
func dropWorld(t *testing.T) (f *fixture, gh *fakeGH, inherited, mc5, mc6 string) {
	t.Helper()
	f = newFixture(t)
	f.release("release-1.0")
	f.tag("v1.0.0")
	inherited = f.prCommits(3, "feat(app): three", "", change{files: map[string]string{"app/three.txt": "3\n"}, message: "feat(app): part of three (#3)"})
	f.squash(6, "docs: what six will change", "", map[string]string{"docs/six.md": "6\n"})
	f.release("release-1.1")
	f.tag("v1.1.0")
	mc5 = f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	mc6 = f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh = &fakeGH{}
	openBatch(t, f, gh)
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "6", "--reason", "not for 1.1", "--push")
	return f, gh, inherited, mc5, mc6
}

// A report's drops and its categories hold each other up: an original it
// has dropped needs a drop that binds to it, and a drop of an original it
// classified needs that original dropped or decided by an earlier rule. A
// drop inherited from before the line branched decides nothing and stays
// readable. Each holds in the body and in a trusted report comment.
func TestReportTiesDropsToTheirCategory(t *testing.T) {
	f, gh, inherited, mc5, _ := dropWorld(t)
	body, comments := gh.find(90).Body, slices.Clone(gh.issueComments)
	s := bodyReportOf(t, body)
	kept := func(n int, merge string) droppedEntry {
		return droppedEntry{Original: n, Merge: merge, Reason: "carried from an earlier batch", On: 89, Date: "2026-10-25"}
	}
	for _, tc := range []struct {
		name   string
		change func(d *reportDoc)
		why    string
	}{
		{"an inherited drop in place of the classified one", func(d *reportDoc) { d.Overlays.Dropped[0] = kept(3, inherited) }, "has #6 dropped, but keeps no drop of it"},
		{"an inherited drop classified as dropped", func(d *reportDoc) {
			d.Primary.Skipped.Dropped = append(d.Primary.Skipped.Dropped, 3)
			d.Overlays.Dropped = append(d.Overlays.Dropped, kept(3, inherited))
		}, "has #3 dropped, but no drop of it binds to an original it classified"},
		{"a drop of an original it picked", func(d *reportDoc) { d.Overlays.Dropped = append(d.Overlays.Dropped, kept(5, mc5)) }, "drops #5, which it has picked"},
		{"an inherited drop beside the classified one", func(d *reportDoc) { d.Overlays.Dropped = append(d.Overlays.Dropped, kept(3, inherited)) }, ""},
	} {
		var d reportDoc
		if err := decodeStrict(s.raw, &d); err != nil {
			t.Fatal(err)
		}
		tc.change(&d)
		d.canonical()
		d.ID = reportID(d)
		forged := storedReport{doc: d, raw: reportJSON(d)}
		check := func(t *testing.T, args ...string) {
			t.Helper()
			code, out, stderr := f.exec(gh, append([]string{"report", "--pr", "90"}, args...)...)
			switch {
			case tc.why == "" && (code != 0 || !strings.Contains(out, "#6 skipped.dropped") || !strings.Contains(out, "dropped: #3, merged as "+inherited[:12]) || strings.Contains(out, "#3 skipped.dropped")):
				t.Errorf("report of a sound report = %d, want it read with #3's drop beside #6's:\n%s%s", code, out, stderr)
			case tc.why != "" && (code != 2 || out != "" || !strings.Contains(stderr, tc.why)):
				t.Errorf("report = %d, want 2 with %q:\n%s%s", code, tc.why, out, stderr)
			}
		}
		t.Run(tc.name+", in the body", func(t *testing.T) {
			gh.find(90).Body = strings.Replace(body, reportBlock(s.raw), reportBlock(forged.raw), 1)
			defer func() { gh.find(90).Body = body }()
			check(t)
		})
		t.Run(tc.name+", in a comment", func(t *testing.T) {
			text, err := reportComment(forged)
			if err != nil {
				t.Fatal(err)
			}
			c := issueComment{ID: 9001, Body: text}
			c.User.Login, c.User.Type = "maintainer", "User"
			gh.issueComments = append(slices.Clone(comments), c)
			defer func() { gh.issueComments = comments }()
			check(t, "--id", d.ID)
		})
	}
}
