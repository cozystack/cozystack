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
	"strings"
	"testing"
	"unicode"
)

func TestCheck(t *testing.T) {
	type state struct {
		f   *fixture
		gh  *fakeGH
		mc5 string
		mc6 string
	}
	setup := func(t *testing.T) *state {
		f := newFixture(t)
		f.lines()
		s := &state{f: f, gh: &fakeGH{}}
		s.mc5 = f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		s.mc6 = f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		f.pr(7, "feat(app): seven", "", map[string]string{"app/seven.txt": "7\n"})
		return s
	}
	// batch pushes a batch branch carrying the given merges and opens its
	// pull request with body.
	batch := func(s *state, body string, merges ...string) {
		s.f.git("switch", "-q", "-c", "batch", "origin/release-1.1")
		for _, mc := range merges {
			s.f.git("cherry-pick", "-x", "-m", "1", mc)
		}
		s.f.git("push", "-q", "origin", "HEAD:refs/heads/"+batchBranch1)
		s.f.git("switch", "-q", "main")
		s.gh.prs = append(s.gh.prs, s.f.batchPR(90, "release-1.1", batchBranch1, body))
	}
	l, _ := parseLine("release-1.1")
	deferred6 := deferredBlock(l, []pick{{v: &verdict{o: &original{number: 6}}, files: []string{"app/six.txt"}}}, nil)
	held6 := deferredBlock(l, nil, []pick{{v: &verdict{o: &original{number: 6}}, reason: "fails the app e2e"}})

	cases := []struct {
		name        string
		arrange     func(s *state)
		code        int
		listed      []string
		notListed   []string
		warnedAbout string
	}{
		{
			name:      "nothing picked, no batch",
			arrange:   func(*state) {},
			code:      1,
			listed:    []string{"/pull/5 ", "/pull/6 "},
			notListed: []string{"/pull/7"},
		},
		{
			name: "both on the line",
			arrange: func(s *state) {
				s.f.onLine("release-1.1", "fix(app): five (#5)\n\n(cherry picked from commit "+s.mc5+")", map[string]string{"app/five.txt": "5\n"})
				s.f.onLine("release-1.1", "[Backport release-1.1] fix(app): six (#80)\n\nBackport of #6.", map[string]string{"app/six.txt": "6\n"})
			},
			code: 0,
		},
		{
			name:    "both on the open batch",
			arrange: func(s *state) { batch(s, "", s.mc5, s.mc6) },
			code:    0,
		},
		{
			name:      "one on the batch, one missing",
			arrange:   func(s *state) { batch(s, "", s.mc5) },
			code:      1,
			listed:    []string{"/pull/6 "},
			notListed: []string{"/pull/5 "},
		},
		{
			name:        "the missing one is a deferred conflict on the batch",
			arrange:     func(s *state) { batch(s, "body\n\n"+deferred6+"\n", s.mc5) },
			code:        0,
			warnedAbout: "/pull/6 ",
		},
		{
			name:        "the missing one is deferred to the next batch",
			arrange:     func(s *state) { batch(s, "body\n\n"+held6+"\n", s.mc5) },
			code:        0,
			warnedAbout: "/pull/6 ",
		},
		{
			name: "a fork's pull request named like a batch claims nothing",
			arrange: func(s *state) {
				batch(s, "body\n\n"+deferred6+"\n", s.mc5)
				s.gh.prs[0].Head.Repo.FullName = "someone/cozystack"
			},
			code:   1,
			listed: []string{"/pull/5 ", "/pull/6 "},
		},
		{
			name: "a batch opened by a member with read access claims nothing",
			arrange: func(s *state) {
				batch(s, "body\n\n"+deferred6+"\n", s.mc5)
				s.gh.perms = map[string]string{"maintainer": "read"}
			},
			code:   1,
			listed: []string{"/pull/5 ", "/pull/6 "},
		},
		{
			name:    "a deferred list for another line",
			arrange: func(s *state) { batch(s, strings.Replace(deferred6, "release-1.1", "release-1.0", 1), s.mc5) },
			code:    2,
		},
		{
			name:    "GitHub cannot be read",
			arrange: func(s *state) { s.gh.err = errString("gh: HTTP 502") },
			code:    2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t)
			tc.arrange(s)
			code, stdout, stderr := s.f.exec(s.gh, "check", "--line", "release-1.1")
			if code != tc.code {
				t.Fatalf("check = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout, stderr)
			}
			for _, want := range tc.listed {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout does not list %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.notListed {
				if strings.Contains(stdout, unwanted) {
					t.Errorf("stdout lists %q:\n%s", unwanted, stdout)
				}
			}
			if tc.warnedAbout != "" && (!strings.Contains(stderr, "warning: deferred") || !strings.Contains(stderr, tc.warnedAbout)) {
				t.Errorf("stderr does not warn about %q:\n%s", tc.warnedAbout, stderr)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// Nothing the tool prints may carry a control sequence, whichever command
// prints it and wherever the text came from.
func TestTerminal(t *testing.T) {
	f := newFixture(t)
	f.lines()
	const evil = "\x1b]0;owned\x07\x1b[2J\u009b31m\u202e"
	f.pr(5, "fix(app): five "+evil, "", map[string]string{"app/five.txt": "5\n"})
	f.git("push", "-q", "origin", "origin/release-1.1:refs/heads/"+batchBranch1)
	gh := &fakeGH{prs: []pullRequest{f.batchPR(90, "release-1.1", batchBranch1, "")}}

	f.git("switch", "-q", "--detach", "origin/"+batchBranch1)
	f.write("app/"+evil+".txt", "x\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "port "+evil)
	commit := f.git("rev-parse", "HEAD")
	f.git("switch", "-q", "main")

	// gh relays what the server said when it fails.
	broken := &fakeGH{err: errString("HTTP 502: " + evil)}
	for _, tc := range []struct {
		gh   *fakeGH
		args []string
	}{
		{gh, []string{"check", "--line", "release-1.1"}},
		{broken, []string{"check", "--line", "release-1.1"}},
		{gh, []string{"fold", "--line", "release-1.1", "--original", "5", "--commit", commit}},
		{gh, []string{"run", "--line", "release-1.1", "--date", "2026-10-26"}},
		{gh, []string{"run", "--line", "release-1.1\x1b[2J"}},
	} {
		args := tc.args
		_, stdout, stderr := f.exec(tc.gh, args...)
		for name, out := range map[string]string{"stdout": stdout, "stderr": stderr} {
			for _, r := range out {
				if r != '\n' && r != '\t' && unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					t.Errorf("%s of %v carries %U:\n%q", name, args[0], r, out)
					break
				}
			}
		}
	}
}
