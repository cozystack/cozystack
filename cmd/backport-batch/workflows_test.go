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

// workflowWorld has #5 change a workflow and #6 change the app and a file
// under .github/ that is no workflow.
func workflowWorld(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(ci): five", "", map[string]string{".github/workflows/five.yaml": "on: push\n", "app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n", ".github/renovate.json": "{}\n"})
	return f
}

// With --no-workflow-files, as the scheduled batch runs, an original that
// changes a workflow is left to a maintainer, with its workflow files, and is
// neither a conflict nor deferred with a reason. Without the flag it is picked
// like any other.
func TestRunLeavesWorkflowChangesToAMaintainer(t *testing.T) {
	l, _ := parseLine("release-1.1")
	f := workflowWorld(t)
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1", "--no-workflow-files")
	if code != 0 {
		t.Fatalf("run --no-workflow-files = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("picked %q, want only #6", got)
	}
	deferred, err := parseDeferred(stdout, l)
	if e := deferred[5]; err != nil || len(deferred) != 1 || !e.Workflows || e.Reason != "" || e.Commit != "" || !slices.Equal(e.Files, []string{".github/workflows/five.yaml"}) {
		t.Errorf("deferred = %+v, %v; want #5 waiting with its workflow file", deferred, err)
	}
	for _, want := range []string{"### Waiting for a maintainer", "[#5](", "`.github/workflows/five.yaml`", "#changes-to-workflows"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the body lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "### Deferred") {
		t.Errorf("the body lists #5 as deferred:\n%s", stdout)
	}
	if !strings.Contains(stderr, "waiting for a maintainer: https://github.com/cozystack/cozystack/pull/5 fix(ci): five changes .github/workflows/five.yaml") {
		t.Errorf("the run does not say #5 waits:\n%s", stderr)
	}

	f = workflowWorld(t)
	code, stdout, stderr = f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(ci): five (#5)", "fix(app): six (#6)"}) {
		t.Errorf("without the flag picked %q, want #5 and #6", got)
	}
	if strings.Contains(stdout, "Waiting for a maintainer") {
		t.Errorf("without the flag the body has something wait:\n%s", stdout)
	}
}

// Waiting is no way off the line: check lists the original until it is on
// it, the scheduled run that continues the batch keeps it waiting rather than
// trying it as a conflict, notify lists it apart from the conflicts, and a
// maintainer's own run picks it onto the open batch.
func TestWorkflowChangesWaitUntilOnTheLine(t *testing.T) {
	f := workflowWorld(t)
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files"); code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	if code, stdout, _ := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/5 ") {
		t.Errorf("check with no batch open = %d, %q; want #5 missing", code, stdout)
	}

	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stderr, "warning: waiting on #90 for a maintainer, since it changes .github/workflows/: https://github.com/cozystack/cozystack/pull/5 ") {
		t.Errorf("check = %d, %q; want 0 and a warning about #5:\n%s", code, stdout, stderr)
	}
	code, stdout, _ = f.exec(gh, "check", "--line", "release-1.1", "--cut")
	if code != 1 || !strings.Contains(stdout, "#5 changes .github/workflows/ and waits on batch PR #90 for a maintainer") {
		t.Errorf("check --cut = %d, %q; want #5 to stop the cut", code, stdout)
	}

	code, stdout, stderr = f.exec(gh, "notify", "--line", "release-1.1", "--push")
	if code != 0 || !strings.Contains(stdout, "1 originals on #90 change .github/workflows/ and wait for a maintainer") || strings.Contains(stdout, "deferred conflicts") {
		t.Errorf("notify = %d, %q; want #5 for a maintainer and no conflict\n%s", code, stdout, stderr)
	}
	if len(gh.issueComments) != 1 || !strings.Contains(gh.issueComments[0].Body, "[#5](") || !strings.Contains(gh.issueComments[0].Body, "#changes-to-workflows") || strings.Contains(gh.issueComments[0].Body, "These conflicts") {
		t.Errorf("the comment does not list #5 for a maintainer: %+v", gh.issueComments)
	}

	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	code, stdout, stderr = f.exec(gh, "run", "--line", "release-1.1", "--continue", "--no-workflow-files", "--push")
	if code != 0 {
		t.Fatalf("run --continue = %d\n%s", code, stderr)
	}
	l, _ := parseLine("release-1.1")
	if deferred, err := parseDeferred(stdout, l); err != nil || !deferred[5].Workflows || len(deferred) != 1 {
		t.Errorf("the continued body has %+v, %v; want #5 still waiting", deferred, err)
	}
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): six (#6)", "fix(app): seven (#7)"}) {
		t.Errorf("the continued batch carries %q", got)
	}

	f.git("switch", "-q", "main")
	reopen(f, gh)
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 0 {
		t.Fatalf("a maintainer's run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): six (#6)", "fix(app): seven (#7)", "fix(ci): five (#5)"}) {
		t.Errorf("after a maintainer's run the batch carries %q, want #5 on top", got)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || strings.Contains(stderr, "waiting") {
		t.Errorf("check after the maintainer's run = %d, %q\n%s", code, stdout, stderr)
	}
}

