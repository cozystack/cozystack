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
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A line's record is the backport-batch block of its newest batch pull
// request, read whatever that pull request's state and written only while it
// is open: a merged or closed pull request is never written again, so what it
// recorded stays what the line's later commands act on until a newer batch
// carries a record of its own.

// lineRecord is the one line of JSON in the block. Version 1, the block
// before it had a version, holds only line and deferred; version 3 adds what
// is parked and which units on the batch do only part of their original.
type lineRecord struct {
	Version  int             `json:"version"`
	Line     string          `json:"line"`
	Built    *builtFrom      `json:"built,omitempty"`
	Report   string          `json:"report,omitempty"`
	Closing  *closing        `json:"closing"`
	Deferred []deferredEntry `json:"deferred"`
	Dropped  []droppedEntry  `json:"dropped"`
	Stale    []int           `json:"stale"`
	Parked   []parkedEntry   `json:"parked,omitempty"`
	Partial  []int           `json:"partial,omitempty"`
}

// recordVersion is the version a run writes.
const recordVersion = 3

// lineRecordV2 is version 2, which has none of version 3's keys.
type lineRecordV2 struct {
	Version  int             `json:"version"`
	Line     string          `json:"line"`
	Built    *builtFrom      `json:"built,omitempty"`
	Report   string          `json:"report,omitempty"`
	Closing  *closing        `json:"closing"`
	Deferred []deferredEntry `json:"deferred"`
	Dropped  []droppedEntry  `json:"dropped"`
	Stale    []int           `json:"stale"`
}

// The kinds of park: an original that builds on what never reaches the
// line, and one whose clean pick would leave part of it without effect.
const (
	parkDependency = "dependency"
	parkPartial    = "partial"
)

// parkedEntry is an original the batch leaves out because it cannot land on
// the line whole: what it needs that never comes, or the paths it would
// leave without effect. announced says notify has named it on the open
// batch, for exactly this entry.
type parkedEntry struct {
	Original  int      `json:"original"`
	Merge     string   `json:"merge"`
	Kind      string   `json:"kind"`
	Needs     []int    `json:"needs,omitempty"`
	Commits   []string `json:"commits,omitempty"`
	Files     []string `json:"files,omitempty"`
	Announced bool     `json:"announced,omitempty"`
}

// validate holds a parked entry to what a run writes. Its errors read after
// "the block" or "report <id>".
func (p parkedEntry) validate() error {
	sortedUnique := func(n int, less func(i int) bool) bool {
		for i := 1; i < n; i++ {
			if !less(i) {
				return false
			}
		}
		return true
	}
	switch {
	case checkSHA(p.Merge) != nil:
		return fmt.Errorf("parks #%d by merge %q", p.Original, p.Merge)
	case !sortedUnique(len(p.Needs), func(i int) bool { return p.Needs[i-1] < p.Needs[i] }) || slices.ContainsFunc(p.Needs, func(n int) bool { return n <= 0 || n == p.Original }):
		return fmt.Errorf("parks #%d for originals %v, which are not what a run writes", p.Original, p.Needs)
	case !sortedUnique(len(p.Commits), func(i int) bool { return p.Commits[i-1] < p.Commits[i] }) || slices.ContainsFunc(p.Commits, func(c string) bool { return checkSHA(c) != nil }):
		return fmt.Errorf("parks #%d for commits %q, which are not what a run writes", p.Original, p.Commits)
	case !sortedUnique(len(p.Files), func(i int) bool { return p.Files[i-1] < p.Files[i] }) || !slices.Equal(p.Files, cleanFiles(p.Files)):
		return fmt.Errorf("parks #%d for files %q, which are not what a run writes", p.Original, p.Files)
	}
	switch p.Kind {
	case parkPartial:
		if len(p.Files) == 0 || len(p.Needs)+len(p.Commits) > 0 {
			return fmt.Errorf("parks #%d as partial with no files it leaves without effect, or with what it needs", p.Original)
		}
	case parkDependency:
		if len(p.Needs)+len(p.Commits) == 0 || len(p.Files) > 0 {
			return fmt.Errorf("parks #%d for a dependency it does not name, or with files", p.Original)
		}
	default:
		return fmt.Errorf("parks #%d as %q, which is no kind of park", p.Original, p.Kind)
	}
	return nil
}

