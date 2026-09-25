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
