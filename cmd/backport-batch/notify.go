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
	"regexp"
	"slices"
	"strings"
	"time"
)

// notifyMarker starts the one comment notify keeps on a batch pull request.
const notifyMarker = "<!-- backport-batch:notify -->"

// stalledAfter is how many days a batch stays open before notify calls it
// stalled: the shorter gap between two scheduled runs, Monday to Thursday, so
// a batch the previous run opened or continued is stalled at the next one.
const stalledAfter = 3

var mentionRE = regexp.MustCompile(`^@?([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:/[A-Za-z0-9_.-]+)?)$`)

// notify reports what waits on the open batch pull request of a line for a
// maintainer: a batch open since before the previous run, the conflicts it
// deferred, and the originals that change workflows. One line per finding goes
// to stdout, for the run's summary. --push writes them into the comment, one
// per pull request, updated in place on every run.
func (a *app) notify(args []string) int {
	fs := a.flags("notify")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	mention := fs.String("mention", "", "team or person the comment mentions, org/team or login, with or without @")
	push := fs.Bool("push", false, "write the comment")
	if err := parse(fs, args); err != nil {
		return 2
	}
	l, err := parseLine(*lineArg)
	if err != nil {
		return a.fail(err)
	}
	if *mention != "" {
		m := mentionRE.FindStringSubmatch(*mention)
		if m == nil {
			return a.fail(fmt.Errorf("--mention %q is not @login or @org/team", *mention))
		}
		*mention = "@" + m[1]
	}
	prs, err := a.gh.openPulls(l.branch())
	if err != nil {
		return a.fail(err)
	}
	pr, warnings, err := batchPull(a.gh, prs, l)
	for _, w := range warnings {
		a.warn("%s", w)
	}
	if err != nil {
		return a.fail(err)
	}
	// With no batch open there is nothing to report on and nowhere to write,
	// but a record nobody can read is said all the same.
	src, err := findLineRecord(a.gh, l, pr)
	if err != nil {
		return a.fail(err)
	}
	if c := src.closingOpen(); c != nil {
		return a.fail(fmt.Errorf("batch pull request #%d is being closed; backport-batch run --line %s --push finishes it", c.PR, l.branch()))
	}
	if pr == nil {
		return 0
	}
	opened, err := branchDate(pr.Head.Ref)
	if err != nil {
		return a.fail(err)
	}
	deferred := map[int]deferredEntry{}
	var partial []int
	var parks []parkedEntry
	if src.open {
		deferred, partial = src.rec.deferred(), src.rec.Partial
		for _, p := range src.rec.Parked {
			if !p.Announced {
				parks = append(parks, p)
			}
		}
	}
	// An original that waits on others is listed once, after what decides
	// its wait, where that one is listed.
	var conflicts, waiting []int
	after := map[int][]int{}
	for n, e := range deferred {
		switch placement(deferred, n, map[int]bool{}) {
		case "workflow":
			waiting = append(waiting, n)
		case "conflict":
			conflicts = append(conflicts, n)
		}
		if len(e.Needs) > 0 {
			after[n] = e.Needs
		}
	}
	slices.Sort(conflicts)
	slices.Sort(waiting)
	item := func(n int) string {
		if needs := after[n]; len(needs) > 0 {
			return fmt.Sprintf("\n- %s, after %s", prLink(n), prLinks(needs))
		}
		return "\n- " + prLink(n)
	}
	count := func(ns []int) int {
		c := 0
		for _, n := range ns {
			if len(after[n]) == 0 {
				c++
			}
		}
		return c
	}

	today := a.now().UTC().Truncate(24 * time.Hour)
	days := int(today.Sub(opened).Hours() / 24)
	stalled := days >= stalledAfter
	var text []string
	if stalled {
		_, _ = fmt.Fprintf(a.stdout, "%s: batch pull request #%d has been open since %s, %d days\n", l.branch(), pr.Number, opened.Format(time.DateOnly), days)
		text = append(text, fmt.Sprintf("This batch has been open since %s, %d days, through at least one scheduled run. Merge it, or close it and the next run starts a new one; until then each run adds what the rules take to it.", opened.Format(time.DateOnly), days))
	}
	if len(partial) > 0 {
		_, _ = fmt.Fprintf(a.stdout, "%s: %d units on #%d carry only part of their originals\n", l.branch(), len(partial), pr.Number)
		var b strings.Builder
		b.WriteString("These are on the batch but carry only part of their originals on this line; take each out with `backport-batch run --defer` before the cut, and port what applies as a pull request of its own:\n")
		for _, n := range partial {
			b.WriteString(item(n))
		}
		text = append(text, b.String())
	}
	if len(conflicts) > 0 {
		_, _ = fmt.Fprintf(a.stdout, "%s: %d deferred conflicts on #%d wait for a maintainer\n", l.branch(), count(conflicts), pr.Number)
		var b strings.Builder
		fmt.Fprintf(&b, "These conflicts wait for a maintainer, as [resolving a conflict](%sresolve-conflict.md) describes; unresolved, each goes to the next batch, and so does what waits on one:\n", tasksURL)
		for _, n := range conflicts {
			b.WriteString(item(n))
		}
		text = append(text, b.String())
	}
	if len(waiting) > 0 {
		if count(waiting) == len(waiting) {
			_, _ = fmt.Fprintf(a.stdout, "%s: %d originals on #%d change %s and wait for a maintainer\n", l.branch(), len(waiting), pr.Number, workflowsDir)
		} else {
			_, _ = fmt.Fprintf(a.stdout, "%s: %d originals on #%d change %s or wait on one that does\n", l.branch(), len(waiting), pr.Number, workflowsDir)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "These change `%s`, which the batch's token may not push, and wait for a maintainer to bring them over, as [changes to workflows](%s#changes-to-workflows) describes, or wait on one that does:\n", workflowsDir, policyURL)
		for _, n := range waiting {
			b.WriteString(item(n))
		}
		text = append(text, b.String())
	}
	if len(parks) > 0 {
		_, _ = fmt.Fprintf(a.stdout, "%s: %d parked originals on #%d, named once\n", l.branch(), len(parks), pr.Number)
		var b strings.Builder
		fmt.Fprintf(&b, "These cannot land on this line whole, so the batch parked them; each is named here once. A maintainer ports what applies as a pull request of its own, as [fixes that exist only on a release line](%s#fixes-that-exist-only-on-a-release-line) describes, or drops the original with `backport-batch run --drop`:\n", policyURL)
		for _, p := range parks {
			fmt.Fprintf(&b, "\n- %s: %s", prLink(p.Original), parkReason(p))
		}
		text = append(text, b.String())
	}
	if !*push {
		return 0
	}
	if code := a.comment(pr, *mention, today, text); code != 0 || len(parks) == 0 {
		return code
	}
	if err := a.announced(pr, src.rec, parks); err != nil {
		return a.fail(fmt.Errorf("%w; the next notify names them again", err))
	}
	return 0
}

