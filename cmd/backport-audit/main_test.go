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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad timestamp %q: %v", s, err)
	}
	return when
}

func name(l *line) string {
	if l == nil {
		return "<none>"
	}
	return l.branch()
}

// The real dates, so a change to freezeContractLandedAt or to the era rules
// shows up here rather than on a release cut.
func liveTimeline(t *testing.T) []lineOpen {
	t.Helper()
	return []lineOpen{
		{line{1, 4}, ts(t, "2026-05-19T13:10:45Z")}, // v1.4.0 published; branch cut earlier, pre-contract
		{line{1, 5}, ts(t, "2026-06-22T15:45:38Z")}, // v1.5.0 published
		{line{1, 6}, ts(t, "2026-07-22T15:21:48Z")}, // v1.6.0 published
	}
}

func TestTargetsAt(t *testing.T) {
	live := liveTimeline(t)

	// A freeze after the cutover opens the line at the rc, not at a stable
	// it does not have yet.
	frozen17 := append(append([]lineOpen{}, live...), lineOpen{line{1, 7}, ts(t, "2026-09-10T09:00:00Z")})

	cases := []struct {
		name              string
		opened            []lineOpen
		merged            string
		current, previous string
	}{
		{
			name:   "before the cutover, the published-stable rule is in force",
			opened: live, merged: "2026-07-01T00:00:00Z",
			current: "release-1.5", previous: "release-1.4",
		},
		{
			name:   "after v1.6.0 publishes, still under the old rule",
			opened: live, merged: "2026-07-30T00:00:00Z",
			current: "release-1.6", previous: "release-1.5",
		},
		{
			name:   "after the cutover with no freeze since, the branch set is unchanged",
			opened: live, merged: "2026-09-01T00:00:00Z",
			current: "release-1.6", previous: "release-1.5",
		},
		{
			name:   "the instant before a 1.7 freeze still targets 1.6",
			opened: frozen17, merged: "2026-09-10T08:59:59Z",
			current: "release-1.6", previous: "release-1.5",
		},
		{
			name:   "once release-1.7 exists it is the target, before v1.7.0 ever publishes",
			opened: frozen17, merged: "2026-09-10T09:00:01Z",
			current: "release-1.7", previous: "release-1.6",
		},
		{
			name:   "only one line exists: no previous",
			opened: live[:1], merged: "2026-06-01T00:00:00Z",
			current: "release-1.4", previous: "<none>",
		},
		{
			name:   "merged before any line opened: no target at all",
			opened: live, merged: "2026-01-01T00:00:00Z",
			current: "<none>", previous: "<none>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current, previous := targetsAt(tc.opened, ts(t, tc.merged))
			if got := name(current); got != tc.current {
				t.Errorf("current = %s, want %s", got, tc.current)
			}
			if got := name(previous); got != tc.previous {
				t.Errorf("previous = %s, want %s", got, tc.previous)
			}
		})
	}
}

// A label is read against the lines at merge time however late it was added:
// the audit has no label timestamp. A PR merged on 2026-07-01 and labelled
// after v1.6.0 published is still a release-1.5 candidate, even though
// backport.yaml, running at label time, would have targeted release-1.6.
func TestCandidateLabel(t *testing.T) {
	live := liveTimeline(t)
	pr := func(merged string, labels ...string) *mainPR {
		p := &mainPR{MergedAt: ts(t, merged), labels: map[string]bool{}}
		for _, l := range labels {
			p.labels[l] = true
		}
		return p
	}
	cases := []struct {
		name string
		pr   *mainPR
		want map[line]string
	}{
		{
			name: "merged in the 1.5 era: kind/backport means release-1.5 for good",
			pr:   pr("2026-07-01T00:00:00Z", labelCurrent),
			want: map[line]string{{1, 4}: "", {1, 5}: labelCurrent, {1, 6}: ""},
		},
		{
			name: "merged in the 1.5 era: kind/backport-previous means release-1.4 for good",
			pr:   pr("2026-07-01T00:00:00Z", labelPrevious),
			want: map[line]string{{1, 4}: labelPrevious, {1, 5}: "", {1, 6}: ""},
		},
		{
			name: "merged in the 1.6 era, both requests",
			pr:   pr("2026-09-01T00:00:00Z", labelCurrent, labelPrevious),
			want: map[line]string{{1, 4}: "", {1, 5}: labelPrevious, {1, 6}: labelCurrent},
		},
		{
			name: "no backport label: a candidate nowhere",
			pr:   pr("2026-09-01T00:00:00Z"),
			want: map[line]string{{1, 4}: "", {1, 5}: "", {1, 6}: ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for want, label := range tc.want {
				if got := candidateLabel(tc.pr, live, want); got != label {
					t.Errorf("%s: candidateLabel = %q, want %q", want.branch(), got, label)
				}
			}
		})
	}
}

// backport.yaml sorts its branch list numerically descending, so the rank is by
// version and never by when a line opened. The two disagree as soon as a freeze
// overlaps the previous line's stabilisation, and 1.10-versus-1.9 is where a
// lexicographic sort would disagree with both.
func TestTargetsAtRanksByVersionNotByOpenTime(t *testing.T) {
	// v1.7.0-rc.1 is cut while v1.6.0 is still unpublished, so the newer line
	// opens first in time.
	opened := []lineOpen{
		{line{1, 7}, ts(t, "2026-09-10T09:00:00Z")},
		{line{1, 6}, ts(t, "2026-09-20T09:00:00Z")},
	}
	current, previous := targetsAt(opened, ts(t, "2026-09-21T00:00:00Z"))
	if got := name(current); got != "release-1.7" {
		t.Errorf("current = %s, want release-1.7", got)
	}
	if got := name(previous); got != "release-1.6" {
		t.Errorf("previous = %s, want release-1.6", got)
	}

	opened = []lineOpen{
		{line{1, 9}, ts(t, "2026-09-10T09:00:00Z")},
		{line{1, 10}, ts(t, "2026-10-10T09:00:00Z")},
	}
	current, previous = targetsAt(opened, ts(t, "2026-10-11T00:00:00Z"))
	if got := name(current); got != "release-1.10" {
		t.Errorf("current = %s, want release-1.10", got)
	}
	if got := name(previous); got != "release-1.9" {
		t.Errorf("previous = %s, want release-1.9", got)
	}
}

