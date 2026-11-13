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
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// Output that never reached its reader fails the command, and stderr says why.
func TestUnwritableOutputFailsTheCommand(t *testing.T) {
	var stderr strings.Builder
	a := &app{gh: &fakeGH{}, stdout: failingWriter{}, stderr: &stderr}
	if code := a.main([]string{"help"}); code != 2 {
		t.Fatalf("help with an unwritable stdout = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Errorf("stderr does not name the write failure:\n%s", stderr.String())
	}
}

// A command that already failed keeps its own exit code when its output is
// lost as well: check still says something is missing.
func TestUnwritableOutputKeepsTheCommandsFailure(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	var stderr strings.Builder
	a := &app{gh: &fakeGH{remoteHead: f.remoteHead}, stdout: failingWriter{}, stderr: &stderr, now: func() time.Time { return f.now }, tool: f.tool}
	if code := a.main([]string{"check", "--line", "release-1.1"}); code != 1 {
		t.Fatalf("check with #6 missing and an unwritable stdout = %d, want 1\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Errorf("stderr does not name the write failure:\n%s", stderr.String())
	}
}

// A closed pipe on stdout does not kill the run before it can say what
// already happened: the write fails like any other, and the run exits 2.
func TestClosedStdoutPipeIsReported(t *testing.T) {
	if os.Getenv("BACKPORT_BATCH_CLOSED_STDOUT_HELPER") == "1" {
		os.Args = []string{"backport-batch", "help"}
		main()
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	t.Cleanup(func() { _ = w.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestClosedStdoutPipeIsReported$")
	cmd.Env = append(os.Environ(), "BACKPORT_BATCH_CLOSED_STDOUT_HELPER=1")
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = w, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("help into a closed pipe = %v, want exit 2\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "output was lost") {
		t.Errorf("stderr does not say the output was lost:\n%s", stderr.String())
	}
}

// failOn fails every write that contains want, and takes every other one.
type failOn struct {
	want string
	buf  strings.Builder
}

func (w *failOn) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.want) {
		return 0, errors.New("broken pipe")
	}
	return w.buf.Write(p)
}

func runWith(f *fixture, gh *fakeGH, stdout, stderr io.Writer, args ...string) int {
	f.t.Helper()
	if gh.remoteHead == nil {
		gh.remoteHead = f.remoteHead
	}
	a := &app{gh: gh, stdout: stdout, stderr: stderr, now: func() time.Time { return f.now }, tool: f.tool}
	return a.main(args)
}

// Output is written before the push; when it is lost, on either stream,
// nothing is pushed and no pull request is opened.
func TestLostOutputPushesNothing(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			f := newFixture(t)
			f.lines()
			f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
			gh := &fakeGH{}
			var stdout, stderr io.Writer = &strings.Builder{}, &strings.Builder{}
			if stream == "stdout" {
				stdout = failingWriter{}
			} else {
				stderr = failingWriter{}
			}
			if code := runWith(f, gh, stdout, stderr, "run", "--line", "release-1.1", "--push"); code != 2 {
				t.Fatalf("run --push with a failing %s = %d, want 2", stream, code)
			}
			if head := f.remoteHead(batchBranch1); head != "" {
				t.Errorf("the batch was pushed at %.12s", head)
			}
			if len(gh.prs) != 0 {
				t.Errorf("a pull request was opened: %+v", gh.prs)
			}
		})
	}
}

// A confirmation lost after the push fails the run: the pull request was
// opened, and the run does not exit 0 without having said so.
func TestLostConfirmationFailsTheRun(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	if code := runWith(f, gh, &strings.Builder{}, &failOn{want: "opened draft"}, "run", "--line", "release-1.1", "--push"); code != 2 {
		t.Fatalf("run --push losing its confirmation = %d, want 2", code)
	}
	if len(gh.prs) != 1 {
		t.Errorf("%d pull requests, want the one the run opened", len(gh.prs))
	}
	if len(gh.issueComments) != 0 {
		t.Errorf("the report was posted after the confirmation was lost: %+v", gh.issueComments)
	}
}

// notify writes what it found before it comments; when that is lost, it
// does not comment.
func TestLostOutputCommentsNothing(t *testing.T) {
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}}
	var stderr strings.Builder
	a := &app{gh: gh, stdout: failingWriter{}, stderr: &stderr, now: func() time.Time { return time.Date(2026, 10, 26, 6, 23, 0, 0, time.UTC) }}
	if code := a.main([]string{"notify", "--line", "release-1.1", "--push"}); code != 2 {
		t.Fatalf("notify --push with a failing stdout = %d, want 2\n%s", code, stderr.String())
	}
	if len(gh.issueComments) != 0 {
		t.Errorf("notify commented: %+v", gh.issueComments)
	}
}

