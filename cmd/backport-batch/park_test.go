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

var line11, _ = parseLine("release-1.1")

// handBatch pushes a batch carrying merges in the batch's shape, as a run
// made one before the partial guard, and opens its pull request with a
// record that defers nothing.
func handBatch(f *fixture, gh *fakeGH, merges ...string) string {
	f.t.Helper()
	f.git("fetch", "-q", "origin")
	f.git("switch", "-q", "--detach", "origin/release-1.1")
	for _, mc := range merges {
		if parents := strings.Fields(f.git("rev-list", "--parents", "-n", "1", mc)); len(parents) == 2 {
			f.git("cherry-pick", "-x", mc)
			continue
		}
		base := f.git("rev-parse", "HEAD")
		for _, c := range strings.Fields(f.git("rev-list", "--reverse", "--no-merges", mc+"^1.."+mc+"^2")) {
			f.git("cherry-pick", "-x", c)
		}
		merge := f.joined(base, "HEAD", f.git("log", "-1", "--format=%B", mc)+"\n\n"+batchTrailer+": "+mc)
		f.git("update-ref", "HEAD", merge)
	}
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "batch\n\n"+deferredBlock(line11, nil, nil, nil)+"\n")}
	return f.remoteHead(batchBranch1)
}

// policyWorld is #5088's shape: #3, a feat the line never gets, adds a
// policy and its test; #5 deletes both again and adds a test of its own.
func policyWorld(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.pr(3, "feat(chart): a network policy", "", map[string]string{"chart/templates/policy.yaml": "kind: NetworkPolicy\n", "chart/tests/policy_test.yaml": "suite: policy\n"})
	mc5 := f.pr(5, "fix(chart): remove the policy a rebase brought back", "```release-note\nfix(chart): the stray network policy is gone.\n```\n",
		map[string]string{"chart/templates/policy.yaml": "", "chart/tests/policy_test.yaml": "", "chart/tests/values_test.yaml": "suite: values\n"})
	return f, mc5
}

