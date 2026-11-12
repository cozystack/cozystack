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
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A report is one classification of a line: where every original merged into
// main since the line branched stands, and what it was made of. The batch
// pull request's body carries the latest one beside the record, and an issue
// comment the tool never edits keeps each one, so a later run that replaces
// the body loses none of them. Its ID hashes the classification, not the pull
// request it lands on, which a new batch does not have yet when the report is
// made.

const (
	reportFence  = "```backport-batch-report"
	reportMarker = "<!-- backport-batch:report "
	// reportFormat is the format a run writes. Format 2 adds parked
	// originals and partial units; format 1, which has neither, is read too.
	reportFormat = 2
	// reportLimit keeps a report comment well inside GitHub's 65536.
	reportLimit = 60000
)

type reportDoc struct {
	Format    int                 `json:"format"`
	ID        string              `json:"id"`
	Line      string              `json:"line"`
	Built     builtFrom           `json:"built"`
	Overrides reportOverrides     `json:"overrides"`
	Primary   reportPrimary       `json:"primary"`
	Overlays  reportOverlays      `json:"overlays"`
	Diag      map[string][]string `json:"diag"`
	Other     []string            `json:"other"`
}

type reportOverrides struct {
	Include []int `json:"backport/include"`
	Skip    []int `json:"backport/skip"`
}

// reportPrimary puts every original in exactly one place.
type reportPrimary struct {
	Picked   []int           `json:"picked"`
	Missing  []int           `json:"missing"`
	Deferred []deferredEntry `json:"deferred"`
	Parked   []parkedEntry   `json:"parked"`
	Skipped  reportSkipped   `json:"skipped"`
}

type reportSkipped struct {
	OnLine      []int `json:"on-line"`
	BeforeStart []int `json:"before-start"`
	Type        []int `json:"type"`
	Breaking    []int `json:"breaking"`
	Touches     []int `json:"touches"`
	Newer       []int `json:"newer"`
	SkipLabel   []int `json:"skip-label"`
	Dropped     []int `json:"dropped"`
}

// reportOverlays mark originals that are also something else: stale ones
// are excluded, or applied only in part, but still on the batch, partial
// ones those whose unit there leaves part of them without effect, and each
// drop is the record's entry, with the merge commit it is bound to and its
// reason.
type reportOverlays struct {
	Dropped []droppedEntry `json:"dropped"`
	Stale   []int          `json:"stale"`
	Changed []int          `json:"changed"`
	Partial []int          `json:"partial,omitempty"`
}

func (s *reportSkipped) list(rule int) *[]int {
	switch rule {
	case ruleOnLine:
		return &s.OnLine
	case ruleBeforeStart:
		return &s.BeforeStart
	case ruleType:
		return &s.Type
	case ruleBreaking:
		return &s.Breaking
	case ruleTouches:
		return &s.Touches
	case ruleNewer:
		return &s.Newer
	case ruleSkipLabel:
		return &s.SkipLabel
	case ruleDropped:
		return &s.Dropped
	}
	return nil
}

type namedList struct {
	name string
	list *[]int
}

// lists names each skipped list as the report does.
func (s *reportSkipped) lists() []namedList {
	return []namedList{
		{"on-line", &s.OnLine}, {"before-start", &s.BeforeStart}, {"type", &s.Type}, {"breaking", &s.Breaking},
		{"touches", &s.Touches}, {"newer", &s.Newer}, {"skip-label", &s.SkipLabel}, {"dropped", &s.Dropped},
	}
}

func numbers(vs []*verdict) []int {
	out := []int{}
	for _, v := range vs {
		out = append(out, v.o.number)
	}
	return out
}

