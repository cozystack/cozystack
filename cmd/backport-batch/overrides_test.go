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
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// overridden builds a line where backport/ labels decide: #5 a feature let in,
// #6 a fix kept out, #7 a breaking fix and #8 a migration let in, #9 carrying
// both labels, #10 a feature let in that the line already carries, adapted, so
// picking it again would conflict rather than come out empty.
func overridden(t *testing.T) (*fixture, *fakeGH) {
	t.Helper()
	f := newFixture(t)
	f.lines()
	f.pr(5, "feat(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.pr(7, "fix(app)!: seven", "", map[string]string{"app/seven.txt": "7\n"})
	f.pr(8, "fix(platform): eight", "", map[string]string{migrationsDir + "61": "#!/bin/sh\n"})
	f.pr(9, "fix(app): nine", "", map[string]string{"app/nine.txt": "9\n"})
	mc10 := f.pr(10, "feat(app): ten", "", map[string]string{"app/ten.txt": "10\n"})
	f.onLine("release-1.1", "feat(app): ten (#10)\n\n(cherry picked from commit "+mc10+")", map[string]string{"app/ten.txt": "10, adapted\n"})
	gh := &fakeGH{labels: map[string][]int{
		labelInclude: {5, 7, 8, 9, 10},
		labelSkip:    {6, 9},
	}}
	return f, gh
}

func TestOverrides(t *testing.T) {
	f, gh := overridden(t)
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	want := []string{"feat(app): five (#5)", "fix(app)!: seven (#7)", "fix(platform): eight (#8)"}
	if got := f.subjects("origin/release-1.1", "HEAD"); !slices.Equal(got, want) {
		t.Errorf("picked %q, want %q", got, want)
	}
	section, _, _ := strings.Cut(stdout[strings.Index(stdout, "### Maintainer overrides"):], "### Skipped")
	for n, outcome := range map[int]string{
		5:  "`backport/include`, on this branch",
		6:  "`backport/skip`, left out",
		7:  "`backport/include`, on this branch",
		8:  "`backport/include`, on this branch",
		9:  "`backport/skip`, left out",
		10: "`backport/include`, skipped by rule 1",
	} {
		if !strings.Contains(section, fmt.Sprintf("/pull/%d)", n)) || !strings.Contains(section, outcome) {
			t.Errorf("the overrides section does not show #%d as %q:\n%s", n, outcome, section)
		}
	}
	if skipped := stdout[strings.Index(stdout, "### Skipped"):]; strings.Contains(skipped, "/pull/6)") || strings.Contains(skipped, "/pull/9)") {
		t.Errorf("a skipped-by-label original is listed under a rule:\n%s", skipped)
	}
}

// check holds the line to what the labels decide, and fold refuses an
// original kept out.
func TestOverridesInCheckAndFold(t *testing.T) {
	f, gh := overridden(t)
	code, stdout, _ := f.exec(gh, "check", "--line", "release-1.1")
	if code != 1 {
		t.Fatalf("check = %d, want 1:\n%s", code, stdout)
	}
	for _, n := range []int{5, 7, 8} {
		if !strings.Contains(stdout, fmt.Sprintf("/pull/%d ", n)) {
			t.Errorf("check does not list #%d:\n%s", n, stdout)
		}
	}
	for _, n := range []int{6, 9, 10} {
		if strings.Contains(stdout, fmt.Sprintf("/pull/%d ", n)) {
			t.Errorf("check lists #%d:\n%s", n, stdout)
		}
	}

	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	f.git("fetch", "-q", "origin")
	gh.prs = []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "")}
	f.git("switch", "-q", "--detach", "origin/"+batchBranch1)
	f.write("app/six.txt", "6\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "port\n\n(cherry picked from commit "+f.git("rev-parse", "origin/main~4")+")")
	commit := f.git("rev-parse", "HEAD")
	f.git("switch", "-q", "main")
	code, stdout, _ = f.exec(gh, "fold", "--line", "release-1.1", "--original", "6", "--commit", commit)
	if code != 1 || !strings.Contains(stdout, "#6 carries backport/skip") {
		t.Errorf("fold of a skipped original = %d, %q; want a refusal", code, stdout)
	}
}

// backport/include lifts rules 2 to 4, not rule 6: an older line still waits
// for the newer one.
func TestIncludeKeepsRuleSix(t *testing.T) {
	f, gh := overridden(t)
	f.git("fetch", "-q", "origin")
	l, _ := parseLine("release-1.0")
	b, err := (&app{gh: gh, stderr: &strings.Builder{}}).prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	if v := b.find(5); v == nil || v.rule != ruleNewer {
		t.Errorf("#5 on release-1.0 = %+v, want rule 6", v)
	}
	if v := b.find(10); v == nil || v.rule != 0 {
		t.Errorf("#10 on release-1.0 = %+v, want it to qualify: release-1.1 carries it", v)
	}
}

