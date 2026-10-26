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
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Titles, release notes and file names in the body were written by whoever
// opened the originals, and the body is published under the name of whoever
// runs --push. None of it is searched for what might be dangerous and then
// rendered by other rules: a title, a reason or a file name goes whole into a
// code span, and the release notes into the fenced release-note block, both
// of which GitHub shows as written, with no mention, reference, link or HTML
// in them acted on. neutralNote is for a note copied out of that block.

const ourRepoURL = "https://github.com/" + upstreamRepo + "/"

const (
	titleLimit = 200
	// GitHub refuses a pull request body longer than this.
	bodyLimit = 65536
)

var (
	backtickRunRE = regexp.MustCompile("`+")
	fenceRunRE    = regexp.MustCompile("`{3,}|~{3,}")
	mdLinkRE      = regexp.MustCompile(`!?\[([^\]]*)\]\(([^)]*)\)`)
	urlRE         = regexp.MustCompile("(?i)\\b(?:https?|ftp)://[^\\s<>()\\[\\]`]+|\\bwww\\.[^\\s<>()\\[\\]`]+")
	// A closing keyword with its target, a cross-repository reference and a
	// mention, wherever they stand: no exception for what precedes them, so
	// nothing depends on agreeing with GitHub about word boundaries.
	actingRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b\s*:?\s*(?:(?:[\w.-]+/[\w.-]+)?#\d+|https?://github\.com/[\w.-]+/[\w.-]+/(?:issues|pull)/\d+)` +
		`|[\w.-]+/[\w.-]+#\d+|@[A-Za-z0-9][A-Za-z0-9-]*(?:/[A-Za-z0-9_.-]+)?`)
	// A link into this repository survives, unless a dot segment or an escape
	// could take it somewhere else.
	safeURLRE    = regexp.MustCompile(`^` + regexp.QuoteMeta(ourRepoURL) + `[\w/?=&#+.-]*$`)
	releaseNotRE = regexp.MustCompile("(?ms)^[ \t]*```+[ \t]*release-note[ \t]*\n(.*?)^[ \t]*```")
	headingRE    = regexp.MustCompile(`^#{1,6} `)
	placeholders = []string{"", "none", "n/a", "na", "no", "-", "nothing"}
)

// clean drops what is not text: control characters other than tab and, when
// asked, newline, which a terminal acts on, and format characters such as the
// zero-width and bidi ones, which make text read as something it is not.
// Invalid UTF-8, a raw 8-bit escape among it, becomes U+FFFD.
func clean(s string, newlines bool) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case r == '\t', r == '\n' && newlines:
			b.WriteRune(r)
		case r == '\r' || unicode.Is(unicode.Cf, r):
		case r == '\n', r == '\u2028', r == '\u2029':
			b.WriteRune(' ')
		case unicode.IsControl(r):
			b.WriteRune('\uFFFD')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

func capped(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return strings.TrimRightFunc(string(r[:limit-1]), unicode.IsSpace) + "…"
	}
	return s
}

// codeSpan puts s on one line in a code span: its fence is longer than any
// backtick run inside, so nothing in s can close it.
func codeSpan(s string) string {
	s = oneLine(clean(s, false))
	longest := 0
	for _, run := range backtickRunRE.FindAllString(s, -1) {
		longest = max(longest, len(run))
	}
	fence := strings.Repeat("`", longest+1)
	pad := ""
	if s == "" || strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return fence + pad + s + pad + fence
}

// titleSpan is a carried title as the body shows it.
func titleSpan(s string) string { return codeSpan(capped(oneLine(clean(s, false)), titleLimit)) }

func prLink(n int) string {
	return fmt.Sprintf("[#%d](%spull/%d)", n, ourRepoURL, n)
}

func ours(url string) bool {
	return safeURLRE.MatchString(url) && !strings.Contains(url, "/.")
}

// inertSigns swaps the characters GitHub acts on for their fullwidth forms,
// which read the same and act on nothing. Unlike a code span, the swap does
// not depend on how the backticks around it pair up.
var inertSigns = strings.NewReplacer("@", "＠", "#", "＃", "://", "：//")

