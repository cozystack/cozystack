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
	"slices"
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

// The comment lists the deferred conflicts for a maintainer, and nothing
// --defer held out.
func TestNotifyConflicts(t *testing.T) {
	conflict := pick{v: &verdict{o: &original{number: 6}}, files: []string{"app/six.txt"}}
	held := pick{v: &verdict{o: &original{number: 7}}, reason: "fails the e2e"}
	body := "text\n\n" + deferredBlock(notifyLine, []pick{conflict}, []pick{held}, nil) + "\n"

	gh := &fakeGH{prs: []pullRequest{notifyPR("20261026", body)}}
	code, stdout, stderr := notifyRun(gh, "--mention", "@cozystack/maintainers", "--push")
	if code != 0 || !strings.Contains(stdout, "release-1.1: 1 deferred conflicts on #90 wait for a maintainer") {
		t.Fatalf("notify = %d, %q\n%s", code, stdout, stderr)
	}
	if len(gh.issueComments) != 1 {
		t.Fatalf("%d comments, want one", len(gh.issueComments))
	}
	c := gh.issueComments[0].Body
	if !strings.Contains(c, "@cozystack/maintainers") || !strings.Contains(c, "[#6](") || !strings.Contains(c, "resolve-conflict.md") || strings.Contains(c, "#7") {
		t.Errorf("the comment does not list #6 alone for a maintainer:\n%s", c)
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

// The scheduled batch writes as its bot: notify keeps to the comment that bot
// wrote, on a pull request it opened, and to no other bot's.
func TestNotifyFindsTheBotsComment(t *testing.T) {
	const bot = "cozystack-ci[bot]"
	ours := issueComment{ID: 7, Body: notifyMarker + "\nearlier"}
	ours.User.Login, ours.User.Type = bot, "Bot"
	other := issueComment{ID: 8, Body: notifyMarker + "\nplanted"}
	other.User.Login, other.User.Type = "renovate[bot]", "Bot"
	// The bot writes the record into every body it opens.
	pr := notifyPR("20261019", deferredBlock(notifyLine, nil, nil, nil))
	pr.User.Login, pr.User.Type = bot, "Bot"
	gh := &fakeGH{prs: []pullRequest{pr}, issueComments: []issueComment{ours, other}, bot: bot}
	if code, stdout, stderr := notifyRun(gh, "--push"); code != 0 || !strings.Contains(stdout, "has been open since") {
		t.Fatalf("notify = %d, %q\n%s", code, stdout, stderr)
	}
	if len(gh.issueComments) != 2 || !strings.Contains(gh.issueComments[0].Body, "has been open since") || gh.issueComments[1].Body != other.Body {
		t.Errorf("comments = %+v; want the bot's own comment edited and the other bot's untouched", gh.issueComments)
	}
	if code, _, stderr := notifyRun(&fakeGH{prs: []pullRequest{pr}, bot: "cozystack-ci"}, "--push"); code != 2 || !strings.Contains(stderr, batchBotEnv) {
		t.Errorf("a %s that is no bot login = %d (%s), want a refusal", batchBotEnv, code, stderr)
	}
}

// A permission GitHub does not answer fails notify before it writes: read as
// a refusal, it would pass over a person's batch, or over the comment notify
// keeps and post a second one.
func TestNotifyFailsOnAnUnreadablePermission(t *testing.T) {
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}, permErr: errString("HTTP 502")}
	if code, stdout, stderr := notifyRun(gh, "--push"); code != 2 || stdout != "" || len(gh.issueComments) != 0 || !strings.Contains(stderr, "HTTP 502") {
		t.Errorf("notify on a person's batch = %d, %q, %d comments (%s); want 2 and nothing written", code, stdout, len(gh.issueComments), stderr)
	}

	const bot = "cozystack-ci[bot]"
	pr := notifyPR("20261019", deferredBlock(notifyLine, nil, nil, nil))
	pr.User.Login, pr.User.Type = bot, "Bot"
	ours := issueComment{ID: 7, Body: notifyMarker + "\nearlier"}
	ours.User.Login, ours.User.Type = "maintainer", "User"
	gh = &fakeGH{prs: []pullRequest{pr}, issueComments: []issueComment{ours}, bot: bot, permErr: errString("HTTP 502")}
	if code, _, stderr := notifyRun(gh, "--push"); code != 2 || len(gh.issueComments) != 1 || gh.issueComments[0].Body != ours.Body || !strings.Contains(stderr, "HTTP 502") {
		t.Errorf("notify with a person's comment = %d, comments %+v (%s); want 2 and the comment untouched", code, gh.issueComments, stderr)
	}
}

