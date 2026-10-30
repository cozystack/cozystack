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
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// visible is what of a body GitHub renders as markdown rather than shows as
// code: fenced blocks and code spans taken out, delimited as CommonMark
// delimits them. Everything carried has to be gone from it.
func visible(body string) string {
	fenceOpen := regexp.MustCompile("^(`{3,}|~{3,})")
	var out []string
	fence := ""
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimLeft(ln, " ")
		indented := len(ln)-len(t) > 3
		if fence != "" {
			run := strings.TrimLeft(t, fence[:1])
			if !indented && len(t)-len(run) >= len(fence) && strings.TrimSpace(run) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceOpen.FindString(t); m != "" && !indented && (m[0] == '~' || !strings.Contains(t[len(m):], "`")) {
			fence = m
			continue
		}
		out = append(out, stripSpans(ln))
	}
	return strings.Join(out, "\n")
}

func stripSpans(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i += 2
			continue
		case s[i] != '`':
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		n, closing := j-i, -1
		for k := j; k < len(s); {
			if s[k] != '`' {
				k++
				continue
			}
			e := k
			for e < len(s) && s[e] == '`' {
				e++
			}
			if e-k == n {
				closing = k
				break
			}
			k = e
		}
		if closing < 0 {
			b.WriteString(s[i:j])
			i = j
			continue
		}
		i = closing + n
	}
	return b.String()
}

// hostile is carried text built to get out of its span or block: each carries
// a marker that must never be visible.
var hostile = []string{
	"zqx01 thanks @alice",
	"zqx02 (@org/team) please look",
	"zqx03 [@bob](https://evil.example/x) [docs](https://github.com/cozystack/cozystack/../../evil/repo)",
	"zqx04 zero\u200b@carol bidi \u202e@dave \ufeff@erin",
	"zqx05 ``` `@frank` ``` still inside",
	"zqx06 ` closes a single span, then @grace",
	"zqx07 `` `` ``` ```` four",
	"zqx08 Fixes #1, closes other/repo#2, resolves https://github.com/other/repo/issues/3",
	"zqx09 </details><img src=x onerror=alert(1)><a href=\"https://evil.example\">x</a>",
	"zqx10 <https://evil.example> www.evil.example <mail@evil.example>",
	"zqx11 [ref]: https://evil.example",
	"zqx12 &#64;heidi &amp; &lt;b&gt;",
	"zqx13 \x1b[31mred\x1b[0m \u009b2J \x07 bell",
	"zqx14 <!-- comment --> <details><summary>x</summary>",
	"`zqx15` starts and ends with a backtick`",
}

// hostileNote is a release note whose own block a bare fence line would end,
// so it hides one behind a zero-width space, which clean then removes.
func hostileNote(i int, s string) string {
	return fmt.Sprintf("fix(x): zqn%02d %s\n\u200b```\n@ivan zqn%02d after a fence\n~~~\n[@judy](https://evil.example) Fixes #9 <img src=x> \x1b[2J", i, s, i)
}

// Every carried title, reason and release note stays inside a code span or the
// release-note block, however it is written.
func TestBodyContainsCarriedText(t *testing.T) {
	f := newFixture(t)
	f.lines()
	for i, s := range hostile {
		typ := "fix"
		if i%2 == 1 {
			typ = "feat" // skipped, so it lands in a <details> list
		}
		body := "## What this PR does\n\n```release-note\n" + hostileNote(i, s) + "\n```\n"
		f.pr(100+i, typ+"(x): "+s, body, map[string]string{fmt.Sprintf("app/%d.txt", i): "x\n"})
	}
	gh := &fakeGH{}
	code, stdout, stderr := f.exec(gh, "run", "--line", "release-1.1", "--push")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	if len(gh.bodies) != 1 {
		t.Fatalf("gh got %d bodies, want 1", len(gh.bodies))
	}
	for _, body := range []string{gh.bodies[0], stdout} {
		for i := range hostile {
			title, note := fmt.Sprintf("zqx%02d", i+1), fmt.Sprintf("zqn%02d", i)
			if i == len(hostile)-1 {
				title = "zqx15"
			}
			if !strings.Contains(body, title) {
				t.Errorf("%s is missing from the body", title)
			}
			if i%2 == 0 && !strings.Contains(body, note) {
				t.Errorf("%s is missing from the release notes", note)
			}
		}
		if v := visible(body); strings.Contains(v, "zqx") || strings.Contains(v, "zqn") {
			t.Errorf("carried text is visible outside code:\n%s", v)
		}
		for _, r := range body {
			if r != '\n' && r != '\t' && unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Errorf("the body carries %U", r)
				break
			}
		}
	}
}