// neutralNote makes a release note safe inside the fenced block and inert
// outside it: no run of backticks or tildes is left to close the fence, links
// lose their target unless it is this repository, and closing keywords,
// cross-repository references and mentions lose the signs that make them
// act. Each step runs on the output of the one before, so a mention a link's
// text carried is still found.
func neutralNote(s string) string {
	s = clean(s, true)
	s = mdLinkRE.ReplaceAllString(s, "$1 ($2)")
	s = urlRE.ReplaceAllStringFunc(s, func(u string) string {
		if ours(u) {
			return u
		}
		return "(link removed)"
	})
	s = actingRE.ReplaceAllStringFunc(s, inertSigns.Replace)
	return fenceRunRE.ReplaceAllStringFunc(s, func(f string) string { return strings.Repeat("'", len(f)) })
}

// releaseNote is the release-note block of a body, or "" when it has none or
// only a placeholder.
func releaseNote(body string) string {
	m := releaseNotRE.FindStringSubmatch(htmlCommentRE.ReplaceAllString(body, ""))
	if m == nil {
		return ""
	}
	note := strings.TrimSpace(m[1])
	if slices.Contains(placeholders, strings.Trim(strings.ToLower(note), " .")) {
		return ""
	}
	return note
}

// deferredFence opens the block check reads the deferred conflicts from.
const deferredFence = "```backport-batch"

type deferredEntry struct {
	Original int      `json:"original"`
	Files    []string `json:"files"`
	More     int      `json:"more,omitempty"`
}

// How many conflicting files of one original the body names, in the list and
// in the block: a vendored chart can conflict in hundreds.
const (
	filesListed = 5
	filesKept   = 50
)

type deferredRecord struct {
	Line     string          `json:"line"`
	Deferred []deferredEntry `json:"deferred"`
}

// deferredBlock is the machine-readable list of deferred conflicts. One line
// of JSON: encoding/json escapes newlines and <, > and &, so nothing in a
// file name can end the fence or open HTML.
func deferredBlock(l line, conflicts []pick) string {
	rec := deferredRecord{Line: l.branch(), Deferred: []deferredEntry{}}
	for _, p := range conflicts {
		files := p.files[:min(len(p.files), filesKept)]
		rec.Deferred = append(rec.Deferred, deferredEntry{Original: p.v.o.number, Files: files, More: len(p.files) - len(files)})
	}
	data, _ := json.Marshal(rec)
	return deferredFence + "\n" + string(data) + "\n```"
}

// parseDeferred reads the deferred conflicts back from a batch body: exactly
// one block, for this line.
func parseDeferred(body string, l line) (map[int]bool, error) {
	var blocks []string
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != deferredFence {
			continue
		}
		if i+2 >= len(lines) || strings.TrimSpace(lines[i+2]) != "```" {
			return nil, fmt.Errorf("the %s block is not one line of JSON", deferredFence)
		}
		blocks = append(blocks, lines[i+1])
		i += 2
	}
	switch len(blocks) {
	case 0:
		return map[int]bool{}, nil
	case 1:
	default:
		return nil, fmt.Errorf("the body holds %d %s blocks", len(blocks), deferredFence)
	}
	dec := json.NewDecoder(strings.NewReader(blocks[0]))
	dec.DisallowUnknownFields()
	var rec deferredRecord
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("reading the %s block: %w", deferredFence, err)
	}
	if rec.Line != l.branch() {
		return nil, fmt.Errorf("the %s block is for %q, not %s", deferredFence, rec.Line, l.branch())
	}
	out := map[int]bool{}
	for _, d := range rec.Deferred {
		if d.Original <= 0 {
			return nil, fmt.Errorf("the %s block names original %d", deferredFence, d.Original)
		}
		out[d.Original] = true
	}
	return out, nil
}

// prTitle is built from the line and the date alone, never from carried text.
func prTitle(l line, date string) string {
	return fmt.Sprintf("fix(backport): batch for %s, %s", l.branch(), date)
}

