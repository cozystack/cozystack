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

// featWorld is #5071's shape: #3, a feat the line never gets, adds a file,
// and #5 fixes that file; #6 is a plain fix beside it.
func featWorld(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.pr(3, "feat(app): tap", "", map[string]string{"app/tap.go": appFile})
	f.pr(5, "fix(app): fix the tap", "```release-note\nfix(app): the tap works.\n```\n", map[string]string{"app/tap.go": strings.Replace(appFile, "two", "TWO", 1)})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	return f
}

// A fix of a file a feat added on main, which the line never gets, is
// parked, not tried, and check takes the park as a note.
func TestParkAFixOfAFeatureTheLineLacks(t *testing.T) {
	f := featWorld(t)
	gh := &fakeGH{}
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push")
	if code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	if strings.Contains(stdout, "### Deferred conflicts") || !strings.Contains(stdout, `"parked":[{"original":5,`) || !strings.Contains(stdout, `"kind":"dependency","needs":[3]`) {
		t.Errorf("#5 is not parked on #3:\n%s", stdout)
	}
	if !strings.Contains(stdout, "### Parked") || !strings.Contains(stdout, "builds on [#3]") {
		t.Errorf("the body does not say #5 builds on #3:\n%s", stdout)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	code, stdout, stderr = f.exec(gh, "check", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stderr, "note: #5 is parked: it builds on #3, which never reaches release-1.1") {
		t.Errorf("check = %d, want 0 with a note that #5 is parked:\n%s\n%s", code, stdout, stderr)
	}
}

// backport/include lifts no park.
func TestIncludeDoesNotLiftThePark(t *testing.T) {
	f := featWorld(t)
	code, stdout, stderr := f.exec(&fakeGH{labels: map[string][]int{labelInclude: {5}}}, "run", "--line", "release-1.1")
	if code != 0 || !strings.Contains(stdout, `"parked":[{"original":5,`) || !strings.Contains(stdout, "`backport/include`, parked: builds on") {
		t.Errorf("run with backport/include on #5 = %d, want #5 still parked:\n%s\n%s", code, stdout, stderr)
	}
}

// A prerequisite that is itself parked, but adapted onto the batch by hand,
// satisfies what builds on it: #3 is parked on #2, a feat, and #5 changes a
// line #3 wrote.
func TestAdaptedPrerequisiteSatisfiesItsDependent(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a and b", "", map[string]string{"app/a.txt": appFile, "app/b.txt": appFile})
	f.lines()
	f.pr(20, "feat(app): a loud two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	mc3 := f.pr(3, "fix(app): an excited two, a loud three", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1), "app/b.txt": strings.Replace(appFile, "three", "THREE", 1)})
	f.pr(5, "fix(app): an excited three", "", map[string]string{"app/b.txt": strings.Replace(appFile, "three", "THREE!", 1)})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || !strings.Contains(stdout, `"kind":"dependency","needs":[3]`) {
		t.Fatalf("run = %d, want #5 parked as long as #3 is:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	w := emptyBatch(f, &fakeGH{}, "batch\n\n"+deferredBlock(line11, nil, nil, nil)+"\n")
	// #3 adapted by hand: every file it changes, changed, in the batch's
	// shape.
	pick := w.pick(w.head, f.git("rev-parse", mc3+"^2"), map[string]string{"app/a.txt": strings.Replace(appFile, "two", "two!", 1), "app/b.txt": strings.Replace(appFile, "three", "THREE", 1)})
	fold := w.merge(w.head, pick, "fix(app): an excited two, a loud three (#3)\n\n"+batchTrailer+": "+mc3)
	f.git("push", "-q", "origin", fold+":refs/heads/"+batchBranch1)
	w.gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, w.gh.prs[0].Body)}
	code, stdout, stderr := f.exec(w.gh, "check", "--line", "release-1.1")
	if code != 1 || strings.Contains(stderr, "#5 is parked") || strings.Contains(stdout+stderr, "waits on") || !strings.Contains(stdout, "/pull/5 ") {
		t.Errorf("check = %d, want #5 missing, not parked:\n%s\n%s", code, stdout, stderr)
	}
	code, stdout, stderr = f.exec(w.gh, "run", "--line", "release-1.1", "--continue")
	if code != 0 || !strings.Contains(stdout, `"picked":[3,5]`) {
		t.Errorf("run --continue = %d, want #5 picked after #3's adaptation:\n%s\n%s", code, stdout, stderr)
	}
}

// What an earlier unit of the batch brought counts: #3 writes the line #8
// changes, a feat takes it back and another feat, #6, writes it again, so
// blame names #6, which never comes; once #3 is picked the branch has the
// line all the same.
func TestContentFromAnotherUnitSatisfies(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.pr(3, "fix(app): a loud two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.pr(4, "feat(app): a quiet two", "", map[string]string{"app/a.txt": appFile})
	f.pr(6, "feat(app): a loud two again", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.pr(8, "fix(app): an excited two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): a loud two (#3)", "fix(app): an excited two (#8)"}) {
		t.Errorf("picked %q, want #3 and #8:\n%s", got, stdout)
	}
}

// auxWorld is #5142's shape: #3, a feat, adds a bats test; #5 fixes a
// workflow and that test, so all it builds on that never comes is a test;
// #6 is a plain fix.
func auxWorld(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.squash(2, "chore: a workflow", "", map[string]string{".github/workflows/release.yaml": "on: push\njobs: {}\n"})
	f.lines()
	f.pr(3, "feat(ci): test the changelog", "", map[string]string{"hack/changelog.bats": "@test x {\n  true\n}\n"})
	f.pr(5, "fix(ci): run the release on tags", "", map[string]string{".github/workflows/release.yaml": "on: [push, tag]\njobs: {}\n", "hack/changelog.bats": "@test x {\n  false\n}\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	return f
}

// A workflow original whose only missing dependency is a test waits for a
// maintainer in the scheduled run, whose token cannot push workflows, and
// check holds the cut on it. A maintainer's own run parks it. With no batch
// open, or one whose body carries no record, it is missing; a record from
// before the mode was saved says the scheduled mode by listing a wait.
func TestWorkflowModeCarriesToCheck(t *testing.T) {
	f := auxWorld(t)
	gh := &fakeGH{}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push"); code != 0 || strings.Contains(stdout, `"parked":[{`) || !strings.Contains(stdout, `{"original":5,"files":[".github/workflows/release.yaml"],"workflows":true}`) {
		t.Fatalf("scheduled run = %d, want #5 waiting, not parked:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || !strings.Contains(stdout, "#5 changes .github/workflows/ and waits on batch PR #90") {
		t.Errorf("check --cut = %d, want #5 waiting:\n%s\n%s", code, stdout, stderr)
	}
	v1 := "batch\n\n" + deferredBlock(line11, nil, nil, []pick{{v: &verdict{o: &original{number: 5}}, files: []string{".github/workflows/release.yaml"}}}) + "\n"
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, v1)}
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || !strings.Contains(stdout, "#5 changes .github/workflows/ and waits on batch PR #90") {
		t.Errorf("check --cut with a version 1 record = %d, want #5 waiting:\n%s\n%s", code, stdout, stderr)
	}
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "a batch someone wrote")}
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/5 ") || strings.Contains(stderr, "parked") {
		t.Errorf("check under a batch with no record = %d, want #5 missing:\n%s\n%s", code, stdout, stderr)
	}

	f = auxWorld(t)
	if code, stdout, stderr := f.exec(nil, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/5 ") || strings.Contains(stderr, "parked") {
		t.Errorf("check with no batch = %d, want #5 missing:\n%s\n%s", code, stdout, stderr)
	}
	gh = &fakeGH{}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push"); code != 0 || !strings.Contains(stdout, `"parked":[{"original":5,`) {
		t.Fatalf("a maintainer's run = %d, want #5 parked:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || !strings.Contains(stderr, "note: #5 is parked") {
		t.Errorf("check after a maintainer's run = %d, want 0 with #5 parked:\n%s\n%s", code, stdout, stderr)
	}
}