// Copied out of the fenced block, a note must still close, ping and link
// nothing.
func TestNeutralNote(t *testing.T) {
	cases := []struct{ in, gone string }{
		{"thanks @alice", "@alice"},
		{"(@org/team) look", "@org/team"},
		{"[@bob](https://evil.example)", "@bob"},
		{"x\u200b@carol", "@carol"},
		{"a@dave.example", "@dave"},
		{"Fixes #1", "Fixes #1"},
		{"closes other/repo#2", "other/repo#2"},
		{"see other/repo#3", "other/repo#3"},
		{"resolves https://github.com/other/repo/issues/3", "github.com/other"},
		{"Fixes https://github.com/cozystack/cozystack/issues/3", "Fixes https://"},
		{"x``@erin", "@erin"},
		{"`code @frank here`", "@frank"},
		{"```@grace```", "@grace"},
		{"go to https://evil.example/x", "evil.example"},
		{"go to www.evil.example", "evil.example"},
		{"[docs](https://evil.example)", "evil.example"},
		{"[docs](https://github.com/cozystack/cozystack/../../evil/repo)", "evil/repo"},
		{"line\n```\nescaped", "```"},
		{"line\n~~~\nescaped", "~~~"},
		{"zero\u200b``\u200b`", "```"},
		{"\x1b[2J", "\x1b"},
	}
	for _, tc := range cases {
		got := neutralNote(tc.in)
		if strings.Contains(visible(got), tc.gone) {
			t.Errorf("neutralNote(%q) = %q, still shows %q", tc.in, got, tc.gone)
		}
	}
	keep := "see https://github.com/cozystack/cozystack/blob/main/docs/release/backports.md"
	if got := neutralNote(keep); got != keep {
		t.Errorf("neutralNote(%q) = %q, want a link into this repository kept", keep, got)
	}
}

func TestCodeSpan(t *testing.T) {
	cases := []string{"plain", "a `b` c", "``x``", "`lead", "trail`", "", "a\nb", "````", "\x1b[31m`\u202e`"}
	for _, s := range cases {
		got := codeSpan(s)
		if v := visible(got); v != "" {
			t.Errorf("codeSpan(%q) = %q leaves %q outside the span", s, got, v)
		}
		if strings.ContainsAny(got, "\n\x1b\u202e") {
			t.Errorf("codeSpan(%q) = %q carries a newline or a control", s, got)
		}
	}
}

func TestDeferredRoundTrip(t *testing.T) {
	l, _ := parseLine("release-1.6")
	v := &verdict{o: &original{number: 3735}}
	held := &verdict{o: &original{number: 3736}}
	block := deferredBlock(l, []pick{{v: v, files: []string{"a\n```\nb", "<c>&d", "e`f"}}}, []pick{{v: held, reason: "breaks\n```\n<b>e2e</b>"}})
	if lines := strings.Split(block, "\n"); len(lines) != 3 {
		t.Fatalf("the block is %d lines, want 3:\n%s", len(lines), block)
	}
	body := "## What this PR does\n\ntext\n\n" + block + "\n\n### Release note\n"
	got, err := parseDeferred(body, l)
	if err != nil || len(got) != 2 || len(got[3735].Files) != 3 || got[3735].Reason != "" {
		t.Errorf("parseDeferred = %v, %v; want the conflict #3735 with its files", got, err)
	}
	if e := got[3736]; e.Reason != "breaks ``` <b>e2e</b>" || len(e.Files) != 0 {
		t.Errorf("parseDeferred = %+v; want #3736 held, for its reason on one line", e)
	}
	if strings.ContainsAny(strings.Split(block, "\n")[1], "<>&") {
		t.Errorf("the JSON line carries raw HTML characters: %s", block)
	}

	other, _ := parseLine("release-1.5")
	for name, body := range map[string]string{
		"another line":    body,
		"two blocks":      body + "\n" + block,
		"not JSON":        deferredFence + "\nnot json\n```",
		"an unknown key":  deferredFence + "\n{\"line\":\"release-1.6\",\"deferred\":[],\"x\":1}\n```",
		"a bad number":    deferredFence + "\n{\"line\":\"release-1.6\",\"deferred\":[{\"original\":-1}]}\n```",
		"a repeat":        deferredFence + "\n{\"line\":\"release-1.6\",\"deferred\":[{\"original\":5},{\"original\":5,\"reason\":\"x\"}]}\n```",
		"a bad commit":    deferredFence + "\n{\"line\":\"release-1.6\",\"deferred\":[{\"original\":5,\"commit\":\"-x\"}]}\n```",
		"a broken fence":  deferredFence + "\n{}\nmore\n```",
		"an empty fence":  deferredFence + "\n```",
		"a trailing line": deferredFence,
	} {
		want := l
		if name == "another line" {
			want = other
		}
		if _, err := parseDeferred(body, want); err == nil {
			t.Errorf("%s: parseDeferred accepted it", name)
		}
	}
	if got, err := parseDeferred("no block here", l); err != nil || len(got) != 0 {
		t.Errorf("no block: %v, %v; want nothing deferred", got, err)
	}
}

func TestReleaseNote(t *testing.T) {
	cases := map[string]string{
		"### Release note\n\n```release-note\nfix(x): y\n```\n": "fix(x): y",
		"```release-note\nNONE\n```":                            "",
		"```release-note\n\n```":                                "",
		"<!--\n```release-note\nhidden\n```\n-->":               "",
		"no block": "",
	}
	for body, want := range cases {
		if got := releaseNote(body); got != want {
			t.Errorf("releaseNote(%q) = %q, want %q", body, got, want)
		}
	}
}