// --defer keeps what waits as it is, and takes one out like any other original
// when a maintainer gives the reason.
func TestDeferKeepsWorkflowChangesWaiting(t *testing.T) {
	f := workflowWorld(t)
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	l, _ := parseLine("release-1.1")
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "7", "--reason", "fails the app e2e", "--push")
	if code != 0 {
		t.Fatalf("run --defer 7 = %d\n%s", code, stderr)
	}
	if deferred, err := parseDeferred(stdout, l); err != nil || !deferred[5].Workflows || deferred[7].Reason != "fails the app e2e" || len(deferred) != 2 {
		t.Errorf("after --defer 7 the body has %+v, %v; want #5 waiting and #7 deferred", deferred, err)
	}

	f.git("switch", "-q", "main")
	reopen(f, gh)
	code, stdout, stderr = f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "waits for the 1.1.4 workflow rework", "--push")
	if code != 0 {
		t.Fatalf("run --defer 5 = %d\n%s", code, stderr)
	}
	if deferred, err := parseDeferred(stdout, l); err != nil || deferred[5].Workflows || deferred[5].Reason == "" {
		t.Errorf("after --defer 5 the body has %+v, %v; want #5 deferred with the reason", deferred, err)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	// #6 and #7 are still only on the batch, which fails the cut; #5 does not.
	if code, stdout, _ := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || strings.Contains(stdout, "#5 changes") || !strings.Contains(stdout, "#6 is only on open batch PR #90") {
		t.Errorf("check --cut = %d, %q; want 1 for #6 and #7 alone, a deferred #5 no longer waiting", code, stdout)
	}
	if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || !strings.Contains(stderr, "deferred on #90 to the next batch: https://github.com/cozystack/cozystack/pull/5 ") {
		t.Errorf("check = %d, want 0 with a warning about #5:\n%s", code, stderr)
	}
}

// A record that has an original wait and also names a failed commit or a
// reason is refused: it is one or the other.
func TestParseDeferredRefusesAMixedEntry(t *testing.T) {
	l, _ := parseLine("release-1.1")
	for _, entry := range []string{
		`{"original":5,"files":[],"workflows":true,"reason":"x"}`,
		`{"original":5,"files":[],"workflows":true,"commit":"` + strings.Repeat("a", 40) + `"}`,
	} {
		body := deferredFence + "\n" + `{"line":"release-1.1","deferred":[` + entry + `]}` + "\n```\n"
		if _, err := parseDeferred(body, l); err == nil {
			t.Errorf("parseDeferred accepted %s", entry)
		}
	}
}

// A pull request that changes a workflow in one commit and takes it back in a
// later one changes no workflow in its merge, but its first pick does, and
// the push carries that pick: it waits as well.
func TestRunHoldsAWorkflowChangeTakenBack(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.prCommits(5, "fix(ci): five", "",
		change{files: map[string]string{".github/workflows/five.yaml": "on: push\n"}},
		change{files: map[string]string{".github/workflows/five.yaml": "", "app/five.txt": "5\n"}})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1", "--no-workflow-files")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("picked %q, want only #6", got)
	}
	l, _ := parseLine("release-1.1")
	if deferred, err := parseDeferred(stdout, l); err != nil || !deferred[5].Workflows || !slices.Equal(deferred[5].Files, []string{".github/workflows/five.yaml"}) {
		t.Errorf("deferred = %+v, %v; want #5 waiting on its workflow file", deferred, err)
	}
}

// A rebuild keeps the count of workflow files a waiting original changes
// beyond those the block names.
func TestDeferKeepsTheWaitingFileCount(t *testing.T) {
	f := newFixture(t)
	f.lines()
	files := map[string]string{"app/five.txt": "5\n"}
	for i := range filesKept + 2 {
		files[fmt.Sprintf(".github/workflows/w%02d.yaml", i)] = "on: push\n"
	}
	f.pr(5, "fix(ci): five", "", files)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "7", "--reason", "fails the app e2e", "--push")
	if code != 0 {
		t.Fatalf("run --defer = %d\n%s", code, stderr)
	}
	l, _ := parseLine("release-1.1")
	deferred, err := parseDeferred(stdout, l)
	if e := deferred[5]; err != nil || !e.Workflows || len(e.Files) != filesKept || e.More != 2 {
		t.Errorf("after the rebuild #5 is %d files and %d more, %v; want %d and 2", len(e.Files), e.More, err, filesKept)
	}
	if want := fmt.Sprintf("%d more", filesKept+2-filesListed); !strings.Contains(stdout, want) {
		t.Errorf("the rebuilt body does not say %q:\n%s", want, stdout)
	}
}