// builtFrom is what a classification was made of; batch is the head it
// describes.
type builtFrom struct {
	Main    string     `json:"main"`
	Line    string     `json:"line"`
	Newer   string     `json:"newer"`
	Policy  string     `json:"policy"`
	Batch   string     `json:"batch"`
	Tool    string     `json:"tool"`
	Run     string     `json:"run"`
	At      string     `json:"at"`
	Options runOptions `json:"options"`
}

type runOptions struct {
	NoWorkflowFiles bool   `json:"noWorkflowFiles"`
	Mode            string `json:"mode"`
}

// The operations a record is written by: a run that assembles the batch, and
// the ones that only change what the record says about it.
const (
	modeNew      = "new"
	modeContinue = "continue"
	modeDefer    = "defer"
	modeDrop     = "drop"
	modeUndrop   = "undrop"
	modeClose    = "close"
)

var modes = []string{modeNew, modeContinue, modeDefer, modeDrop, modeUndrop, modeClose}

// closing is written into the record of an open batch that the run is about
// to close: the head it froze, which the close and the branch's deletion are
// leased on.
type closing struct {
	PR     int    `json:"pr"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
}

// droppedEntry is a maintainer's decision that an original stays off the
// line, bound to its merge commit on main.
type droppedEntry struct {
	Original int    `json:"original"`
	Merge    string `json:"merge"`
	Reason   string `json:"reason"`
	On       int    `json:"on"`
	Date     string `json:"date"`
}

var (
	toolRE     = regexp.MustCompile(`^[0-9a-f]{40}(\+dirty)?$`)
	reportIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// oneLineText is s as a run writes a reason: one line, cleaned and capped.
// A record holding anything else was not written by a run, and its text
// would reach a terminal, a body or a comment as someone else wrote it.
func oneLineText(s string) string { return capped(oneLine(clean(s, false)), titleLimit) }

// fencedLines lists the one line of each block that fence opens.
func fencedLines(body, fence string) ([]string, error) {
	var blocks []string
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != fence {
			continue
		}
		if i+2 >= len(lines) || strings.TrimSpace(lines[i+2]) != "```" {
			return nil, fmt.Errorf("the %s block is not one line of JSON", fence)
		}
		blocks = append(blocks, lines[i+1])
		i += 2
	}
	return blocks, nil
}

// decodeStrict reads exactly one JSON value into v, refusing a key v does not
// have and anything after the value.
func decodeStrict(data string, v any) error {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var rest json.RawMessage
	if dec.More() || dec.Decode(&rest) != io.EOF {
		return errors.New("something follows the JSON value")
	}
	return nil
}

// parseRecord reads the line's record from a batch body: nil when the body
// has no block, an error when it has more than one or one it cannot vouch
// for whole.
func parseRecord(body string, l line) (*lineRecord, error) {
	blocks, err := fencedLines(body, deferredFence)
	switch {
	case err != nil:
		return nil, err
	case len(blocks) == 0:
		return nil, nil
	case len(blocks) > 1:
		return nil, fmt.Errorf("the body holds %d %s blocks", len(blocks), deferredFence)
	}
	var head struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal([]byte(blocks[0]), &head); err != nil {
		return nil, fmt.Errorf("reading the %s block: %w", deferredFence, err)
	}
	version := 1
	if head.Version != nil {
		version = *head.Version
	}
	var rec lineRecord
	switch version {
	case 1:
		var v1 struct {
			Version  int             `json:"version"`
			Line     string          `json:"line"`
			Deferred []deferredEntry `json:"deferred"`
		}
		err = decodeStrict(blocks[0], &v1)
		rec = lineRecord{Version: 1, Line: v1.Line, Deferred: v1.Deferred}
	case 2:
		var v2 lineRecordV2
		err = decodeStrict(blocks[0], &v2)
		rec = lineRecordWithout(v2)
	case 3:
		err = decodeStrict(blocks[0], &rec)
	default:
		return nil, fmt.Errorf("the %s block is version %d, which this backport-batch does not read; it reads 1 to %d", deferredFence, version, recordVersion)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the %s block: %w", deferredFence, err)
	}
	if err := rec.validate(l); err != nil {
		return nil, fmt.Errorf("the %s block %w", deferredFence, err)
	}
	return &rec, nil
}