func TestParseFreezeDates(t *testing.T) {
	// Tag names and dates as `git for-each-ref` prints them.
	const out = `v1.4.0 2026-05-19T18:10:45+05:00
v1.4.0-rc.1 2026-05-13T19:10:58+05:00
v1.4.0-rc.2 2026-05-14T10:15:38+05:00
v1.6.0-rc.1 2026-07-07T17:46:06+05:00
v1.6.1-rc.1 2026-08-04T18:52:49+05:00
v1.7.0-rc.2 2026-09-12T12:00:00+05:00
v1.7.0-rc.1 2026-09-10T14:00:00+05:00
v1.8.0-beta.1 2026-10-01T12:00:00+05:00
v1.8.0-alpha.1 2026-10-02T12:00:00+05:00
`
	frozen, err := parseFreezeDates(out)
	if err != nil {
		t.Fatalf("parseFreezeDates: %v", err)
	}

	// 1.4 and 1.6 are pre-cutover: their branches were not created by a freeze,
	// so the rc tag says nothing about when they appeared.
	for _, l := range []line{{1, 4}, {1, 6}} {
		if when, ok := frozen[l]; ok {
			t.Errorf("%s: pre-cutover rc must not set a freeze date, got %s", l.branch(), when)
		}
	}
	// A patch-line rc is cut from a branch that already exists, and alpha/beta
	// do not freeze at all.
	for _, l := range []line{{1, 8}} {
		if _, ok := frozen[l]; ok {
			t.Errorf("%s: alpha/beta must not freeze a line", l.branch())
		}
	}
	// 1.7 freezes at rc.1, not at whichever rc the listing happens to print first.
	want := ts(t, "2026-09-10T14:00:00+05:00")
	got, ok := frozen[line{1, 7}]
	if !ok {
		t.Fatalf("release-1.7: expected a freeze date")
	}
	if !got.Equal(want) {
		t.Errorf("release-1.7 froze at %s, want %s", got, want)
	}
	if len(frozen) != 1 {
		t.Errorf("expected exactly one frozen line, got %d: %v", len(frozen), frozen)
	}
}

// The label listing goes through GitHub search, which stops at 1000 results
// whatever --limit says, so a limit above that must not let a listing cut at
// 1000 pass for a complete one.
func TestCandidateCeiling(t *testing.T) {
	cases := []struct {
		limit, got int
		truncated  bool
	}{
		{limit: 400, got: 399, truncated: false},
		{limit: 400, got: 400, truncated: true},
		{limit: 1000, got: 999, truncated: false},
		{limit: 1000, got: 1000, truncated: true},
		{limit: 2000, got: 999, truncated: false},
		{limit: 2000, got: 1000, truncated: true},
	}
	for _, tc := range cases {
		ceiling, remedy := candidateCeiling(tc.limit)
		err := truncated(tc.got, ceiling, "the listing", remedy)
		if (err != nil) != tc.truncated {
			t.Errorf("--limit %d, %d results: truncated = %v, want %v", tc.limit, tc.got, err != nil, tc.truncated)
		}
	}
}