// announced marks parks as named in the record of the open batch pull
// request, which the comment just named: only while it is open and its body
// still carries the record notify read, so a run that rewrote the body
// meanwhile loses nothing; the next notify names those parks again.
func (a *app) announced(pr *pullRequest, rec *lineRecord, parks []parkedEntry) error {
	marked := *rec
	marked.Parked = slices.Clone(rec.Parked)
	for i, p := range marked.Parked {
		if slices.ContainsFunc(parks, func(q parkedEntry) bool { return q.Original == p.Original && parkEvent(q) == parkEvent(p) }) {
			marked.Parked[i].Announced = true
		}
	}
	now, err := a.gh.pull(pr.Number)
	if err != nil {
		return err
	}
	if st := prState(&now); st != "open" {
		return fmt.Errorf("batch pull request #%d is %s now; nothing is written to it", pr.Number, st)
	}
	lines, err := fencedLines(now.Body, deferredFence)
	if err != nil || len(lines) != 1 {
		return fmt.Errorf("batch pull request #%d no longer carries the record notify read", pr.Number)
	}
	read, err := parseRecord(now.Body, notifyLineOf(rec))
	if err != nil || recordBlock(*read) != recordBlock(*rec) {
		return fmt.Errorf("batch pull request #%d no longer carries the record notify read", pr.Number)
	}
	// GitHub keeps the CRLF line ends a body edited on the web has; the
	// record is found, and the body written back, with LF ones.
	body := strings.ReplaceAll(now.Body, "\r\n", "\n")
	block := deferredFence + "\n" + lines[0] + "\n```"
	if strings.Count(body, block) != 1 {
		return fmt.Errorf("batch pull request #%d carries the record notify read, but not as one block it can mark", pr.Number)
	}
	body = strings.Replace(body, block, recordBlock(marked), 1)
	name, err := writeTemp("backport-batch-body-*.md", body)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()
	return a.gh.editBody(pr.Number, name)
}

