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