// Bodies are the ones real backport PRs carry, trimmed, so a change to the
// grammar is judged against how people actually write them.
func TestBodyOrigins(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []int
	}{
		{
			name: "the plain form",
			body: "Backport of #3742 to `release-1.6`.\n\nCherry-picked clean with `-x`.",
			want: []int{3742},
		},
		{
			name: "two originals in one phrase, and an issue further on that is not one",
			body: "Backport of #3938 and #4280 to `release-1.6`, together, because #3938 alone breaks every Kafka deletion (#4276) and #4280 is what fixes that.",
			want: []int{3938, 4280},
		},
		{
			name: "a list without the serial comma",
			body: "Backport of #1, #2 and #3 to `release-1.6`.",
			want: []int{1, 2, 3},
		},
		{
			name: "a list with the serial comma",
			body: "Backport of #1, #2, and #3.",
			want: []int{1, 2, 3},
		},
		{
			name: "a list ending the line",
			body: "Backport of #1, #2 and #3\n\nAll cherry-picked with `-x`.",
			want: []int{1, 2, 3},
		},
		{
			name: "a list ending the body",
			body: "Backport of #1 and #2",
			want: []int{1, 2},
		},
		{
			name: "a list running on into a clause about its items keeps only the first",
			body: "Backport of #10, #20 is not included.",
			want: []int{10},
		},
		{
			name: "a list running into a parenthesis keeps only the first",
			body: "Backport of #10 and #20 (the second only in part).",
			want: []int{10},
		},
		{
			name: "a to that does not name a release line does not close a list",
			body: "Backport of #10, #20 to follow in a separate PR.",
			want: []int{10},
		},
		{
			name: "an unquoted release line closes a list, whichever line it names",
			body: "Backport of #1 and #2 to release-1.5, both clean.",
			want: []int{1, 2},
		},
		{
			name: "an unquoted release line before a full stop closes a list",
			body: "Backport of #1 and #2 to release-1.5.",
			want: []int{1, 2},
		},
		{
			name: "an unquoted release line ending the body closes a list",
			body: "Backport of #1 and #2 to release-1.5",
			want: []int{1, 2},
		},
		{
			name: "a branch that only starts with a release line does not close a list",
			body: "Backport of #10, #20 to release-1.6-fixes will follow later.",
			want: []int{10},
		},
		{
			name: "a patch version is not a release line",
			body: "Backport of #10, #20 to release-1.6.1 later.",
			want: []int{10},
		},
		{
			name: "a dotted suffix is not a release line",
			body: "Backport of #10, #20 to release-1.6.fixes will follow later.",
			want: []int{10},
		},
		{
			name: "a full stop and a space after the release line close a list",
			body: "Backport of #10, #20 to release-1.6. Next, the tests.",
			want: []int{10, 20},
		},
		{
			name: "a quoted release line running on into a word does not close a list",
			body: "Backport of #10, #20 to `release-1.6`-ish branches.",
			want: []int{10},
		},
		{
			name: "an ampersand is not a separator",
			body: "Backport of #1 & #2.",
			want: []int{1},
		},
		{
			name: "repository-qualified references, alone and in a list",
			body: "Backport of cozystack/cozystack#3471 and #3472 to `release-1.6` (clean cherry-pick).",
			want: []int{3471, 3472},
		},
		{
			name: "this repository's qualifier matches case-insensitively",
			body: "Backport of CozyStack/Cozystack#7.",
			want: []int{7},
		},
		{
			name: "another repository's reference in a list is skipped",
			body: "Backport of #10 and other/repo#20.",
			want: []int{10},
		},
		{
			name: "another repository's reference alone is skipped",
			body: "Backport of cozystack/website#20 to `release-1.6`.",
			want: nil,
		},
		{
			name: "another repository's reference first does not hide the rest of a closed list",
			body: "Backport of other/repo#20 and #10.",
			want: []int{10},
		},
		{
			name: "a dependency pulled along with together-with, as #4328 writes it",
			body: "Backport of #4253 to `release-1.6`, together with #3460 which it depends on. On 1.6 kube-ovn #99 differs.",
			want: []int{4253, 3460},
		},
		{
			name: "the same as #4377 writes it",
			body: "Backport of #4333 to `release-1.6`, together with #4231 which it depends on. 1.6 has no in-tree chart.",
			want: []int{4333, 4231},
		},
		{
			name: "the same as #4421 writes it",
			body: "## What this PR does\n\nManual backport of #3938 to `release-1.6`, together with #4280.\n\n#3938 cannot go to 1.6 on its own.",
			want: []int{3938, 4280},
		},
		{
			name: "together-with and an unquoted branch",
			body: "Manual backport of #3938 to release-1.6, together with #4280.",
			want: []int{3938, 4280},
		},
		{
			name: "a closed together-with list keeps every item",
			body: "Backport of #10 to `release-1.6`, together with #20 and #30.",
			want: []int{10, 20, 30},
		},
		{
			name: "a together-with list running on into a clause keeps only its first",
			body: "Backport of #10 to release-1.6, together with #20, #30 is tracked separately.",
			want: []int{10, 20},
		},
		{
			name: "a together-with list running into a to that is not a release line keeps only its first",
			body: "Backport of #10 to `release-1.6`, together with #20, #30 to follow later.",
			want: []int{10, 20},
		},
		{
			name: "a together-with list running into a branch that only starts with a release line keeps only its first",
			body: "Backport of #10 to `release-1.6`, together with #20, #30 to release-1.6-fixes later.",
			want: []int{10, 20},
		},
		{
			name: "a together-with list running into a patch version keeps only its first",
			body: "Backport of #10 to `release-1.6`, together with #20, #30 to release-1.6.1 later.",
			want: []int{10, 20},
		},
		{
			name: "a together-with list running into a dotted suffix keeps only its first",
			body: "Backport of #10 to `release-1.6`, together with #20, #30 to release-1.6.fixes later.",
			want: []int{10, 20},
		},
		{
			name: "a together-with list closed by a release line, a full stop and a space keeps every item",
			body: "Backport of #10 to `release-1.6`, together with #20, #30 to release-1.6. Next, the tests.",
			want: []int{10, 20, 30},
		},
		{
			name: "a together-with list running into a quoted release line and a word keeps only its first",
			body: "Backport of #10 to `release-1.6`, together with #20, #30 to `release-1.6`-ish branches.",
			want: []int{10, 20},
		},
		{
			name: "a together-with list closed by a quoted release line keeps every item",
			body: "Backport of #10 to `release-1.6`, together with #20 and #30 to `release-1.5` as well.",
			want: []int{10, 20, 30},
		},
		{
			name: "together-with in a later sentence is not part of the phrase",
			body: "Backport of #7 to `release-1.6`. Together with #8 it fixes the flake.",
			want: []int{7},
		},
		{
			name: "a hyphenated prefix still anchors",
			body: "Hand-backport of #3034 to `release-1.5`. Fixes #12 and #13.",
			want: []int{3034},
		},
		{
			name: "a list that turns into prose stops at the prose",
			body: "Backport of #1, which fixes #2, and #3.",
			want: []int{1},
		},
		{
			name: "and followed by prose stops at the prose",
			body: "Backport of #1 and the follow-up to #2.",
			want: []int{1},
		},
		{
			name: "every phrase in the body counts, each number once",
			body: "Backport of #10 to `release-1.6`.\n\nThe second commit is a backport of #11 and #10.",
			want: []int{10, 11},
		},
		{
			name: "an article between the phrase and the number is not a reference",
			body: "Hand-redone backport of the **kube-ovn-webhook half** of #3997.",
			want: nil,
		},
		{
			name: "no phrase at all",
			body: "Fixes #4276 on this branch.",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bodyOrigins(tc.body, "cozystack/cozystack"); !slices.Equal(got, tc.want) {
				t.Errorf("bodyOrigins = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClaimedOrigins(t *testing.T) {
	cases := []struct {
		name string
		pr   backportPR
		want []int
	}{
		{
			name: "a hand backport reusing the bot's head naming is counted once",
			pr:   backportPR{HeadRefName: "backport-3938-to-release-1.6", Body: "Backport of #3938 and #4280 to `release-1.6`."},
			want: []int{3938, 4280},
		},
		{
			name: "a head naming another branch says nothing about this one",
			pr:   backportPR{HeadRefName: "backport-3455-to-release-1.5"},
			want: nil,
		},
		{
			name: "a head off the bot's pattern leaves only the body",
			pr:   backportPR{HeadRefName: "backport-3938-release-1.6", Body: "Manual backport of #3938 to `release-1.6`, together with #4280."},
			want: []int{3938, 4280},
		},
		{
			name: "qualifiers are read against the repository the PR is in",
			pr: backportPR{URL: "https://github.com/example/fork/pull/5",
				Body: "Backport of example/fork#3 and cozystack/cozystack#4."},
			want: []int{3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimedOrigins(tc.pr, "release-1.6"); !slices.Equal(got, tc.want) {
				t.Errorf("claimedOrigins = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRepoOf(t *testing.T) {
	cases := map[string]string{
		"https://github.com/cozystack/cozystack/pull/4431": "cozystack/cozystack",
		"https://github.com/example/fork/pull/5":           "example/fork",
		"":                                                 upstreamRepo,
	}
	for url, want := range cases {
		if got := repoOf(url); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", url, got, want)
		}
	}
}

func bp(n int, state, head, body string) backportPR {
	return backportPR{
		Number:      n,
		State:       state,
		URL:         originURL("https://github.com/cozystack/cozystack/pull/1", n),
		HeadRefName: head,
		Body:        body,
	}
}

func numbersOf[T any](records []T, number func(T) int) []int {
	out := []int{}
	for _, r := range records {
		out = append(out, number(r))
	}
	return out
}

// Modelled on release-1.6: hand backports of main PRs that carry no backport
// label, one of them bundling two originals, next to a closed fork PR that had
// claimed the same pair, and a labelled candidate whose backport merged.
func release16(state4421, state4456 string) []backportPR {
	return []backportPR{
		bp(4431, "MERGED", "backport-3455-to-release-1.6", "Backport of #3455 to `release-1.6`."),
		bp(4437, "MERGED", "backport-4020-to-release-1.6", "Backport of #4020 to `release-1.6`, without its product changes."),
		bp(4456, state4456, "backport-3938-to-release-1.6",
			"Backport of #3938 and #4280 to `release-1.6`, together, because #3938 alone breaks every Kafka deletion (#4276)."),
		bp(4421, state4421, "backport-3938-release-1.6",
			"## What this PR does\n\nManual backport of #3938 to `release-1.6`, together with #4280."),
		bp(4474, "MERGED", "backport-4291-to-release-1.6", "Backport of #4291 to `release-1.6`."),
	}
}

func TestCrossCheck(t *testing.T) {
	labelled := &mainPR{Number: 4291, Title: "fix(cluster-api): live migration before host eviction",
		URL: "https://github.com/cozystack/cozystack/pull/4291", labels: map[string]bool{labelCurrent: true}}
	// Labelled, but for another line: it is not audited here, so a backport of
	// it on this branch is exactly as unaccounted for as an unlabelled one.
	elsewhere := &mainPR{Number: 3455, Title: "fix(e2e): disable guest fsync on ephemeral CI VM disks",
		URL: "https://github.com/cozystack/cozystack/pull/3455", labels: map[string]bool{labelPrevious: true}}
	cands := map[int]*mainPR{4291: labelled, 3455: elsewhere}
	audited := map[int]string{4291: labelCurrent}

	t.Run("merged unlabelled backports are listed, the candidate is not", func(t *testing.T) {
		index := indexByOrigin(release16("CLOSED", "MERGED"), "release-1.6")
		unlabelled, duplicates := crossCheck(index, audited, cands)

		got := numbersOf(unlabelled, func(u unlabelledBackport) int { return u.Number })
		if want := []int{3455, 3938, 4020, 4280}; !slices.Equal(got, want) {
			t.Fatalf("unlabelled originals = %v, want %v", got, want)
		}
		if len(duplicates) != 0 {
			t.Errorf("a closed PR next to a merged one is not a duplicate, got %+v", duplicates)
		}

		byNumber := map[int]unlabelledBackport{}
		for _, u := range unlabelled {
			byNumber[u.Number] = u
		}
		if u := byNumber[4280]; u.URL != "https://github.com/cozystack/cozystack/pull/4280" || u.Title != "" || len(u.Labels) != 0 {
			t.Errorf("#4280: want a derived URL, no title yet and no labels, got %+v", u)
		}
		if u := byNumber[3455]; u.Title != elsewhere.Title || !slices.Equal(u.Labels, []string{labelPrevious}) {
			t.Errorf("#3455: want the candidate's title and its label, got %+v", u)
		}
		if got := numbersOf(byNumber[3938].BackportPRs, func(l linkedPR) int { return l.Number }); !slices.Equal(got, []int{4421, 4456}) {
			t.Errorf("#3938: backport PRs = %v, want both claims sorted", got)
		}
	})

	t.Run("two open claims on the same pair are both duplicates", func(t *testing.T) {
		index := indexByOrigin(release16("OPEN", "OPEN"), "release-1.6")
		_, duplicates := crossCheck(index, audited, cands)
		got := numbersOf(duplicates, func(d duplicateBackport) int { return d.Number })
		if want := []int{3938, 4280}; !slices.Equal(got, want) {
			t.Fatalf("duplicate originals = %v, want %v", got, want)
		}
		for _, d := range duplicates {
			if d.Kind != dupOpenTwice || d.Label != "" {
				t.Errorf("#%d: want kind %s and no label, got %+v", d.Number, dupOpenTwice, d)
			}
		}
	})

	t.Run("an open claim after a merged one is a leftover", func(t *testing.T) {
		index := indexByOrigin(release16("OPEN", "MERGED"), "release-1.6")
		_, duplicates := crossCheck(index, audited, cands)
		got := numbersOf(duplicates, func(d duplicateBackport) int { return d.Number })
		if want := []int{3938, 4280}; !slices.Equal(got, want) {
			t.Fatalf("duplicate originals = %v, want %v", got, want)
		}
		for _, d := range duplicates {
			if d.Kind != dupOpenAfterMerge {
				t.Errorf("#%d: want kind %s, got %s", d.Number, dupOpenAfterMerge, d.Kind)
			}
		}
	})

	t.Run("a labelled candidate claimed twice is a duplicate carrying its label", func(t *testing.T) {
		prs := append(release16("CLOSED", "MERGED"),
			bp(4480, "OPEN", "fix-migration-again", "Backport of #4291 to `release-1.6`."))
		unlabelled, duplicates := crossCheck(indexByOrigin(prs, "release-1.6"), audited, cands)
		if len(duplicates) != 1 || duplicates[0].Number != 4291 || duplicates[0].Label != labelCurrent ||
			duplicates[0].Kind != dupOpenAfterMerge || duplicates[0].Title != labelled.Title {
			t.Fatalf("want one open-after-merge duplicate for #4291 under %s, got %+v", labelCurrent, duplicates)
		}
		for _, u := range unlabelled {
			if u.Number == 4291 {
				t.Errorf("an audited candidate must not also be listed as unlabelled")
			}
		}
	})
}

func TestDuplicateKind(t *testing.T) {
	cases := []struct {
		states []string
		want   string
	}{
		{[]string{"OPEN"}, ""},
		{[]string{"MERGED"}, ""},
		{[]string{"CLOSED", "OPEN"}, ""},
		{[]string{"MERGED", "MERGED"}, ""},
		{[]string{"CLOSED", "CLOSED", "MERGED"}, ""},
		{[]string{"OPEN", "OPEN"}, dupOpenTwice},
		{[]string{"OPEN", "CLOSED", "OPEN"}, dupOpenTwice},
		{[]string{"MERGED", "OPEN"}, dupOpenAfterMerge},
		{[]string{"OPEN", "MERGED", "OPEN"}, dupOpenAfterMerge},
	}
	for _, tc := range cases {
		var claims []backportPR
		for i, s := range tc.states {
			claims = append(claims, backportPR{Number: i + 1, State: s})
		}
		if got := duplicateKind(claims); got != tc.want {
			t.Errorf("duplicateKind(%v) = %q, want %q", tc.states, got, tc.want)
		}
	}
}

// The gate answers whether everything labelled landed. Unlabelled backports
// can only add to a branch, so they never fail it; a duplicate needs a human
// before the cut, so it always does.
func TestBranchReportClean(t *testing.T) {
	landed := []verdict{{Number: 1, Status: statusBackported}, {Number: 2, Status: statusInBranch}}
	unlabelled := []unlabelledBackport{{Number: 3455, BackportPRs: []linkedPR{{Number: 4431, State: "OPEN"}}}}
	duplicate := []duplicateBackport{{Number: 3938, Kind: dupOpenTwice}}

	cases := []struct {
		name string
		rep  branchReport
		want bool
	}{
		{"nothing at all", branchReport{}, true},
		{"everything landed", branchReport{Candidates: landed}, true},
		{"only unlabelled backports, even an open one", branchReport{Candidates: landed, Unlabelled: unlabelled}, true},
		{"a duplicate", branchReport{Candidates: landed, Duplicates: duplicate}, false},
		{"a missing candidate", branchReport{Candidates: append(landed, verdict{Number: 3, Status: statusMissing})}, false},
		{"a pending candidate", branchReport{Candidates: append(landed, verdict{Number: 3, Status: statusPending})}, false},
		{"a dropped candidate", branchReport{Candidates: append(landed, verdict{Number: 3, Status: statusDropped})}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rep.clean(); got != tc.want {
				t.Errorf("clean() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOriginURL(t *testing.T) {
	if got := originURL("https://github.com/cozystack/cozystack/pull/4431", 3455); got != "https://github.com/cozystack/cozystack/pull/3455" {
		t.Errorf("originURL = %q", got)
	}
	if got := originURL("not a PR URL", 3455); got != "" {
		t.Errorf("originURL of a non-PR URL = %q, want empty", got)
	}
}

// A real listing, of the PR merged in f1f383626: its third commit is an empty
// "re-trigger CI" commit, which the backport bot drops.
const prLogWithEmptyCommit = "\x00fea40d6314424a4105d31c7ee77f818062f5bc1a\x01fix(ci): only overlay current-main images on main-based PRs\n\n" +
	".github/workflows/pull-requests.yaml\nhack/overlay-main-images_test.bats\n" +
	"\x002330d6f3ca42306da92190b5e9dddf0bc1054fcd\x01fix(ci): overlay images from the PR base branch, and publish per-line artifacts\n\n" +
	".github/workflows/build-release.yaml\n.github/workflows/pull-requests.yaml\nhack/overlay-main-images_test.bats\n" +
	"\x00007d0b1a22ec1aadca95960645d8232a5b202fcb\x01chore(ci): re-trigger CI after a label event produced a no-op run\n"

func TestParsePRCommits(t *testing.T) {
	got := parsePRCommits(prLogWithEmptyCommit)
	want := []prCommit{
		{"fea40d6314424a4105d31c7ee77f818062f5bc1a", "fix(ci): only overlay current-main images on main-based PRs"},
		{"2330d6f3ca42306da92190b5e9dddf0bc1054fcd", "fix(ci): overlay images from the PR base branch, and publish per-line artifacts"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parsePRCommits = %+v, want %+v", got, want)
	}
}

// fakeSource serves a PR's commits from a fixture, or fails the read.
type fakeSource struct {
	commitsOf func(string) []prCommit
	err       error
}

func (f fakeSource) commits(mergeCommit string) ([]prCommit, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.commitsOf(mergeCommit), nil
}

// branchCommit is one commit on a release branch in a test's history.
type branchCommit struct {
	oid, subject string
	picks        []string
}

// branchOf is release-1.6 holding exactly these commits.
func branchOf(cs ...branchCommit) *branchHistory {
	h := &branchHistory{branch: "release-1.6", ref: "origin/release-1.6",
		commits: map[string]bool{}, subjects: map[string][]string{}, refs: map[string][]string{}}
	for _, c := range cs {
		h.commits[c.oid] = true
		h.subjects[c.subject] = append(h.subjects[c.subject], c.oid)
		if len(c.picks) > 0 {
			h.refs[c.oid] = c.picks
		}
	}
	return h
}

// history is a release branch with one commit per given subject, one per -x
// reference, and the given commits themselves.
func history(subjects, picked, reachable []string) *branchHistory {
	var cs []branchCommit
	for i, s := range subjects {
		cs = append(cs, branchCommit{oid: fmt.Sprintf("s%039d", i), subject: s})
	}
	for i, p := range picked {
		cs = append(cs, branchCommit{oid: fmt.Sprintf("p%039d", i), subject: "cherry-pick " + p, picks: []string{p}})
	}
	for _, r := range reachable {
		cs = append(cs, branchCommit{oid: r, subject: "reachable " + r})
	}
	return branchOf(cs...)
}

func comment(login, association, body, url string) prComment {
	c := prComment{AuthorAssociation: association, Body: body, URL: url}
	c.Author.Login = login
	return c
}

const (
	firstHalf  = "1111111111111111111111111111111111111111"
	secondHalf = "2222222222222222222222222222222222222222"
	emptyOne   = "3333333333333333333333333333333333333333"
	squashOne  = "4444444444444444444444444444444444444444"
	mergeOf    = "9999999999999999999999999999999999999999"
)

// A two-commit PR, #4399, plus an empty commit that must never count.
func twoHalves(string) []prCommit {
	return parsePRCommits("\x00" + firstHalf + "\x01fix(x): first half\n\nx.go\n" +
		"\x00" + secondHalf + "\x01fix(x): second half\n\ny.go\n" +
		"\x00" + emptyOne + "\x01chore(ci): re-trigger CI\n")
}

func pr4399() *mainPR {
	pr := &mainPR{Number: 4399, Title: "fix(x): two halves"}
	pr.MergeCommit.Oid = mergeOf
	return pr
}

// The bot's conflict drafts stop at the first commit that does not apply and
// drop the rest, so one merged draft reads as a finished backport to anything
// that looks at the PR, or at any single commit, rather than at every commit.
func TestClassifyJudgesEveryCommit(t *testing.T) {
	oneCommit := func(string) []prCommit {
		return []prCommit{{squashOne, "fix(y): the whole change (#4398)"}}
	}
	onlyEmpty := func(string) []prCommit {
		return parsePRCommits("\x00" + emptyOne + "\x01chore(ci): re-trigger CI\n")
	}
	bpMerged := backportPR{Number: 4400, State: "MERGED"}
	bpOpen := backportPR{Number: 4401, State: "OPEN", IsDraft: true}
	botSubject := "[Backport release-1.6] fix(x): two halves"

	cases := []struct {
		name      string
		hist      *branchHistory
		linked    []backportPR
		commitsOf func(string) []prCommit
		status    string
		evidence  string
		missing   []string
	}{
		{
			name:      "only the first commit carried, by -x reference: partial",
			hist:      history(nil, []string{firstHalf}, nil),
			commitsOf: twoHalves,
			status:    statusPartial, evidence: "1 of 2 commits on branch", missing: []string{secondHalf},
		},
		{
			name:      "both carried, one by -x reference and one by subject; the empty commit is not needed",
			hist:      history([]string{"fix(x): second half"}, []string{firstHalf}, nil),
			commitsOf: twoHalves,
			status:    statusBackported, evidence: "2 of 2 commits on branch",
		},
		{
			name:      "reworded on the branch, so only an abbreviated -x reference proves it, and the other is reachable",
			hist:      history(nil, []string{firstHalf[:7]}, []string{secondHalf}),
			commitsOf: twoHalves,
			status:    statusBackported, evidence: "2 of 2 commits on branch",
		},
		{
			name:      "a merged backport PR does not make a partial backport whole",
			hist:      history(nil, []string{firstHalf}, nil),
			linked:    []backportPR{bpMerged},
			commitsOf: twoHalves,
			status:    statusPartial, evidence: "backport PR #4400 merged, 1 of 2 commits on branch", missing: []string{secondHalf},
		},
		{
			name:      "partial with the rest still open is partial, and says so",
			hist:      history(nil, []string{firstHalf}, nil),
			linked:    []backportPR{bpOpen},
			commitsOf: twoHalves,
			status:    statusPartial, evidence: "1 of 2 commits on branch; backport PR #4401 open (draft/conflict)", missing: []string{secondHalf},
		},
		{
			name:      "a merged backport PR with none of several commits found proves only that something merged: unverified",
			hist:      history(nil, nil, nil),
			linked:    []backportPR{bpMerged},
			commitsOf: twoHalves,
			status:    statusUnverified, evidence: "backport PR #4400 merged, 0 of 2 commits on branch", missing: []string{firstHalf, secondHalf},
		},
		{
			name:      "the bot's merge subject alone is no better for several commits",
			hist:      history([]string{botSubject}, nil, nil),
			commitsOf: twoHalves,
			status:    statusUnverified, evidence: "bot merge commit on branch, 0 of 2 commits on branch", missing: []string{firstHalf, secondHalf},
		},
		{
			name:      "an -x reference to the merge commit credits no commit by itself",
			hist:      history(nil, []string{mergeOf[:12]}, nil),
			linked:    []backportPR{bpMerged},
			commitsOf: twoHalves,
			status:    statusUnverified, evidence: "backport PR #4400 merged, 0 of 2 commits on branch", missing: []string{firstHalf, secondHalf},
		},
		{
			name:      "nor does it lift a partial one",
			hist:      history(nil, []string{mergeOf[:12], firstHalf}, nil),
			commitsOf: twoHalves,
			status:    statusPartial, evidence: "1 of 2 commits on branch", missing: []string{secondHalf},
		},
		{
			name:      "no commit found and only an open backport PR: pending",
			hist:      history(nil, nil, nil),
			linked:    []backportPR{bpOpen},
			commitsOf: twoHalves,
			status:    statusPending, evidence: "backport PR #4401 open (draft/conflict)",
		},
		{
			name:      "a squashed PR's one commit carried: backported",
			hist:      history([]string{"fix(y): the whole change (#4398)"}, nil, nil),
			commitsOf: oneCommit,
			status:    statusBackported, evidence: "1 of 1 commit on branch",
		},
		{
			name:      "a squashed PR's commit not found but its backport PR merged: backported, as before",
			hist:      history(nil, nil, nil),
			linked:    []backportPR{bpMerged},
			commitsOf: oneCommit,
			status:    statusBackported, evidence: "backport PR #4400 merged, 0 of 1 commit on branch",
		},
		{
			name:      "and the bot's merge subject settles it the same way",
			hist:      history([]string{botSubject}, nil, nil),
			commitsOf: oneCommit,
			status:    statusBackported, evidence: "bot merge commit on branch, 0 of 1 commit on branch",
		},
		{
			name:      "nothing a backport has to carry: the merged backport PR decides",
			hist:      history(nil, nil, nil),
			linked:    []backportPR{bpMerged},
			commitsOf: onlyEmpty,
			status:    statusBackported, evidence: "backport PR #4400 merged",
		},
		{
			name:      "nothing anywhere: MISSING",
			hist:      history(nil, nil, nil),
			commitsOf: twoHalves,
			status:    statusMissing, evidence: "no backport PR, nothing on branch",
		},
		{
			name: "merged before the cut: in-branch, without reading its commits",
			hist: history(nil, nil, []string{mergeOf}),
			commitsOf: func(string) []prCommit {
				t.Fatalf("commits read for a PR whose merge commit is on the branch")
				return nil
			},
			status: statusInBranch, evidence: "merged before branch cut",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := classify(pr4399(), labelCurrent, tc.hist, tc.linked, fakeSource{commitsOf: tc.commitsOf})
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if v.Status != tc.status || v.Evidence != tc.evidence {
				t.Errorf("got %s %q, want %s %q", v.Status, v.Evidence, tc.status, tc.evidence)
			}
			var missing []string
			for _, c := range v.MissingCommits {
				missing = append(missing, c.Oid)
			}
			if !slices.Equal(missing, tc.missing) {
				t.Errorf("missing commits = %v, want %v", missing, tc.missing)
			}
			// Partial and unverified are outstanding, like MISSING, so they fail
			// the gate.
			rep := branchReport{Candidates: []verdict{v}}
			if want := v.Status == statusBackported || v.Status == statusInBranch; rep.clean() != want {
				t.Errorf("clean() = %v for a %s verdict, want %v", rep.clean(), v.Status, want)
			}
		})
	}
}

// Only a person's explicit word, on the backport PR that merged, lifts partial
// or unverified; nothing is inferred, and nothing else will do.
func TestClassifyConfirmation(t *testing.T) {
	const marker = "backport-audit: complete"
	const url = "https://github.com/cozystack/cozystack/pull/4400#issuecomment-1"
	onPR := func(state string, comments ...prComment) backportPR {
		return backportPR{Number: 4400, State: state, Comments: comments}
	}
	cases := []struct {
		name   string
		hist   *branchHistory
		linked []backportPR
		status string
		by     string
	}{
		{
			name:   "partial, attested by a person on the merged backport PR: confirmed",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("coderabbit-reviewer", "NONE", "looks fine", ""), comment("alice", "MEMBER", marker, url))},
			status: statusConfirmed, by: "alice",
		},
		{
			name:   "unverified, attested: confirmed",
			hist:   history(nil, nil, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "MEMBER", marker, url))},
			status: statusConfirmed, by: "alice",
		},
		{
			name:   "the marker from the backport bot is ignored",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("github-actions", "MEMBER", marker, url))},
			status: statusPartial,
		},
		{
			name:   "also under the bot's [bot] login",
			hist:   history(nil, nil, nil),
			linked: []backportPR{onPR("MERGED", comment("github-actions[bot]", "MEMBER", marker, url))},
			status: statusUnverified,
		},
		{
			name:   "an owner's attestation confirms",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "OWNER", marker, url))},
			status: statusConfirmed, by: "alice",
		},
		{
			name:   "a collaborator's attestation confirms",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "COLLABORATOR", marker, url))},
			status: statusConfirmed, by: "alice",
		},
		{
			name:   "a contributor's marker is ignored",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "CONTRIBUTOR", marker, url))},
			status: statusPartial,
		},
		{
			name:   "a first-time contributor's marker is ignored",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "FIRST_TIME_CONTRIBUTOR", marker, url))},
			status: statusPartial,
		},
		{
			name:   "a marker from someone with no association is ignored",
			hist:   history(nil, nil, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "NONE", marker, url))},
			status: statusUnverified,
		},
		{
			name:   "a review bot is ignored even as a member with a well-formed marker",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("coderabbitai", "MEMBER", marker, url))},
			status: statusPartial,
		},
		{
			name:   "the marker with an explanation above it attests nothing",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "MEMBER", "Squashed by the contributor; both commits are in.\n\n"+marker, url))},
			status: statusPartial,
		},
		{
			name:   "a member who only mentions the marker in a sentence attests nothing",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "MEMBER", "Do not post backport-audit: complete yet; the second commit is missing.", url))},
			status: statusPartial,
		},
		{
			name: "a maintainer's attestation counts even after a contributor's",
			hist: history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED",
				comment("bob", "CONTRIBUTOR", marker, "https://example.invalid/bob"), comment("alice", "MEMBER", marker, url))},
			status: statusConfirmed, by: "alice",
		},
		{
			name:   "the marker on an open backport PR is ignored",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("OPEN", comment("alice", "MEMBER", marker, url))},
			status: statusPartial,
		},
		{
			name:   "the marker on a backport PR closed unmerged is ignored, even next to a merged one",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED"), {Number: 4402, State: "CLOSED", Comments: []prComment{comment("alice", "MEMBER", marker, url)}}},
			status: statusPartial,
		},
		{
			name:   "anything short of the literal marker is not an attestation",
			hist:   history(nil, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "MEMBER", "backport-audit complete, I think", url))},
			status: statusPartial,
		},
		{
			name:   "complete commit evidence needs no attestation",
			hist:   history([]string{"fix(x): second half"}, []string{firstHalf}, nil),
			linked: []backportPR{onPR("MERGED", comment("alice", "MEMBER", marker, url))},
			status: statusBackported,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := classify(pr4399(), labelCurrent, tc.hist, tc.linked, fakeSource{commitsOf: twoHalves})
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if v.Status != tc.status {
				t.Fatalf("status = %s (%s), want %s", v.Status, v.Evidence, tc.status)
			}
			if tc.by == "" {
				if v.Confirmation != nil {
					t.Errorf("unexpected confirmation %+v", v.Confirmation)
				}
				return
			}
			want := confirmation{By: tc.by, URL: url, BackportPR: 4400}
			if v.Confirmation == nil || *v.Confirmation != want {
				t.Errorf("confirmation = %+v, want %+v", v.Confirmation, want)
			}
			if !strings.HasPrefix(v.Evidence, "confirmed by @"+tc.by+" on backport PR #4400; ") {
				t.Errorf("evidence %q does not name who confirmed it", v.Evidence)
			}
			if rep := (branchReport{Candidates: []verdict{v}}); !rep.clean() {
				t.Errorf("a confirmed candidate must not fail the gate")
			}
			if len(v.MissingCommits) == 0 {
				t.Errorf("a confirmed candidate keeps the commits nothing on the branch names")
			}
		})
	}
}