// validate holds the record to what a run writes. Its errors read after
// "the block".
func (rec *lineRecord) validate(l line) error {
	if rec.Line != l.branch() {
		return fmt.Errorf("is for %q, not %s", rec.Line, l.branch())
	}
	seen := map[int]bool{}
	once := func(n int) error {
		if n <= 0 {
			return fmt.Errorf("names original %d", n)
		}
		if seen[n] {
			return fmt.Errorf("names #%d twice", n)
		}
		seen[n] = true
		return nil
	}
	for _, d := range rec.Deferred {
		if err := once(d.Original); err != nil {
			return err
		}
		if d.Commit != "" && checkSHA(d.Commit) != nil {
			return fmt.Errorf("names commit %q for #%d", d.Commit, d.Original)
		}
		// A conflict names the commit that failed and its files, a deferral
		// its reason, and a wait its workflow files.
		kinds := 0
		for _, is := range []bool{d.Commit != "" || len(d.Files) > 0 && !d.Workflows && d.Reason == "", d.Reason != "", d.Workflows} {
			if is {
				kinds++
			}
		}
		switch {
		case d.Workflows && (d.Commit != "" || d.Reason != ""):
			return fmt.Errorf("has #%d wait for a maintainer and defers it as well", d.Original)
		case kinds != 1:
			return fmt.Errorf("does not say whether #%d is a conflict, deferred for a reason or waiting", d.Original)
		case d.Reason != "" && strings.TrimSpace(d.Reason) == "":
			return fmt.Errorf("defers #%d for an empty reason", d.Original)
		case d.Reason != oneLineText(d.Reason):
			return fmt.Errorf("defers #%d for %q, which is no reason a run writes", d.Original, d.Reason)
		case !slices.Equal(d.Files, cleanFiles(d.Files)):
			return fmt.Errorf("names files of #%d that are not as a run writes them: %q", d.Original, d.Files)
		case d.More < 0:
			return fmt.Errorf("counts %d more files for #%d", d.More, d.Original)
		}
	}
	for _, d := range rec.Dropped {
		if err := once(d.Original); err != nil {
			return err
		}
		if _, err := time.Parse(time.DateOnly, d.Date); err != nil || checkSHA(d.Merge) != nil || strings.TrimSpace(d.Reason) == "" || d.On <= 0 {
			return fmt.Errorf("drops #%d with a merge, reason, pull request or date it cannot have", d.Original)
		}
		if d.Reason != oneLineText(d.Reason) {
			return fmt.Errorf("drops #%d for %q, which is no reason a run writes", d.Original, d.Reason)
		}
	}
	for _, p := range rec.Parked {
		if err := once(p.Original); err != nil {
			return err
		}
		if err := p.validate(); err != nil {
			return err
		}
	}
	staleSeen := map[int]bool{}
	for _, n := range rec.Stale {
		if n <= 0 || staleSeen[n] {
			return fmt.Errorf("lists %d as still on the batch more than once or not at all", n)
		}
		staleSeen[n] = true
	}
	partialSeen := map[int]bool{}
	for _, n := range rec.Partial {
		// A unit that does only part of its original is still on the batch.
		if !staleSeen[n] || partialSeen[n] {
			return fmt.Errorf("lists #%d as a partial unit more than once, or not as still on the batch", n)
		}
		partialSeen[n] = true
	}
	if rec.Version < 2 {
		return nil
	}
	if b := rec.Built; b == nil {
		return errors.New("does not say what it was built from")
	} else if err := b.validate(); err != nil {
		return err
	}
	if !reportIDRE.MatchString(rec.Report) {
		return fmt.Errorf("names report %q", rec.Report)
	}
	if c := rec.Closing; c != nil {
		m := batchBranchRE.FindStringSubmatch(c.Branch)
		if c.PR <= 0 || m == nil || m[1] != l.branch() || checkSHA(c.Head) != nil {
			return fmt.Errorf("closes #%d on %q at %q, which is no batch of %s", c.PR, c.Branch, c.Head, l.branch())
		}
	}
	return nil
}

