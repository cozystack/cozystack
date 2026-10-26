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

// fold accepts one commit on the batch head that ports one original inside
// the files it touches and names it in its -x line, and nothing else.
func TestFold(t *testing.T) {
	type world struct {
		f        *fixture
		gh       *fakeGH
		head     string
		mc5, mc6 string
		pr5      string
	}
	setup := func(t *testing.T) *world {
		f := newFixture(t)
		f.lines()
		f.onLine("release-1.1", "fix(chart): a line-only value", map[string]string{"chart/values.yaml": "replicas: 3\nimage: app:1\n"})
		w := &world{f: f}
		w.mc5 = f.pr(5, "fix(chart): five", "", map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n", "app/five.txt": "5\n"})
		w.pr5 = f.git("rev-parse", w.mc5+"^2")
		w.mc6 = f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		f.pr(7, "feat(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
		f.git("fetch", "-q", "origin")
		w.head = f.git("rev-parse", "origin/"+batchBranch1)
		w.gh = &fakeGH{prs: []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "")}}
		return w
	}
	// commit writes files on parent with message and returns the commit.
	commit := func(w *world, parent, message string, files map[string]string) string {
		w.f.git("switch", "-q", "--detach", parent)
		w.f.apply(files)
		w.f.git("commit", "-q", "--allow-empty", "-m", message)
		sha := w.f.git("rev-parse", "HEAD")
		w.f.git("switch", "-q", "main")
		return sha
	}
	resolved := map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n", "app/five.txt": "5\n"}
	cases := []struct {
		name     string
		original int
		make     func(w *world) string
		refusal  string
	}{
		{
			name: "a resolution of #5", original: 5,
			make: func(w *world) string {
				return commit(w, w.head, "fix(chart): five (#5)\n\n(cherry picked from commit "+w.mc5+")", resolved)
			},
		},
		{
			name: "named by one of the pull request's commits", original: 5,
			make: func(w *world) string {
				return commit(w, w.head, "port\n\n(cherry picked from commit "+w.pr5+")", resolved)
			},
		},
		{
			name: "a commit not on the batch head", original: 5,
			make: func(w *world) string {
				parent := commit(w, w.head, "something else", map[string]string{"app/five.txt": "5\n"})
				return commit(w, parent, "port\n\n(cherry picked from commit "+w.mc5+")", resolved)
			},
			refusal: "not on the head of batch pull request #90",
		},
		{
			name: "a merge", original: 5,
			make: func(w *world) string {
				side := commit(w, w.head, "side", map[string]string{"app/five.txt": "5\n"})
				w.f.git("switch", "-q", "--detach", w.head)
				w.f.git("merge", "-q", "--no-ff", "-m", "port\n\n(cherry picked from commit "+w.mc5+")", side)
				sha := w.f.git("rev-parse", "HEAD")
				w.f.git("switch", "-q", "main")
				return sha
			},
			refusal: "has 2 parents",
		},
		{
			name: "a file #5 does not touch", original: 5,
			make: func(w *world) string {
				files := map[string]string{"chart/values.yaml": "replicas: 5\nimage: app:1\n", "app/five.txt": "5\n", "app/smuggled.txt": "x\n"}
				return commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")", files)
			},
			refusal: `touches "app/smuggled.txt"`,
		},
		{
			name: "no -x line", original: 5,
			make:    func(w *world) string { return commit(w, w.head, "fix(chart): five (#5)", resolved) },
			refusal: "carries no (cherry picked from commit",
		},
		{
			name: "an -x line for another original too", original: 5,
			make: func(w *world) string {
				return commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")\n(cherry picked from commit "+w.mc6+")", resolved)
			},
			refusal: "which is not a commit of #5",
		},
		{
			name: "an abbreviated -x line", original: 5,
			make: func(w *world) string {
				return commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5[:12]+")", resolved)
			},
			refusal: "carries no (cherry picked from commit",
		},
		{
			name: "an -x line in the body, not in the trailers", original: 5,
			make: func(w *world) string {
				return commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")\n\nMore to say.", resolved)
			},
			refusal: "carries no (cherry picked from commit",
		},
		{
			name: "an empty commit that only claims #5", original: 5,
			make: func(w *world) string {
				return commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")", nil)
			},
			refusal: "changes nothing",
		},
		{
			name: "an original the batch does not take", original: 7,
			make:    func(w *world) string { return commit(w, w.head, "port", map[string]string{"app/seven.txt": "7\n"}) },
			refusal: "rule 2",
		},
		{
			name: "an original that never merged here", original: 4242,
			make:    func(w *world) string { return commit(w, w.head, "port", map[string]string{"app/x.txt": "x\n"}) },
			refusal: "did not merge into main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := setup(t)
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
		w := setup(t)
		sha := commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")", resolved)
		code, stdout, _ := w.f.exec(&fakeGH{}, "fold", "--line", "release-1.1", "--original", "5", "--commit", sha)
		if code != 1 || !strings.Contains(stdout, "no batch pull request is open") {
			t.Errorf("fold = %d, %q; want a refusal", code, stdout)
		}
	})
	t.Run("already carried by the batch", func(t *testing.T) {
		w := setup(t)
		folded := commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")", resolved)
		w.f.git("push", "-q", "origin", folded+":refs/heads/"+batchBranch1)
		w.gh.prs[0].Head.SHA = folded
		again := commit(w, folded, "again\n\n(cherry picked from commit "+w.mc5+")", map[string]string{"app/five.txt": "5 again\n"})
		code, stdout, _ := w.f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", again)
		if code != 1 || !strings.Contains(stdout, "already carries #5") {
			t.Errorf("fold = %d, %q; want a refusal", code, stdout)
		}
	})
	t.Run("never commits", func(t *testing.T) {
		w := setup(t)
		before := w.f.git("for-each-ref")
		sha := commit(w, w.head, "port\n\n(cherry picked from commit "+w.mc5+")", resolved)
		w.f.exec(w.gh, "fold", "--line", "release-1.1", "--original", "5", "--commit", sha)
		if after := w.f.git("for-each-ref"); after != before || w.f.remoteHead(batchBranch1) != w.head {
			t.Errorf("fold moved a ref:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}