// A read of the PR's commits that fails must end the audit, not feed a verdict:
// with a merged backport PR linked, falling back to it would say backported on
// the strength of nothing.
func TestClassifyFailedReadIsAnError(t *testing.T) {
	linked := []backportPR{{Number: 4400, State: "MERGED"}}
	_, err := classify(pr4399(), labelCurrent, history(nil, nil, nil), linked,
		fakeSource{err: errors.New("fatal: bad object 9999999")})
	if err == nil || !strings.Contains(err.Error(), "bad object") || !strings.Contains(err.Error(), "#4399") {
		t.Fatalf("err = %v, want the failed read of #4399", err)
	}
}

func TestGitSourceCommits(t *testing.T) {
	const m = mergeOf
	boom := errors.New("fatal: bad object")
	cases := []struct {
		name      string
		merge     string
		parents   string
		parentErr error
		logErr    error
		wantRange string
		wantN     int
		wantErr   bool
	}{
		{name: "a merge commit: its second parent's side", merge: m, parents: m + " a b\n", wantRange: m + "^1.." + m + "^2", wantN: 2},
		{name: "a squashed PR: the commit itself", merge: m, parents: m + " a\n", wantRange: m + "^.." + m, wantN: 2},
		{name: "the merge commit is not in the repository", merge: m, parentErr: boom, wantErr: true},
		{name: "the log fails", merge: m, parents: m + " a b\n", logErr: boom, wantRange: m + "^1.." + m + "^2", wantErr: true},
		{name: "an octopus merge is not a PR merge", merge: m, parents: m + " a b c\n", wantErr: true},
		{name: "no merge commit recorded", merge: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRange := ""
			src := gitSource{run: func(args ...string) (string, error) {
				switch args[0] {
				case "rev-list":
					return tc.parents, tc.parentErr
				case "log":
					gotRange = args[len(args)-1]
					return prLogWithEmptyCommit, tc.logErr
				}
				return "", fmt.Errorf("unexpected git %v", args)
			}}
			commits, err := src.commits(tc.merge)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if gotRange != tc.wantRange {
				t.Errorf("log range = %q, want %q", gotRange, tc.wantRange)
			}
			if err != nil && commits != nil {
				t.Errorf("a failed read must not return commits, got %v", commits)
			}
			if !tc.wantErr && len(commits) != tc.wantN {
				t.Errorf("got %d commits, want %d", len(commits), tc.wantN)
			}
		})
	}
}