// deferredEntries is what the record and the report say about the conflicts,
// the deferrals and the waits of a run.
func deferredEntries(conflicts, held, waiting, needs []pick) []deferredEntry {
	out := []deferredEntry{}
	for _, p := range conflicts {
		files := cleanFiles(p.files[:min(len(p.files), filesKept)])
		out = append(out, deferredEntry{Original: p.v.o.number, Commit: p.failed, Files: files, More: len(p.files) - len(files) + p.more})
	}
	for _, p := range held {
		out = append(out, deferredEntry{Original: p.v.o.number, Files: []string{}, Reason: capped(oneLine(clean(p.reason, false)), titleLimit)})
	}
	for _, p := range waiting {
		files := cleanFiles(p.files[:min(len(p.files), filesKept)])
		out = append(out, deferredEntry{Original: p.v.o.number, Files: files, More: len(p.files) - len(files) + p.more, Workflows: true})
	}
	for _, p := range needs {
		out = append(out, deferredEntry{Original: p.v.o.number, Files: []string{}, Needs: slices.Sorted(slices.Values(p.needs))})
	}
	return out
}

// parkedEntries is what the record and the report say about what a run
// parked; whether notify named it is the record's alone.
func parkedEntries(vs []*verdict) []parkedEntry {
	out := []parkedEntry{}
	for _, v := range vs {
		p := *v.park
		p.Announced = false
		out = append(out, p)
	}
	return out
}

// buildReport makes the report of a run whose built is set. Every original
// main merged since the line branched has to land in exactly one primary
// category; one that does not is an error rather than a report that
// silently drops it.
func buildReport(b *batch, r *report) (reportDoc, error) {
	doc := reportDoc{Format: reportFormat, Line: b.line.branch(), Built: r.built, Diag: map[string][]string{}, Other: append([]string{}, b.other...)}
	doc.Overrides = reportOverrides{Include: []int{}, Skip: []int{}}
	for _, v := range r.overrides {
		switch v.label {
		case labelInclude:
			doc.Overrides.Include = append(doc.Overrides.Include, v.o.number)
		case labelSkip:
			doc.Overrides.Skip = append(doc.Overrides.Skip, v.o.number)
		}
	}
	doc.Primary = reportPrimary{Picked: numbers(r.onBranch), Missing: numbers(r.missing), Deferred: deferredEntries(r.deferred, r.held, r.waiting, r.needs), Parked: parkedEntries(r.parked)}
	for _, l := range doc.Primary.Skipped.lists() {
		*l.list = []int{}
	}
	for rule, vs := range r.skipped {
		list := doc.Primary.Skipped.list(rule)
		if list == nil {
			return doc, fmt.Errorf("the report has no place for what rule %d skipped", rule)
		}
		*list = append(*list, numbers(vs)...)
	}
	doc.Overlays = reportOverlays{Dropped: []droppedEntry{}, Stale: numbers(r.stale), Changed: numbers(r.changed())}
	if len(r.partial) > 0 {
		doc.Overlays.Partial = numbers(r.partial)
	}
	for _, v := range b.verdicts {
		if len(v.diag) > 0 {
			doc.Diag[strconv.Itoa(v.o.number)] = slices.Clone(v.diag)
		}
	}
	doc.Overlays.Dropped = append(doc.Overlays.Dropped, r.dropped...)
	doc.canonical()
	where, err := doc.categories()
	if err != nil {
		return doc, err
	}
	for _, v := range b.verdicts {
		if where[v.o.number] == "" {
			return doc, fmt.Errorf("the report of %s puts #%d nowhere", b.line.branch(), v.o.number)
		}
		delete(where, v.o.number)
	}
	for n := range where {
		return doc, fmt.Errorf("the report of %s names #%d, which main did not merge after the line branched", b.line.branch(), n)
	}
	doc.ID = reportID(doc)
	return doc, nil
}

