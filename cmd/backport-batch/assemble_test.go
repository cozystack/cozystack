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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A clean pick lands with the original message and its -x line, in main's
// merge order, -m 1 for a merge and a plain pick for a squash.
func TestRunPicksInMergeOrder(t *testing.T) {
	f := newFixture(t)
	f.lines()
	mc5 := f.pr(5, "fix(app): five", "Body.\n\n```release-note\nfix(app): five is fixed\n```", map[string]string{"app/five.txt": "5\n"})
	sq6 := f.squash(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.pr(7, "feat(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	mc8 := f.pr(8, "fix(app): eight", "", map[string]string{"app/five.txt": "5 and 8\n"})

	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.git("branch", "--show-current"); got != batchBranch1 {
		t.Fatalf("on %q, want %s", got, batchBranch1)
	}
	want := []string{"fix(app): five (#5)", "fix(app): six (#6)", "fix(app): eight (#8)"}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, want) {
		t.Errorf("picked %q, want %q", got, want)
	}
	for i, mc := range []string{mc5, sq6, mc8} {
		msg := f.git("log", "-1", "--format=%B", "HEAD~"+strconv.Itoa(2-i))
		if !strings.Contains(msg, "(cherry picked from commit "+mc+")\nSigned-off-by: Batch Test <batch@example.invalid>") {
			t.Errorf("pick %d lacks its -x line for %s or its sign-off:\n%s", i, mc, msg)
		}
	}
	if f.git("rev-parse", "HEAD:app/five.txt") != f.git("rev-parse", mc8+":app/five.txt") {
		t.Error("app/five.txt is not what main has after #8")
	}
	for _, s := range []string{"[#5]", "[#6]", "[#8]", "fix(app): five is fixed", "Rule 2"} {
		if !strings.Contains(stdout, s) {
			t.Errorf("the body lacks %q", s)
		}
	}
	if strings.Contains(stdout, "[#7](") && !strings.Contains(stdout, "<details>") {
		t.Error("#7 is listed outside the skipped details")
	}
}

// A conflict is aborted, recorded with its files, and the next original is
// still picked.
func TestRunRecordsConflictsAndGoesOn(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})
	f.pr(5, "fix(chart): five", "", map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})

	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("picked %q, want only #6", got)
	}
	if out := f.git("status", "--porcelain"); out != "" {
		t.Errorf("the tree is left dirty:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git", "CHERRY_PICK_HEAD")); err == nil {
		t.Error("a cherry-pick is left in progress")
	}
	l, _ := parseLine("release-1.1")
	deferred, err := parseDeferred(stdout, l)
	if err != nil || !deferred[5] || len(deferred) != 1 {
		t.Errorf("deferred = %v, %v; want #5", deferred, err)
	}
	if !strings.Contains(stdout, "### Deferred conflicts") || !strings.Contains(stdout, "`chart/values.yaml`") {
		t.Errorf("the body does not name the conflict and its file:\n%s", stdout)
	}
}

// An original the earlier picks already carry is skipped as an empty pick.
func TestRunSkipsAnEmptyPick(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): add x", "", map[string]string{"app/x.txt": "x\n"})
	f.pr(6, "revert(app): drop x", "", map[string]string{"app/x.txt": ""})
	f.pr(7, "fix(app): add x again", "", map[string]string{"app/x.txt": "x\n"})

	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): add x (#5)"}) {
		t.Errorf("picked %q, want only #5", got)
	}
	if !strings.Contains(stdout, "the earlier picks already carry it") {
		t.Errorf("#7 is not reported as carried by the earlier picks:\n%s", stdout)
	}
}

// A hook in the repository or the user's configuration never rewrites what a
// pick carries.
func TestRunIgnoresHooks(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	hooks := filepath.Join(f.dir, ".git", "hooks")
	for _, hook := range []string{"prepare-commit-msg", "commit-msg"} {
		if err := os.WriteFile(filepath.Join(hooks, hook), []byte("#!/bin/sh\necho rewritten by a hook > \"$1\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): five (#5)"}) {
		t.Errorf("picked %q, want #5 as it was merged", got)
	}
}

func TestRunRefusesADirtyTree(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.write("README.md", "changed\n")
	code, _, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 2 || !strings.Contains(stderr, "has changes") {
		t.Errorf("run on a dirty tree = %d (%s), want 2", code, stderr)
	}
	if f.git("branch", "--show-current") != "main" {
		t.Error("the tree was switched anyway")
	}

	// A pick that stopped empty leaves the tree clean and the pick going.
	f.git("checkout", "-q", "--", "README.md")
	if err := os.WriteFile(filepath.Join(f.dir, ".git", "CHERRY_PICK_HEAD"), []byte(f.git("rev-parse", "HEAD")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = f.exec(nil, "run", "--line", "release-1.1")
	if code != 2 || !strings.Contains(stderr, "operation is in progress (CHERRY_PICK_HEAD)") {
		t.Errorf("run during a cherry-pick = %d (%s), want 2", code, stderr)
	}
}

// --push pushes a fresh branch and opens the draft, and a second push of the
// same branch updates the open pull request instead.
func TestRunPush(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	head := f.git("rev-parse", "HEAD")
	if got := f.remoteHead(batchBranch1); got != head {
		t.Errorf("remote %s = %q, want %s", batchBranch1, got, head)
	}
	if want := []string{"release-1.1 " + batchBranch1 + " fix(backport): batch for release-1.1, 2026-10-26"}; !slices.Equal(gh.created, want) {
		t.Errorf("created %q, want %q", gh.created, want)
	}

	f.git("switch", "-q", "main")
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, gh.bodies[0])}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--onto", head, "--push"); code != 0 {
		t.Fatalf("run --onto --push = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", f.remoteHead(batchBranch1)); !slices.Equal(got, []string{"fix(app): five (#5)", "fix(app): six (#6)"}) {
		t.Errorf("the pushed batch carries %q, want #5 then #6", got)
	}
	if len(gh.edited) != 1 || gh.edited[0] != "#90 fix(backport): batch for release-1.1, 2026-10-26" {
		t.Errorf("edited %q, want #90", gh.edited)
	}
	if body := gh.bodies[len(gh.bodies)-1]; !strings.Contains(body, "[#5]") || !strings.Contains(body, "[#6]") {
		t.Errorf("the updated body does not list both originals:\n%s", body)
	}
}

// With a batch pull request open on another branch, a fresh push would open
// a second one; it is refused before anything is pushed.
func TestRunPushRefusesASecondBatch(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	old := "backport-batch/release-1.1-20261022"
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+old)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{prs: []pullRequest{f.batchPR(90, "release-1.1", old, "")}}
	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push")
	if code != 2 || !strings.Contains(stderr, "continue it with --onto") {
		t.Errorf("run --push = %d (%s), want a refusal", code, stderr)
	}
	if f.remoteHead(batchBranch1) != "" || len(gh.created)+len(gh.edited) != 0 {
		t.Error("something was pushed or opened")
	}
}