// An unlabelled original is never checked commit by commit, so the only thing
// that can make its merged backport read as confirmed is the same attestation.
func TestCrossCheckCarriesConfirmation(t *testing.T) {
	attested := bp(4456, "MERGED", "backport-3938-to-release-1.6", "Backport of #3938 to `release-1.6`.")
	attested.Comments = []prComment{comment("alice", "MEMBER", "backport-audit: complete", "https://example.invalid/c/1")}
	plain := bp(4431, "MERGED", "backport-3455-to-release-1.6", "Backport of #3455 to `release-1.6`.")
	unlabelled, _ := crossCheck(indexByOrigin([]backportPR{attested, plain}, "release-1.6"), nil, nil)
	got := map[int]*confirmation{}
	for _, u := range unlabelled {
		got[u.Number] = u.Confirmation
	}
	if c := got[3938]; c == nil || c.By != "alice" || c.BackportPR != 4456 {
		t.Errorf("#3938: confirmation = %+v, want alice on #4456", c)
	}
	if c := got[3455]; c != nil {
		t.Errorf("#3455: confirmation = %+v, want none", c)
	}
}

func TestSameCommit(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", full, full, true},
		{"the reference abbreviates", full, full[:7], true},
		{"the other side abbreviates", full[:7], full, true},
		{"different commits", full, "fff1234000000000000000000000000000000000", false},
		{"an empty name is nobody's commit", full, "", false},
		{"nor is it on the other side", "", full, false},
	}
	for _, tc := range cases {
		if got := sameCommit(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameCommit(%q, %q) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

// One branch commit is evidence for one original commit. Two commits that share
// a subject such as "fix tests" need two branch commits of that name, and a
// branch commit that has already said what it copies cannot stand in for
// another commit by its subject.
func TestMissingFrom(t *testing.T) {
	const (
		a     = "aaaa000000000000000000000000000000000000"
		b     = "bbbb000000000000000000000000000000000000"
		merge = "eeee000000000000000000000000000000000000"
		other = "ffff000000000000000000000000000000000000"
	)
	pr := []prCommit{{a, "fix tests"}, {b, "fix tests"}}
	cases := []struct {
		name    string
		hist    *branchHistory
		missing []string
	}{
		{
			name:    "one copy of a shared subject carries one commit, not both",
			hist:    branchOf(branchCommit{oid: "c1", subject: "fix tests"}),
			missing: []string{b},
		},
		{
			name:    "two copies carry both",
			hist:    branchOf(branchCommit{oid: "c1", subject: "fix tests"}, branchCommit{oid: "c2", subject: "fix tests"}),
			missing: nil,
		},
		{
			name:    "a copy that names the first commit with -x is spent on it, not on the second",
			hist:    branchOf(branchCommit{oid: "c1", subject: "fix tests", picks: []string{a[:9]}}),
			missing: []string{b},
		},
		{
			name:    "the first commit on the branch itself is spent on it, not on the second",
			hist:    branchOf(branchCommit{oid: a, subject: "fix tests"}),
			missing: []string{b},
		},
		{
			name:    "a copy whose -x names the PR's merge commit can still carry one commit by subject",
			hist:    branchOf(branchCommit{oid: "c1", subject: "fix tests", picks: []string{merge[:9]}}),
			missing: []string{b},
		},
		{
			name:    "a copy whose -x names some other commit carries nothing here by its subject",
			hist:    branchOf(branchCommit{oid: "c1", subject: "fix tests", picks: []string{other}}),
			missing: []string{a, b},
		},
		{
			name:    "an -x reference carries its commit whatever the subject",
			hist:    branchOf(branchCommit{oid: "c1", subject: "reworded", picks: []string{a}}, branchCommit{oid: "c2", subject: "fix tests"}),
			missing: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range tc.hist.missingFrom(pr, merge) {
				got = append(got, c.Oid)
			}
			if !slices.Equal(got, tc.missing) {
				t.Errorf("missing = %v, want %v", got, tc.missing)
			}
		})
	}
}

// A shared subject with only one copy on the branch leaves the candidate
// partial, so the gate stays shut.
func TestClassifySharedSubject(t *testing.T) {
	pr := pr4399()
	commits := func(string) []prCommit {
		return []prCommit{{firstHalf, "fix tests"}, {secondHalf, "fix tests"}}
	}
	v, err := classify(pr, labelCurrent, history([]string{"fix tests"}, nil, nil), nil, fakeSource{commitsOf: commits})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != statusPartial || v.Evidence != "1 of 2 commits on branch" {
		t.Errorf("got %s %q, want partial 1 of 2", v.Status, v.Evidence)
	}
	if (&branchReport{Candidates: []verdict{v}}).clean() {
		t.Errorf("a partial candidate must fail the gate")
	}
}

// The marker attests only as the whole comment. Every input that got past a
// line-by-line markdown reading in an earlier version is here, rejected by the
// one rule without any markdown in it.
func TestMarkerComment(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"the bare marker", "backport-audit: complete", true},
		{"with blank lines before it and whitespace after it", "\n\nbackport-audit: complete \t\n\n", true},
		{"with CRLF around it", "\r\nbackport-audit: complete\r\n", true},
		{"a sentence that says not to", "Do not post backport-audit: complete yet; the second commit is missing.", false},
		{"mid-sentence", "I think backport-audit: complete applies here.", false},
		{"with words after it", "backport-audit: complete, except the docs", false},
		{"in another case", "Backport-Audit: Complete", false},
		{"quoted", "> backport-audit: complete", false},
		{"quoted, then a reply", "> backport-audit: complete\n\nAgreed?", false},
		{"inside a backtick fence", "```\nbackport-audit: complete\n```", false},
		{"inside a tilde fence", "~~~\nbackport-audit: complete\n~~~", false},
		{"inside nested tilde fences", "~~~~\n~~~\nbackport-audit: complete\n~~~\n~~~~", false},
		{"as an indented code block", "Post this once checked:\n\n    backport-audit: complete", false},
		{"indented by a space and a tab after a blank line", "\n\n \tbackport-audit: complete", false},
		{"indented by a tab", "\tbackport-audit: complete", false},
		{"indented by two spaces", "  backport-audit: complete", false},
		{"after a line of spaces", "   \nbackport-audit: complete", false},
		{"with text glued in front", "xbackport-audit: complete", false},
		{"with an explanation above it", "Checked every commit by hand.\n\nbackport-audit: complete", false},
		{"with a note below it", "backport-audit: complete\n\nthanks!", false},
		{"twice", "backport-audit: complete\nbackport-audit: complete", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := markerComment(tc.body); got != tc.want {
			t.Errorf("%s: markerComment(%q) = %v, want %v", tc.name, tc.body, got, tc.want)
		}
	}
}

