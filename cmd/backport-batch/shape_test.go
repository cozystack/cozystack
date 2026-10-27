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

// A conflict on a commit in the middle of a pull request takes the batch back
// to where it was before that original: none of its commits stays, the
// conflict is recorded with the commit that failed, and the next original is
// still picked.
func TestRunConflictInAMiddleCommit(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})
	mc5 := f.prCommits(5, "fix(chart): five", "",
		change{files: map[string]string{"app/five.txt": "5\n"}},
		change{files: map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n"}},
		change{files: map[string]string{"app/five.txt": "5, done\n"}})
	own := strings.Fields(f.git("rev-list", "--reverse", mc5+"^1.."+mc5+"^2"))
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})

	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("the batch carries %q, want only #6", got)
	}
	if log := f.git("log", "--format=%B", "origin/release-1.1..HEAD"); strings.Contains(log, own[0]) || strings.Contains(log, own[1]) {
		t.Errorf("a commit of #5 stayed on the batch:\n%s", log)
	}
	if out := f.git("status", "--porcelain"); out != "" {
		t.Errorf("the tree is left dirty:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git", "CHERRY_PICK_HEAD")); err == nil {
		t.Error("a cherry-pick is left in progress")
	}
	l, _ := parseLine("release-1.1")
	deferred, err := parseDeferred(stdout, l)
	if err != nil || deferred[5].Commit != own[1] || !slices.Equal(deferred[5].Files, []string{"chart/values.yaml"}) {
		t.Errorf("deferred = %+v, %v; want #5 at %s in chart/values.yaml", deferred, err, own[1])
	}
	if !strings.Contains(stdout, "`fix(chart): five` at `"+own[1][:12]+"`: `chart/values.yaml`") {
		t.Errorf("the body does not name the commit that failed:\n%s", stdout)
	}
}

