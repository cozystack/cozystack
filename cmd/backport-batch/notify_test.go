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
	"bytes"
	"strings"
	"testing"
	"time"
)

// notifyPR is an open batch pull request for release-1.1 on the branch opened
// on date, YYYYMMDD, with body.
func notifyPR(date, body string) pullRequest {
	var pr pullRequest
	pr.Number, pr.Body = 90, body
	pr.User.Login, pr.User.Type = "maintainer", "User"
	pr.Head.Ref, pr.Head.SHA = "backport-batch/release-1.1-"+date, strings.Repeat("a", 40)
	pr.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{upstreamRepo}
	pr.Base.Ref, pr.Base.Repo.FullName = "release-1.1", upstreamRepo
	return pr
}

// notifyRun runs notify on 2026-10-26 with gh faked.
func notifyRun(gh *fakeGH, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	a := &app{gh: gh, stdout: &stdout, stderr: &stderr, now: func() time.Time { return time.Date(2026, 10, 26, 6, 23, 0, 0, time.UTC) }}
	code := a.main(append([]string{"notify", "--line", "release-1.1"}, args...))
	return code, stdout.String(), stderr.String()
}

var notifyLine, _ = parseLine("release-1.1")

// A batch open since before the previous run gets one comment, mentioning
// the team, and keeps that one comment however many runs see it.
func TestNotifyStalled(t *testing.T) {
	for _, tc := range []struct {
		date    string
		stalled bool
	}{
		{"20261026", false},
		{"20261024", false},
		{"20261023", true},
		{"20261019", true},
	} {
		gh := &fakeGH{prs: []pullRequest{notifyPR(tc.date, "")}}
		code, stdout, stderr := notifyRun(gh, "--mention", "cozystack/maintainers", "--push")
		if code != 0 {
			t.Fatalf("%s: notify = %d\n%s", tc.date, code, stderr)
		}
		if got := strings.Contains(stdout, "release-1.1: batch pull request #90 has been open since"); got != tc.stalled {
			t.Errorf("%s: stalled = %v, want %v:\n%s", tc.date, got, tc.stalled, stdout)
		}
		if got := len(gh.issueComments); got != map[bool]int{true: 1, false: 0}[tc.stalled] {
			t.Errorf("%s: %d comments, want one only when stalled", tc.date, got)
		}
		if !tc.stalled {
			continue
		}
		c := gh.issueComments[0].Body
		if !strings.HasPrefix(c, notifyMarker+"\n@cozystack/maintainers\n") || !strings.Contains(c, "open since") {
			t.Errorf("%s: the comment does not mention the team and say why:\n%s", tc.date, c)
		}
		for range 2 {
			if code, _, stderr := notifyRun(gh, "--mention", "@cozystack/maintainers", "--push"); code != 0 {
				t.Fatalf("notify again = %d\n%s", code, stderr)
			}
		}
		if len(gh.issueComments) != 1 {
			t.Errorf("%s: %d comments after three runs, want the one updated", tc.date, len(gh.issueComments))
		}
	}

	gh := &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}}
	if code, _, _ := notifyRun(gh, "--push"); code != 0 || strings.Contains(gh.issueComments[0].Body, "@") {
		t.Errorf("notify with no team = %d and wrote %q; want no mention", code, gh.issueComments[0].Body)
	}
	gh = &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}}
	if code, stdout, _ := notifyRun(gh); code != 0 || stdout == "" || len(gh.issueComments) != 0 {
		t.Errorf("notify without --push = %d, %q and %d comments; want the line and no comment", code, stdout, len(gh.issueComments))
	}
}

