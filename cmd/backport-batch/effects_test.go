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
	"strings"
	"testing"
)

const appFile = "one\ntwo\nthree\nfour\nfive\n"

// renameWorld is the sign-off's rename history: the line keeps app/old.txt,
// main renames it to app/new.txt with #3, a feat, and #5 fixes app/new.txt.
func renameWorld(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.squash(2, "chore: an app file", "", map[string]string{"app/old.txt": appFile})
	f.lines()
	f.pr(3, "feat(app): rename the app file", "", map[string]string{"app/old.txt": "", "app/new.txt": appFile})
	mc5 := f.pr(5, "fix(app): fix the app file", "", map[string]string{"app/new.txt": strings.Replace(appFile, "three", "THREE", 1)})
	return f, mc5
}

// emptyBatch pushes the line's head as the batch branch and opens a batch
// pull request on it with body, for a fold to land on.
func emptyBatch(f *fixture, gh *fakeGH, body string) *foldWorld {
	f.t.Helper()
	f.git("fetch", "-q", "origin")
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	f.git("fetch", "-q", "origin")
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, body)}
	return &foldWorld{f: f, gh: gh, head: f.git("rev-parse", "origin/"+batchBranch1)}
}

// A fix of a file main renamed picks onto the file's old name on the line,
// and every check of the batch's shape takes the old name as the same file:
// verify, a hand fold, and a rebuild by --defer.
func TestRenamePickThroughFoldAndVerify(t *testing.T) {
	f, _ := renameWorld(t)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	head := openBatch(t, f, gh)
	if got := f.subjects("origin/release-1.1", head); !slices.Equal(got, []string{"fix(app): fix the app file (#5)", "fix(app): six (#6)"}) {
		t.Fatalf("the batch carries %q, want #5 and #6", got)
	}
	if got := f.git("show", head+":app/old.txt"); !strings.Contains(got, "THREE") {
		t.Fatalf("#5 did not land on app/old.txt: %q", got)
	}
	if code, out, stderr := f.exec(gh, "verify", "--line", "release-1.1", "--head", head); code != 0 {
		t.Errorf("verify = %d\n%s\n%s", code, out, stderr)
	}
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-27T06:00:00Z")
	if code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--defer", "6", "--reason", "fails the e2e", "--push"); code != 0 {
		t.Fatalf("run --defer 6 --push = %d\n%s", code, stderr)
	}
	if code, out, stderr := f.exec(gh, "verify", "--line", "release-1.1", "--head", f.remoteHead(batchBranch1)); code != 0 {
		t.Errorf("verify after the rebuild = %d\n%s\n%s", code, out, stderr)
	}

	f, mc5 := renameWorld(t)
	w := emptyBatch(f, &fakeGH{}, "")
	pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/old.txt": strings.Replace(appFile, "three", "THREE", 1)})
	fold := w.merge(w.head, pick, "fix(app): fix the app file (#5)\n\n"+batchTrailer+": "+mc5)
	if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 0 || out != "ok\n" {
		t.Errorf("fold of #5 onto app/old.txt = %d, %q\n%s", code, out, stderr)
	}
}