// What counts as carrying a merged pull request of several commits: a batch
// merge naming its merge commit, an -x line naming the merge commit, or -x
// lines naming every one of its own commits; not some of them, and not a
// trailer outside the trailer block.
func TestPresenceOfAMultiCommitOriginal(t *testing.T) {
	cases := []struct {
		name    string
		land    func(f *fixture, mc string, own []string)
		carried string
	}{
		{
			name: "a batch merge naming it",
			land: func(f *fixture, mc string, _ []string) {
				f.onLine("release-1.1", "fix(app): five (#5)\n\n"+batchTrailer+": "+mc, map[string]string{"app/a.txt": "a\n"})
			},
			carried: "batched in",
		},
		{
			name: "an -x line naming the merge commit",
			land: func(f *fixture, mc string, _ []string) {
				f.onLine("release-1.1", "port\n\n(cherry picked from commit "+mc+")", map[string]string{"app/a.txt": "a\n"})
			},
			carried: "picked in",
		},
		{
			name: "-x lines naming every commit of its own",
			land: func(f *fixture, _ string, own []string) {
				f.onLine("release-1.1", "a\n\n(cherry picked from commit "+own[0]+")", map[string]string{"app/a.txt": "a\n"})
				f.onLine("release-1.1", "b\n\n(cherry picked from commit "+own[1]+")", map[string]string{"app/b.txt": "b, half\n"})
			},
			carried: "its 2 commits picked",
		},
		{
			name: "an -x line naming one of its two commits",
			land: func(f *fixture, _ string, own []string) {
				f.onLine("release-1.1", "a\n\n(cherry picked from commit "+own[0]+")", map[string]string{"app/a.txt": "a\n"})
			},
		},
		{
			name: "the batch trailer in the body",
			land: func(f *fixture, mc string, _ []string) {
				f.onLine("release-1.1", "port\n\n"+batchTrailer+": "+mc+"\n\nMore to say.", map[string]string{"app/a.txt": "a\n"})
			},
		},
		{
			name: "the batch trailer naming another merge commit",
			land: func(f *fixture, _ string, _ []string) {
				f.onLine("release-1.1", "port\n\n"+batchTrailer+": 0123456789abcdef0123456789abcdef01234567", map[string]string{"app/a.txt": "a\n"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.lines()
			mc := f.prCommits(5, "fix(app): five", "",
				change{files: map[string]string{"app/a.txt": "a\n"}},
				change{files: map[string]string{"app/b.txt": "b\n"}})
			own := strings.Fields(f.git("rev-list", "--reverse", mc+"^1.."+mc+"^2"))
			tc.land(f, mc, own)
			v := verdictOn(t, f, nil, "release-1.1", 5)
			switch {
			case tc.carried == "" && v.rule != 0:
				t.Errorf("verdict = rule %d (%s), want it to qualify", v.rule, v.reason)
			case tc.carried != "" && (v.rule != ruleOnLine || !strings.HasPrefix(v.reason, tc.carried)):
				t.Errorf("verdict = rule %d (%s), want rule 1 with %q", v.rule, v.reason, tc.carried)
			}
		})
	}
}

// verify passes a batch the run built and fails one with an original that is
// not in the batch's shape, reading nothing from GitHub when given the head.
func TestVerify(t *testing.T) {
	type world struct {
		f    *fixture
		gh   *fakeGH
		head string
		mc5  string
		sq6  string
	}
	setup := func(t *testing.T) *world {
		f := newFixture(t)
		f.lines()
		w := &world{f: f}
		w.mc5 = f.prCommits(5, "fix(app): five", "",
			change{files: map[string]string{"app/five.txt": "5\n"}},
			change{files: map[string]string{"app/five.txt": "5, done\n"}})
		w.sq6 = f.squash(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		w.gh = &fakeGH{}
		w.head = openBatch(t, f, w.gh)
		return w
	}
	// on adds a commit with message and files on top of the batch head.
	on := func(w *world, message string, files map[string]string) string {
		w.f.git("switch", "-q", "--detach", w.head)
		w.f.apply(files)
		w.f.git("commit", "-q", "--allow-empty", "-m", message)
		sha := w.f.git("rev-parse", "HEAD")
		w.f.git("switch", "-q", "main")
		return sha
	}
	cases := []struct {
		name    string
		head    func(w *world) string
		refusal string
	}{
		{name: "the batch as the run built it", head: func(w *world) string { return w.head }},
		{
			name: "an original picked as one commit with -m 1",
			head: func(w *world) string {
				return on(w, "fix(app): five (#5)\n\n(cherry picked from commit "+w.mc5+")", map[string]string{"app/five.txt": "5, again\n"})
			},
			refusal: "picks the merge commit of #5 as one commit",
		},
		{
			name: "a commit that is no original",
			head: func(w *world) string {
				return on(w, "fix(ci): adjust for the line", map[string]string{"ci.txt": "x\n"})
			},
			refusal: "carries 0 (cherry picked from commit",
		},
		{
			name: "an original carried twice",
			head: func(w *world) string {
				own := strings.Fields(w.f.git("rev-list", "--reverse", w.mc5+"^1.."+w.mc5+"^2"))
				p := on(w, "again\n\n(cherry picked from commit "+own[1]+")", map[string]string{"app/five.txt": "5, done again\n"})
				w.f.git("switch", "-q", "--detach", w.head)
				w.f.git("merge", "-q", "--no-ff", "-m", "fix(app): five (#5)\n\n"+batchTrailer+": "+w.mc5, p)
				sha := w.f.git("rev-parse", "HEAD")
				w.f.git("switch", "-q", "main")
				return sha
			},
			refusal: "carries #5 again",
		},
		{
			name: "a squash-merged original carried as a merge",
			head: func(w *world) string {
				p := on(w, "six\n\n(cherry picked from commit "+w.sq6+")", map[string]string{"app/six.txt": "6, again\n"})
				w.f.git("switch", "-q", "--detach", w.head)
				w.f.git("merge", "-q", "--no-ff", "-m", "fix(app): six (#6)\n\n"+batchTrailer+": "+w.sq6, p)
				sha := w.f.git("rev-parse", "HEAD")
				w.f.git("switch", "-q", "main")
				return sha
			},
			refusal: "which main took as one commit",
		},
		{
			name: "a merge of the line into the batch",
			head: func(w *world) string {
				line := w.f.onLine("release-1.1", "fix(chart): on the line", map[string]string{"chart/values.yaml": "replicas: 2\nimage: app:1\n"})
				w.f.git("switch", "-q", "--detach", w.head)
				w.f.git("merge", "-q", "--no-ff", "-m", "Merge branch 'release-1.1' into the batch", line)
				sha := w.f.git("rev-parse", "HEAD")
				w.f.git("switch", "-q", "main")
				return sha
			},
			refusal: "with 0 " + batchTrailer + " trailers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := setup(t)
			head := tc.head(w)
			// GitHub is not asked: a failing fake would fail the run.
			gh := &fakeGH{err: errString("gh: not to be asked")}
			code, stdout, stderr := w.f.exec(gh, "verify", "--line", "release-1.1", "--head", head)
			switch {
			case tc.refusal == "" && code != 0:
				t.Errorf("verify = %d\n%s\n%s", code, stdout, stderr)
			case tc.refusal != "" && (code != 1 || !strings.Contains(stdout, tc.refusal)):
				t.Errorf("verify = %d, want 1 with %q:\n%s\n%s", code, tc.refusal, stdout, stderr)
			}
		})
	}

	t.Run("the open batch pull request", func(t *testing.T) {
		w := setup(t)
		code, stdout, stderr := w.f.exec(w.gh, "verify", "--line", "release-1.1")
		if code != 0 || !strings.Contains(stdout, "3 units at "+w.head[:12]) {
			t.Errorf("verify = %d\n%s\n%s", code, stdout, stderr)
		}
		if code, stdout, _ := w.f.exec(&fakeGH{}, "verify", "--line", "release-1.1"); code != 0 || !strings.Contains(stdout, "nothing to verify") {
			t.Errorf("verify with no batch open = %d, %q", code, stdout)
		}
	})
	t.Run("a head that is not here", func(t *testing.T) {
		w := setup(t)
		if code, _, stderr := w.f.exec(w.gh, "verify", "--line", "release-1.1", "--head", strings.Repeat("ab", 20)); code != 2 || !strings.Contains(stderr, "fetch it first") {
			t.Errorf("verify = %d (%s), want 2", code, stderr)
		}
	})
}

// A pick that lands among lines the line changed is the same change, and the
// body does not call it changed; one whose change differs is called so.
func TestSummaryCallsOnlyChangedPicksChanged(t *testing.T) {
	f := newFixture(t)
	f.release("release-1.0")
	f.squash(2, "chore(app): a file to change", "", map[string]string{"app/ctx.txt": "a\nb\nc\nd\ne\n"})
	f.pr(1, "feat(app): something for 1.1", "", map[string]string{"app/feature.txt": "1.1\n"})
	f.release("release-1.1")
	f.onLine("release-1.1", "fix(app): a line-only change", map[string]string{"app/ctx.txt": "A\nb\nc\nd\ne\n"})
	f.pr(5, "fix(app): five", "", map[string]string{"app/ctx.txt": "a\nb\nC\nd\ne\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "`fix(app): five`, 1 commit\n") {
		t.Errorf("a pick git only fit to its place is called changed:\n%s", stdout)
	}
}