// Evidence found only in a test file still parks an original that changes
// no workflow: #4973's shape.
func TestTestOnlyEvidenceParksOtherOriginals(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: nodes", "", map[string]string{"app/nodes.go": "package app\n", "app/nodes_test.go": "package app\n\nfunc a() {}\n"})
	f.lines()
	f.pr(3, "feat(app): proxmox nodes", "", map[string]string{"app/nodes_test.go": "package app\n\nfunc a() { proxmox() }\n"})
	f.pr(5, "fix(app): nodes", "", map[string]string{"app/nodes.go": "package app\n\nvar fixed = true\n", "app/nodes_test.go": "package app\n\nfunc a() { proxmox(); fixed() }\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 || strings.Contains(stdout, "### Deferred conflicts") || !strings.Contains(stdout, `"parked":[{"original":5,`) {
		t.Errorf("run = %d, want #5 parked on #3:\n%s\n%s", code, stdout, stderr)
	}
}

// What an original builds on through lines it adds is not seen: #4973's
// $isProxmox. The original is picked; CI on the batch is what catches it.
func TestAddedLinesDependencyIsNotSeen(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: nodes", "", map[string]string{"app/nodes.tpl": "a\nb\n"})
	f.lines()
	f.pr(3, "feat(app): define isProxmox", "", map[string]string{"app/proxmox.tpl": "isProxmox\n"})
	f.pr(5, "fix(app): use isProxmox", "", map[string]string{"app/nodes.tpl": "a\nif isProxmox\nb\n"})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || strings.Contains(stdout, `"parked":[{`) || !strings.Contains(stdout, `"picked":[5]`) {
		t.Errorf("run = %d, want #5 picked:\n%s\n%s", code, stdout, stderr)
	}
}

// With no batch open, check reads a closed record whatever its version and
// whatever mode it saved as the scheduled run's: the workflow original it
// left waiting is missing, not parked, while its drops and its other
// deferrals still count.
func TestClosedRecordModeFallback(t *testing.T) {
	for _, version := range []int{2, 3} {
		for _, saved := range []bool{false, true} {
			t.Run(fmt.Sprintf("version %d, noWorkflowFiles %v", version, saved), func(t *testing.T) {
				f := auxWorld(t)
				mc6 := f.git("log", "--first-parent", "--format=%H", "-1", "--grep", "(#6)$", "main")
				mc7 := f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
				sha := f.git("rev-parse", "origin/main")
				rec := lineRecord{Version: version, Line: "release-1.1", Report: strings.Repeat("b", 64),
					Built:    &builtFrom{Main: sha, Line: sha, Policy: sha, Batch: sha, At: "2026-10-26T06:00:00Z", Options: runOptions{NoWorkflowFiles: saved, Mode: modeContinue}},
					Deferred: []deferredEntry{{Original: 5, Files: []string{".github/workflows/release.yaml"}, Workflows: true}, {Original: 7, Commit: f.git("rev-parse", mc7+"^2"), Files: []string{"app/seven.txt"}}},
					Dropped:  []droppedEntry{{Original: 6, Merge: mc6, Reason: "not for this line", On: 90, Date: "2026-10-26"}}, Stale: []int{}}
				pr := f.batchPR(90, "release-1.1", batchBranch1, "batch\n\n"+recordBlock(rec)+"\n")
				at := "2026-10-26T12:00:00Z"
				pr.State, pr.MergedAt = "closed", &at
				code, stdout, stderr := f.exec(&fakeGH{prs: []pullRequest{pr}}, "check", "--line", "release-1.1")
				if code != 1 || !strings.Contains(stdout, "/pull/5 ") || strings.Contains(stderr, "#5 is parked") {
					t.Errorf("check = %d, want #5 missing and not parked:\n%s\n%s", code, stdout, stderr)
				}
				if !strings.Contains(stderr, "note: #6 is dropped") || !strings.Contains(stderr, "warning: deferred conflict on #90 (merged): https://github.com/cozystack/cozystack/pull/7 ") {
					t.Errorf("check lost the closed record's drop or deferral:\n%s", stderr)
				}
			})
		}
	}
}

// Withdrawing a drop puts the original back where the rules and the line
// put it: a partial original is parked again, in the record as in check.
func TestUndropRestoresThePark(t *testing.T) {
	for name, world := range map[string]func(*testing.T) *fixture{
		"partial": func(t *testing.T) *fixture {
			f, _ := policyWorld(t)
			f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
			return f
		},
		"dependency": featWorld,
	} {
		t.Run(name, func(t *testing.T) {
			f := world(t)
			gh := &fakeGH{}
			openBatch(t, f, gh)
			mustRun(t, f, gh, "run", "--line", "release-1.1", "--drop", "5", "--reason", "not for 1.1", "--push")
			reopen(f, gh)
			mustRun(t, f, gh, "run", "--line", "release-1.1", "--undrop", "5", "--push")
			reopen(f, gh)
			if rec := recordOf(t, gh); len(rec.Parked) != 1 || rec.Parked[0].Original != 5 || rec.Parked[0].Kind != name {
				t.Errorf("after the undrop the record parks %+v, want #5 parked %s", rec.Parked, name)
			}
			if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || !strings.Contains(stderr, "note: #5 is parked") {
				t.Errorf("check = %d, want #5 parked:\n%s\n%s", code, stdout, stderr)
			}
		})
	}
}

// A prerequisite the open batch carries is there for what builds on it,
// whatever its label now says: check and run agree that #7 is not parked.
func TestPrerequisiteStillOnTheBatchSatisfies(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	f.pr(7, "fix(app): seven", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	gh.labels = map[string][]int{labelSkip: {5}}
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || strings.Contains(stderr, "#7 is parked") || !strings.Contains(stdout, "/pull/7 ") {
		t.Errorf("check = %d, want #7 missing, not parked:\n%s\n%s", code, stdout, stderr)
	}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue"); code != 0 || !strings.Contains(stdout, `"picked":[7]`) {
		t.Errorf("run --continue = %d, want #7 picked:\n%s\n%s", code, stdout, stderr)
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

// A reason a person deferred an original for comes before its partial
// park.
func TestReasonComesBeforeThePartialPark(t *testing.T) {
	f, _ := policyWorld(t)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "waits for the policy rework", "--push")
	reopen(f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || !strings.Contains(stderr, "warning: deferred on #90 to the next batch: https://github.com/cozystack/cozystack/pull/5 ") || strings.Contains(stderr, "#5 is parked") {
		t.Errorf("check = %d, want #5 deferred for its reason:\n%s\n%s", code, stdout, stderr)
	}
}

// The workflow exception holds only while what never comes is tests or
// docs, and while the workflow lines build on nothing missing: a
// workflow original that also fixes a file a feat added, or whose workflow
// line another original still to come wrote, parks in the scheduled run too.
func TestWorkflowExceptionNeedsTestsOnly(t *testing.T) {
	t.Run("a never dependency outside tests", func(t *testing.T) {
		f := auxWorld(t)
		f.pr(20, "feat(app): tap", "", map[string]string{"app/tap.go": appFile})
		f.pr(7, "fix(ci): test the tap, and fix it", "", map[string]string{".github/workflows/tap.yaml": "on: push\n", "app/tap.go": strings.Replace(appFile, "two", "TWO", 1)})
		if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1", "--no-workflow-files"); code != 0 || !strings.Contains(stdout, `"parked":[{"original":7,`) {
			t.Errorf("scheduled run = %d, want #7 parked:\n%s\n%s", code, stdout, stderr)
		}
	})
	t.Run("a workflow line still to come", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: a workflow", "", map[string]string{".github/workflows/release.yaml": "on: push\njobs: {}\n", "app/four.txt": "old\n"})
		f.lines()
		f.onLine("release-1.1", "fix(app): four, the line's way", map[string]string{"app/four.txt": "line\n"})
		f.pr(3, "feat(ci): test the changelog", "", map[string]string{"hack/changelog.bats": "@test x {\n  true\n}\n"})
		f.pr(4, "fix(ci): four", "", map[string]string{"app/four.txt": "four\n", ".github/workflows/release.yaml": "on: [push]\njobs: {}\n"})
		f.pr(5, "fix(ci): run the release on tags", "", map[string]string{".github/workflows/release.yaml": "on: [push, tag]\njobs: {}\n", "hack/changelog.bats": "@test x {\n  false\n}\n"})
		if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1", "--no-workflow-files"); code != 0 || !strings.Contains(stdout, `"parked":[{"original":5,`) {
			t.Errorf("scheduled run = %d, want #5 parked:\n%s\n%s", code, stdout, stderr)
		}
	})
}

