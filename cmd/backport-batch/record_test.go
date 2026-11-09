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
	"strings"
	"testing"
)

// The records the live batches carried before records had a version, as
// GitHub returned them on 2026-11-09, read as version 1.
func TestRecordReadsVersion1(t *testing.T) {
	for line, block := range map[string]string{
		"release-1.7": `{"line":"release-1.7","deferred":[{"original":4756,"commit":"647407545d630291cfe2a20880cce45af22edf0c","files":["packages/system/backupstrategy-controller/values.yaml"]},{"original":4867,"commit":"813c6180861efe1896c26d62a7afcbc1ff590ebc","files":[".github/renovate.json"]},{"original":5082,"commit":"e93db8848027450c2e23d21348409efa641cb6f1","files":["pkg/registry/core/tap/rest.go"]},{"original":4888,"files":[".github/workflows/cut-prerelease.yaml"],"workflows":true},{"original":4971,"files":[".github/workflows/e2e-proxmox.yaml"],"workflows":true}]}`,
		"release-1.6": `{"line":"release-1.6","deferred":[{"original":5071,"commit":"3fa534f3cf6cb5774a183f0202f7f5c13e9ae211","files":["internal/operator/tapmaterializer_reconciler.go","internal/operator/tapmaterializer_reconciler_test.go"]},{"original":5086,"commit":"5265ee662c591db409c8ef1ed8bb457cf29f5c22","files":["packages/apps/tenant/templates/gateway.yaml"]},{"original":5087,"commit":"8d8179f20a2c10de03face1472435d5083cde38d","files":["internal/controller/applicationdefinition_helmreconciler.go","internal/controller/applicationdefinition_helmreconciler_test.go"]},{"original":5142,"files":[".github/workflows/pull-requests-release.yaml",".github/workflows/update-releasenotes.yaml"],"workflows":true}]}`,
	} {
		l, _ := parseLine(line)
		rec, err := parseRecord("## What this PR does\n\ntext\n\n"+deferredFence+"\n"+block+"\n```\n\n### Release note\n", l)
		if err != nil {
			t.Fatalf("%s: parseRecord = %v", line, err)
		}
		waits, conflicts := 0, 0
		for _, e := range rec.Deferred {
			switch {
			case e.Workflows:
				waits++
			case e.Commit != "":
				conflicts++
			}
		}
		if rec.Version != 1 || rec.Line != line || rec.Closing != nil || len(rec.Dropped) != 0 || waits+conflicts != len(rec.Deferred) || waits == 0 || conflicts == 0 {
			t.Errorf("%s: record = %+v; want version 1 with its conflicts and waits", line, rec)
		}
	}
}

// v2 is a version 2 record with each value it may hold.
func v2(extra string) string {
	sha := strings.Repeat("a", 40)
	return `{"version":2,"line":"release-1.1","built":{"main":"` + sha + `","line":"` + sha + `","newer":"","policy":"` + sha + `","batch":"` + sha + `","tool":"` + sha + `+dirty","run":"","at":"2026-10-26T06:00:00Z","options":{"noWorkflowFiles":true,"mode":"continue"}},` +
		`"report":"` + strings.Repeat("b", 64) + `","closing":null,"deferred":[{"original":5,"commit":"` + sha + `","files":["app/five.txt"]},{"original":6,"files":[],"reason":"fails the e2e"},{"original":7,"files":[".github/workflows/x.yaml"],"workflows":true}],` +
		`"dropped":[{"original":8,"merge":"` + sha + `","reason":"needs #4","on":90,"date":"2026-10-26"}],"stale":[9]` + extra + `}`
}

