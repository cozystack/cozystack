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
	"strings"
	"testing"
)

// chainWorld has #5 conflict on the line and change a shared line, and #7
// change that line again: #7 builds on #5.
func chainWorld(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.squash(2, "chore: five and shared", "", map[string]string{"app/five.txt": "old\n", "app/shared.txt": appFile})
	f.lines()
	f.onLine("release-1.1", "fix(app): five, the line's way", map[string]string{"app/five.txt": "line\n"})
	mc5 := f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "five\n", "app/shared.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/shared.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	return f, mc5
}

// An original that builds on a deferred conflict waits for it: check warns,
// notify lists it after the conflict, and once the conflict lands, the next
// run picks it.
func TestWaitForADeferredDependency(t *testing.T) {
	f, mc5 := chainWorld(t)
	gh := &fakeGH{}
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push")
	if code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, `{"original":7,"files":[],"needs":[5]}`) || !strings.Contains(stdout, "### Waiting on another original") {
		t.Errorf("#7 does not wait on #5:\n%s", stdout)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	code, stdout, stderr = f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stderr, "warning: #7 waits on #5, a deferred conflict on #90: https://github.com/cozystack/cozystack/pull/7 ") {
		t.Errorf("check = %d, want a warning that #7 waits on #5:\n%s\n%s", code, stdout, stderr)
	}
	code, stdout, _ = f.exec(gh, "notify", "--line", "release-1.1", "--push")
	if code != 0 || !strings.Contains(gh.issueComments[len(gh.issueComments)-1].Body, "- [#7](https://github.com/cozystack/cozystack/pull/7), after [#5](https://github.com/cozystack/cozystack/pull/5)") {
		t.Errorf("notify = %d, want #7 listed after #5:\n%s\n%v", code, stdout, gh.issueComments)
	}
	// The batch merges, and #5 lands on the line by hand, resolved: the
	// next batch picks #7.
	mergeBatch(f, gh)
	f.onLine("release-1.1", "fix(app): five (#5)\n\n(cherry picked from commit "+mc5+")", map[string]string{"app/five.txt": "five\n", "app/shared.txt": strings.Replace(appFile, "two", "TWO", 1)})
	code, stdout, stderr = f.exec(gh, "run", "--line", "release-1.1", "--continue")
	if code != 0 || !strings.Contains(stdout, `"picked":[7]`) {
		t.Errorf("run --continue after #5 landed = %d, want #7 picked:\n%s\n%s", code, stdout, stderr)
	}
}

