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

// pushed builds and pushes a batch for #5 and returns its head.
func pushed(t *testing.T, f *fixture) string {
	t.Helper()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	if code, _, stderr := f.exec(&fakeGH{}, "run", "--line", "release-1.1", "--push"); code != 0 {
		t.Fatalf("run --push = %d\n%s", code, stderr)
	}
	head := f.git("rev-parse", "HEAD")
	f.git("switch", "-q", "main")
	return head
}

// --onto continues the pushed head and nothing else: it keeps what the batch
// carries and picks only what is new.
func TestOntoContinues(t *testing.T) {
	f := newFixture(t)
	f.lines()
	head := pushed(t, f)
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	code, stdout, stderr := f.exec(&fakeGH{}, "run", "--line", "release-1.1", "--onto", head, "--date", "2026-10-29")
	if code != 2 || !strings.Contains(stderr, "not the head of backport-batch/release-1.1-20261029") {
		t.Errorf("--onto with another date = %d (%s), want a refusal", code, stderr)
	}
	f.now = f.now.AddDate(0, 0, 3)
	code, stdout, stderr = f.exec(&fakeGH{}, "run", "--line", "release-1.1", "--onto", head)
	if code != 0 {
		t.Fatalf("run --onto = %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "title: fix(backport): batch for release-1.1, 2026-10-26\n") {
		t.Errorf("the continued batch is not titled for the date it opened on:\n%s", stderr)
	}
	if got := f.subjects(head, "HEAD"); !slices.Equal(got, []string{"fix(app): six (#6)"}) {
		t.Errorf("picked %q on top of the batch, want only #6", got)
	}
	if !isAncestor(head, f.git("rev-parse", "HEAD")) {
		t.Error("the new head does not descend from --onto")
	}
	if listed, _, _ := strings.Cut(stdout, "### Skipped"); !strings.Contains(listed, "[#5]") || !strings.Contains(listed, "[#6]") {
		t.Errorf("the body does not list what the batch carries and what it picked:\n%s", stdout)
	}
}

// A head that is not the one the remote has now is refused, so work pushed
// since cannot be dropped.
func TestOntoRefusesAStaleHead(t *testing.T) {
	f := newFixture(t)
	f.lines()
	head := pushed(t, f)
	f.git("switch", "-q", "--detach", head)
	f.write("app/fold.txt", "folded\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "fold")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")

	code, _, stderr := f.exec(&fakeGH{}, "run", "--line", "release-1.1", "--onto", head)
	if code != 2 || !strings.Contains(stderr, "is not the head of any batch branch") {
		t.Errorf("run --onto a stale head = %d (%s), want a refusal", code, stderr)
	}
	for _, bad := range []string{head[:12], "-" + head[1:], strings.ToUpper(head), "HEAD", "--help"} {
		if code, _, _ := f.exec(&fakeGH{}, "run", "--line", "release-1.1", "--onto", bad); code != 2 {
			t.Errorf("--onto %q = %d, want 2", bad, code)
		}
	}
}

// A rebuilt branch that does not descend from what the remote has is never
// pushed over it.
func TestPushRefusesANonFastForward(t *testing.T) {
	f := newFixture(t)
	f.lines()
	head := pushed(t, f)
	f.git("switch", "-q", "--detach", head)
	f.write("app/fold.txt", "folded\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "fold")
	folded := f.git("rev-parse", "HEAD")
	f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})

	code, _, stderr := f.exec(&fakeGH{}, "run", "--line", "release-1.1", "--push")
	// git would refuse too, but only after trying; the tool refuses first.
	if code != 2 || !strings.Contains(stderr, "refusing a non-fast-forward push") {
		t.Errorf("a fresh rebuild pushed over the batch = %d (%s), want a refusal", code, stderr)
	}
	if got := f.remoteHead(batchBranch1); got != folded {
		t.Errorf("the remote batch moved to %.12s, want it left at %.12s", got, folded)
	}
}