func (b *builtFrom) validate() error {
	for _, sha := range []string{b.Main, b.Line, b.Policy, b.Batch} {
		if checkSHA(sha) != nil {
			return fmt.Errorf("says it was built from %q", sha)
		}
	}
	if b.Newer != "" && checkSHA(b.Newer) != nil {
		return fmt.Errorf("says it was built from %q", b.Newer)
	}
	if b.Tool != "" && !toolRE.MatchString(b.Tool) {
		return fmt.Errorf("names tool %q", b.Tool)
	}
	if b.Run != "" && !runURLRE.MatchString(b.Run) {
		return fmt.Errorf("names run %q", b.Run)
	}
	if _, err := time.Parse(time.RFC3339, b.At); err != nil {
		return fmt.Errorf("was built at %q", b.At)
	}
	if !slices.Contains(modes, b.Options.Mode) {
		return fmt.Errorf("was written by %q, which is no operation of backport-batch", b.Options.Mode)
	}
	return nil
}

var runURLRE = regexp.MustCompile(`^https://[^/\s]+/[^/\s]+/[^/\s]+/actions/runs/[0-9]+(/attempts/[0-9]+)?$`)

// lineRecordWithout is a version 2 record as the newest version holds it.
func lineRecordWithout(v2 lineRecordV2) lineRecord {
	return lineRecord{Version: v2.Version, Line: v2.Line, Built: v2.Built, Report: v2.Report, Closing: v2.Closing, Deferred: v2.Deferred, Dropped: v2.Dropped, Stale: v2.Stale}
}

// recordBlock is the block that holds rec. One line of JSON: encoding/json
// escapes newlines and <, > and &, so nothing in a file name or a reason can
// end the fence or open HTML.
func recordBlock(rec lineRecord) string {
	data, _ := json.Marshal(rec)
	return deferredFence + "\n" + string(data) + "\n```"
}

// savedNoWorkflowFiles is the --no-workflow-files the open batch was last
// assembled with, which an operation that only changes the record keeps. A
// record from before it was saved says so by listing a wait.
func (s recordSource) savedNoWorkflowFiles() bool {
	if !s.open || s.rec == nil {
		return false
	}
	if s.rec.Built != nil {
		return s.rec.Built.Options.NoWorkflowFiles
	}
	return slices.ContainsFunc(s.rec.Deferred, func(d deferredEntry) bool { return d.Workflows })
}

// dropped is what the record drops, which every later record keeps.
func (rec *lineRecord) dropped() []droppedEntry {
	if rec == nil {
		return nil
	}
	return slices.Clone(rec.Dropped)
}

// deferred maps the originals the record defers to their entries.
func (rec *lineRecord) deferred() map[int]deferredEntry {
	out := map[int]deferredEntry{}
	if rec != nil {
		for _, d := range rec.Deferred {
			out[d.Original] = d
		}
	}
	return out
}

// recordSource is where a line's record was found.
type recordSource struct {
	// carrier is the pull request whose block is the record, nil when the
	// line has none.
	carrier *pullRequest
	// open says the carrier is the open batch, so the record may be written.
	open bool
	rec  *lineRecord
	// code is the open batch pull request, if any. One whose body someone
	// wrote by hand is the batch the line builds on but carries no record.
	code *pullRequest
}