// writesGitHub records the GitHub writes that reach it.
type writesGitHub struct {
	github
	writes []string
}

func (g *writesGitHub) createPull(string, string, string, string) (int, string, error) {
	g.writes = append(g.writes, "createPull")
	return 1, "", nil
}
func (g *writesGitHub) editPull(int, string, string) error {
	g.writes = append(g.writes, "editPull")
	return nil
}
func (g *writesGitHub) editBody(int, string) error {
	g.writes = append(g.writes, "editBody")
	return nil
}
func (g *writesGitHub) closePull(int) error {
	g.writes = append(g.writes, "closePull")
	return nil
}
func (g *writesGitHub) createComment(int, string) error {
	g.writes = append(g.writes, "createComment")
	return nil
}
func (g *writesGitHub) editComment(int64, string) error {
	g.writes = append(g.writes, "editComment")
	return nil
}

// Every GitHub write is refused once output was lost. Every method of
// github is either a read or in this table, so a write added later without
// a guard fails here.
func TestEveryGitHubWriteIsGuarded(t *testing.T) {
	reads := map[string]bool{"openPulls": true, "recentPulls": true, "permission": true, "labelled": true, "pull": true, "comments": true, "batchBot": true}
	inner := &writesGitHub{}
	g := guardedGitHub{github: inner, a: &app{unwritten: errors.New("broken pipe")}}
	writes := map[string]func() error{
		"createPull":    func() error { _, _, err := g.createPull("b", "h", "t", "f"); return err },
		"editPull":      func() error { return g.editPull(1, "t", "f") },
		"editBody":      func() error { return g.editBody(1, "f") },
		"closePull":     func() error { return g.closePull(1) },
		"createComment": func() error { return g.createComment(1, "f") },
		"editComment":   func() error { return g.editComment(1, "f") },
	}
	iface := reflect.TypeFor[github]()
	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if _, ok := writes[name]; !ok && !reads[name] {
			t.Errorf("github.%s is neither a known read nor a guarded write", name)
		}
	}
	for name, write := range writes {
		if err := write(); err == nil || !strings.Contains(err.Error(), "output was lost") {
			t.Errorf("%s with output lost = %v, want a refusal", name, err)
		}
	}
	if len(inner.writes) != 0 {
		t.Errorf("writes reached GitHub after output was lost: %q", inner.writes)
	}
}

// A warning lost while the open batch is being updated stops the update:
// nothing is pushed, posted or edited.
func TestLostWarningLeavesTheOpenBatchAlone(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--push")
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	bad := issueComment{ID: 8, Body: reportMarker + "names none\n"}
	bad.User.Login, bad.User.Type = "maintainer", "User"
	gh.issueComments = append(gh.issueComments, bad)
	head, comments, body := f.remoteHead(batchBranch1), len(gh.issueComments), gh.find(90).Body
	f.now = f.now.AddDate(0, 0, 1)
	if code := runWith(f, gh, &strings.Builder{}, &failOn{want: "does not count as a report"}, "run", "--line", "release-1.1", "--continue", "--push"); code != 2 {
		t.Fatalf("run --continue --push losing a warning = %d, want 2", code)
	}
	if got := f.remoteHead(batchBranch1); got != head {
		t.Errorf("the batch moved to %.12s", got)
	}
	if len(gh.issueComments) != comments {
		t.Errorf("a comment was posted: %d comments, want %d", len(gh.issueComments), comments)
	}
	if gh.find(90).Body != body {
		t.Error("the batch pull request was edited")
	}
}

// A warning lost while a batch is being closed leaves it open, with no
// closing record half written.
func TestLostWarningLeavesTheBatchUnclosed(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	mustRun(t, f, gh, "run", "--line", "release-1.1", "--push")
	bad := issueComment{ID: 8, Body: reportMarker + "names none\n"}
	bad.User.Login, bad.User.Type = "maintainer", "User"
	gh.issueComments = append(gh.issueComments, bad)
	body := gh.find(90).Body
	if code := runWith(f, gh, &strings.Builder{}, &failOn{want: "does not count as a report"}, "run", "--line", "release-1.1", "--defer", "5", "--reason", "x", "--push"); code != 2 {
		t.Fatalf("run --defer losing a warning = %d, want 2", code)
	}
	if gh.find(90).Body != body {
		t.Error("the batch pull request was edited")
	}
	if !gh.isOpen(90) {
		t.Error("the batch pull request was closed")
	}
}