// shaped reads the line from the fixture's remote as verify does.
func shaped(t *testing.T, f *fixture) *batch {
	t.Helper()
	f.git("fetch", "-q", "origin")
	b, err := shapeOf(line11)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// commitOn writes files onto parent and returns the commit, leaving every
// branch where it was.
func commitOn(f *fixture, parent string, files map[string]string) string {
	f.t.Helper()
	f.git("switch", "-q", "--detach", parent)
	f.apply(files)
	f.git("commit", "-q", "--allow-empty", "-m", "a unit")
	sha := f.git("rev-parse", "HEAD")
	f.git("switch", "-q", "main")
	return sha
}

// A path of the original is followed to where the line keeps the same file:
// through a rename on main, on the line, or both, composed through the path
// at the base. Where git skipped rename detection the path is unknown, never
// absent.
func TestCorrespondence(t *testing.T) {
	corr := func(t *testing.T, f *fixture, n int, limit ...int) (map[string]string, []string) {
		t.Helper()
		b := shaped(t, f)
		if len(limit) > 0 {
			b.renameLimit = limit[0]
		}
		v := b.find(n)
		if err := b.load(v); err != nil {
			t.Fatal(err)
		}
		c, unknown, err := b.correspondence(v, b.lineHead)
		if err != nil {
			t.Fatal(err)
		}
		return c, unknown
	}
	t.Run("renamed on main", func(t *testing.T) {
		f, _ := renameWorld(t)
		if c, unknown := corr(t, f, 5); c["app/new.txt"] != "app/old.txt" || len(unknown) != 0 {
			t.Errorf("correspondence = %v, %v; want app/new.txt on app/old.txt", c, unknown)
		}
	})
	t.Run("renamed on the line", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: an app file", "", map[string]string{"app/a.txt": appFile})
		f.lines()
		f.onLine("release-1.1", "fix(app): the line renames it", map[string]string{"app/a.txt": "", "app/line.txt": appFile})
		f.pr(5, "fix(app): fix it", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		if c, unknown := corr(t, f, 5); c["app/a.txt"] != "app/line.txt" || len(unknown) != 0 {
			t.Errorf("correspondence = %v, %v; want app/a.txt on app/line.txt", c, unknown)
		}
	})
	t.Run("renamed on both", func(t *testing.T) {
		f, _ := renameWorld(t)
		f.onLine("release-1.1", "fix(app): the line renames it its own way", map[string]string{"app/old.txt": "", "app/release.txt": appFile})
		if c, unknown := corr(t, f, 5); c["app/new.txt"] != "app/release.txt" || len(unknown) != 0 {
			t.Errorf("correspondence = %v, %v; want app/new.txt on app/release.txt", c, unknown)
		}
	})
	t.Run("not renamed, absent", func(t *testing.T) {
		f := newFixture(t)
		f.lines()
		f.pr(3, "feat(app): x", "", map[string]string{"app/x.txt": appFile})
		f.pr(5, "fix(app): fix x", "", map[string]string{"app/x.txt": strings.Replace(appFile, "two", "TWO", 1)})
		if c, unknown := corr(t, f, 5); c["app/x.txt"] != "app/x.txt" || len(unknown) != 0 {
			t.Errorf("correspondence = %v, %v; want app/x.txt on itself", c, unknown)
		}
	})
	t.Run("rename detection skipped", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: two app files", "", map[string]string{"app/a.txt": appFile, "app/b.txt": appFile + "six\n"})
		f.lines()
		f.onLine("release-1.1", "fix(app): the line renames both, changed", map[string]string{"app/a.txt": "", "app/b.txt": "", "app/x.txt": appFile + "x\n", "app/y.txt": appFile + "six\ny\n"})
		f.pr(5, "fix(app): fix a", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
		// Past the limit, git says it skipped looking; a configured limit
		// of its own does not decide for the tool.
		f.git("config", "diff.renameLimit", "1")
		if c, unknown := corr(t, f, 5, 0); c["app/a.txt"] != "app/x.txt" || len(unknown) != 0 {
			t.Errorf("correspondence under diff.renameLimit=1 = %v, %v; want app/a.txt on app/x.txt", c, unknown)
		}
		if c, unknown := corr(t, f, 5, 1); !slices.Equal(unknown, []string{"app/a.txt"}) || c["app/a.txt"] != "" {
			t.Errorf("correspondence past the limit = %v, %v; want app/a.txt unknown", c, unknown)
		}
	})
}

// What a change leaves without effect: a modified file the line lacks on both
// sides of it, a deleted file the line never had. A mode change that only a
// person can judge is a diagnostic, not an omission.
func TestOmittedEffects(t *testing.T) {
	omitted := func(t *testing.T, f *fixture, n int, files map[string]string) ([]string, []string) {
		t.Helper()
		b := shaped(t, f)
		after := commitOn(f, b.lineHead, files)
		om, diag, err := b.omittedEffects(b.find(n), b.lineHead, after)
		if err != nil {
			t.Fatal(err)
		}
		return om, diag
	}
	t.Run("a modified file missing before and after", func(t *testing.T) {
		f := newFixture(t)
		f.lines()
		f.pr(3, "feat(app): x", "", map[string]string{"app/x.txt": appFile})
		f.pr(5, "fix(app): fix x and five", "", map[string]string{"app/x.txt": "fixed\n", "app/five.txt": "5\n"})
		if om, diag := omitted(t, f, 5, map[string]string{"app/five.txt": "5\n"}); !slices.Equal(om, []string{"app/x.txt"}) || len(diag) != 0 {
			t.Errorf("omitted = %q, %q; want app/x.txt", om, diag)
		}
	})
	t.Run("a deleted file absent before", func(t *testing.T) {
		f, _ := policyWorld(t)
		if om, _ := omitted(t, f, 5, map[string]string{"chart/tests/values_test.yaml": "suite: values\n"}); !slices.Equal(om, []string{"chart/templates/policy.yaml", "chart/tests/policy_test.yaml"}) {
			t.Errorf("omitted = %q; want both policy files", om)
		}
	})
	t.Run("a mode change", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: a script", "", map[string]string{"app/run.sh": "#!/bin/sh\n", "app/b.txt": "old\n"})
		f.lines()
		f.git("switch", "-q", "-c", "pr-5")
		if err := os.Chmod(filepath.Join(f.dir, "app/run.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		f.write("app/b.txt", "fixed\n")
		f.git("add", "-A")
		f.git("commit", "-q", "-m", "run it")
		f.git("switch", "-q", "main")
		f.git("merge", "-q", "--no-ff", "-m", "fix(app): make the script runnable (#5)", "pr-5")
		f.git("push", "-q", "origin", "main")
		om, diag := omitted(t, f, 5, map[string]string{"app/b.txt": "fixed\n"})
		if len(om) != 0 || len(diag) != 1 || !strings.Contains(diag[0], `the mode or type of "app/run.sh" changes`) {
			t.Errorf("omitted = %q, %q; want a diagnostic for app/run.sh and nothing omitted", om, diag)
		}
	})
}

// What git says when it fails names file names as text, never as lines a
// terminal or a CI log would take for its own.
func TestGitErrorsQuoteFileNames(t *testing.T) {
	newFixture(t)
	const evil = "x\n::warning::forged"
	if _, err := gitTimed("log", "--no-such-option", "--", evil); err == nil || strings.Contains(err.Error(), "\n::warning") {
		t.Errorf("gitTimed error = %v, want the path quoted", err)
	}
	if _, err := treeEntries("no-such-revision", []string{evil}); err == nil || strings.Contains(err.Error(), "\n::warning") {
		t.Errorf("treeEntries error = %v, want the path quoted", err)
	}
}

// fixedWorld is the sign-off's two-file case: the base has a and b as old,
// #5 fixes both, and the line already has a as onLine says, if anything.
func fixedWorld(t *testing.T, a, old, onLine, fixed string) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.squash(2, "chore: a and b", "", map[string]string{a: old, "app/b.txt": "old\n"})
	f.lines()
	if onLine != "" {
		f.onLine("release-1.1", "fix(app): a, on the line", map[string]string{a: onLine})
	}
	mc5 := f.pr(5, "fix(app): a and b", "", map[string]string{a: fixed, "app/b.txt": "fixed\n"})
	return f, mc5
}

// The line can already have what an original does to a file: then a pick
// that leaves the file alone carries the original whole. The check-time
// pass, the picker, fold and verify all take it, binary files included,
// without asking git merge-file what blob equality settles. A different fix
// on the line, or a deletion of what the line never had, is still left out.
func TestAlreadyAppliedFileIsNotOmitted(t *testing.T) {
	const textPost = "one\nfixed\nthree\nfour\nfive\n"
	accepted := func(t *testing.T, a, old, onLine, fixed string) {
		t.Helper()
		f, _ := fixedWorld(t, a, old, onLine, fixed)
		if code, stdout, stderr := f.exec(nil, "check", "--line", "release-1.1"); code != 1 || !strings.Contains(stdout, "/pull/5 ") || strings.Contains(stderr, "parked") {
			t.Errorf("check = %d, want #5 missing and not parked:\n%s\n%s", code, stdout, stderr)
		}
		gh := &fakeGH{}
		head := openBatch(t, f, gh)
		if got := f.subjects("origin/release-1.1", head); !slices.Equal(got, []string{"fix(app): a and b (#5)"}) {
			t.Errorf("picked %q, want #5", got)
		}
		if code, out, stderr := f.exec(gh, "verify", "--line", "release-1.1", "--head", head); code != 0 {
			t.Errorf("verify = %d\n%s\n%s", code, out, stderr)
		}
		if code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1", "--cut"); code != 1 || strings.Contains(stdout, "only part of it applies") {
			t.Errorf("check --cut = %d, want #5 only on the batch, whole:\n%s\n%s", code, stdout, stderr)
		}
		f, mc5 := fixedWorld(t, a, old, onLine, fixed)
		w := emptyBatch(f, &fakeGH{}, "")
		pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/b.txt": "fixed\n"})
		fold := w.merge(w.head, pick, "fix(app): a and b (#5)\n\n"+batchTrailer+": "+mc5)
		if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 0 {
			t.Errorf("fold = %d, %q\n%s", code, out, stderr)
		}
	}
	t.Run("the line has the original's file", func(t *testing.T) {
		accepted(t, "app/a.txt", "old\n", "fixed\n", "fixed\n")
	})
	t.Run("the line has the fix beside lines of its own", func(t *testing.T) {
		accepted(t, "app/a.txt", strings.Replace(textPost, "fixed", "old", 1), textPost+"line only\n", textPost)
	})
	t.Run("the line has the original's binary file", func(t *testing.T) {
		accepted(t, "app/a.bin", "old\x00payload", "fixed\x00payload", "fixed\x00payload")
		f, _ := fixedWorld(t, "app/a.bin", "old\x00payload", "fixed\x00payload", "fixed\x00payload")
		b := shaped(t, f)
		om, diag, err := b.omittedEffects(b.find(5), b.lineHead, commitOn(f, b.lineHead, map[string]string{"app/b.txt": "fixed\n"}))
		if err != nil || len(om)+len(diag) != 0 || b.mergeFiles != 0 {
			t.Errorf("omitted = %q, %q, %v, merge-file ran %d times; want nothing, and merge-file never asked", om, diag, err, b.mergeFiles)
		}
	})
	t.Run("an earlier unit of the batch brought the file", func(t *testing.T) {
		f := newFixture(t)
		f.squash(2, "chore: a and b", "", map[string]string{"app/a.txt": "old\n", "app/b.txt": "old\n"})
		f.lines()
		f.pr(4, "fix(app): a", "", map[string]string{"app/a.txt": "fixed\n"})
		f.squash(40, "chore: put a back for now", "", map[string]string{"app/a.txt": "old\n"})
		f.pr(5, "fix(app): a and b", "", map[string]string{"app/a.txt": "fixed\n", "app/b.txt": "fixed\n"})
		gh := &fakeGH{}
		head := openBatch(t, f, gh)
		if got := f.subjects("origin/release-1.1", head); !slices.Equal(got, []string{"fix(app): a (#4)", "fix(app): a and b (#5)"}) {
			t.Errorf("picked %q, want #4 and #5", got)
		}
		if code, out, stderr := f.exec(gh, "verify", "--line", "release-1.1", "--head", head); code != 0 {
			t.Errorf("verify = %d\n%s\n%s", code, out, stderr)
		}
	})
	t.Run("the line has a different fix", func(t *testing.T) {
		f, mc5 := fixedWorld(t, "app/a.txt", "old\n", "other\n", "fixed\n")
		w := emptyBatch(f, &fakeGH{}, "")
		pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/b.txt": "fixed\n"})
		fold := w.merge(w.head, pick, "fix(app): a and b (#5)\n\n"+batchTrailer+": "+mc5)
		if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 1 || !strings.Contains(out, `leaves "app/a.txt" without effect`) {
			t.Errorf("fold = %d, %q, want it refused for app/a.txt\n%s", code, out, stderr)
		}
	})
	t.Run("the line has a different binary file", func(t *testing.T) {
		f, _ := fixedWorld(t, "app/a.bin", "old\x00payload", "other\x00payload", "fixed\x00payload")
		b := shaped(t, f)
		om, diag, err := b.omittedEffects(b.find(5), b.lineHead, commitOn(f, b.lineHead, map[string]string{"app/b.txt": "fixed\n"}))
		if err != nil || len(om) != 0 || len(diag) != 1 || !strings.Contains(diag[0], `cannot merge "app/a.bin" as text`) {
			t.Errorf("omitted = %q, %q, %v; want a diagnostic for app/a.bin, nothing omitted", om, diag, err)
		}
	})
	t.Run("a deletion of what the line never had", func(t *testing.T) {
		f, mc5 := policyWorld(t)
		w := emptyBatch(f, &fakeGH{}, "")
		pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"chart/tests/values_test.yaml": "suite: values\n"})
		fold := w.merge(w.head, pick, "fix(chart): remove the policy a rebase brought back (#5)\n\n"+batchTrailer+": "+mc5)
		if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 1 || !strings.Contains(out, "without effect") {
			t.Errorf("fold = %d, %q, want it refused\n%s", code, out, stderr)
		}
	})
}

// A change that makes none, whatever names its two sides, leaves a deletion
// the line already made as it is: the original is there, not partial.
func TestDeletionAlreadyMadeByNoChange(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a policy", "", map[string]string{"app/policy.yaml": "allow: all\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): the line drops the policy", map[string]string{"app/policy.yaml": ""})
	f.pr(5, "fix(app): drop the policy", "", map[string]string{"app/policy.yaml": ""})
	b := shaped(t, f)
	tree := f.git("rev-parse", b.lineHead+"^{tree}")
	for _, after := range []string{b.lineHead, tree} {
		if om, _, err := b.omittedEffects(b.find(5), b.lineHead, after); err != nil || len(om) != 0 {
			t.Errorf("omitted against %.12s = %q, %v; want nothing", after, om, err)
		}
	}
}