// skipRules are the rules a skipped original is reported under, in order.
var skipRules = []int{ruleBeforeStart, ruleOnLine, ruleType, ruleBreaking, ruleTouches, ruleNewer}

// report is what one run put on the batch branch and what it left out.
type report struct {
	line     line
	newer    *line
	onBranch []*verdict // carried from --onto and picked now, in merge order
	deferred []pick
	skipped  map[int][]*verdict
	other    int
	// overrides are the originals a backport/ label kept out or let in.
	overrides []*verdict
	start     time.Time
}

func (r *report) ruleTitle(rule int) string {
	switch rule {
	case ruleBeforeStart:
		return "Rule 1, merged before " + codeSpan(r.line.branch()) + " started under the policy on " + r.start.Format(time.DateOnly)
	case ruleOnLine:
		return "Rule 1, already on " + codeSpan(r.line.branch())
	case ruleType:
		return "Rule 2, the title type is not `fix` or `security`, or the scope is `deps`"
	case ruleBreaking:
		return "Rule 3, a breaking change"
	case ruleTouches:
		return "Rule 4, touches platform migrations, `targetVersion`, `api/` or a CRD"
	case ruleNewer:
		return "Rule 6, not on " + codeSpan(r.newer.branch()) + " yet"
	}
	return fmt.Sprintf("Rule %d", rule)
}

// what is the What this PR does section, with the skipped lists of the rules
// in details written out.
func (r *report) what(details map[int]bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Backports to %s from `main`, picked by the rules of the [backport policy](%s) and applied with `git cherry-pick -x` in merge order. Each commit keeps the original message and names its original in a `(cherry picked from commit …)` line.\n\n", codeSpan(r.line.branch()), policyURL)
	if len(r.onBranch) == 0 {
		b.WriteString("Nothing is picked yet.\n")
	}
	for _, v := range r.onBranch {
		fmt.Fprintf(&b, "- %s %s\n", prLink(v.o.number), titleSpan(v.o.title))
	}
	if len(r.deferred) > 0 {
		b.WriteString("\n### Deferred conflicts\n\nThese do not pick cleanly onto this branch. Each moves to the next batch, unless a resolution checked with `backport-batch fold` is pushed here first.\n\n")
		for _, p := range r.deferred {
			var files []string
			for _, f := range p.files[:min(len(p.files), filesListed)] {
				files = append(files, codeSpan(f))
			}
			if more := len(p.files) - len(files); more > 0 {
				files = append(files, fmt.Sprintf("%d more", more))
			}
			fmt.Fprintf(&b, "- %s %s: %s\n", prLink(p.v.o.number), titleSpan(p.v.o.title), strings.Join(files, ", "))
		}
	}
	if len(r.overrides) > 0 {
		b.WriteString("\n### Maintainer overrides\n\n")
		for _, v := range r.overrides {
			fmt.Fprintf(&b, "- %s %s: %s, %s\n", prLink(v.o.number), titleSpan(v.o.title), codeSpan(v.label), r.outcome(v))
		}
	}
	b.WriteString("\n### Skipped\n\n")
	for _, rule := range skipRules {
		items := r.skipped[rule]
		if len(items) == 0 {
			continue
		}
		left := ""
		if !details[rule] {
			left = ", too many to list here; the run's output lists them"
		}
		fmt.Fprintf(&b, "- %s: %d%s\n", r.ruleTitle(rule), len(items), left)
	}
	if r.other > 0 {
		fmt.Fprintf(&b, "- Commits on `main` that merged no pull request: %d\n", r.other)
	}
	for _, rule := range skipRules {
		items := r.skipped[rule]
		if len(items) > 0 && details[rule] {
			fmt.Fprintf(&b, "\n<details><summary>%s</summary>\n\n", r.ruleTitle(rule))
			for _, v := range items {
				fmt.Fprintf(&b, "- %s %s, %s\n", prLink(v.o.number), titleSpan(v.o.title), titleSpan(v.reason))
			}
			b.WriteString("\n</details>\n")
		}
	}
	b.WriteString("\n" + deferredBlock(r.line, r.deferred) + "\n")
	return b.String()
}