// notifyLineOf is the line a record is for, which it was read against.
func notifyLineOf(rec *lineRecord) line {
	l, _ := parseLine(rec.Line)
	return l
}

// placement says under which list notify names original n of the record:
// workflow for a wait for a maintainer, conflict for a deferred conflict, ""
// for neither, a deferral for a reason among them. One that waits on others
// goes where what decides its wait goes, followed down the record; one that
// is not in the record is held by rule 6 or missing, and waits on nobody.
func placement(deferred map[int]deferredEntry, n int, seen map[int]bool) string {
	e, ok := deferred[n]
	switch {
	case !ok || seen[n]:
		return ""
	case e.Workflows:
		return "workflow"
	case e.Reason != "":
		return ""
	case len(e.Needs) == 0:
		return "conflict"
	}
	seen[n] = true
	place := ""
	for _, f := range e.Needs {
		switch p := placement(deferred, f, seen); p {
		case "workflow":
			return p
		case "conflict":
			place = p
		}
	}
	return place
}

// comment writes what waits into the one notify comment on the batch pull
// request: it edits the comment if there is one, and opens it only when
// something waits. Only a comment by an author the batch trusts is taken for
// it, so a look-alike someone else wrote is neither edited nor believed.
func (a *app) comment(pr *pullRequest, mention string, today time.Time, text []string) int {
	comments, err := a.gh.comments(pr.Number)
	if err != nil {
		return a.fail(err)
	}
	var ours *issueComment
	for i := range comments {
		c := &comments[i]
		if !strings.HasPrefix(c.Body, notifyMarker) {
			continue
		}
		why, err := trustedAuthor(a.gh, c.User.Login, c.User.Type)
		if err != nil {
			return a.fail(err)
		}
		if why == "" {
			ours = c
		}
	}
	if ours == nil && len(text) == 0 {
		return 0
	}
	var b strings.Builder
	b.WriteString(notifyMarker + "\n")
	if mention != "" && len(text) > 0 {
		b.WriteString(mention + "\n\n")
	}
	if len(text) == 0 {
		text = []string{"Nothing waits on a maintainer."}
	}
	b.WriteString(strings.Join(text, "\n\n"))
	fmt.Fprintf(&b, "\n\n<sub>Updated by the backport batch on %s.</sub>\n", today.Format(time.DateOnly))
	f, err := os.CreateTemp("", "backport-batch-comment-*.md")
	if err != nil {
		return a.fail(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return a.fail(err)
	}
	if err := f.Close(); err != nil {
		return a.fail(err)
	}
	if err := a.stillOpen(pr.Number); err != nil {
		return a.fail(err)
	}
	if ours != nil {
		err = a.gh.editComment(ours.ID, f.Name())
	} else {
		err = a.gh.createComment(pr.Number, f.Name())
	}
	if err != nil {
		return a.fail(err)
	}
	return 0
}

// lines prints the lines the batch runs for, newest first, in the order the
// policy has them processed.
func (a *app) lines(args []string) int {
	fs := a.policyFlags("lines")
	if err := parse(fs, args); err != nil {
		return 2
	}
	if err := fetch(); err != nil {
		return a.fail(err)
	}
	mainHead, err := resolve("refs/remotes/" + remote + "/main")
	if err != nil {
		return a.fail(err)
	}
	pol, _, err := a.policy(mainHead)
	if err != nil {
		return a.fail(err)
	}
	lines, err := batchLines(pol.released)
	if err != nil {
		return a.fail(err)
	}
	for _, l := range slices.Backward(lines) {
		_, _ = fmt.Fprintln(a.stdout, l.branch())
	}
	return 0
}