// What a rebuild takes off the batch is no longer there for what builds on
// it: deferring #5 makes #7, which merged after the batch was built, wait
// on it.
func TestRebuildTakesThePrerequisiteAway(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	f.pr(7, "fix(app): seven", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "fails the e2e"); code != 0 || !strings.Contains(stdout, `{"original":7,"files":[],"needs":[5]}`) {
		t.Errorf("run --defer 5 = %d, want #7 waiting on #5:\n%s\n%s", code, stdout, stderr)
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

// A partial park only the picker saw, where the net merge at the line's
// head looks whole, holds in check and in the operations that pick nothing
// as the open record keeps it: check does not call the original missing.
func TestPickerParkHoldsInCheck(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a policy", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.lines()
	f.pr(4, "fix(app): drop the policy", "", map[string]string{"app/policy.yaml": "", "app/four.txt": "4\n"})
	f.pr(40, "feat(app): the policy again", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.pr(5, "fix(app): drop the policy and test it", "", map[string]string{"app/policy.yaml": "", "app/policy_test.txt": "gone\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 || !strings.Contains(stderr, "note: #5 is parked: only part of it applies") {
		t.Errorf("check = %d, want #5 parked as the record says:\n%s\n%s", code, stdout, stderr)
	}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "fails the e2e"); code != 0 || !strings.Contains(stdout, `"parked":[{"original":5,`) {
		t.Errorf("run --defer 6 = %d, want #5 still parked:\n%s\n%s", code, stdout, stderr)
	}
	// Deferring #4, which made #5 partial, ends the park: #5 applies whole
	// on what the batch carries now.
	f.git("switch", "-q", "main")
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "4", "--reason", "fails the e2e", "--push")
	if code != 0 || strings.Contains(stdout, `"parked":[{`) || !strings.Contains(stdout, `"missing":[5]`) {
		t.Errorf("run --defer 4 = %d, want #5 missing, no longer parked:\n%s\n%s", code, stdout, stderr)
	}
	reopen(f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || strings.Contains(stderr, "#5 is parked") || !strings.Contains(stdout, "/pull/5 ") {
		t.Errorf("check after deferring #4 = %d, want #5 missing:\n%s\n%s", code, stdout, stderr)
	}
}

// A workflow original that waits for a maintainer only as the open record
// lists it: one merged since the batch was built is missing, and so is
// what waits on it.
func TestUnlistedWorkflowWaitIsMissing(t *testing.T) {
	f := auxWorld(t)
	gh := &fakeGH{}
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push")
	f.git("switch", "-q", "main")
	reopen(f, gh)
	f.pr(8, "fix(ci): run the new release on tags", "", map[string]string{".github/workflows/new.yaml": "on: tag\n", "hack/changelog.bats": "@test x {\n  false\n}\n# eight\n", "app/eight.txt": appFile + "TWO\n"})
	f.pr(9, "fix(app): nine", "", map[string]string{"app/eight.txt": appFile + "TWO!\n"})
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/9 fix(app): nine (it waits on #8, which is not on the line either)") {
		t.Errorf("check = %d, want #9 missing, waiting on #8:\n%s\n%s", code, stdout, stderr)
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

// A prerequisite the line carries is there, though the rules would keep it
// out: a feat carried by a hand adaptation does not park what builds on it.
func TestCarriedExcludedPrerequisiteSatisfies(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	mc3 := f.pr(3, "feat(app): a loud two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.onLine("release-1.1", "feat(app): a loud two, adapted\n\n(cherry picked from commit "+mc3+")", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "Two", 1)})
	f.pr(5, "fix(app): an excited two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || strings.Contains(stdout, `"parked":[{`) {
		t.Errorf("run = %d, want #5 not parked on the carried #3:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	if code, stdout, stderr := f.exec(nil, "check", "--line", "release-1.1"); code != 1 || strings.Contains(stderr, "#5 is parked") {
		t.Errorf("check = %d, want #5 missing, not parked:\n%s\n%s", code, stdout, stderr)
	}
}

// With no batch open, a workflow wait a closed record keeps waits on
// nothing: the original is judged as the scheduled run judges it, so one
// that also builds on a feat the line lacks is parked, in check as in run.
func TestClosedWorkflowWaitIsJudgedAsRunJudgesIt(t *testing.T) {
	f := auxWorld(t)
	f.pr(20, "feat(app): tap", "", map[string]string{"app/tap.go": appFile})
	f.pr(7, "fix(ci): test the tap, and fix it", "", map[string]string{".github/workflows/tap.yaml": "on: push\n", "app/tap.go": strings.Replace(appFile, "two", "TWO", 1)})
	sha := f.git("rev-parse", "origin/main")
	rec := lineRecord{Version: 2, Line: "release-1.1", Report: strings.Repeat("b", 64),
		Built:    &builtFrom{Main: sha, Line: sha, Policy: sha, Batch: sha, At: "2026-10-26T06:00:00Z", Options: runOptions{NoWorkflowFiles: true, Mode: modeContinue}},
		Deferred: []deferredEntry{{Original: 7, Files: []string{".github/workflows/tap.yaml"}, Workflows: true}}, Dropped: []droppedEntry{}, Stale: []int{}}
	pr := f.batchPR(90, "release-1.1", batchBranch1, "batch\n\n"+recordBlock(rec)+"\n")
	at := "2026-10-26T12:00:00Z"
	pr.State, pr.MergedAt = "closed", &at
	gh := &fakeGH{prs: []pullRequest{pr}}
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); strings.Contains(stdout, "/pull/7 ") || !strings.Contains(stderr, "note: #7 is parked: it builds on #20") {
		t.Errorf("check --cut = %d, want #7 parked:\n%s\n%s", code, stdout, stderr)
	}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--no-workflow-files"); code != 0 || !strings.Contains(stdout, `"parked":[{"original":7,`) {
		t.Errorf("scheduled run = %d, want #7 parked:\n%s\n%s", code, stdout, stderr)
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

// A stored wait on an original merged after the one that waits cannot be
// what a run wrote.
func TestForwardWaitIsRefused(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	sha := f.git("rev-parse", "origin/main")
	rec := lineRecord{Version: recordVersion, Line: "release-1.1", Report: strings.Repeat("b", 64),
		Built:    &builtFrom{Main: sha, Line: sha, Policy: sha, Batch: sha, At: "2026-10-26T06:00:00Z", Options: runOptions{Mode: modeContinue}},
		Deferred: []deferredEntry{{Original: 5, Files: []string{}, Needs: []int{7}}}, Dropped: []droppedEntry{}, Stale: []int{}}
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	gh := &fakeGH{prs: []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "batch\n\n"+recordBlock(rec)+"\n")}}
	if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 2 || !strings.Contains(stderr, "#5 wait on #7") {
		t.Errorf("check = %d, want 2 for a wait on a later original:\n%s", code, stderr)
	}
}

// The workflow exception looks only at what still keeps the original from
// the line: a workflow line its prerequisite wrote, which the open batch
// carries though that prerequisite is labelled backport/skip, does not
// stop it, in check as in run.
func TestWorkflowExceptionIgnoresWhatIsThere(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a workflow", "", map[string]string{".github/workflows/release.yaml": "on: push\njobs: {}\n"})
	f.lines()
	f.pr(3, "feat(ci): test the changelog", "", map[string]string{"hack/changelog.bats": "@test x {\n  true\n}\n"})
	f.pr(4, "fix(ci): run the release on pushes", "", map[string]string{".github/workflows/release.yaml": "on: [push]\njobs: {}\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.pr(7, "fix(ci): run the release on tags too", "", map[string]string{".github/workflows/release.yaml": "on: [push, tag]\njobs: {}\n", "hack/changelog.bats": "@test x {\n  false\n}\n"})
	gh := &fakeGH{}
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--push")
	f.git("switch", "-q", "main")
	reopen(f, gh)
	gh.labels = map[string][]int{labelSkip: {4}}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--no-workflow-files", "--push"); code != 0 || !strings.Contains(stdout, `{"original":7,"files":[".github/workflows/release.yaml"],"workflows":true}`) {
		t.Fatalf("scheduled run = %d, want #7 waiting for a maintainer:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || !strings.Contains(stdout, "#7 changes .github/workflows/ and waits on batch PR #90") {
		t.Errorf("check --cut = %d, want #7 waiting, as run has it:\n%s\n%s", code, stdout, stderr)
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

// A file the line already has as the original makes it builds on nothing
// the line lacks: #5 fixes a.txt, which the line fixed the same way, and
// b.txt, so it picks whole and is not parked on the feat that wrote a.txt.
func TestAlreadyAppliedFileBuildsOnNothing(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a and b", "", map[string]string{"app/a.txt": appFile, "app/b.txt": "old\n"})
	f.lines()
	f.pr(3, "feat(app): a loud two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.onLine("release-1.1", "fix(app): two, the line's way", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	f.pr(5, "fix(app): an excited two, and b", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1), "app/b.txt": "fixed\n"})
	if code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1"); code != 0 || strings.Contains(stdout, `"parked":[{`) || !strings.Contains(stdout, `"picked":[5]`) {
		t.Errorf("run = %d, want #5 picked, not parked:\n%s\n%s", code, stdout, stderr)
	}
}