func TestNotifyRefuses(t *testing.T) {
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261019", "")}}
	for _, args := range [][]string{
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

// notify reads the line's record the way the other commands do, and stays
// silent with no batch open, but a record it cannot read, or one that says
// its open batch is being closed, stops it.
func TestNotifyValidatesTheRecord(t *testing.T) {
	sha := strings.Repeat("a", 40)
	closingRecord := strings.Replace(v2(""), `"closing":null`, `"closing":{"pr":91,"branch":"backport-batch/release-1.1-20261002","head":"`+sha+`"}`, 1)
	block := func(json string) string { return "text\n\n" + deferredFence + "\n" + json + "\n```\n" }
	for _, tc := range []struct {
		name  string
		pr    pullRequest
		code  int
		quiet bool
	}{
		{"a clean closed record", recordPR(91, "closed", "maintainer", block(v2(""))), 0, true},
		{"a malformed record", recordPR(91, "closed", "maintainer", block(`{"line":`)), 2, false},
		{"a closed record that was closing", recordPR(91, "closed", "maintainer", block(closingRecord)), 0, true},
		{"an open record that is closing", recordPR(91, "open", "maintainer", block(closingRecord)), 2, false},
	} {
		gh := &fakeGH{prs: []pullRequest{tc.pr}}
		code, stdout, stderr := notifyRun(gh, "--push")
		if code != tc.code || tc.quiet && (stdout != "" || stderr != "") || len(gh.issueComments) != 0 {
			t.Errorf("%s: notify = %d, %q, %q, %d comments; want %d and nothing written", tc.name, code, stdout, stderr, len(gh.issueComments), tc.code)
		}
	}
}

// The maintainer's example: with 1.6 and 1.7 released, release-1.8 joins them
// when it branches, and 1.8.0 drops 1.6.
func TestSupportedLines(t *testing.T) {
	l := func(minor int) line { return line{1, minor} }
	cases := []struct {
		name     string
		all      []line
		shipped  []line
		released int
		want     []line
	}{
		{"two released", []line{l(5), l(6), l(7)}, []line{l(5), l(6), l(7)}, 2, []line{l(6), l(7)}},
		{"release-1.8 branched", []line{l(5), l(6), l(7), l(8)}, []line{l(5), l(6), l(7)}, 2, []line{l(6), l(7), l(8)}},
		{"1.8.0 shipped", []line{l(5), l(6), l(7), l(8)}, []line{l(5), l(6), l(7), l(8)}, 2, []line{l(7), l(8)}},
		{"three released", []line{l(5), l(6), l(7), l(8)}, []line{l(5), l(6), l(7)}, 3, []line{l(5), l(6), l(7), l(8)}},
		{"one released", []line{l(6), l(7), l(8)}, []line{l(6), l(7)}, 1, []line{l(7), l(8)}},
		{"a line between releases that never shipped", []line{l(5), l(6), l(7)}, []line{l(5), l(7)}, 2, []line{l(5), l(7)}},
		{"nothing shipped yet", []line{l(0), l(1)}, nil, 2, []line{l(0), l(1)}},
	}
	for _, tc := range cases {
		shipped := map[line]bool{}
		for _, s := range tc.shipped {
			shipped[s] = true
		}
		if got := supportedLines(tc.all, shipped, tc.released); !slices.Equal(got, tc.want) {
			t.Errorf("%s: supportedLines = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// lines prints the lines the batch runs for, newest first, which is the order
// the scheduled run takes them in: the newest released lines, as many as the
// policy says, and a line branched since, whose stable release drops the
// oldest.
func TestLines(t *testing.T) {
	f := newFixture(t)
	f.release("release-0.9")
	f.tag("v0.9.0")
	f.lines()
	f.release("release-1.2")
	f.release("release-1.2.0-rc.1")
	f.tag("v1.2.0-rc.1")
	lines := func() string {
		t.Helper()
		code, stdout, stderr := f.exec(nil, "lines")
		if code != 0 {
			t.Fatalf("lines = %d (%s)", code, stderr)
		}
		return stdout
	}
	if got := lines(); got != "release-1.2\nrelease-1.1\nrelease-1.0\n" {
		t.Errorf("with release-1.2 at its first rc, lines = %q; want it before the two released lines", got)
	}
	f.setPolicy(releasedKey + ": 3\n")
	if got := lines(); got != "release-1.2\nrelease-1.1\nrelease-1.0\nrelease-0.9\n" {
		t.Errorf("with three released lines, lines = %q; want release-0.9 too", got)
	}
	f.setPolicy("")
	f.tag("v1.2.0")
	if got := lines(); got != "release-1.2\nrelease-1.1\n" {
		t.Errorf("once v1.2.0 ships, lines = %q; want release-1.0 dropped", got)
	}
	f.setPolicy(releasedKey + ": 3\n")
	if got := lines(); got != "release-1.2\nrelease-1.1\nrelease-1.0\n" {
		t.Errorf("once v1.2.0 ships, with three released lines, lines = %q", got)
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
