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
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// releaseNoteBlock is the release-note block of a body.
func releaseNoteBlock(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile("(?s)```release-note\n(.*?)\n```").FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no release-note block in:\n%s", body)
	}
	return m[1]
}

// A pick that differs from its original keeps its release note out of the
// release-note block, under Check these notes with what to compare: one git
// had to fit to a line that already had part of the change, and one whose
// first commit came out empty because the line had resized the value itself.
func TestChangedOriginalNoteIsSetApart(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	f.onLine("release-1.1", "fix(app): two, on the line", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.onLine("release-1.1", "fix(chart): two replicas, on the line", map[string]string{"chart/values.yaml": "replicas: 2\nimage: app:1\n"})
	f.pr(5, "fix(app): two and four", "```release-note\nfix(app): two and four are loud.\n```\n", map[string]string{"app/a.txt": strings.NewReplacer("two", "TWO", "four", "FOUR").Replace(appFile)})
	f.prCommits(6, "fix(chart): two replicas and six", "```release-note\nfix(chart): two replicas by default.\n```\n",
		change{files: map[string]string{"chart/values.yaml": "replicas: 2\nimage: app:1\n"}},
		change{files: map[string]string{"app/six.txt": "6\n"}})
	f.pr(7, "fix(app): seven", "```release-note\nfix(app): seven is calm.\n```\n", map[string]string{"app/seven.txt": "7\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, []string{"fix(app): two and four (#5)", "fix(chart): two replicas and six (#6)", "fix(app): seven (#7)"}) {
		t.Fatalf("picked %q, want #5, #6 and #7", got)
	}
	notes := releaseNoteBlock(t, stdout)
	if !strings.Contains(notes, "seven is calm") || strings.Contains(notes, "two and four are loud") || strings.Contains(notes, "two replicas by default") {
		t.Errorf("the release-note block keeps a changed pick's note, or loses #7's:\n%s", notes)
	}
	_, check, ok := strings.Cut(stdout, "### Check these notes")
	check, _, _ = strings.Cut(check, "\n#")
	if !ok || !strings.Contains(check, "two and four are loud") || !strings.Contains(check, "two replicas by default") || strings.Contains(check, "seven is calm") {
		t.Errorf("Check these notes does not list #5 and #6 alone:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"changed":[5,6]`) {
		t.Errorf("the record or the report does not mark #5 and #6 changed:\n%s", stdout)
	}
}

// The command printed for a changed pick pairs the pick with the commit it
// came from, however much the pick lost: the default creation factor leaves
// a pick that carries only #5088's test unpaired.
func TestRangeDiffCommandPairsThePick(t *testing.T) {
	f, mc5 := policyWorld(t)
	gh := &fakeGH{}
	head := handBatch(f, gh, mc5)
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--continue")
	if code != 0 {
		t.Fatalf("run --continue = %d\n%s", code, stderr)
	}
	m := regexp.MustCompile("`git range-diff --creation-factor=999 ([0-9a-f]{12})\\^! ([0-9a-f]{12})\\^!`").FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("the body prints no range-diff for #5's pick:\n%s", stdout)
	}
	src, pick := f.git("rev-parse", mc5+"^2"), f.git("rev-parse", head+"^2")
	if !strings.HasPrefix(src, m[1]) || !strings.HasPrefix(pick, m[2]) {
		t.Errorf("the range-diff names %s and %s, want #5's commit %.12s and its pick %.12s", m[1], m[2], src, pick)
	}
	out, err := exec.Command("git", "range-diff", "--no-color", "--creation-factor=999", m[1]+"^!", m[2]+"^!").Output()
	if want := "1:  " + f.git("rev-parse", "--short", src) + " ! 1:  " + f.git("rev-parse", "--short", pick); err != nil || !strings.HasPrefix(string(out), want) {
		t.Errorf("the printed range-diff gives %q, %v; want it to start %q", out, err, want)
	}
	if out := f.git("range-diff", "--no-color", mc5+"^1.."+mc5+"^2", head+"^1.."+head+"^2"); !strings.Contains(out, " < ") {
		t.Errorf("the default creation factor pairs #5's pick after all, so the test shows nothing:\n%s", out)
	}
}
