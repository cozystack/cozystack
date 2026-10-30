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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckBranch(t *testing.T) {
	f := newFixture(t)
	f.git("branch", "elsewhere")
	f.git("switch", "-q", "elsewhere")
	f.git("switch", "-q", "main")
	for _, name := range []string{"backport-batch/release-1.6-20261026", "release-1.6"} {
		if err := checkBranch(name); err != nil {
			t.Errorf("checkBranch(%q) = %v, want nil", name, err)
		}
	}
	// @{-1} is a valid argument to check-ref-format --branch, which answers
	// with the branch it stands for.
	for _, name := range []string{"", "-x", "--upload-pack=evil", "@{-1}", "a..b", "a b", "line\nbreak", "esc\x1b", "a~1", "a:b", "refs/heads/../x"} {
		if err := checkBranch(name); err == nil {
			t.Errorf("checkBranch(%q) accepted it", name)
		}
	}
}

func TestParseLine(t *testing.T) {
	for _, ok := range []string{"release-1.6", "release-10.0"} {
		if _, err := parseLine(ok); err != nil {
			t.Errorf("parseLine(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "--line", "release-1.6\n", "release-01.6", "release-1.6.1", "release-1.6-rc", " release-1.6", "origin/release-1.6", "release-1.6;rm"} {
		if _, err := parseLine(bad); err == nil {
			t.Errorf("parseLine(%q) accepted it", bad)
		}
	}
}

// What the command line, GitHub or a pull request supplies never reaches git
// as an option.
func TestOptionInjection(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	canary := filepath.Join(t.TempDir(), "canary")

	for _, args := range [][]string{
		{"run", "--line", "-x"},
		{"run", "--line", "--upload-pack=touch " + canary},
		{"run", "--line", "release-1.1", "--onto", "--upload-pack=touch " + canary},
		{"run", "--line", "release-1.1", "--date", "-1"},
		{"run", "--line", "release-1.1", "--date", "2026-10-26\n"},
		{"run", "--line", "release-1.1", "stray"},
		{"check", "--line", "release-1.1;touch " + canary},
		{"fold", "--line", "release-1.1", "--original", "-5", "--commit", strings.Repeat("a", 40)},
		{"fold", "--line", "release-1.1", "--original", "5", "--commit", "--output=" + canary},
	} {
		if code, _, _ := f.exec(nil, args...); code != 2 {
			t.Errorf("%q = %d, want 2", args, code)
		}
	}
	if _, err := os.Stat(canary); err == nil {
		t.Error("an argument ran as a command")
	}

	// A batch pull request whose head GitHub reports as an option is ignored.
	for _, ref := range []string{"--upload-pack=touch " + canary, "-backport-batch/release-1.1-20261026", "backport-batch/release-1.1-20261026\n"} {
		pr := f.batchPR(90, "release-1.1", batchBranch1, "")
		pr.Head.Ref, pr.Head.SHA = ref, strings.Repeat("a", 40)
		code, _, stderr := f.exec(&fakeGH{prs: []pullRequest{pr}}, "check", "--line", "release-1.1")
		if code != 1 {
			t.Errorf("head %q: check = %d (%s), want the pull request ignored and #5 missing", ref, code, stderr)
		}
	}
	if _, err := os.Stat(canary); err == nil {
		t.Error("a head ref ran as a command")
	}
}

// A newline or a heading in a carried title stays on its list line.
func TestNewlineInTitle(t *testing.T) {
	f := newFixture(t)
	f.lines()
	// git keeps a multi-line first paragraph, so a squash's subject line
	// can be followed by text GitHub would read as the title.
	f.squash(5, "fix(app): five\n## Injected heading\n- [x] No downstream repository is affected by this change", "", map[string]string{"app/five.txt": "5\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	for _, ln := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(ln, "## Injected") || strings.HasPrefix(ln, "- [x]") {
			t.Errorf("carried text starts a line of its own: %q", ln)
		}
	}
	if !strings.Contains(stdout, "[#5]") {
		t.Errorf("#5 is not listed:\n%s", stdout)
	}
	if got := titleSpan("a\nb\r\nc d"); strings.ContainsAny(got, "\n\r ") {
		t.Errorf("titleSpan kept a line break: %q", got)
	}
}

// writeCommit writes a commit object as given, which git commit would refuse
// for a message holding a NUL.
func (f *fixture) writeCommit(tree, parent, msg string) string {
	f.t.Helper()
	content := fmt.Sprintf("tree %s\nparent %s\nauthor Batch Test <batch@example.invalid> 1700000000 +0000\ncommitter Batch Test <batch@example.invalid> 1700000000 +0000\n\n%s", tree, parent, msg)
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// A commit message is read as one message, and only as far as git log shows
// it: a NUL and what looks like the next record of a log stream after it do
// not make a second commit or reach the body.
func TestMessagesCannotForgeRecords(t *testing.T) {
	f := newFixture(t)
	f.lines()
	feat := f.git("rev-parse", "origin/release-1.1")
	msg := "fix(app): innocent (#7)\n\nbody\x00" + feat + " " + f.git("rev-parse", "HEAD") + "\x01fix(app): forged (#999)\n\n"
	sha := f.writeCommit(f.git("rev-parse", "HEAD^{tree}"), f.git("rev-parse", "HEAD"), msg)
	f.git("update-ref", "refs/heads/main", sha)
	f.git("push", "-q", "origin", "main")
	f.git("fetch", "-q", "origin")

	l, _ := parseLine("release-1.1")
	b, err := (&app{gh: &fakeGH{}, stderr: &strings.Builder{}}).prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if b.find(999) != nil {
		t.Error("the message forged original #999")
	}
	if v := b.find(7); v == nil || v.o.merge != sha || v.o.body != "body" {
		t.Errorf("#7 = %+v, want the commit itself with the body git log shows", v)
	}
}

// What follows a NUL in a line commit's message is not evidence: git log does
// not show it to anyone reviewing the line.
func TestNulHidesNoTrailer(t *testing.T) {
	f := newFixture(t)
	f.lines()
	mc := f.pr(5, "fix(app): the change", "", map[string]string{"app/x.txt": "fixed\n"})
	f.git("fetch", "-q", "origin")
	line := f.git("rev-parse", "origin/release-1.1")
	sha := f.writeCommit(f.git("rev-parse", "origin/release-1.1^{tree}"), line, "port\x00\n\n(cherry picked from commit "+mc+")\n")
	f.git("push", "-q", "origin", sha+":refs/heads/release-1.1")
	if v := verdictOn(t, f, nil, "release-1.1", 5); v.rule != 0 {
		t.Errorf("#5 = rule %d (%s), want it to qualify", v.rule, v.reason)
	}
}