// A record is read whole or not at all: anything it cannot vouch for stops
// the command rather than being half believed.
func TestRecordRefuses(t *testing.T) {
	l, _ := parseLine("release-1.1")
	block := func(json string) string { return "text\n\n" + deferredFence + "\n" + json + "\n```\n" }
	if rec, err := parseRecord(block(v2("")), l); err != nil || rec.Version != 2 || len(rec.Dropped) != 1 || rec.Built.Options.Mode != "continue" {
		t.Fatalf("parseRecord of a whole v2 record = %+v, %v", rec, err)
	}
	sha := strings.Repeat("a", 40)
	for name, json := range map[string]string{
		"version 4":                   strings.Replace(v2(""), `"version":2`, `"version":4`, 1),
		"trailing data":               v2("") + `{}`,
		"trailing text":               v2("") + ` x`,
		"an original listed twice":    strings.Replace(v2(""), `"original":8`, `"original":5`, 1),
		"a needs entry with a commit": strings.Replace(v2(""), `{"original":6,"files":[],"reason":"fails the e2e"}`, `{"original":6,"commit":"`+sha+`","files":[],"needs":[4]}`, 1),
		"an empty reason":             strings.Replace(v2(""), `"reason":"fails the e2e"`, `"reason":""`, 1),
		"an empty drop reason":        strings.Replace(v2(""), `"reason":"needs #4"`, `"reason":" "`, 1),
		"a bad tool":                  strings.Replace(v2(""), `+dirty"`, `-dirty"`, 1),
		"an unknown mode":             strings.Replace(v2(""), `"mode":"continue"`, `"mode":"rebase"`, 1),
		"a reason with a commit":      strings.Replace(v2(""), `"reason":"fails the e2e"`, `"reason":"x","commit":"`+sha+`"`, 1),
		"a short merge":               strings.Replace(v2(""), `"merge":"`+sha+`"`, `"merge":"abc"`, 1),
		"a bad date":                  strings.Replace(v2(""), `"date":"2026-10-26"`, `"date":"26.10.2026"`, 1),
		"a bad time":                  strings.Replace(v2(""), `"at":"2026-10-26T06:00:00Z"`, `"at":"yesterday"`, 1),
		"a short report":              strings.Replace(v2(""), strings.Repeat("b", 64), "bbbb", 1),
		"a closing on another line":   strings.Replace(v2(""), `"closing":null`, `"closing":{"pr":90,"branch":"backport-batch/release-1.0-20261026","head":"`+sha+`"}`, 1),
		"a closing without a head":    strings.Replace(v2(""), `"closing":null`, `"closing":{"pr":90,"branch":"`+batchBranch1+`","head":""}`, 1),
		"a negative stale":            strings.Replace(v2(""), `"stale":[9]`, `"stale":[-9]`, 1),
		"an unknown key":              v2(`,"parked":[]`),
		"an entry of no kind":         strings.Replace(v2(""), `{"original":6,"files":[],"reason":"fails the e2e"}`, `{"original":6,"files":[]}`, 1),
		"another line":                strings.Replace(v2(""), `"line":"release-1.1"`, `"line":"release-1.0"`, 1),
	} {
		if _, err := parseRecord(block(json), l); err == nil {
			t.Errorf("%s: parseRecord accepted it", name)
		}
	}
	closing := strings.Replace(v2(""), `"closing":null`, `"closing":{"pr":90,"branch":"`+batchBranch1+`","head":"`+sha+`"}`, 1)
	if rec, err := parseRecord(block(closing), l); err != nil || rec.Closing == nil || rec.Closing.PR != 90 {
		t.Errorf("a record that is closing = %+v, %v", rec, err)
	}
}

// recordPR is a batch pull request on release-1.1 carrying body, numbered n,
// in state, opened by login.
func recordPR(n int, state, login, body string) pullRequest {
	pr := notifyPR("20261026", body)
	pr.Number, pr.State = n, state
	pr.Head.Ref = fmt.Sprintf("backport-batch/release-1.1-202610%02d", n%30+1)
	pr.User.Login = login
	if strings.HasSuffix(login, "[bot]") {
		pr.User.Type = "Bot"
	}
	return pr
}

func recordBody(conflict int) string {
	return "text\n\n" + deferredFence + "\n" + fmt.Sprintf(`{"line":"release-1.1","deferred":[{"original":%d,"commit":"%s","files":["a"]}]}`, conflict, strings.Repeat("a", 40)) + "\n```\n"
}

