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
	"strings"
	"testing"
)

// pickOnLine lands original n on release-1.1 as a maintainer's pick of its
// merge commit, with the -x line git appends, so the line carries it.
func pickOnLine(f *fixture, n int, files map[string]string) {
	f.t.Helper()
	mc, _, _ := strings.Cut(f.git("log", "--first-parent", "--format=%H", "--grep", fmt.Sprintf("(#%d)$", n), "main"), "\n")
	f.onLine("release-1.1", fmt.Sprintf("fix: original %d\n\n(cherry picked from commit %s)", n, mc), files)
}

// A workflow original that waits on the open batch fails the cut, also when
// it is all the batch has left: the pick beside it was labelled
// backport/skip after the pick, or reached the line by hand.
func TestCutFailsWhenOnlyAWorkflowWaitIsLeft(t *testing.T) {
	for _, tc := range []struct {
		name  string
		leave func(f *fixture, gh *fakeGH)
	}{
		{"skip after the pick", func(_ *fixture, gh *fakeGH) { gh.labels = map[string][]int{labelSkip: {6}} }},
		{"picked onto the line by hand", func(f *fixture, _ *fakeGH) {
			pickOnLine(f, 6, map[string]string{"app/six.txt": "6\n", ".github/renovate.json": "{}\n"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := workflowWorld(t)
			gh := &fakeGH{}
			if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 {
				t.Fatalf("run --push = %d\n%s", code, stderr)
			}
			f.git("switch", "-q", "main")
			reopen(f, gh)
			tc.leave(f, gh)
			code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut")
			if code != 1 || !strings.Contains(stdout, "#5 changes .github/workflows/ and waits on batch PR #90 for a maintainer") {
				t.Errorf("check --cut = %d, want 1 naming #5:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "on the line or on the batch") {
				t.Errorf("check --cut passes the line while #5 waits:\n%s", stdout)
			}
		})
	}
}

// A conflict the batch deferred is still deferred once that batch merged:
// the next run picks nothing, opens nothing and writes nothing to the merged
// pull request, and check reads the merged batch's record.
func TestCheckAfterTheBatchMerged(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): a line-only five", map[string]string{"app/five.txt": "line\n"})
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	mergeBatch(f, gh)
	written := len(gh.created) + len(gh.edited) + len(gh.issueComments)
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 0 {
		t.Fatalf("run --continue --push = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	if got := len(gh.created) + len(gh.edited) + len(gh.issueComments); got != written || f.remoteHead(batchBranch1) != "" {
		t.Errorf("the run picked nothing and still wrote: created %q, edited %q", gh.created, gh.edited)
	}
	for _, cut := range []bool{false, true} {
		args := []string{"check", "--line", "release-1.1"}
		if cut {
			args = append(args, "--cut")
		}
		code, stdout, stderr := f.exec(gh, args...)
		if code != 0 || !strings.Contains(stderr, "warning: deferred conflict on #90 (merged): https://github.com/cozystack/cozystack/pull/5 ") {
			t.Errorf("%q = %d, want 0 with #5 a deferred conflict:\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		}
	}
}

// A workflow original waits only while a batch is open to list it: once the
// batch that listed it merged, it is missing.
func TestWorkflowWaitAfterTheBatchMergedIsMissing(t *testing.T) {
	f := workflowWorld(t)
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	mergeBatch(f, gh)
	for _, cut := range []bool{false, true} {
		args := []string{"check", "--line", "release-1.1"}
		if cut {
			args = append(args, "--cut")
		}
		if code, stdout, stderr := f.exec(gh, args...); code != 1 || !strings.Contains(stdout, "/pull/5 ") {
			t.Errorf("%q = %d, want 1 with #5 missing:\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		}
	}
}