// Of several prerequisites still to come, the strongest decides: one not on
// the line at all, then one waiting for a maintainer, then a conflict or a
// deferral, then one rule 6 holds; a chain inherits what its end decides.
func TestWaitReduction(t *testing.T) {
	t.Run("a conflict and a workflow wait", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: files", "", map[string]string{"app/five.txt": "old\n", "app/a.txt": appFile, "app/b.txt": appFile})
		f.lines()
		f.onLine("release-1.1", "fix(app): five, the line's way", map[string]string{"app/five.txt": "line\n"})
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "five\n", "app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		f.pr(6, "fix(ci): six", "", map[string]string{".github/workflows/six.yaml": "on: push\n", "app/b.txt": strings.Replace(appFile, "two", "TWO", 1)})
		f.pr(7, "fix(app): seven", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1), "app/b.txt": strings.Replace(appFile, "two", "TWO!", 1)})
		f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
		gh := &fakeGH{}
		if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 || !strings.Contains(stdout, `{"original":7,"files":[],"needs":[6]}`) {
			t.Fatalf("run = %d, want #7 waiting on #6 alone:\n%s\n%s", code, stdout, stderr)
		}
		f.git("switch", "-q", "main")
		reopen(f, gh)
		if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || !strings.Contains(stdout, "#7 waits on #6, which changes .github/workflows/ and waits on batch PR #90 for a maintainer") {
			t.Errorf("check --cut = %d, want #7 to hold the cut:\n%s\n%s", code, stdout, stderr)
		}
		if code, stdout, _ := f.exec(gh, "notify", "--line", "release-1.1"); code != 0 || !strings.Contains(stdout, "2 originals on #90 change .github/workflows/ or wait on one that does") {
			t.Errorf("notify = %d, want #7 among the waits:\n%s", code, stdout)
		}
	})
	t.Run("a conflict and one not on the line", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: files", "", map[string]string{"app/five.txt": "old\n", "app/a.txt": appFile, "app/b.txt": appFile})
		f.lines()
		f.onLine("release-1.1", "fix(app): five, the line's way", map[string]string{"app/five.txt": "line\n"})
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "five\n", "app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		gh := &fakeGH{}
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 0 {
			t.Fatalf("run = %d\n%s", code, stderr)
		}
		f.git("switch", "-q", "main")
		reopen(f, gh)
		f.pr(9, "fix(app): nine", "", map[string]string{"app/b.txt": strings.Replace(appFile, "two", "TWO", 1)})
		f.pr(10, "fix(app): ten", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1), "app/b.txt": strings.Replace(appFile, "two", "TWO!", 1)})
		if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/10 fix(app): ten (it waits on #9, which is not on the line either)") {
			t.Errorf("check = %d, want #10 to fail it, waiting on #9:\n%s\n%s", code, stdout, stderr)
		}
	})
	t.Run("a prerequisite on the open batch", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
		f.lines()
		f.pr(5, "fix(app): five", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		f.pr(7, "fix(app): seven", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
		gh := &fakeGH{}
		if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 0 || !strings.Contains(stdout, `"picked":[5,7]`) {
			t.Fatalf("run = %d, want #5 and #7 picked:\n%s\n%s", code, stdout, stderr)
		}
		f.git("switch", "-q", "main")
		reopen(f, gh)
		if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || !strings.Contains(stdout, "#5 is only on open batch PR #90") || strings.Contains(stdout+stderr, "waits on") {
			t.Errorf("check --cut = %d, want #5 and #7 only on the batch and no wait:\n%s\n%s", code, stdout, stderr)
		}
	})
	t.Run("a direct reason", func(t *testing.T) {
		f := featWorld(t)
		gh := &fakeGH{}
		openBatch(t, f, gh)
		if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "needs the tap", "--push"); code != 0 {
			t.Fatalf("run --defer = %d\n%s", code, stderr)
		}
		reopen(f, gh)
		if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || !strings.Contains(stderr, "warning: deferred on #90 to the next batch: https://github.com/cozystack/cozystack/pull/5 ") || strings.Contains(stderr, "parked") {
			t.Errorf("check = %d, want #5 deferred for its reason, not parked:\n%s\n%s", code, stdout, stderr)
		}
	})
	t.Run("a prerequisite rule 6 holds", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
		f.lines()
		f.pr(5, "fix(app): five", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		mc7 := f.pr(7, "fix(app): seven", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1), "app/seven.txt": "7\n"})
		f.onLine("release-1.1", "fix(app): seven (#7)\n\n(cherry picked from commit "+mc7+")", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1), "app/seven.txt": "7\n"})
		if code, stdout, stderr := f.exec(nil, "check", "--line", "release-1.0"); code != 0 || !strings.Contains(stderr, "note: #7 waits on #5, which is not on release-1.1 yet") {
			t.Errorf("check of release-1.0 = %d, want a note that #7 waits on #5:\n%s\n%s", code, stdout, stderr)
		}
	})
	t.Run("a chain", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
		f.lines()
		f.pr(4, "fix(app): four", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		f.pr(5, "fix(app): five", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
		f.pr(7, "fix(app): seven", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!!", 1)})
		if code, stdout, stderr := f.exec(nil, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/7 fix(app): seven (it waits on #5, which is not on the line either)") {
			t.Errorf("check = %d, want #7 failing it through #5:\n%s\n%s", code, stdout, stderr)
		}
	})
}

// run's report, check and notify give an original one disposition: a
// workflow original whose only missing dependency is a test waits for a
// maintainer, and one deferred for a reason stays deferred, though it
// builds on what never comes; neither is parked.
func TestDispositionOrderIsSharedByRunCheckNotify(t *testing.T) {
	f := auxWorld(t)
	f.pr(20, "feat(app): tap", "", map[string]string{"app/tap.go": appFile})
	f.pr(7, "fix(app): fix the tap", "", map[string]string{"app/tap.go": strings.Replace(appFile, "two", "TWO", 1)})
	gh := &fakeGH{}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "7", "--reason", "waits for the tap", "--push")
	if code != 0 {
		t.Fatalf("run --defer = %d\n%s", code, stderr)
	}
	if strings.Contains(stdout, `"parked":[{`) || !strings.Contains(stdout, `{"original":5,"files":[".github/workflows/release.yaml"],"workflows":true}`) || !strings.Contains(stdout, `{"original":7,"files":[],"reason":"waits for the tap"}`) {
		t.Errorf("the report does not have #5 waiting and #7 deferred, unparked:\n%s", stdout)
	}
	reopen(f, gh)
	code, stdout, stderr = f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 || strings.Contains(stderr, "parked") || !strings.Contains(stderr, "warning: waiting on #90 for a maintainer, since it changes .github/workflows/: https://github.com/cozystack/cozystack/pull/5 ") || !strings.Contains(stderr, "warning: deferred on #90 to the next batch: https://github.com/cozystack/cozystack/pull/7 ") {
		t.Errorf("check = %d, want #5 waiting and #7 deferred:\n%s\n%s", code, stdout, stderr)
	}
	code, stdout, _ = f.exec(gh, "notify", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stdout, "1 originals on #90 change .github/workflows/") || strings.Contains(stdout, "parked") || strings.Contains(stdout, "conflicts") {
		t.Errorf("notify = %d, want #5 waiting and nothing for #7:\n%s", code, stdout)
	}
}

// A never and a pending prerequisite together park.
func TestParkWinsOverWait(t *testing.T) {
	f, _ := chainWorld(t)
	f.pr(20, "feat(app): tap", "", map[string]string{"app/tap.go": appFile})
	f.pr(8, "fix(app): eight", "", map[string]string{"app/shared.txt": strings.Replace(appFile, "two", "TWO!!", 1), "app/tap.go": strings.Replace(appFile, "two", "TWO", 1)})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stdout, `"kind":"dependency","needs":[20]`) || strings.Contains(stdout, `"original":8,"files":[],"needs"`) {
		t.Errorf("run = %d, want #8 parked on #20, not waiting on #7:\n%s\n%s", code, stdout, stderr)
	}
}