// The record is the block of the newest batch pull request that carries one,
// whatever its state, among those a trusted author opened from a batch branch
// of this repository; anything that leaves the newest record in doubt stops
// the command rather than falling back to an older one.
func TestFindLineRecord(t *testing.T) {
	const bot = defaultBatchBot
	other := func(n int) pullRequest {
		pr := recordPR(n, "closed", "maintainer", "")
		pr.Head.Ref = fmt.Sprintf("fix-%d", n)
		return pr
	}
	fork := recordPR(92, "closed", "maintainer", recordBody(92))
	fork.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{"someone/cozystack"}
	cases := []struct {
		name    string
		prs     []pullRequest
		open    int // the open batch the caller found, or 0
		bot     string
		carrier int // 0 for none
		isOpen  bool
		refusal string
		pages   int
	}{
		{name: "the open batch first", prs: []pullRequest{recordPR(92, "open", "maintainer", recordBody(92)), recordPR(91, "closed", "maintainer", recordBody(91))}, open: 92, carrier: 92, isOpen: true},
		{name: "the newest closed record", prs: []pullRequest{recordPR(91, "closed", bot, recordBody(91)), recordPR(90, "closed", bot, recordBody(90))}, bot: bot, carrier: 91},
		{name: "a person's batch without a block is passed over", prs: []pullRequest{recordPR(92, "closed", "maintainer", "picked by hand"), recordPR(91, "closed", "maintainer", recordBody(91))}, carrier: 91},
		{name: "the bot's batch without a block", prs: []pullRequest{recordPR(92, "closed", bot, "edited away"), recordPR(91, "closed", bot, recordBody(91))}, bot: bot, refusal: "carries no ```backport-batch block"},
		{name: "a fork is no batch", prs: []pullRequest{fork, recordPR(91, "closed", "maintainer", recordBody(91))}, carrier: 91},
		{name: "a newer record by someone untrusted", prs: []pullRequest{recordPR(92, "closed", "someone", recordBody(92)), recordPR(91, "closed", "maintainer", recordBody(91))}, refusal: "#92"},
		{name: "a closed record newer than the open batch", prs: []pullRequest{recordPR(92, "closed", "maintainer", recordBody(92)), recordPR(91, "open", "maintainer", recordBody(91))}, open: 91, refusal: "newer than the open batch pull request #91"},
		{name: "a record on the second page", prs: append(func() (out []pullRequest) {
			for n := 1000; n < 1000+recentPullsPage; n++ {
				out = append(out, other(n))
			}
			return out
		}(), recordPR(91, "closed", "maintainer", recordBody(91))), carrier: 91, pages: 2},
		{name: "ten full pages without a record", prs: func() (out []pullRequest) {
			for n := 1000; n < 1000+10*recentPullsPage+1; n++ {
				out = append(out, other(n))
			}
			return out
		}(), refusal: "pull requests", pages: 10},
		{name: "the bot's record when no bot is trusted", prs: []pullRequest{recordPR(91, "closed", bot, recordBody(91))}, refusal: "#91"},
		{name: "a hand-written open batch beside a closed record", prs: []pullRequest{recordPR(92, "open", "maintainer", "picked by hand"), recordPR(91, "closed", "maintainer", recordBody(91))}, open: 92, carrier: 91},
		{name: "no batch ever", prs: []pullRequest{other(5)}},
		{name: "an unreadable record", prs: []pullRequest{recordPR(91, "closed", "maintainer", "text\n\n"+deferredFence+"\n{\n```\n")}, refusal: "#91 (closed)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := parseLine("release-1.1")
			gh := &fakeGH{prs: tc.prs, bot: tc.bot}
			var open *pullRequest
			for i := range tc.prs {
				if tc.prs[i].Number == tc.open {
					open = &tc.prs[i]
				}
			}
			src, err := findLineRecord(gh, l, open)
			switch {
			case tc.refusal != "":
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Errorf("findLineRecord = %+v, %v; want a refusal with %q", src, err, tc.refusal)
				}
			case err != nil:
				t.Errorf("findLineRecord: %v", err)
			case tc.carrier == 0 && (src.carrier != nil || src.rec != nil):
				t.Errorf("found #%d, want no record", src.carrier.Number)
			case tc.carrier != 0 && (src.carrier == nil || src.carrier.Number != tc.carrier || src.rec == nil || src.open != tc.isOpen || src.rec.Deferred[0].Original != tc.carrier):
				t.Errorf("found %+v, want #%d open=%v", src, tc.carrier, tc.isOpen)
			}
			if tc.pages != 0 && gh.listed != tc.pages {
				t.Errorf("read %d pages, want %d", gh.listed, tc.pages)
			}
			if tc.open != 0 && src.code != open {
				t.Errorf("the batch to build on is %+v, want #%d", src.code, tc.open)
			}
		})
	}
}

// A run that picks nothing while no batch is open writes nothing, since
// merged and closed batches are never written: it says its classification is
// not kept, and check still reads the merged batch's record.
func TestZeroPickRunLeavesNoRecord(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.onLine("release-1.1", "fix(app): a line-only five", map[string]string{"app/five.txt": "line\n", "app/seven.txt": "line\n"})
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	mergeBatch(f, gh)
	f.pr(7, "fix(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
	edits, comments := len(gh.edited), len(gh.issueComments)

	code, _, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue", "--push")
	if code != 0 || !strings.Contains(stderr, "no batch pull request is open on release-1.1; this run's classification is not retained") {
		t.Errorf("run = %d, want 0 saying nothing is retained:\n%s", code, stderr)
	}
	if len(gh.created) != 1 || len(gh.edited) != edits || len(gh.issueComments) != comments || f.remoteHead(batchBranch1) != "" {
		t.Errorf("created %q, edited %q, %d comments, branch at %q; want nothing written", gh.created, gh.edited, len(gh.issueComments), f.remoteHead(batchBranch1))
	}
	f.git("switch", "-q", "main")
	code, stdout, stderr := f.exec(gh, "check", "--line", "release-1.1")
	if code != 1 || !strings.Contains(stdout, "/pull/7 ") || strings.Contains(stdout, "/pull/5 ") || !strings.Contains(stderr, "deferred conflict on #90 (merged)") {
		t.Errorf("check = %d, want 1 for #7 alone and #5 still a deferred conflict:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// The open batch's author is asked for once although both the open batch and
// its record are read.
func TestRecordAsksForAPermissionOnce(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	gh := &fakeGH{}
	openBatch(t, f, gh)
	gh.asked = nil
	if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 0 {
		t.Fatalf("check = %d\n%s", code, stderr)
	}
	if len(gh.asked) != 1 {
		t.Errorf("asked GitHub for %q, want the author once", gh.asked)
	}
}