// A clean pick that would delete files the line never had, and so carry only
// #5's test, is withheld: #5 is parked as partial, with the paths it would
// leave without effect, and its release note stays out of the body.
func TestPartialPickIsWithheld(t *testing.T) {
	f, mc5 := policyWorld(t)
	f.pr(6, "fix(app): six", "```release-note\nfix(app): six works.\n```\n", map[string]string{"app/six.txt": "6\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("picked %q, want only #6", got)
	}
	want := `"parked":[{"original":5,"merge":"` + mc5 + `","kind":"partial","files":["chart/templates/policy.yaml","chart/tests/policy_test.yaml"]}]`
	if !strings.Contains(stdout, want) {
		t.Errorf("the record does not park #5 as partial (%s):\n%s", want, stdout)
	}
	if !strings.Contains(stdout, "### Parked") || !strings.Contains(stdout, "`chart/templates/policy.yaml`") {
		t.Errorf("the body does not say #5 is parked and why:\n%s", stdout)
	}
	if strings.Contains(stdout, "stray network policy") || !strings.Contains(stdout, "six works") {
		t.Errorf("the release notes are not #6's alone:\n%s", stdout)
	}
}

// A file the line removed itself, which no original added after the line
// branched, still leaves #5's deletion without effect: check finds it with no
// record and parks it, and run picks nothing.
func TestPartialWithoutAnAdder(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: add a policy", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): the line drops the policy", map[string]string{"app/policy.yaml": ""})
	f.pr(5, "fix(app): drop the policy", "", map[string]string{"app/policy.yaml": "", "app/policy_test.txt": "gone\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); len(got) != 0 {
		t.Errorf("picked %q, want nothing", got)
	}
	if !strings.Contains(stdout, "### Parked") {
		t.Errorf("the body does not park #5:\n%s", stdout)
	}
	f.git("switch", "-q", "main")
	code, stdout, stderr = f.exec(nil, "check", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stderr, "note: #5 is parked: only part of it applies to release-1.1") {
		t.Errorf("check = %d, want 0 with a parked-partial note:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// The picker judges what its picks did, not what the original's merge did:
// #5's first commit deletes a file the line no longer has and comes out
// empty, its second adds a test, and the unit left would be the test alone.
func TestPartialByAnEmptyOwnCommit(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: add a policy", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): the line drops the policy", map[string]string{"app/policy.yaml": ""})
	f.prCommits(5, "fix(app): drop the policy and test it", "```release-note\nfix(app): no more policy.\n```\n",
		change{files: map[string]string{"app/policy.yaml": ""}},
		change{files: map[string]string{"app/policy_test.txt": "gone\n"}})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); len(got) != 0 {
		t.Errorf("picked %q, want #5 withheld", got)
	}
	if strings.Contains(stdout, "no more policy") || !strings.Contains(stdout, "### Parked") {
		t.Errorf("#5's note is in the body, or #5 is not parked:\n%s", stdout)
	}
}

// A batch built before the guard can already carry a partial unit. It fails
// the cut and verify whatever its label says, and --defer takes it off,
// closing a batch it was the last unit of.
func TestExistingPartialUnitFailsTheCut(t *testing.T) {
	f, mc5 := policyWorld(t)
	gh := &fakeGH{}
	head := handBatch(f, gh, mc5)
	code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut")
	if code != 1 || !strings.Contains(stdout, "#5 is on batch #90, but only part of it applies to release-1.1") {
		t.Errorf("check --cut = %d, want 1 naming the partial unit:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	code, stdout, stderr = f.exec(gh, "verify", "--line", "release-1.1", "--head", head)
	if code != 1 || !strings.Contains(stdout, `#5 leaves "chart/templates/policy.yaml", "chart/tests/policy_test.yaml" without effect on release-1.1`) {
		t.Errorf("verify = %d, want 1 naming what #5 leaves out:\n%s\n%s", code, stdout, stderr)
	}
	gh.labels = map[string][]int{labelSkip: {5}}
	code, stdout, stderr = f.exec(gh, "check", "--line", "release-1.1", "--cut")
	if code != 1 || !strings.Contains(stdout, "#5 is on batch #90, but only part of it applies to release-1.1") {
		t.Errorf("check --cut with backport/skip = %d, want the partial unit to still fail it:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	code, _, stderr = f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "only part of it applies", "--push")
	if code != 0 || prState(gh.find(90)) != "closed" || f.remoteHead(batchBranch1) != "" {
		t.Errorf("run --defer 5 --push = %d, #90 %s, branch at %q; want the batch closed and its branch gone:\n%s", code, prState(gh.find(90)), f.remoteHead(batchBranch1), stderr)
	}
}

// An original whose first pick came out empty, because the line already had
// that change, is on the batch all the same: check does not call it missing,
// a continuation keeps it picked, and so does a rebuild without another
// original.
func TestOnBatchAfterAnEmptyFirstPick(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a and b", "", map[string]string{"app/a.txt": "old\n", "app/b.txt": "old\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): a, on the line first", map[string]string{"app/a.txt": "fixed\n"})
	f.prCommits(5, "fix(app): a and b", "",
		change{files: map[string]string{"app/a.txt": "fixed\n"}},
		change{files: map[string]string{"app/b.txt": "fixed\n"}})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	if got := f.subjects("origin/release-1.1", head); !slices.Equal(got, []string{"fix(app): a and b (#5)", "fix(app): six (#6)"}) {
		t.Fatalf("the batch carries %q, want #5 and #6", got)
	}
	code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 {
		t.Errorf("check = %d, want 0 with #5 on the batch:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	code, stdout, _ = f.exec(gh, "check", "--line", "release-1.1", "--cut")
	if code != 1 || !strings.Contains(stdout, "#5 is only on open batch PR #90") {
		t.Errorf("check --cut = %d, want #5 only on the batch:\n%s", code, stdout)
	}
	code, stdout, stderr = f.exec(gh, "run", "--line", "release-1.1", "--continue")
	if code != 0 || !strings.Contains(stdout, `"picked":[5,6]`) {
		t.Errorf("run --continue = %d, want #5 and #6 picked:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	code, stdout, stderr = f.exec(gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "fails the e2e")
	if code != 0 || !strings.Contains(stdout, `"picked":[5]`) || !strings.Contains(stdout, `"missing":[]`) {
		t.Errorf("run --defer 6 = %d, want #5 still picked:\n%s\n%s", code, stdout, stderr)
	}
}

// The picker judges what its picks did where the run has got to: #4
// deletes the file #5 also deletes, so after #4 #5 would bring only its
// test, which the net merge at the line's head cannot see.
func TestPartialByAnEarlierPick(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a policy", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.lines()
	f.pr(4, "fix(app): drop the policy", "", map[string]string{"app/policy.yaml": "", "app/four.txt": "4\n"})
	f.pr(40, "feat(app): the policy again", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.pr(5, "fix(app): drop the policy and test it", "", map[string]string{"app/policy.yaml": "", "app/policy_test.txt": "gone\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stdout, `"picked":[4]`) || !strings.Contains(stdout, `"parked":[{"original":5,`) {
		t.Errorf("run = %d, want #4 picked and #5 parked as partial:\n%s\n%s", code, stdout, stderr)
	}
}

// What an earlier pick brought clears a partial park the line's head
// gave: the line removed the policy, #4 brings it back, and #5's deletion
// then has something to delete.
func TestPartialClearedByAnEarlierPick(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a policy", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): the line drops the policy", map[string]string{"app/policy.yaml": ""})
	f.pr(3, "feat(app): no policy", "", map[string]string{"app/policy.yaml": ""})
	f.pr(4, "fix(app): the policy, back", "", map[string]string{"app/policy.yaml": "allow: some\n"})
	f.pr(5, "fix(app): drop the policy and test it", "", map[string]string{"app/policy.yaml": "", "app/policy_test.txt": "gone\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stdout, `"picked":[4,5]`) {
		t.Errorf("run = %d, want #4 and #5 picked:\n%s\n%s", code, stdout, stderr)
	}
}

// A file the line renamed the way the original does is the original's own
// rename, not a deletion left without effect.
func TestSameRenameOnBothSidesIsWhole(t *testing.T) {
	const long = "l01\nl02\nl03\nl04\nl05\nl06\nl07\nl08\nl09\nl10\nl11\nl12\n"
	f := newFixture(t)
	f.squash(2, "chore: a file", "", map[string]string{"app/p.txt": long})
	f.lines()
	f.onLine("release-1.1", "fix(app): the line renames p to q", map[string]string{"app/p.txt": "", "app/q.txt": long})
	f.pr(5, "fix(app): rename p to q, and fix l10", "", map[string]string{"app/p.txt": "", "app/q.txt": strings.Replace(long, "l10", "L10", 1)})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || !strings.Contains(stdout, `"picked":[5]`) {
		t.Errorf("run = %d, want #5 picked whole:\n%s\n%s", code, stdout, stderr)
	}
}

// The line's own rename stands for the original's only with the original's
// change in it: a fold that leaves the renamed file as it was is refused.
func TestSameRenameStillNeedsItsFix(t *testing.T) {
	const long = "l01\nl02\nl03\nl04\nl05\nl06\nl07\nl08\nl09\nl10\nl11\nl12\n"
	f := newFixture(t)
	f.squash(2, "chore: a file", "", map[string]string{"app/p.txt": long})
	f.lines()
	f.onLine("release-1.1", "fix(app): the line renames p to q", map[string]string{"app/p.txt": "", "app/q.txt": long})
	mc5 := f.pr(5, "fix(app): rename p to q, fix l10, and five", "", map[string]string{"app/p.txt": "", "app/q.txt": strings.Replace(long, "l10", "L10", 1), "app/five.txt": "5\n"})
	w := emptyBatch(f, &fakeGH{}, "")
	pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/five.txt": "5\n"})
	fold := w.merge(w.head, pick, "fix(app): rename p to q, fix l10, and five (#5)\n\n"+batchTrailer+": "+mc5)
	if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 1 || !strings.Contains(out, `leaves "app/p.txt" without effect`) {
		t.Errorf("fold without the fix in q = %d, %q\n%s", code, out, stderr)
	}
}

// A pick that comes out empty is judged too: #5's own commit did half its
// fix, which the line has, and a merge of main into its branch did the
// rest, which no pick carries; #5 is not on the line.
func TestEmptyPicksStillJudged(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: b", "", map[string]string{"app/b.txt": "old\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): b, half of it", map[string]string{"app/b.txt": "middle\n"})
	f.git("switch", "-q", "-c", "pr-5", "main")
	f.apply(map[string]string{"app/b.txt": "middle\n"})
	f.git("commit", "-q", "-m", "b, half of it")
	f.git("switch", "-q", "main")
	f.apply(map[string]string{"app/main.txt": "moved\n"})
	f.git("commit", "-q", "-m", "chore: move main along")
	f.git("switch", "-q", "pr-5")
	f.git("merge", "-q", "--no-ff", "--no-commit", "main")
	f.apply(map[string]string{"app/b.txt": "fixed\n"})
	f.git("commit", "-q", "-m", "Merge branch 'main' into pr-5")
	f.git("switch", "-q", "main")
	f.git("merge", "-q", "--no-ff", "-m", "fix(app): b (#5)", "pr-5")
	f.git("push", "-q", "origin", "main")
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || strings.Contains(stdout, `"on-line":[5]`) || !strings.Contains(stdout, `"parked":[{"original":5,`) {
		t.Errorf("run = %d, want #5 parked as partial, not on the line:\n%s\n%s", code, stdout, stderr)
	}
}

// --defer takes off a unit that does only part of its original whatever
// rule now keeps that original out, as check tells it to.
func TestDeferTakesOffAPartialExcludedUnit(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(3, "feat(chart): a network policy", "", map[string]string{"chart/templates/policy.yaml": "kind: NetworkPolicy\n"})
	mc5 := f.pr(5, "feat(chart): remove the policy, and test values", "", map[string]string{"chart/templates/policy.yaml": "", "chart/tests/values_test.yaml": "suite: values\n"})
	gh := &fakeGH{}
	handBatch(f, gh, mc5)
	if code, stdout, _ := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || !strings.Contains(stdout, "only part of it applies") {
		t.Fatalf("check --cut = %d, want the partial unit to fail it:\n%s", code, stdout)
	}
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "only part of it applies", "--push"); code != 0 || prState(gh.find(90)) != "closed" {
		t.Errorf("run --defer 5 --push = %d, #90 %s; want the unit taken off and the batch closed:\n%s", code, prState(gh.find(90)), stderr)
	}
}

// An empty pick leaves a deletion undone that only a merge of main into the
// original's branch made: the original is not on the line.
func TestEmptyPickLeavesADeletionUndone(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a and b", "", map[string]string{"app/a.txt": "old feature\n", "app/b.txt": "old\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): b, already", map[string]string{"app/b.txt": "fixed\n"})
	f.git("switch", "-q", "-c", "pr-5", "main")
	f.apply(map[string]string{"app/b.txt": "fixed\n"})
	f.git("commit", "-q", "-m", "b")
	f.git("switch", "-q", "main")
	f.apply(map[string]string{"app/main.txt": "moved\n"})
	f.git("commit", "-q", "-m", "chore: move main along")
	f.git("switch", "-q", "pr-5")
	f.git("merge", "-q", "--no-ff", "--no-commit", "main")
	f.git("rm", "-q", "app/a.txt")
	f.git("commit", "-q", "-m", "Merge branch 'main' into pr-5")
	f.git("switch", "-q", "main")
	f.git("merge", "-q", "--no-ff", "-m", "fix(app): b, and a goes (#5)", "pr-5")
	f.git("push", "-q", "origin", "main")
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || strings.Contains(stdout, `"on-line":[5]`) || !strings.Contains(stdout, `"parked":[{"original":5,`) {
		t.Errorf("run = %d, want #5 parked as partial, not on the line:\n%s\n%s", code, stdout, stderr)
	}
}
