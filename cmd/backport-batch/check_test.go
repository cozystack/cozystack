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
	"regexp"
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
	deferred6 := deferredBlock(l, []pick{{v: &verdict{o: &original{number: 6}}, files: []string{"app/six.txt"}}}, nil, nil)
	held6 := deferredBlock(l, nil, []pick{{v: &verdict{o: &original{number: 6}}, reason: "fails the app e2e"}}, nil)

	cases := []struct {
		name        string
		arrange     func(s *state)
		cut         bool
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
			name:    "--cut: both on the open batch, which the cut would not carry",
			arrange: func(s *state) { batch(s, "", s.mc5, s.mc6) },
			cut:     true,
			code:    1,
			listed:  []string{"#5 is only on open batch PR #90, merge it before the cut: https://github.com/cozystack/cozystack/pull/5 ", "#6 is only on open batch PR #90, merge it before the cut: https://github.com/cozystack/cozystack/pull/6 "},
		},
		{
			name: "--cut: both on the line",
			arrange: func(s *state) {
				s.f.onLine("release-1.1", "fix(app): five (#5)\n\n(cherry picked from commit "+s.mc5+")", map[string]string{"app/five.txt": "5\n"})
				s.f.onLine("release-1.1", "fix(app): six (#6)\n\n(cherry picked from commit "+s.mc6+")", map[string]string{"app/six.txt": "6\n"})
				batch(s, "")
			},
			cut:  true,
			code: 0,
		},
		{
			name: "--cut: one on the line, the other deferred to the next batch, which stays a warning",
			arrange: func(s *state) {
				s.f.onLine("release-1.1", "fix(app): five (#5)\n\n(cherry picked from commit "+s.mc5+")", map[string]string{"app/five.txt": "5\n"})
				batch(s, "body\n\n"+held6+"\n")
			},
			cut:         true,
			code:        0,
			warnedAbout: "/pull/6 ",
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
				batch(s, "body\n\n", s.mc5)
				s.gh.perms = map[string]string{"maintainer": "read"}
			},
			code:   1,
			listed: []string{"/pull/5 ", "/pull/6 "},
		},
		{
			// An older record is never taken in place of a newer one nobody
			// can vouch for.
			name: "a record by a member with read access stops it",
			arrange: func(s *state) {
				batch(s, "body\n\n"+deferred6+"\n", s.mc5)
				s.gh.perms = map[string]string{"maintainer": "read"}
			},
			code: 2,
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
			args := []string{"check", "--line", "release-1.1"}
			if tc.cut {
				args = append(args, "--cut")
			}
			code, stdout, stderr := s.f.exec(s.gh, args...)
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

// Every category an original can be in, through the commands that put it
// there, with what check and check --cut make of it: the exit code, and the
// warning or note on stderr that leaves it 0.
func TestCheckDispositions(t *testing.T) {
	plain := func(t *testing.T) *fixture {
		f := newFixture(t)
		f.lines()
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		return f
	}
	// conflicting has #5 conflict on the line, beside #6, which picks.
	conflicting := func(t *testing.T) *fixture {
		f := newFixture(t)
		f.lines()
		f.onLine("release-1.1", "fix(app): a line-only five", map[string]string{"app/five.txt": "line\n"})
		f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
		f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
		return f
	}
	must := func(t *testing.T, f *fixture, gh *fakeGH, args ...string) {
		t.Helper()
		if code, _, stderr := f.exec(gh, args...); code != 0 {
			t.Fatalf("%q = %d\n%s", args, code, stderr)
		}
		f.git("switch", "-q", "main")
		if len(gh.bodies) > 0 {
			reopen(f, gh)
		}
	}
	sixOnLine := func(f *fixture) { pickOnLine(f, 6, map[string]string{"app/six.txt": "6\n"}) }
	cases := []struct {
		name      string
		arrange   func(t *testing.T) (*fixture, *fakeGH)
		line      string
		check     int
		cut       int
		checkErr  string // on stderr from check
		cutOut    string // on stdout from check --cut
		cutErr    string // on stderr from check --cut
		notCutOut string
	}{
		{
			name:    "missing",
			arrange: func(t *testing.T) (*fixture, *fakeGH) { return plain(t), &fakeGH{} },
			check:   1, cut: 1, cutOut: "/pull/5 ",
		},
		{
			name: "carried",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f := plain(t)
				pickOnLine(f, 5, map[string]string{"app/five.txt": "5\n"})
				sixOnLine(f)
				return f, &fakeGH{}
			},
			check: 0, cut: 0, notCutOut: "/pull/5",
		},
		{
			name: "picked, on the open batch",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f, gh := plain(t), &fakeGH{}
				openBatch(t, f, gh)
				return f, gh
			},
			check: 0, cut: 1, cutOut: "#5 is only on open batch PR #90",
		},
		{
			name: "deferred conflict, open record",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f, gh := conflicting(t), &fakeGH{}
				openBatch(t, f, gh)
				sixOnLine(f)
				return f, gh
			},
			check: 0, cut: 0, checkErr: "warning: deferred conflict on #90: https://github.com/cozystack/cozystack/pull/5 ", cutErr: "warning: deferred conflict on #90",
		},
		{
			name: "deferred with a reason, open record",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f, gh := plain(t), &fakeGH{}
				openBatch(t, f, gh)
				must(t, f, gh, "run", "--line", "release-1.1", "--defer", "5", "--reason", "fails the app e2e", "--push")
				sixOnLine(f)
				return f, gh
			},
			check: 0, cut: 0, checkErr: "warning: deferred on #90 to the next batch: https://github.com/cozystack/cozystack/pull/5 ", cutErr: "(fails the app e2e)",
		},
		{
			name: "workflow wait, open record, beside a pick",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f, gh := workflowWorld(t), &fakeGH{}
				must(t, f, gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push")
				return f, gh
			},
			check: 0, cut: 1, checkErr: "warning: waiting on #90 for a maintainer", cutOut: "#5 changes .github/workflows/ and waits on batch PR #90",
		},
		{
			name: "workflow wait, open record, alone",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f, gh := workflowWorld(t), &fakeGH{}
				must(t, f, gh, "run", "--line", "release-1.1", "--no-workflow-files", "--push")
				pickOnLine(f, 6, map[string]string{"app/six.txt": "6\n", ".github/renovate.json": "{}\n"})
				return f, gh
			},
			check: 0, cut: 1, checkErr: "warning: waiting on #90 for a maintainer", cutOut: "#5 changes .github/workflows/ and waits on batch PR #90",
		},
		{
			name: "backport/skip, not picked",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f := plain(t)
				sixOnLine(f)
				return f, &fakeGH{labels: map[string][]int{labelSkip: {5}}}
			},
			check: 0, cut: 0, notCutOut: "/pull/5",
		},
		{
			name: "backport/skip after the pick, still on the open batch",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f, gh := plain(t), &fakeGH{}
				openBatch(t, f, gh)
				sixOnLine(f)
				gh.labels = map[string][]int{labelSkip: {5}}
				return f, gh
			},
			check: 0, cut: 0, checkErr: "warning: #5 is still on batch #90; take it out with backport-batch run --defer", cutErr: "still on batch #90",
		},
		{
			name: "rule 6",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f := newFixture(t)
				f.lines()
				f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
				return f, &fakeGH{}
			},
			line:  "release-1.0",
			check: 0, cut: 0, notCutOut: "/pull/5",
		},
		{
			name: "rules 2 to 4",
			arrange: func(t *testing.T) (*fixture, *fakeGH) {
				f := newFixture(t)
				f.lines()
				f.pr(5, "feat(app): five", "", map[string]string{"app/five.txt": "5\n"})
				return f, &fakeGH{}
			},
			check: 0, cut: 0, notCutOut: "/pull/5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, gh := tc.arrange(t)
			line := tc.line
			if line == "" {
				line = "release-1.1"
			}
			code, stdout, stderr := f.exec(gh, "check", "--line", line)
			if code != tc.check || !strings.Contains(stderr, tc.checkErr) {
				t.Errorf("check = %d, want %d with %q\nstdout:\n%s\nstderr:\n%s", code, tc.check, tc.checkErr, stdout, stderr)
			}
			code, stdout, stderr = f.exec(gh, "check", "--line", line, "--cut")
			if code != tc.cut || !strings.Contains(stdout, tc.cutOut) || !strings.Contains(stderr, tc.cutErr) {
				t.Errorf("check --cut = %d, want %d with %q, %q\nstdout:\n%s\nstderr:\n%s", code, tc.cut, tc.cutOut, tc.cutErr, stdout, stderr)
			}
			if tc.notCutOut != "" && strings.Contains(stdout, tc.notCutOut) {
				t.Errorf("check --cut lists %q:\n%s", tc.notCutOut, stdout)
			}
			if tc.cut == 0 && !strings.Contains(stdout, "no change the policy takes is missing before the cut") {
				t.Errorf("check --cut passes without saying so:\n%s", stdout)
			}
		})
	}
}