// canonical sorts every list, so equal classifications are equal bytes.
func (d *reportDoc) canonical() {
	lists := []*[]int{&d.Primary.Picked, &d.Primary.Missing, &d.Overrides.Include, &d.Overrides.Skip, &d.Overlays.Stale, &d.Overlays.Changed, &d.Overlays.Partial}
	for _, l := range d.Primary.Skipped.lists() {
		lists = append(lists, l.list)
	}
	for _, l := range lists {
		slices.Sort(*l)
	}
	slices.SortFunc(d.Primary.Deferred, func(a, b deferredEntry) int { return cmp.Compare(a.Original, b.Original) })
	slices.SortFunc(d.Primary.Parked, func(a, b parkedEntry) int { return cmp.Compare(a.Original, b.Original) })
	slices.SortFunc(d.Overlays.Dropped, func(a, b droppedEntry) int { return cmp.Compare(a.Original, b.Original) })
	slices.Sort(d.Other)
}

// categories maps each original the report names to its primary category,
// refusing one named twice.
func (d *reportDoc) categories() (map[int]string, error) {
	where := map[int]string{}
	put := func(n int, category string) error {
		if n <= 0 {
			return fmt.Errorf("the report puts original %d in %s", n, category)
		}
		if prev := where[n]; prev != "" {
			return fmt.Errorf("the report puts #%d in both %s and %s", n, prev, category)
		}
		where[n] = category
		return nil
	}
	for _, n := range d.Primary.Picked {
		if err := put(n, "picked"); err != nil {
			return nil, err
		}
	}
	for _, n := range d.Primary.Missing {
		if err := put(n, "missing"); err != nil {
			return nil, err
		}
	}
	for _, e := range d.Primary.Deferred {
		if err := put(e.Original, "deferred"); err != nil {
			return nil, err
		}
	}
	for _, e := range d.Primary.Parked {
		if err := put(e.Original, "parked"); err != nil {
			return nil, err
		}
	}
	for _, l := range d.Primary.Skipped.lists() {
		for _, n := range *l.list {
			if err := put(n, "skipped."+l.name); err != nil {
				return nil, err
			}
		}
	}
	return where, nil
}