// outcome is what became of an original a label overrode.
func (r *report) outcome(v *verdict) string {
	switch {
	case v.label == labelSkip:
		return "left out"
	case slices.Contains(r.onBranch, v):
		return "on this branch"
	case slices.ContainsFunc(r.deferred, func(p pick) bool { return p.v == v }):
		return "a deferred conflict"
	case v.rule != 0:
		return "skipped by rule " + strconv.Itoa(v.rule) + ", " + titleSpan(v.reason)
	}
	return "not picked"
}

func (r *report) notes() string {
	var notes []string
	for _, v := range r.onBranch {
		if n := releaseNote(v.o.body); n != "" {
			notes = append(notes, neutralNote(n))
		}
	}
	return strings.Join(notes, "\n\n")
}

// section is one heading of the pull request template and what follows it.
type section struct {
	heading string
	text    string
}

func sections(template string) (string, []section) {
	var lead strings.Builder
	var out []section
	inFence := false
	for _, ln := range strings.Split(htmlCommentRE.ReplaceAllString(template, ""), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inFence = !inFence
		}
		if !inFence && headingRE.MatchString(ln) {
			out = append(out, section{heading: ln})
			continue
		}
		if len(out) == 0 {
			lead.WriteString(ln + "\n")
		} else {
			out[len(out)-1].text += ln + "\n"
		}
	}
	return strings.TrimSpace(lead.String()), out
}

func headingIs(heading, name string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.TrimLeft(heading, "#")), name)
}

// body fills the line's own pull request template: the What, Screenshots and
// Release note sections are the batch's, every other section stays as the
// template has it. A body over GitHub's limit loses its longest skipped lists
// first, and its release notes' tail last; left out are the rules whose lists
// it lost.
func (r *report) body(template string) (body string, left []int) {
	details := map[int]bool{}
	for rule, items := range r.skipped {
		details[rule] = len(items) > 0
	}
	notes := r.notes()
	for {
		full := r.render(template, r.what(details), notes)
		if len(full) <= bodyLimit {
			return full, left
		}
		longest := 0
		for _, rule := range skipRules {
			if details[rule] && (longest == 0 || len(r.skipped[rule]) > len(r.skipped[longest])) {
				longest = rule
			}
		}
		if longest == 0 {
			const marker = "\n\n(release notes truncated; see the original pull requests)"
			cut := max(0, len(notes)-(len(full)-bodyLimit)-len(marker))
			for cut > 0 && !utf8Start(notes[cut]) {
				cut--
			}
			return r.render(template, r.what(details), notes[:cut]+marker), left
		}
		details[longest] = false
		left = append(left, longest)
	}
}

func utf8Start(c byte) bool { return c&0xC0 != 0x80 }

func (r *report) render(template, what, notes string) string {
	lead, secs := sections(template)
	var b strings.Builder
	if lead != "" {
		b.WriteString(lead + "\n\n")
	}
	seen := map[string]bool{}
	for _, s := range secs {
		switch {
		case headingIs(s.heading, "What this PR does"):
			seen["what"] = true
			b.WriteString(s.heading + "\n\n" + what + "\n")
		case headingIs(s.heading, "Screenshots"):
			b.WriteString(s.heading + "\n\nSee the original pull requests.\n\n")
		case headingIs(s.heading, "Release note"):
			seen["note"] = true
			b.WriteString(s.heading + "\n\n```release-note\n" + notes + "\n```\n\n")
		default:
			b.WriteString(s.heading + "\n\n" + strings.TrimSpace(s.text) + "\n\n")
		}
	}
	out := b.String()
	if !seen["what"] {
		out = "## What this PR does\n\n" + what + "\n" + out
	}
	if !seen["note"] {
		out += "### Release note\n\n```release-note\n" + notes + "\n```\n"
	}
	return strings.TrimSpace(regexp.MustCompile(`\n{3,}`).ReplaceAllString(out, "\n\n")) + "\n"
}
