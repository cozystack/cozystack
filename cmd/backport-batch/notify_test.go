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
	"errors"
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

// notify writes its comment only after GitHub says the batch is still open,
// so a batch closed while notify reads it is not written to, whether the
// comment would be new or an update of its own.
func TestNotifyWritesNothingOnABatchClosedMeanwhile(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "a new comment", true: "an update"}[update], func(t *testing.T) {
			gh := &fakeGH{prs: []pullRequest{notifyPR("20261023", recordBody(5))}}
			if update {
				ours := issueComment{ID: 7, Body: notifyMarker + "\nearlier"}
				ours.User.Login, ours.User.Type = "maintainer", "User"
				gh.issueComments = []issueComment{ours}
			}
			gh.after = map[string]func(){"comments": func() { gh.find(90).State = "closed" }}
			code, _, stderr := notifyRun(gh, "--push")
			if code != 2 || !strings.Contains(stderr, "batch pull request #90 is closed now; nothing is written to it") || len(gh.closedWrites) != 0 || update && gh.issueComments[0].Body != notifyMarker+"\nearlier" {
				t.Errorf("notify = %d, closed writes %q, comments %+v; want 2 and nothing written:\n%s", code, gh.closedWrites, gh.issueComments, stderr)
			}
		})
	}
}

// parkedBody is an open batch body whose version 3 record parks p.
func parkedBody(p ...parkedEntry) string {
	sha := strings.Repeat("a", 40)
	rec := lineRecord{Version: recordVersion, Line: "release-1.1", Report: strings.Repeat("b", 64),
		Built:    &builtFrom{Main: sha, Line: sha, Policy: sha, Batch: sha, At: "2026-10-26T06:00:00Z", Options: runOptions{Mode: modeContinue}},
		Deferred: []deferredEntry{}, Dropped: []droppedEntry{}, Stale: []int{}, Parked: p}
	return "batch\n\n" + recordBlock(rec) + "\n"
}

// notifyAt runs notify at a time of day, with gh faked.
func notifyAt(gh *fakeGH, at time.Time, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	a := &app{gh: gh, stdout: &stdout, stderr: &stderr, now: func() time.Time { return at }}
	code := a.main(append([]string{"notify", "--line", "release-1.1"}, args...))
	return code, stdout.String(), stderr.String()
}

// A park is announced once on the open batch: notify names it in its
// comment and marks it announced in the record; a later notify is silent
// about it. A mark that fails to land repeats the announcement once, the
// next day. With no batch open nothing is announced, and the first notify of
// the next batch does it.
func TestNotifyAnnouncesAParkOnce(t *testing.T) {
	park := parkedEntry{Original: 5, Merge: strings.Repeat("c", 40), Kind: parkDependency, Needs: []int{3}}
	parked := func(gh *fakeGH) []parkedEntry {
		t.Helper()
		rec, err := parseRecord(gh.prs[0].Body, notifyLine)
		if err != nil {
			t.Fatal(err)
		}
		return rec.Parked
	}
	gh := &fakeGH{prs: []pullRequest{notifyPR("20261026", parkedBody(park))}}
	code, stdout, stderr := notifyRun(gh, "--push")
	if code != 0 || !strings.Contains(stdout, "release-1.1: 1 parked originals on #90, named once") || len(gh.issueComments) != 1 || !strings.Contains(gh.issueComments[0].Body, "- [#5](https://github.com/cozystack/cozystack/pull/5): builds on [#3](") {
		t.Fatalf("notify = %d, want #5 announced:\n%s\n%s\n%v", code, stdout, stderr, gh.issueComments)
	}
	if p := parked(gh); len(p) != 1 || !p[0].Announced {
		t.Errorf("the record parks %+v, want #5 marked announced", p)
	}
	if code, stdout, _ = notifyRun(gh, "--push"); code != 0 || strings.Contains(stdout, "parked") || strings.Contains(gh.issueComments[0].Body, "[#5]") {
		t.Errorf("notify again = %d, want nothing about #5:\n%s\n%s", code, stdout, gh.issueComments[0].Body)
	}

	gh = &fakeGH{prs: []pullRequest{notifyPR("20261026", parkedBody(park))}, fail: map[string]error{"editBody": errors.New("HTTP 502")}}
	if code, _, _ := notifyRun(gh, "--push"); code != 2 || len(gh.issueComments) != 1 || parked(gh)[0].Announced {
		t.Fatalf("notify with a failing mark = %d, want 2 after the comment, the record unmarked", code)
	}
	code, _, stderr = notifyAt(gh, time.Date(2026, 10, 27, 0, 5, 0, 0, time.UTC), "--push")
	if code != 0 || !strings.Contains(gh.issueComments[0].Body, "[#5](") || !parked(gh)[0].Announced {
		t.Errorf("notify the next day = %d, want #5 named once more and marked:\n%s\n%s", code, stderr, gh.issueComments[0].Body)
	}

	// Closed, or rewritten by a run, between the comment and the mark: the
	// mark is not written, and the next notify names #5 again.
	gh = &fakeGH{prs: []pullRequest{notifyPR("20261026", parkedBody(park))}}
	gh.after = map[string]func(){"createComment": func() { gh.prs[0].State = "closed" }}
	if code, _, _ := notifyRun(gh, "--push"); code != 2 || len(gh.closedWrites) != 0 || parked(gh)[0].Announced {
		t.Errorf("notify on a batch closed meanwhile = %d, writes %q; want 2 and no mark", code, gh.closedWrites)
	}
	gh = &fakeGH{prs: []pullRequest{notifyPR("20261026", parkedBody(park))}}
	rewritten := parkedBody(park, parkedEntry{Original: 6, Merge: strings.Repeat("d", 40), Kind: parkPartial, Files: []string{"x"}})
	gh.after = map[string]func(){"createComment": func() { gh.prs[0].Body = rewritten }}
	if code, _, _ := notifyRun(gh, "--push"); code != 2 || gh.prs[0].Body != rewritten || len(gh.bodies) != 0 {
		t.Errorf("notify on a body rewritten meanwhile = %d; want 2 and the new body left as it is", code)
	}

	// A body someone edited on the web comes back with CRLF line ends; the
	// mark lands all the same.
	gh = &fakeGH{prs: []pullRequest{notifyPR("20261026", strings.ReplaceAll(parkedBody(park), "\n", "\r\n"))}}
	if code, _, stderr := notifyRun(gh, "--push"); code != 0 || !parked(gh)[0].Announced {
		t.Errorf("notify on a CRLF body = %d, want #5 marked:\n%s", code, stderr)
	}

	closed := notifyPR("20261026", parkedBody(park))
	at := "2026-10-26T12:00:00Z"
	closed.State, closed.MergedAt = "closed", &at
	gh = &fakeGH{prs: []pullRequest{closed}}
	if code, stdout, _ := notifyRun(gh, "--push"); code != 0 || stdout != "" || len(gh.issueComments) != 0 || len(gh.bodies) != 0 {
		t.Errorf("notify with no batch open = %d, %q, %d comments, %d edits; want nothing", code, stdout, len(gh.issueComments), len(gh.bodies))
	}
	next := notifyPR("20261027", parkedBody(park))
	next.Number = 91
	gh.prs = append(gh.prs, next)
	if code, _, _ := notifyRun(gh, "--push"); code != 0 || len(gh.issueComments) != 1 || !strings.Contains(gh.issueComments[0].Body, "[#5](") {
		t.Errorf("the next batch's notify = %d, want #5 announced on it: %v", code, gh.issueComments)
	}
}