// A prerequisite picked earlier in the same batch satisfies what builds on
// it, and so does one whose pick came out empty because an earlier pick
// already carried it: #4280 and #3938.
func TestDependencySatisfiedEarlierInTheBatch(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.pr(3, "fix(kafka): delete hooks", "", map[string]string{"app/hooks.yaml": appFile})
	f.pr(4, "fix(kafka): fix the delete hook", "", map[string]string{"app/hooks.yaml": strings.Replace(appFile, "two", "TWO", 1)})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || !strings.Contains(stdout, `"picked":[3,4]`) {
		t.Errorf("run = %d, want #3 and #4 picked:\n%s\n%s", code, stdout, stderr)
	}

	// #30 adds back the file #2 brought and a feat took away, so its pick
	// comes out empty after #2's, and #4 builds on #30.
	f = newFixture(t)
	f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.pr(20, "fix(kafka): delete hooks, and two", "", map[string]string{"app/hooks.yaml": appFile, "app/two.txt": "2\n"})
	f.pr(21, "feat(kafka): no delete hooks", "", map[string]string{"app/hooks.yaml": ""})
	f.pr(30, "fix(kafka): delete hooks again", "", map[string]string{"app/hooks.yaml": appFile})
	f.pr(4, "fix(kafka): fix the delete hook", "", map[string]string{"app/hooks.yaml": strings.Replace(appFile, "two", "TWO", 1)})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || !strings.Contains(stdout, `"picked":[4,20]`) || strings.Contains(stdout, `"needs"`) {
		t.Errorf("run with #30's pick empty = %d, want #4 picked:\n%s\n%s", code, stdout, stderr)
	}
}