// The body is the line's template with the batch's sections filled in, and
// fits the size GitHub accepts.
func TestBodyFollowsTheTemplate(t *testing.T) {
	l, _ := parseLine("release-1.6")
	template := "<!-- tips -->\n\n## What this PR does\n\n\n### Screenshots\n\n<!-- shots -->\n\n### Downstream repositories\n\n- [ ] No downstream repository is affected by this change\n\n### Release note\n\n```release-note\n\n```\n"
	o := &original{number: 7, title: "fix(x): y", body: "```release-note\nfix(x): note\n```"}
	r := &report{line: l, onBranch: []*verdict{{o: o}}, skipped: map[int][]*verdict{}}
	body, _ := r.body(template)
	for _, want := range []string{"## What this PR does\n\nBackports to", "- [#7](https://github.com/cozystack/cozystack/pull/7) `fix(x): y`", "### Downstream repositories\n\n- [ ] No downstream", "```release-note\nfix(x): note\n```", deferredFence} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<!--") {
		t.Errorf("the body keeps template comments:\n%s", body)
	}
	if body, _ := r.body("no headings at all"); !strings.Contains(body, "## What this PR does") || !strings.Contains(body, "```release-note") {
		t.Errorf("a template without the sections gets them added:\n%s", body)
	}

	long := strings.Repeat("y", 4000)
	var many []*verdict
	for i := range 40 {
		many = append(many, &verdict{o: &original{number: i + 1, title: "fix(x): y", body: "```release-note\n" + long + "\n```"}})
	}
	r = &report{line: l, onBranch: many, skipped: map[int][]*verdict{ruleType: many}}
	if body, _ := r.body(template); len(body) > bodyLimit || !strings.Contains(body, "truncated") || !strings.Contains(body, deferredFence) {
		t.Errorf("a body over the limit is %d bytes (limit %d), truncated=%v", len(body), bodyLimit, strings.Contains(body, "truncated"))
	}

	// The longest list goes first; a short one and the notes stay whole.
	var lots, few []*verdict
	for i := range 600 {
		lots = append(lots, &verdict{o: &original{number: 1000 + i, title: "feat(x): " + strings.Repeat("z", 150)}, reason: "type feat"})
	}
	for i := range 3 {
		few = append(few, &verdict{o: &original{number: 10 + i, title: "fix(x): carried"}, reason: "picked in 0123456789ab"})
	}
	conflict := pick{v: &verdict{o: &original{number: 9, title: "fix(x): conflict"}}}
	for i := range 80 {
		conflict.files = append(conflict.files, fmt.Sprintf("chart/f%02d.yaml", i))
	}
	r = &report{line: l, onBranch: many[:1], deferred: []pick{conflict}, skipped: map[int][]*verdict{ruleType: lots, ruleOnLine: few}}
	body, left := r.body(template)
	if !slices.Equal(left, []int{ruleType}) {
		t.Errorf("left out the lists of rules %v, want only rule 2", left)
	}
	for _, want := range []string{"<details><summary>Rule 1", "[#12]", ": 600, too many to list here", "`chart/f04.yaml`, 75 more", `"more":30`} {
		if !strings.Contains(body, want) {
			t.Errorf("the fitted body lacks %q", want)
		}
	}
	for _, unwanted := range []string{"<details><summary>Rule 2", "[#1000]", "`chart/f05.yaml`", "truncated"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the fitted body keeps %q", unwanted)
		}
	}
	if len(body) > bodyLimit {
		t.Errorf("the fitted body is %d bytes", len(body))
	}
}

func TestPRTitle(t *testing.T) {
	l, _ := parseLine("release-1.6")
	if got := prTitle(l, "2026-10-23"); got != "fix(backport): batch for release-1.6, 2026-10-23" {
		t.Errorf("prTitle = %q", got)
	}
}

// Every task page the body links to is in the tree, under the path the link
// names on main.
func TestBodyLinksTaskPagesThatExist(t *testing.T) {
	l, _ := parseLine("release-1.6")
	v := &verdict{o: &original{number: 9, title: "fix(x): conflict"}}
	r := &report{line: l, deferred: []pick{{v: v, files: []string{"a"}}}, held: []pick{{v: &verdict{o: &original{number: 10}}, reason: "r"}}, skipped: map[int][]*verdict{}}
	body, _ := r.body("")
	links := regexp.MustCompile(regexp.QuoteMeta(tasksURL)+`([\w.-]+)`).FindAllStringSubmatch(body, -1)
	if len(links) < 2 {
		t.Fatalf("the body links %d task pages, want the index and the conflict task:\n%s", len(links), body)
	}
	for _, m := range links {
		if _, err := os.Stat(filepath.Join("..", "..", "docs", "release", "backport-tasks", m[1])); err != nil {
			t.Errorf("the body links %s, which is not in the tree: %v", m[0], err)
		}
	}
}