// In manual mode the comment lists the deferred conflicts for a maintainer,
// and nothing --defer held out; in ci mode each conflict goes to the agent
// with the agent's own token.
func TestNotifyConflicts(t *testing.T) {
	conflict := pick{v: &verdict{o: &original{number: 6}}, files: []string{"app/six.txt"}}
	held := pick{v: &verdict{o: &original{number: 7}}, reason: "fails the e2e"}
	body := "text\n\n" + deferredBlock(notifyLine, []pick{conflict}, []pick{held}) + "\n"

	gh := &fakeGH{prs: []pullRequest{notifyPR("20261026", body)}}
	code, stdout, stderr := notifyRun(gh, "--mention", "@cozystack/maintainers", "--push")
	if code != 0 || !strings.Contains(stdout, "release-1.1: 1 deferred conflicts on #90 wait for a maintainer") {
		t.Fatalf("notify = %d, %q\n%s", code, stdout, stderr)
	}
	if len(gh.issueComments) != 1 || len(gh.tasks) != 0 {
		t.Fatalf("%d comments and %d agent tasks, want one comment", len(gh.issueComments), len(gh.tasks))
	}
	c := gh.issueComments[0].Body
	if !strings.Contains(c, "@cozystack/maintainers") || !strings.Contains(c, "[#6](") || !strings.Contains(c, "resolve-conflict.md") || strings.Contains(c, "#7") {
		t.Errorf("the comment does not list #6 alone for a maintainer:\n%s", c)
	}

	t.Setenv("BACKPORT_AGENT_TOKEN", "")
	gh = &fakeGH{prs: []pullRequest{notifyPR("20261026", body)}}
	if code, _, stderr := notifyRun(gh, "--agent", "ci", "--push"); code != 2 || !strings.Contains(stderr, "BACKPORT_AGENT_TOKEN") {
		t.Errorf("--agent ci without a token = %d (%s), want a refusal", code, stderr)
	}
	t.Setenv("BACKPORT_AGENT_TOKEN", "agent-token")
	if code, stdout, _ := notifyRun(gh, "--agent", "ci"); code != 0 || len(gh.tasks) != 0 || !strings.Contains(stdout, "go to the CI agent") {
		t.Errorf("--agent ci without --push = %d, %q, started %q; want nothing started", code, stdout, gh.tasks)
	}
	if code, _, stderr := notifyRun(gh, "--agent", "ci", "--push"); code != 0 {
		t.Fatalf("--agent ci = %d\n%s", code, stderr)
	}
	if len(gh.tasks) != 1 || !strings.HasPrefix(gh.tasks[0], "agent-token release-1.1 backport-batch/release-1.1-20261026 ") ||
		!strings.Contains(gh.tasks[0], "#6") || !strings.Contains(gh.tasks[0], "resolve-conflict.md") || !strings.Contains(gh.tasks[0], batchTrailer+" trailer") || strings.Contains(gh.tasks[0], "#7") {
		t.Errorf("agent tasks = %q, want one for #6 on the batch branch", gh.tasks)
	}
	if len(gh.issueComments) != 0 {
		t.Errorf("ci mode wrote %d comments with nothing stalled", len(gh.issueComments))
	}
}

// Only a comment by an author the batch trusts is taken for the notify
// comment: a look-alike is neither edited nor counted.
func TestNotifyKeepsToItsOwnComment(t *testing.T) {
	look := issueComment{ID: 7, Body: notifyMarker + "\nplanted"}
	look.User.Login, look.User.Type = "someone", "User"
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}, issueComments: []issueComment{look}}
	if code, _, stderr := notifyRun(gh, "--push"); code != 0 {
		t.Fatalf("notify = %d\n%s", code, stderr)
	}
	if len(gh.issueComments) != 2 || gh.issueComments[0].Body != look.Body {
		t.Errorf("comments = %+v; want the look-alike untouched and one of ours", gh.issueComments)
	}

	// Ours says so once nothing waits any more, and calls nobody for it.
	gh.prs = []pullRequest{notifyPR("20261026", "")}
	if code, _, _ := notifyRun(gh, "--mention", "@cozystack/maintainers", "--push"); code != 0 || len(gh.issueComments) != 2 ||
		!strings.Contains(gh.issueComments[1].Body, "Nothing waits") || strings.Contains(gh.issueComments[1].Body, "@cozystack") {
		t.Errorf("comments = %+v; want ours updated to say nothing waits, mentioning nobody", gh.issueComments)
	}
}

func TestNotifyRefuses(t *testing.T) {
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}}
	for _, args := range [][]string{
		{"--agent", "copilot"},
		{"--mention", "@a b"},
		{"--mention", "@org/team/x"},
		{"--mention", "@-x"},
	} {
		if code, _, _ := notifyRun(gh, args...); code != 2 {
			t.Errorf("notify %q = %d, want 2", args, code)
		}
	}
	if code, stdout, _ := notifyRun(&fakeGH{}, "--push"); code != 0 || stdout != "" {
		t.Errorf("notify with no batch open = %d, %q; want nothing", code, stdout)
	}
	pr := notifyPR("20261019", "")
	pr.User.Login = "someone"
	if code, stdout, _ := notifyRun(&fakeGH{prs: []pullRequest{pr}}, "--push"); code != 0 || stdout != "" {
		t.Errorf("notify on a batch someone untrusted opened = %d, %q; want nothing", code, stdout)
	}
}

// lines prints the two newest lines, newest first, which is the order the
// scheduled run takes them in.
func TestLines(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.release("release-1.2")
	f.release("release-1.2.0-rc.1")
	code, stdout, stderr := f.exec(nil, "lines")
	if code != 0 || stdout != "release-1.2\nrelease-1.1\n" {
		t.Errorf("lines = %d, %q (%s); want release-1.2 then release-1.1", code, stdout, stderr)
	}
}

// A scheduled run with nothing to pick for a line succeeds without pushing or
// opening anything.
func TestRunWithNothingToPick(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "feat(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
	if code != 0 || !strings.Contains(stderr, "nothing to push") {
		t.Errorf("run with nothing to pick = %d, want 0:\n%s", code, stderr)
	}
	if f.remoteHead(batchBranch1) != "" || len(gh.created) != 0 {
		t.Errorf("pushed %q and opened %q with nothing picked", f.remoteHead(batchBranch1), gh.created)
	}
}
