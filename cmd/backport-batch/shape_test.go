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