// The git and gh children keep the default SIGPIPE: catching it here must not
// leave it ignored in them, as signal.Ignore would through exec.
func TestChildrenKeepTheDefaultSIGPIPE(t *testing.T) {
	catchSIGPIPE()
	out, err := exec.Command("sh", "-c", "kill -PIPE $$; echo survived").Output()
	if err == nil || strings.Contains(string(out), "survived") {
		t.Fatalf("a child survived SIGPIPE: %q, %v", out, err)
	}
}

// The branch a close left is not deleted once output was lost: a deletion
// is a push like any other.
func TestLostOutputDeletesNoBranch(t *testing.T) {
	f, gh, h1 := closeWorld(t)
	f.git("switch", "-q", "main")
	l, err := parseLine("release-1.1")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{gh: gh, unwritten: errors.New("broken pipe")}
	if err := a.deleteBranch(l, closing{PR: 90, Branch: batchBranch1, Head: h1}); err == nil || !strings.Contains(err.Error(), "output was lost") {
		t.Fatalf("deleteBranch with output lost = %v, want a refusal", err)
	}
	if got := f.remoteHead(batchBranch1); got != h1 {
		t.Errorf("the branch is at %q, want it left at %.12s", got, h1)
	}
}

// A park line lost on stdout stops notify before it writes: no comment, and
// the record is not marked as announced.
func TestLostParkLineWritesNothing(t *testing.T) {
	park := parkedEntry{Original: 5, Merge: strings.Repeat("c", 40), Kind: parkDependency, Needs: []int{3}}
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261026", parkedBody(park))}}
	body := gh.prs[0].Body
	var stderr strings.Builder
	a := &app{gh: gh, stdout: &failOn{want: "parked originals"}, stderr: &stderr, now: func() time.Time { return time.Date(2026, 10, 26, 6, 23, 0, 0, time.UTC) }}
	if code := a.main([]string{"notify", "--line", "release-1.1", "--push"}); code != 2 {
		t.Fatalf("notify --push losing the park line = %d, want 2\n%s", code, stderr.String())
	}
	if len(gh.issueComments) != 0 {
		t.Errorf("notify commented: %+v", gh.issueComments)
	}
	if gh.prs[0].Body != body {
		t.Error("notify marked the park as announced")
	}
}

// The hint naming the commits a reset drops is the only way back to them, so
// the local branch is not reset when the hint is lost.
func TestLostResetHintKeepsTheBranch(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.git("switch", "-q", "-C", batchBranch1, "origin/release-1.1")
	f.write("app/mine.txt", "unpushed\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "an unpushed resolution")
	tip := f.git("rev-parse", "HEAD")
	f.git("switch", "-q", "main")
	if code := runWith(f, &fakeGH{}, &strings.Builder{}, &failOn{want: "resetting the local"}, "run", "--line", "release-1.1"); code != 2 {
		t.Fatalf("run losing the reset hint = %d, want 2", code)
	}
	if got := f.git("rev-parse", batchBranch1); got != tip {
		t.Errorf("the local %s moved to %.12s, want it kept at %.12s", batchBranch1, got, tip)
	}
}

// Once output is lost the run moves no local branch, even one a reset would
// only fast-forward.
func TestLostOutputMovesNoLocalBranch(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.git("branch", "-f", batchBranch1, "origin/release-1.1^")
	before := f.git("rev-parse", batchBranch1)
	if code := runWith(f, &fakeGH{}, &strings.Builder{}, &failOn{want: "prepared"}, "run", "--line", "release-1.1"); code != 2 {
		t.Fatalf("run losing an early line = %d, want 2", code)
	}
	if got := f.git("rev-parse", batchBranch1); got != before {
		t.Errorf("the local %s moved to %.12s, want it kept at %.12s", batchBranch1, got, before)
	}
}

// No new unit starts once output is lost: neither a pick nor a replay touches
// the branch.
func TestLostOutputStartsNoUnit(t *testing.T) {
	f := newFixture(t)
	f.lines()
	before := f.git("rev-parse", "HEAD")
	a := &app{unwritten: errors.New("broken pipe")}
	if _, err := a.pickUnit(&verdict{}, nil); err == nil || !strings.Contains(err.Error(), "output was lost") {
		t.Errorf("pickUnit with output lost = %v, want a refusal", err)
	}
	if _, _, err := a.replayNext(unit{}); err == nil || !strings.Contains(err.Error(), "output was lost") {
		t.Errorf("replayNext with output lost = %v, want a refusal", err)
	}
	if got := f.git("rev-parse", "HEAD"); got != before {
		t.Errorf("HEAD moved to %.12s", got)
	}
}