// closingOpen is the close the record says is under way on a batch that is
// still open, or nil.
func (s recordSource) closingOpen() *closing {
	if !s.open || s.rec == nil {
		return nil
	}
	return s.rec.Closing
}

func prState(pr *pullRequest) string {
	switch {
	case pr.MergedAt != nil:
		return "merged"
	case pr.State == "" || pr.State == "open":
		return "open"
	case pr.State == "closed":
		return "closed"
	}
	return fmt.Sprintf("in state %q", pr.State)
}

func hasRecord(body string) bool {
	return slices.ContainsFunc(strings.Split(body, "\n"), func(ln string) bool { return strings.TrimSpace(ln) == deferredFence })
}

// findLineRecord finds the line's record: the block of the newest pull
// request opened from a batch branch of this repository for the line by an
// author the batch trusts, open, merged or closed. open is the open batch
// pull request batchPull chose, or nil. Anything that leaves in doubt which
// record is the newest is an error, never a fall back to an older one: a
// newer block by an author the batch does not trust, a block the bot wrote
// and someone removed, a record it cannot read, a closed record newer than
// the open batch, or more pull requests than it reads.
func findLineRecord(gh github, l line, open *pullRequest) (recordSource, error) {
	src := recordSource{code: open}
	for page := 1; src.carrier == nil; page++ {
		if page > recentPullsPages {
			return src, fmt.Errorf("none of the %d newest pull requests against %s is a batch pull request with a record, so the line's record cannot be told apart from none", recentPullsPages*recentPullsPage, l.branch())
		}
		prs, err := gh.recentPulls(l.branch(), page)
		if err != nil {
			return src, err
		}
		for i := range prs {
			pr := &prs[i]
			m := batchBranchRE.FindStringSubmatch(pr.Head.Ref)
			if m == nil || m[1] != l.branch() || pr.Base.Ref != l.branch() || pr.Head.Repo == nil || pr.Head.Repo.FullName != pr.Base.Repo.FullName {
				continue
			}
			if open != nil && pr.Number == open.Number {
				pr = open
			}
			if !hasRecord(pr.Body) {
				if pr.User.Type == "Bot" && gh.batchBot() != "" && pr.User.Login == gh.batchBot() {
					return src, fmt.Errorf("batch pull request #%d (%s) was opened by %s but carries no %s block; someone removed it, so the line's record is unknown", pr.Number, prState(pr), pr.User.Login, deferredFence)
				}
				continue
			}
			why, err := trusted(gh, *pr)
			if err != nil {
				return src, fmt.Errorf("batch pull request #%d: %w", pr.Number, err)
			}
			if why != "" {
				return src, fmt.Errorf("batch pull request #%d (%s) carries a %s block but was %s; an older record is not taken in its place", pr.Number, prState(pr), deferredFence, why)
			}
			rec, err := parseRecord(pr.Body, l)
			if err != nil {
				return src, fmt.Errorf("batch pull request #%d (%s): %w", pr.Number, prState(pr), err)
			}
			src.carrier, src.rec = pr, rec
			break
		}
		if len(prs) < recentPullsPage {
			break
		}
	}
	switch c := src.carrier; {
	case open != nil && hasRecord(open.Body) && (c == nil || c.Number < open.Number):
		return src, fmt.Errorf("GitHub does not list the open batch pull request #%d among the pull requests against %s; run again", open.Number, l.branch())
	case open != nil && hasRecord(open.Body) && c.Number > open.Number:
		return src, fmt.Errorf("batch pull request #%d (%s) carries a record newer than the open batch pull request #%d; close one of them by hand", c.Number, prState(c), open.Number)
	case open != nil && c == open:
		src.open = true
	case c != nil && prState(c) == "open":
		return src, fmt.Errorf("batch pull request #%d carries the line's record and is open, but is not the batch this run builds on", c.Number)
	}
	return src, nil
}