func TestBotAuthor(t *testing.T) {
	for login, want := range map[string]bool{
		"github-actions":      true,
		"github-actions[bot]": true,
		"coderabbitai":        true,
		"CodeRabbitAI":        true,
		"coderabbitai[bot]":   true,
		"dependabot":          true,
		"renovate":            true,
		"copilot":             true,
		"some-new-app[bot]":   true,
		"myasnikovdaniil":     false,
		"alice":               false,
		"renovate-fan":        false,
		"bot-enthusiast":      false,
	} {
		if got := botAuthor(login); got != want {
			t.Errorf("botAuthor(%q) = %v, want %v", login, got, want)
		}
	}
}

func TestClaimState(t *testing.T) {
	alice := &confirmation{By: "alice"}
	cases := []struct {
		states []string
		conf   *confirmation
		want   string
	}{
		{[]string{"MERGED"}, nil, "backport PR merged"},
		{[]string{"CLOSED", "MERGED"}, nil, "backport PR merged"},
		{[]string{"OPEN", "MERGED"}, nil, "backport PR merged"},
		{[]string{"MERGED"}, alice, "backport PR merged, confirmed by @alice"},
		{[]string{"OPEN"}, nil, "claimed here, not merged"},
		{[]string{"CLOSED", "OPEN"}, nil, "claimed here, not merged"},
		{[]string{"CLOSED"}, nil, "claimed here, closed unmerged"},
	}
	for _, tc := range cases {
		var prs []linkedPR
		for i, s := range tc.states {
			prs = append(prs, linkedPR{Number: i + 1, State: s})
		}
		if got := claimState(prs, tc.conf); got != tc.want {
			t.Errorf("claimState(%v, %v) = %q, want %q", tc.states, tc.conf, got, tc.want)
		}
	}
}