func TestOverridesUnreadable(t *testing.T) {
	f, gh := overridden(t)
	gh.listErr = errString("gh: HTTP 502")
	if code, _, stderr := f.exec(gh, "check", "--line", "release-1.1"); code != 2 || !strings.Contains(stderr, "HTTP 502") {
		t.Errorf("check with labels unreadable = %d (%s), want 2", code, stderr)
	}
}

// ghCLI against a stub gh on PATH: the exact argv, and a listing that fills
// GitHub search's cap is refused rather than read as complete.
func TestGHCLI(t *testing.T) {
	bin := t.TempDir()
	args, out := filepath.Join(bin, "args"), filepath.Join(bin, "out")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + args + "\"\ncat \"" + out + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(out, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(args)
	}
	called := func() string {
		data, _ := os.ReadFile(args)
		return strings.TrimSpace(string(data))
	}

	write(`[{"number":5},{"number":7}]`)
	got, err := ghCLI{}.labelled(labelInclude)
	if err != nil || !slices.Equal(got, []int{5, 7}) {
		t.Errorf("labelled = %v, %v; want [5 7]", got, err)
	}
	if want := "pr list --base main --state merged --label backport/include --limit 1000 --json number"; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}

	var full []string
	for i := range searchResultCap {
		full = append(full, fmt.Sprintf(`{"number":%d}`, i+1))
	}
	write("[" + strings.Join(full, ",") + "]")
	if _, err := (ghCLI{}).labelled(labelSkip); err == nil {
		t.Error("a listing as long as the search cap was read as complete")
	}

	write(`{"permission":"write"}`)
	if p, err := (ghCLI{}).permission("maintainer"); err != nil || p != "write" {
		t.Errorf("permission = %q, %v", p, err)
	}
	if want := "api repos/{owner}/{repo}/collaborators/maintainer/permission"; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}
	write(`{"permission":"admin"}`)
	for _, login := range []string{"x/../../orgs/y", "-x", "a b", "github-actions[bot]", ""} {
		if _, err := (ghCLI{}).permission(login); err == nil || called() != "" {
			t.Errorf("permission(%q) = %v and called gh %q; want a refusal before gh", login, err, called())
		}
	}

	write("[]")
	if _, err := (ghCLI{}).openPulls("release-1.6"); err != nil {
		t.Fatal(err)
	}
	if want := "api -X GET repos/{owner}/{repo}/pulls -f state=open -f base=release-1.6 -f per_page=100"; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}
	var page []string
	for i := range openPullsCap {
		page = append(page, fmt.Sprintf(`{"number":%d}`, i+1))
	}
	write("[" + strings.Join(page, ",") + "]")
	if _, err := (ghCLI{}).openPulls("release-1.6"); err == nil {
		t.Error("a full page of open pull requests was read as all of them")
	}

	write(`[[{"id":1,"body":"a","user":{"login":"x","type":"User"}}],[{"id":2,"body":"b","user":{"login":"y","type":"Bot"}}]]`)
	comments, err := ghCLI{}.comments(90)
	if err != nil || len(comments) != 2 || comments[1].ID != 2 || comments[1].User.Type != "Bot" {
		t.Errorf("comments = %+v, %v; want both pages", comments, err)
	}
	if want := "api --paginate --slurp repos/{owner}/{repo}/issues/90/comments"; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}
	write("{}")
	if err := (ghCLI{}).createComment(90, "/tmp/body.md"); err != nil {
		t.Fatal(err)
	}
	if want := "api --method POST repos/{owner}/{repo}/issues/90/comments -F body=@/tmp/body.md"; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}
	write("{}")
	if err := (ghCLI{}).editComment(12345678901, "/tmp/body.md"); err != nil {
		t.Fatal(err)
	}
	if want := "api --method PATCH repos/{owner}/{repo}/issues/comments/12345678901 -F body=@/tmp/body.md"; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}

	// The agent's token reaches the agent task call and nothing else, and no
	// token of the job's stands in for it.
	stub = "#!/bin/sh\nprintf 'GH_TOKEN=%s GITHUB_TOKEN=%s %s\\n' \"$GH_TOKEN\" \"$GITHUB_TOKEN\" \"$*\" >> \"" + args + "\"\ncat \"" + out + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "batch-token")
	t.Setenv("GITHUB_TOKEN", "job-token")
	write("{}")
	if err := (ghCLI{}).startTask("agent-token", "release-1.7", batchBranch1, "do it"); err != nil {
		t.Fatal(err)
	}
	if want := "GH_TOKEN=agent-token GITHUB_TOKEN= api --method POST agents/repos/{owner}/{repo}/tasks -H X-GitHub-Api-Version: 2026-03-10 -f prompt=do it -f base_ref=release-1.7 -f head_ref=" + batchBranch1; called() != want {
		t.Errorf("gh %q, want gh %q", called(), want)
	}
	write("{}")
	if err := (ghCLI{}).createComment(90, "/tmp/body.md"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(called(), "GH_TOKEN=batch-token ") {
		t.Errorf("a comment went out as %q, want the batch's token", called())
	}
}