// reportID hashes everything a report says but when and in which job it was
// made, so a run that finds what the last one found makes the same report.
func reportID(doc reportDoc) string {
	doc.ID, doc.Built.At, doc.Built.Run = "", "", ""
	data, _ := json.Marshal(doc)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// storedReport is a report as it was written, whose bytes a replay posts
// unchanged.
type storedReport struct {
	doc reportDoc
	raw string
}

func reportJSON(doc reportDoc) string {
	data, _ := json.Marshal(doc)
	return string(data)
}

func reportBlock(raw string) string { return reportFence + "\n" + raw + "\n```" }

// parseReport reads one report line of a batch pull request against l. An
// ID that matches the content shows only that nobody changed it since it
// was hashed, so everything it says is also held to what a run writes.
func parseReport(raw string, l line) (reportDoc, error) {
	var doc reportDoc
	if err := decodeStrict(raw, &doc); err != nil {
		return doc, fmt.Errorf("reading the report: %w", err)
	}
	if doc.Format != 1 && doc.Format != reportFormat {
		return doc, fmt.Errorf("the report is format %d, which this backport-batch does not read", doc.Format)
	}
	if !reportIDRE.MatchString(doc.ID) || reportID(doc) != doc.ID {
		return doc, fmt.Errorf("report %.12s does not match what it says", terminalSafe(doc.ID))
	}
	if err := doc.validate(l); err != nil {
		return doc, fmt.Errorf("report %.12s %w", doc.ID, err)
	}
	if err := doc.bindDrops(); err != nil {
		return doc, fmt.Errorf("report %.12s %w", doc.ID, err)
	}
	return doc, nil
}

// validate holds a report to what a run writes. Its errors read after
// "report <id>".
func (d *reportDoc) validate(l line) error {
	if d.Line != l.branch() {
		return fmt.Errorf("is for %q, not %s", d.Line, l.branch())
	}
	if err := d.Built.validate(); err != nil {
		return err
	}
	for _, sha := range d.Other {
		if checkSHA(sha) != nil {
			return fmt.Errorf("counts %q as a commit on main", sha)
		}
	}
	if _, err := d.categories(); err != nil {
		return err
	}
	// The deferred and dropped entries are the record's, held to the
	// record's rules.
	kept := lineRecord{Version: 1, Line: d.Line, Deferred: d.Primary.Deferred, Dropped: d.Overlays.Dropped}
	if err := kept.validate(l); err != nil {
		return err
	}
	for n, lines := range d.Diag {
		if k, err := strconv.Atoi(n); err != nil || k <= 0 || strconv.Itoa(k) != n {
			return fmt.Errorf("has diagnostics for %q, which is no original", n)
		}
		for _, ln := range lines {
			if ln != oneLineText(ln) {
				return fmt.Errorf("has a diagnostic for #%s that is not one clean line: %q", n, ln)
			}
		}
	}
	for _, p := range d.Primary.Parked {
		if p.Original <= 0 {
			return fmt.Errorf("names original %d", p.Original)
		}
		if err := p.validate(); err != nil {
			return err
		}
		// Whether notify named it is the record's to say, not the
		// classification's.
		if p.Announced {
			return fmt.Errorf("has #%d announced", p.Original)
		}
	}
	for _, list := range [][]int{d.Overrides.Include, d.Overrides.Skip, d.Overlays.Stale, d.Overlays.Changed, d.Overlays.Partial} {
		for _, n := range list {
			if n <= 0 {
				return fmt.Errorf("names original %d", n)
			}
		}
	}
	return d.links()
}

// links holds the fields of a report that speak of one another to agree as
// a run writes them, so no field says something another contradicts. Its
// errors read after "report <id>".
func (d *reportDoc) links() error {
	where, err := d.categories()
	if err != nil {
		return err
	}
	// Format 1 was written before anything was parked.
	if d.Format == 1 && len(d.Primary.Parked)+len(d.Overlays.Partial) > 0 {
		return errors.New("parks originals, which format 1 does not")
	}
	// What a dependency park needs never reaches the line: it is excluded
	// by a rule, a label, a drop or a park of its own, or a commit on main
	// that merged no pull request.
	other := map[string]bool{}
	for _, sha := range d.Other {
		other[sha] = true
	}
	for _, p := range d.Primary.Parked {
		for _, n := range p.Needs {
			switch c := where[n]; c {
			case "parked", "skipped.before-start", "skipped.type", "skipped.breaking", "skipped.touches", "skipped.skip-label", "skipped.dropped":
			default:
				return fmt.Errorf("parks #%d for #%d, which it has %s", p.Original, n, or(c, "nowhere"))
			}
		}
		if other[p.Merge] || slices.Contains(p.Commits, p.Merge) {
			return fmt.Errorf("parks #%d by a merge it counts as no pull request's, or for its own merge", p.Original)
		}
		for _, c := range p.Commits {
			if !other[c] {
				return fmt.Errorf("parks #%d for %.12s, which it does not count among main's commits that merged no pull request", p.Original, c)
			}
		}
	}
	// A partial unit is still on the batch.
	stale := map[int]bool{}
	for _, n := range d.Overlays.Stale {
		stale[n] = true
	}
	for _, n := range d.Overlays.Partial {
		if !stale[n] {
			return fmt.Errorf("marks #%d partial on the batch, but not still on it", n)
		}
	}
	drops := map[int]bool{}
	for _, e := range d.Overlays.Dropped {
		drops[e.Original] = true
	}
	for _, n := range d.Primary.Skipped.Dropped {
		if !drops[n] {
			return fmt.Errorf("has #%d dropped, but keeps no drop of it", n)
		}
	}
	// Stale marks something a run took or excluded, and changed a pick.
	for _, n := range d.Overlays.Stale {
		if c := where[n]; c != "picked" && !strings.HasPrefix(c, "skipped.") {
			return fmt.Errorf("marks #%d stale, which it has %s", n, or(c, "nowhere"))
		}
	}
	for _, n := range d.Overlays.Changed {
		if c := where[n]; c != "picked" {
			return fmt.Errorf("marks #%d changed, which it has %s", n, or(c, "nowhere"))
		}
	}
	// backport/skip decides before any rule, and backport/include lifts
	// rules 1 to 4.
	skip := map[int]bool{}
	for _, n := range d.Overrides.Skip {
		skip[n] = true
		if where[n] != "skipped.skip-label" {
			return fmt.Errorf("has #%d labelled %s, which it has %s", n, labelSkip, or(where[n], "nowhere"))
		}
	}
	for _, n := range d.Primary.Skipped.SkipLabel {
		if !skip[n] {
			return fmt.Errorf("has #%d skipped for a %s label it does not list", n, labelSkip)
		}
	}
	for _, n := range d.Overrides.Include {
		switch c := where[n]; c {
		case "", "skipped.before-start", "skipped.type", "skipped.breaking", "skipped.touches", "skipped.skip-label":
			return fmt.Errorf("has #%d labelled %s, which it has %s", n, labelInclude, or(c, "nowhere"))
		}
	}
	for n := range d.Diag {
		if k, err := strconv.Atoi(n); err != nil || where[k] == "" {
			return fmt.Errorf("has diagnostics for %q, which it puts nowhere", n)
		}
	}
	for _, e := range d.Primary.Deferred {
		if e.Workflows && !d.Built.Options.NoWorkflowFiles {
			return fmt.Errorf("has #%d waiting for its workflow files, but was made bringing them over", e.Original)
		}
		// What a wait waits on is still to come: not on the line, deferred,
		// or held by rule 6.
		for _, n := range e.Needs {
			switch c := where[n]; c {
			case "missing", "deferred", "skipped.newer":
			default:
				return fmt.Errorf("has #%d wait on #%d, which it has %s", e.Original, n, or(c, "nowhere"))
			}
		}
		if d.Format == 1 && len(e.Needs) > 0 {
			return fmt.Errorf("has #%d wait on other originals, which format 1 does not", e.Original)
		}
	}
	needs := map[int][]int{}
	for _, e := range d.Primary.Deferred {
		needs[e.Original] = e.Needs
	}
	for _, p := range d.Primary.Parked {
		needs[p.Original] = p.Needs
	}
	if err := waitCycle(needs); err != nil {
		return err
	}
	if d.Built.Options.Mode == modeClose && len(d.Primary.Picked)+len(d.Overlays.Stale) > 0 {
		return errors.New("closes the batch, but still has originals on it")
	}
	return nil
}

// bodyReport is the report a batch body against l carries, or nil.
func bodyReport(body string, l line) (*storedReport, error) {
	blocks, err := fencedLines(body, reportFence)
	switch {
	case err != nil:
		return nil, err
	case len(blocks) == 0:
		return nil, nil
	case len(blocks) > 1:
		return nil, fmt.Errorf("the body holds %d %s blocks", len(blocks), reportFence)
	}
	doc, err := parseReport(blocks[0], l)
	if err != nil {
		return nil, err
	}
	return &storedReport{doc: doc, raw: blocks[0]}, nil
}

// reportComment is the comment that keeps a report: a marker naming it, and
// the report folded away in the same block the body carries. Nothing in it
// mentions anyone: numbers are bare, and text sits inside the fence.
//
// Only checked values reach the summary GitHub renders: an ID of hex digits,
// a release line and the date of a time that parses.
func reportComment(s storedReport) (string, error) {
	if !reportIDRE.MatchString(s.doc.ID) {
		return "", fmt.Errorf("report %q has no ID", s.doc.ID)
	}
	if _, err := parseLine(s.doc.Line); err != nil {
		return "", fmt.Errorf("report %.12s: %w", s.doc.ID, err)
	}
	at, err := time.Parse(time.RFC3339, s.doc.Built.At)
	if err != nil {
		return "", fmt.Errorf("report %.12s was made at %q", s.doc.ID, s.doc.Built.At)
	}
	date := at.UTC().Format(time.DateOnly)
	c := reportMarker + s.doc.ID + " -->\n" +
		"<details><summary>Backport batch report for " + s.doc.Line + ", " + date + " (" + s.doc.ID[:12] + ")</summary>\n\n" +
		reportBlock(s.raw) + "\n</details>\n"
	if len(c) > reportLimit {
		return "", fmt.Errorf("the report comment is %d bytes, over the %d it may have", len(c), reportLimit)
	}
	return c, nil
}

var reportMarkerRE = regexp.MustCompile(`^` + regexp.QuoteMeta(reportMarker) + `([0-9a-f]{64}) -->\n`)

// findReports reads the report comments on batch pull request n, against l,
// by authors the batch trusts, as it trusts a record's author, by ID. One
// whose content does not match the ID its marker gives, or is not what a run
// writes, is returned apart, with why; a look-alike by anyone else is not
// read, whatever it says.
func findReports(gh github, n int, l line) (map[string]storedReport, map[string]string, error) {
	comments, err := gh.comments(n)
	if err != nil {
		return nil, nil, err
	}
	found, bad := map[string]storedReport{}, map[string]string{}
	for _, c := range comments {
		if !strings.HasPrefix(c.Body, reportMarker) {
			continue
		}
		why, err := trustedAuthor(gh, c.User.Login, c.User.Type)
		if err != nil {
			return nil, nil, err
		}
		if why != "" {
			continue
		}
		m := reportMarkerRE.FindStringSubmatch(c.Body)
		if m == nil {
			bad[strconv.FormatInt(c.ID, 10)] = fmt.Sprintf("comment %d starts like a report but names none", c.ID)
			continue
		}
		blocks, err := fencedLines(c.Body, reportFence)
		if err == nil && len(blocks) != 1 {
			err = fmt.Errorf("it holds %d reports", len(blocks))
		}
		var doc reportDoc
		if err == nil {
			doc, err = parseReport(blocks[0], l)
		}
		if err == nil && doc.ID != m[1] {
			err = fmt.Errorf("its marker names %.12s", m[1])
		}
		if err != nil {
			bad[m[1]] = fmt.Sprintf("comment %d: %v", c.ID, err)
			continue
		}
		found[m[1]] = storedReport{doc: doc, raw: blocks[0]}
	}
	return found, bad, nil
}

// toolVersion is the commit this binary was built from, with +dirty when its
// tree had changes, or "" when the build did not record it, as go run does
// not.
func toolVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if checkSHA(rev) != nil {
		return ""
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}

// runURL is the Actions run the command runs in, from the variables GitHub
// sets for it, or "" outside one.
func runURL(lookup func(string) (string, bool)) string {
	get := func(key string) string { v, _ := lookup(key); return v }
	url := get("GITHUB_SERVER_URL") + "/" + get("GITHUB_REPOSITORY") + "/actions/runs/" + get("GITHUB_RUN_ID")
	if attempt := get("GITHUB_RUN_ATTEMPT"); attempt != "" && attempt != "1" {
		url += "/attempts/" + attempt
	}
	if !runURLRE.MatchString(url) {
		return ""
	}
	return url
}

// report prints a report a batch pull request keeps, writing nothing, so it
// works on merged and closed batches too. It reads GitHub, and for a report
// that keeps drops also this clone's history at the heads the report was
// made from, which it never fetches.
func (a *app) report(args []string) int {
	fs := a.flags("report")
	number := fs.Int("pr", 0, "the batch pull request")
	id := fs.String("id", "", "the report, by its ID (default: the one the body carries)")
	asJSON := fs.Bool("json", false, "print the report's JSON")
	if err := parse(fs, args); err != nil {
		return 2
	}
	if *number <= 0 {
		return a.fail(fmt.Errorf("--pr %d is not a pull request number", *number))
	}
	if *id != "" && !reportIDRE.MatchString(*id) {
		return a.fail(fmt.Errorf("--id %q is not a report ID", *id))
	}
	pr, err := a.gh.pull(*number)
	if err != nil {
		return a.fail(err)
	}
	l, err := parseLine(pr.Base.Ref)
	if m := batchBranchRE.FindStringSubmatch(pr.Head.Ref); err != nil || m == nil || m[1] != l.branch() || pr.Head.Repo == nil || pr.Head.Repo.FullName != pr.Base.Repo.FullName {
		return a.fail(fmt.Errorf("#%d is not a batch pull request of this repository", pr.Number))
	}
	why, err := trusted(a.gh, pr)
	if err != nil {
		return a.fail(err)
	}
	if why != "" {
		return a.fail(fmt.Errorf("batch pull request #%d was %s, so what it says is not the batch's", pr.Number, why))
	}
	inBody, err := bodyReport(pr.Body, l)
	if err != nil {
		return a.fail(fmt.Errorf("batch pull request #%d: %w", pr.Number, err))
	}
	if *id == "" {
		if inBody == nil {
			return a.fail(fmt.Errorf("batch pull request #%d carries no report in its body; name one with --id", pr.Number))
		}
		*id = inBody.doc.ID
	}
	posted, bad, err := findReports(a.gh, pr.Number, l)
	if err != nil {
		return a.fail(err)
	}
	if why := bad[*id]; why != "" {
		return a.fail(fmt.Errorf("batch pull request #%d: the report %.12s is not what its comment says: %s", pr.Number, *id, why))
	}
	s, inComment := posted[*id]
	switch {
	case inComment:
	case inBody != nil && inBody.doc.ID == *id:
		s = *inBody
	default:
		return a.fail(fmt.Errorf("batch pull request #%d keeps no report %.12s", pr.Number, *id))
	}
	if *asJSON {
		_, _ = fmt.Fprintln(a.stdout, s.raw)
		return 0
	}
	a.printReport(&pr, s, inBody, inComment)
	return 0
}

// printReport prints a report. It was checked when it was read, and every
// field still goes through terminalSafe, so nothing it carries spans lines
// or drives a terminal.
func (a *app) printReport(pr *pullRequest, s storedReport, inBody *storedReport, inComment bool) {
	d, b := s.doc, s.doc.Built
	safe := terminalSafe
	_, _ = fmt.Fprintf(a.stdout, "report %s for %s, made %s by a %s run", safe(d.ID), safe(d.Line), safe(b.At), safe(b.Options.Mode))
	if b.Options.NoWorkflowFiles {
		_, _ = fmt.Fprint(a.stdout, " with --no-workflow-files")
	}
	_, _ = fmt.Fprintln(a.stdout)
	_, _ = fmt.Fprintf(a.stdout, "  main %s, line %s, newer line %s, policy %s\n  batch %s, tool %s, run %s\n", safe(b.Main), safe(b.Line), safe(or(b.Newer, "none")), safe(b.Policy), safe(b.Batch), safe(or(b.Tool, "unknown")), safe(or(b.Run, "none")))
	current := "none"
	if inBody != nil {
		current = safe(inBody.doc.ID)
	}
	switch {
	case inBody != nil && inBody.doc.ID == d.ID:
		_, _ = fmt.Fprintf(a.stdout, "  #%d's body carries this report\n", pr.Number)
	case pr.Head.SHA == b.Batch:
		_, _ = fmt.Fprintf(a.stdout, "  #%d's head is %.12s, the batch this report describes, but its body carries report %.12s\n", pr.Number, safe(b.Batch), current)
	default:
		_, _ = fmt.Fprintf(a.stdout, "  publication unconfirmed: #%d's head is %.12s, not %.12s, and its body carries report %.12s\n", pr.Number, safe(pr.Head.SHA), safe(b.Batch), current)
	}
	if !inComment {
		if prState(pr) == "open" {
			_, _ = fmt.Fprintln(a.stdout, "  comment not posted; the next backport-batch run --push posts it")
		} else {
			_, _ = fmt.Fprintf(a.stdout, "  comment not posted; kept in the body only, since nothing is written to a %s pull request\n", prState(pr))
		}
	}
	type line struct {
		n    int
		text string
	}
	var lines []line
	for _, n := range d.Primary.Picked {
		lines = append(lines, line{n, "picked"})
	}
	for _, n := range d.Primary.Missing {
		lines = append(lines, line{n, "missing"})
	}
	for _, e := range d.Primary.Deferred {
		text := "deferred: "
		switch {
		case e.Workflows:
			text += "waits for a maintainer, changes " + strings.Join(e.Files, ", ")
		case len(e.Needs) > 0:
			var needs []string
			for _, n := range e.Needs {
				needs = append(needs, "#"+strconv.Itoa(n))
			}
			text += "waits on " + strings.Join(needs, ", ")
		case e.Reason != "":
			text += e.Reason
		default:
			text += fmt.Sprintf("conflict at %.12s in %s", e.Commit, strings.Join(e.Files, ", "))
		}
		if e.More > 0 {
			text += fmt.Sprintf(" and %d more", e.More)
		}
		lines = append(lines, line{e.Original, text})
	}
	for _, e := range d.Primary.Parked {
		text := "parked: "
		switch {
		case e.Kind == parkPartial:
			text += "only part of it applies, it would leave " + strings.Join(e.Files, ", ") + " without effect"
		default:
			var needs []string
			for _, n := range e.Needs {
				needs = append(needs, "#"+strconv.Itoa(n))
			}
			for _, c := range e.Commits {
				needs = append(needs, c[:12])
			}
			text += "builds on " + strings.Join(needs, ", ") + ", which never reaches the line"
		}
		lines = append(lines, line{e.Original, text})
	}
	for _, l := range d.Primary.Skipped.lists() {
		for _, n := range *l.list {
			lines = append(lines, line{n, "skipped." + l.name})
		}
	}
	slices.SortFunc(lines, func(x, y line) int { return cmp.Compare(x.n, y.n) })
	for _, l := range lines {
		_, _ = fmt.Fprintf(a.stdout, "#%d %s\n", l.n, terminalSafe(l.text))
	}
	overlay := func(name string, list []int) {
		if len(list) > 0 {
			_, _ = fmt.Fprintf(a.stdout, "%s: %s\n", name, strings.Trim(fmt.Sprint(list), "[]"))
		}
	}
	overlay("stale, still on the batch", d.Overlays.Stale)
	overlay("only part of it applies, still on the batch", d.Overlays.Partial)
	for _, e := range d.Overlays.Dropped {
		_, _ = fmt.Fprintf(a.stdout, "dropped: #%d, merged as %.12s, on #%d: %s\n", e.Original, terminalSafe(e.Merge), e.On, terminalSafe(e.Reason))
	}
	overlay("changed in the pick", d.Overlays.Changed)
	for _, n := range slices.Sorted(maps.Keys(d.Diag)) {
		for _, ln := range d.Diag[n] {
			_, _ = fmt.Fprintf(a.stdout, "#%s: %s\n", terminalSafe(n), terminalSafe(ln))
		}
	}
	if len(d.Other) > 0 {
		other := make([]string, len(d.Other))
		for i, sha := range d.Other {
			other[i] = safe(sha)
		}
		_, _ = fmt.Fprintf(a.stdout, "commits on main that merged no pull request: %s\n", strings.Join(other, ", "))
	}
}

func or(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// postReport writes a report comment on pull request n, after GitHub says
// it is still open: nothing is written to a merged or closed one.
func (a *app) postReport(n int, s storedReport) error {
	if err := a.stillOpen(n); err != nil {
		return err
	}
	c, err := reportComment(s)
	if err != nil {
		return err
	}
	name, err := writeTemp("backport-batch-report-*.md", c)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()
	return a.gh.createComment(n, name)
}

// stillOpen reads pull request n again and refuses to go on unless it is
// open.
func (a *app) stillOpen(n int) error {
	pr, err := a.gh.pull(n)
	if err != nil {
		return err
	}
	if st := prState(&pr); st != "open" {
		return fmt.Errorf("batch pull request #%d is %s now; nothing is written to it", n, st)
	}
	return nil
}

func writeTemp(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
