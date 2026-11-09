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
// before it had a version, holds only line and deferred.
type lineRecord struct {
	Version  int             `json:"version"`
	Line     string          `json:"line"`
	Built    *builtFrom      `json:"built,omitempty"`
	Report   string          `json:"report,omitempty"`
	Closing  *closing        `json:"closing"`
	Deferred []deferredEntry `json:"deferred"`
	Dropped  []droppedEntry  `json:"dropped"`
	Stale    []int           `json:"stale"`
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
		err = decodeStrict(blocks[0], &rec)
	default:
		return nil, fmt.Errorf("the %s block is version %d, which this backport-batch does not read; it reads 1 and 2", deferredFence, version)
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
	}
	staleSeen := map[int]bool{}
	for _, n := range rec.Stale {
		if n <= 0 || staleSeen[n] {
			return fmt.Errorf("lists %d as still on the batch more than once or not at all", n)
		}
		staleSeen[n] = true
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
	case pr.State == "":
		return "open"
	}
	return pr.State
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
