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
	"strconv"
	"strings"
	"testing"
)

// foldWorld is a line whose own change makes the first of #5's two commits
// conflict, an empty batch pushed for it, and the originals around #5.
type foldWorld struct {
	f        *fixture
	gh       *fakeGH
	head     string
	mc5, mc6 string
	c1, c2   string // #5's own commits
	c6       string // #6's one commit
	sq8      string
}

func newFoldWorld(t *testing.T) *foldWorld {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})
	w := &foldWorld{f: f}
	w.mc5 = f.prCommits(5, "fix(chart): five", "",
		change{files: map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n"}},
		change{files: map[string]string{"app/five.txt": "5\n"}})
	own := strings.Fields(f.git("rev-list", "--reverse", w.mc5+"^1.."+w.mc5+"^2"))
	w.c1, w.c2 = own[0], own[1]
	w.mc6 = f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	w.c6 = f.git("rev-parse", w.mc6+"^2")
	f.pr(7, "feat(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	w.sq8 = f.squash(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	f.git("fetch", "-q", "origin")
	w.head = f.git("rev-parse", "origin/"+batchBranch1)
	w.gh = &fakeGH{prs: []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "")}}
	return w
}

// commit writes files on parent with message and returns the commit.
func (w *foldWorld) commit(parent, message string, files map[string]string) string {
	w.f.git("switch", "-q", "--detach", parent)
	w.f.apply(files)
	w.f.git("commit", "-q", "--allow-empty", "-m", message)
	sha := w.f.git("rev-parse", "HEAD")
	w.f.git("switch", "-q", "main")
	return sha
}

// pick is a pick of source on parent, written as files, with its -x line.
func (w *foldWorld) pick(parent, source string, files map[string]string) string {
	return w.commit(parent, "port\n\n(cherry picked from commit "+source+")", files)
}

func (w *foldWorld) merge(base, tip, message string) string {
	w.f.git("switch", "-q", "--detach", base)
	w.f.git("merge", "-q", "--no-ff", "-m", message, tip)
	sha := w.f.git("rev-parse", "HEAD")
	w.f.git("switch", "-q", "main")
	return sha
}

// joined makes a merge of tip onto base with tip's tree, whatever a merge of
// the two would make.
func (w *foldWorld) joined(base, tip, message string) string {
	return w.f.git("commit-tree", "-p", base, "-p", tip, "-m", message, tip+"^{tree}")
}

func batchMessage(merges ...string) string {
	msg := "fix(chart): five (#5)\n"
	for _, m := range merges {
		msg += "\n" + batchTrailer + ": " + m
	}
	return msg
}

var (
	resolved1 = map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n"}
	resolved2 = map[string]string{"app/five.txt": "5\n"}
)

// resolution is the right fold of #5: both its commits picked in order on the
// batch head, the conflict resolved, under a merge that names it.
func (w *foldWorld) resolution() string {
	r1 := w.pick(w.head, w.c1, resolved1)
	r2 := w.pick(r1, w.c2, resolved2)
	return w.merge(w.head, r2, batchMessage(w.mc5))
}