// Output names a rule as the policy does: the numbers the tool keeps for
// what is no rule of the policy never reach a reader.
func TestOutputNamesNoInternalRule(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	f.setPolicy("release-1.1: 2099-01-01\n")
	gh := &fakeGH{labels: map[string][]int{labelInclude: {6}}}
	head := openBatch(t, f, gh)
	internal := regexp.MustCompile(`rule 1[0-9][0-9]`)
	for _, args := range [][]string{
		{"fold", "--line", "release-1.1", "--original", "5", "--commit", head},
		{"run", "--line", "release-1.1", "--defer", "5", "--reason", "x"},
	} {
		code, stdout, stderr := f.exec(gh, args...)
		f.git("switch", "-q", "main")
		if code == 0 || !strings.Contains(stdout+stderr, "#5 is not one the batch takes: rule 1, before the line's start, merged ") {
			t.Errorf("%s = %d, want a refusal naming rule 1:\n%s%s", args[0], code, stdout, stderr)
		}
		if m := internal.FindString(stdout + stderr); m != "" {
			t.Errorf("%s prints %q:\n%s%s", args[0], m, stdout, stderr)
		}
	}
}

// A refusal fold prints is one line with no control sequence, whatever the
// reason it repeats says: a drop's reason comes from a pull request body, a
// rule 4 reason from file names an original's author chose.
func TestFoldRefusalPrintsNothingThatDrivesATerminal(t *testing.T) {
	l, _ := parseLine("release-1.1")
	for _, reason := range []string{"dropped on #90: x\n\x1b[2Jrefused: forged", "touches \"app/a\x1b]0;x\x07\nok\""} {
		b := &batch{line: l, verdicts: []*verdict{{o: &original{number: 5}, rule: ruleDropped, reason: reason}}}
		why, err := refuseFold(b, 5, strings.Repeat("a", 40))
		if err != nil || why == "" || strings.ContainsFunc(why, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			t.Errorf("refuseFold = %q, %v; want one clean line", why, err)
		}
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