// A park carries its announcement into the next record only while it is
// the same park: the same merge, kind, prerequisites, commits and files.
func TestParkEventIdentity(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	was := parkedEntry{Original: 5, Merge: a, Kind: parkDependency, Commits: []string{a}, Announced: true}
	carrier := &lineRecord{Parked: []parkedEntry{was}}
	for name, tc := range map[string]struct {
		now       parkedEntry
		announced bool
	}{
		"the same park":             {parkedEntry{Original: 5, Merge: a, Kind: parkDependency, Commits: []string{a}}, true},
		"another unnumbered commit": {parkedEntry{Original: 5, Merge: a, Kind: parkDependency, Commits: []string{b}}, false},
		"another kind":              {parkedEntry{Original: 5, Merge: a, Kind: parkPartial, Files: []string{"x"}}, false},
		"another prerequisite":      {parkedEntry{Original: 5, Merge: a, Kind: parkDependency, Commits: []string{a}, Needs: []int{3}}, false},
		"another merge":             {parkedEntry{Original: 5, Merge: b, Kind: parkDependency, Commits: []string{a}}, false},
		"another original":          {parkedEntry{Original: 6, Merge: a, Kind: parkDependency, Commits: []string{a}}, false},
	} {
		got := carryAnnounced([]parkedEntry{tc.now}, carrier)
		if got[0].Announced != tc.announced {
			t.Errorf("%s: announced = %v, want %v", name, got[0].Announced, tc.announced)
		}
	}
	if got := carryAnnounced([]parkedEntry{was}, nil); got[0].Announced {
		t.Error("a park with no carrier came out announced")
	}
	unannounced := was
	unannounced.Announced = false
	if got := carryAnnounced([]parkedEntry{was}, &lineRecord{Parked: []parkedEntry{unannounced}}); got[0].Announced {
		t.Error("a park the carrier holds unannounced came out announced")
	}
}

// A park keeps its announcement through a continuation and into the next
// batch, so notify does not name it again.
func TestRolloverKeepsTheAnnouncement(t *testing.T) {
	f := featWorld(t)
	gh := &fakeGH{}
	openBatch(t, f, gh)
	if code, _, stderr := f.exec(gh, "notify", "--line", "release-1.1", "--push"); code != 0 || !strings.Contains(notifyComment(gh), "[#5](") {
		t.Fatalf("notify = %d, want #5 announced:\n%s", code, stderr)
	}
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 0 || !strings.Contains(stdout, `"needs":[3],"announced":true`) {
		t.Fatalf("run --continue = %d, want #5's park still announced:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	reopen(f, gh)
	mergeBatch(f, gh)
	f.pr(8, "fix(app): eight", "", map[string]string{"app/eight.txt": "8\n"})
	gh.nextPR = 91
	f.now = f.now.Add(24 * time.Hour)
	if code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push"); code != 0 || !strings.Contains(stdout, `"needs":[3],"announced":true`) {
		t.Fatalf("the next batch's run = %d, want #5's park announced from the merged batch:\n%s\n%s", code, stdout, stderr)
	}
	f.git("switch", "-q", "main")
	gh.issueComments = nil
	if code, stdout, _ := f.exec(gh, "notify", "--line", "release-1.1", "--push"); code != 0 || strings.Contains(stdout, "parked") || strings.Contains(notifyComment(gh), "[#5](") {
		t.Errorf("notify on the next batch = %d, want nothing about #5:\n%s\n%s", code, stdout, notifyComment(gh))
	}
}

// notifyComment is the notify comment on the fake, or "".
func notifyComment(gh *fakeGH) string {
	for _, c := range slices.Backward(gh.issueComments) {
		if strings.HasPrefix(c.Body, notifyMarker) {
			return c.Body
		}
	}
	return ""
}