// fold accepts one original on the batch head in the batch's shape, inside the
// files it touches, and nothing else.
func TestFold(t *testing.T) {
	cases := []struct {
		name     string
		original int
		make     func(w *foldWorld) string
		refusal  string
	}{
		{
			name: "a resolution of #5", original: 5,
			make: (*foldWorld).resolution,
		},
		{
			name: "one commit, the way -m 1 picked it", original: 5,
			make: func(w *foldWorld) string {
				return w.commit(w.head, "fix(chart): five (#5)\n\n(cherry picked from commit "+w.mc5+")", map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n", "app/five.txt": "5\n"})
			},
			refusal: "has 1 parents; #5 is a merged pull request",
		},
		{
			name: "a merge not on the batch head", original: 5,
			make: func(w *foldWorld) string {
				base := w.commit(w.head, "something else", map[string]string{"app/five.txt": "x\n"})
				r1 := w.pick(base, w.c1, resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				return w.merge(base, r2, batchMessage(w.mc5))
			},
			refusal: "not on the head of batch pull request #90",
		},
		{
			name: "a merge that does not name #5", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				return w.merge(w.head, r2, batchMessage())
			},
			refusal: "with 0 " + batchTrailer + " trailers",
		},
		{
			name: "two originals in one resolution", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				r3 := w.pick(r2, w.c6, map[string]string{"app/six.txt": "6\n"})
				return w.merge(w.head, r3, batchMessage(w.mc5, w.mc6))
			},
			refusal: "with 2 " + batchTrailer + " trailers",
		},
		{
			name: "a commit of another pull request", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				r3 := w.pick(r2, w.c6, map[string]string{"app/six.txt": "6\n"})
				return w.merge(w.head, r3, batchMessage(w.mc5))
			},
			refusal: "picks a commit of #6 into the merge of #5",
		},
		{
			name: "an extra commit", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				r3 := w.commit(r2, "fix up the port", map[string]string{"app/five.txt": "5, tidied\n"})
				return w.merge(w.head, r3, batchMessage(w.mc5))
			},
			refusal: "ends in no (cherry picked from commit",
		},
		{
			name: "a file #5 does not touch", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, resolved1)
				r2 := w.pick(r1, w.c2, map[string]string{"app/five.txt": "5\n", "app/smuggled.txt": "x\n"})
				return w.merge(w.head, r2, batchMessage(w.mc5))
			},
			refusal: `touches "app/smuggled.txt"`,
		},
		{
			name: "a pick without its -x line", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.commit(w.head, "port", resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				return w.merge(w.head, r2, batchMessage(w.mc5))
			},
			refusal: "ends in no (cherry picked from commit",
		},
		{
			name: "an abbreviated -x line", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1[:12], resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				return w.merge(w.head, r2, batchMessage(w.mc5))
			},
			refusal: "ends in no (cherry picked from commit",
		},
		{
			name: "an -x line in the body, not in the trailers", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.commit(w.head, "port\n\n(cherry picked from commit "+w.c1+")\n\nMore to say.", resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				return w.merge(w.head, r2, batchMessage(w.mc5))
			},
			refusal: "ends in no (cherry picked from commit",
		},
		{
			name: "both commits in one pick", original: 5,
			make: func(w *foldWorld) string {
				both := w.commit(w.head, "port\n\n(cherry picked from commit "+w.c1+")\n(cherry picked from commit "+w.c2+")", map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n", "app/five.txt": "5\n"})
				return w.merge(w.head, both, batchMessage(w.mc5))
			},
			refusal: "claims more than one commit of #5",
		},
		{
			name: "the commits in the wrong order", original: 5,
			make: func(w *foldWorld) string {
				r2 := w.pick(w.head, w.c2, resolved2)
				r1 := w.pick(r2, w.c1, resolved1)
				return w.merge(w.head, r1, batchMessage(w.mc5))
			},
			refusal: "out of the order of #5's commits",
		},
		{
			name: "one commit picked twice", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, resolved1)
				again := w.pick(r1, w.c1, map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:2\n"})
				return w.merge(w.head, again, batchMessage(w.mc5))
			},
			refusal: "out of the order of #5's commits",
		},
		{
			name: "picks that do not start on the batch head", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head+"~1", w.c1, resolved1)
				r2 := w.pick(r1, w.c2, resolved2)
				return w.joined(w.head, r2, batchMessage(w.mc5))
			},
			refusal: "is not on a straight line of picks",
		},
		{
			name: "picks that change nothing", original: 5,
			make: func(w *foldWorld) string {
				r1 := w.pick(w.head, w.c1, nil)
				r2 := w.pick(r1, w.c2, nil)
				return w.merge(w.head, r2, batchMessage(w.mc5))
			},
			refusal: "changes nothing",
		},
		{
			name: "a sound fold of another original", original: 5,
			make: func(w *foldWorld) string {
				p := w.pick(w.head, w.c6, map[string]string{"app/six.txt": "6\n"})
				return w.merge(w.head, p, "fix(app): six (#6)\n\n"+batchTrailer+": "+w.mc6)
			},
			refusal: "carries #6, not #5",
		},
		{
			name: "one pick of a squash-merged original", original: 8,
			make: func(w *foldWorld) string {
				return w.pick(w.head, w.sq8, map[string]string{"app/eight.txt": "8\n"})
			},
		},
		{
			name: "a squash-merged original as a merge", original: 8,
			make: func(w *foldWorld) string {
				p := w.pick(w.head, w.sq8, map[string]string{"app/eight.txt": "8\n"})
				return w.merge(w.head, p, "fix(app): eight (#8)\n\n"+batchTrailer+": "+w.sq8)
			},
			refusal: "has 2 parents; #8 merged as one commit",
		},
		{
			name: "an original the batch does not take", original: 7,
			make:    func(w *foldWorld) string { return w.commit(w.head, "port", map[string]string{"app/seven.txt": "7\n"}) },
			refusal: "rule 2",
		},
		{
			name: "an original that never merged here", original: 4242,
			make:    func(w *foldWorld) string { return w.commit(w.head, "port", map[string]string{"app/x.txt": "x\n"}) },
			refusal: "did not merge into main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoldWorld(t)
			sha := tc.make(w)
			code, stdout, stderr := w.f.exec(w.gh, "fold", "--line", "release-1.1", "--original", strconv.Itoa(tc.original), "--commit", sha)
			switch {
			case tc.refusal == "" && (code != 0 || strings.TrimSpace(stdout) != "ok"):
				t.Errorf("fold = %d, %q, %s; want ok", code, stdout, stderr)
			case tc.refusal != "" && (code != 1 || !strings.Contains(stdout, tc.refusal)):
				t.Errorf("fold = %d, %q, %s; want a refusal with %q", code, stdout, stderr, tc.refusal)
			}
		})
	}

	t.Run("no open batch pull request", func(t *testing.T) {
		w := newFoldWorld(t)
		code, stdout, _ := w.f.exec(&fakeGH{}, "fold", "--line", "release-1.1", "--original", "5", "--commit", w.resolution())
		if code != 1 || !strings.Contains(stdout, "no batch pull request is open") {
			t.Errorf("fold = %d, %q; want a refusal", code, stdout)
		}
	})
	t.Run("already carried by the batch", func(t *testing.T) {
		w := newFoldWorld(t)
		folded := w.resolution()
		w.f.git("push", "-q", "origin", folded+":refs/heads/"+batchBranch1)
		w.gh.prs[0].Head.SHA = folded
		w.head = folded
		code, stdout, _ := w.f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", w.resolution())
		if code != 1 || !strings.Contains(stdout, "already carries #5") {
			t.Errorf("fold = %d, %q; want a refusal", code, stdout)
		}
	})
	t.Run("never commits", func(t *testing.T) {
		w := newFoldWorld(t)
		sha := w.resolution()
		before := w.f.git("for-each-ref")
		w.f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", sha)
		if after := w.f.git("for-each-ref"); after != before || w.f.remoteHead(batchBranch1) != w.head {
			t.Errorf("fold moved a ref:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

// A resolution of an original a maintainer dropped from the line is refused:
// the drop is the decision that it does not go there.
func TestFoldRefusesADroppedOriginal(t *testing.T) {
	w := newFoldWorld(t)
	sha := strings.Repeat("a", 40)
	rec := strings.Replace(v2(""), `"dropped":[{"original":8,"merge":"`+sha+`"`, `"dropped":[{"original":5,"merge":"`+w.mc5+`"`, 1)
	rec = strings.Replace(rec, `"deferred":[{"original":5,"commit":"`+sha+`","files":["app/five.txt"]},`, `"deferred":[`, 1)
	rec = strings.Replace(rec, `"stale":[9]`, `"stale":[]`, 1)
	w.gh.prs[0].Body = "text\n\n" + deferredFence + "\n" + rec + "\n```\n"
	if _, err := parseRecord(w.gh.prs[0].Body, notifyLine); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := w.f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", w.resolution())
	if code != 1 || !strings.Contains(stdout, "#5 is not one the batch takes: a maintainer's drop, dropped on #90: needs #4") {
		t.Errorf("fold = %d, %q, %s; want a refusal naming the drop", code, stdout, stderr)
	}
}

// A resolution that ports what the line has of #5 and leaves alone a file
// the line lacks is no fold: what #5 does to that file would be lost, and a
// line-only port is a pull request of its own.
func TestFoldRefusesAnOmittedEffect(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(3, "feat(app): a feature the line never gets", "", map[string]string{"app/feature2.txt": "v1\n"})
	mc5 := f.pr(5, "fix(app): five", "", map[string]string{"app/feature2.txt": "v2\n", "app/five.txt": "5\n"})
	w := emptyBatch(f, &fakeGH{}, "")
	pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/five.txt": "5\n"})
	fold := w.merge(w.head, pick, "fix(app): five (#5)\n\n"+batchTrailer+": "+mc5)
	code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold)
	if code != 1 || out != "refused: #5 leaves \"app/feature2.txt\" without effect on release-1.1; port only what the line has as a pull request of its own\n" {
		t.Errorf("fold = %d, %q, want it refused for app/feature2.txt\n%s", code, out, stderr)
	}
}

// A resolution that keeps a file the original deletes leaves the deletion
// without effect.
func TestFoldRefusesAKeptDeletion(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: x", "", map[string]string{"app/x.txt": "x\n"})
	f.lines()
	mc5 := f.pr(5, "fix(app): five, and x goes", "", map[string]string{"app/x.txt": "", "app/five.txt": "5\n"})
	w := emptyBatch(f, &fakeGH{}, "")
	pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/five.txt": "5\n"})
	fold := w.merge(w.head, pick, "fix(app): five, and x goes (#5)\n\n"+batchTrailer+": "+mc5)
	if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 1 || !strings.Contains(out, `leaves "app/x.txt" without effect`) {
		t.Errorf("fold that keeps app/x.txt = %d, %q\n%s", code, out, stderr)
	}
}

// An original parked for what it builds on can still land as a complete
// hand adaptation: every file it changes, changed, in the batch's shape.
func TestFoldAcceptsACompleteAdaptationOfAParkedOriginal(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.pr(3, "feat(app): a loud two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	mc5 := f.pr(5, "fix(app): an excited two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	w := emptyBatch(f, &fakeGH{}, "")
	pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/a.txt": strings.Replace(appFile, "two", "two!", 1)})
	fold := w.merge(w.head, pick, "fix(app): an excited two (#5)\n\n"+batchTrailer+": "+mc5)
	if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 0 || out != "ok\n" {
		t.Errorf("fold of an adaptation of #5 = %d, %q\n%s", code, out, stderr)
	}
}

// A fold that keeps a file the original deletes leaves the deletion without
// effect, whatever it changes in the file: a port that keeps what the line
// made of it, or the file with a line added, is a pull request of its own.
func TestFoldRefusesAChangedKeptDeletion(t *testing.T) {
	for name, kept := range map[string]string{
		"the line's own content":  "line only\n",
		"the file, one line more": "feature\nline only\n# reviewed\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.squash(2, "chore: a feature", "", map[string]string{"app/x.txt": "feature\n"})
			f.lines()
			f.onLine("release-1.1", "fix(app): a line-only setting beside the feature", map[string]string{"app/x.txt": "feature\nline only\n"})
			mc5 := f.pr(5, "fix(app): drop the feature, and five", "", map[string]string{"app/x.txt": "", "app/five.txt": "5\n"})
			w := emptyBatch(f, &fakeGH{}, "")
			pick := w.pick(w.head, f.git("rev-parse", mc5+"^2"), map[string]string{"app/x.txt": kept, "app/five.txt": "5\n"})
			fold := w.merge(w.head, pick, "fix(app): drop the feature, and five (#5)\n\n"+batchTrailer+": "+mc5)
			if code, out, stderr := f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", fold); code != 1 || !strings.Contains(out, `leaves "app/x.txt" without effect`) {
				t.Errorf("fold = %d, %q, want it refused\n%s", code, out, stderr)
			}
		})
	}
}
