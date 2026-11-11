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
	if src.open {
		deferred = src.rec.deferred()
	}
	var conflicts, waiting []int
	for n, e := range deferred {
		switch {
		case e.Workflows:
			waiting = append(waiting, n)
		case e.Reason == "":
			conflicts = append(conflicts, n)
		}
	}
	slices.Sort(conflicts)
	slices.Sort(waiting)

	today := a.now().UTC().Truncate(24 * time.Hour)
	days := int(today.Sub(opened).Hours() / 24)
	stalled := days >= stalledAfter
	var text []string
	if stalled {
		fmt.Fprintf(a.stdout, "%s: batch pull request #%d has been open since %s, %d days\n", l.branch(), pr.Number, opened.Format(time.DateOnly), days)
		text = append(text, fmt.Sprintf("This batch has been open since %s, %d days, through at least one scheduled run. Merge it, or close it and the next run starts a new one; until then each run adds what the rules take to it.", opened.Format(time.DateOnly), days))
	}
	if len(conflicts) > 0 {
		fmt.Fprintf(a.stdout, "%s: %d deferred conflicts on #%d wait for a maintainer\n", l.branch(), len(conflicts), pr.Number)
		var b strings.Builder
		fmt.Fprintf(&b, "These conflicts wait for a maintainer, as [resolving a conflict](%sresolve-conflict.md) describes; unresolved, each goes to the next batch:\n", tasksURL)
		for _, n := range conflicts {
			fmt.Fprintf(&b, "\n- %s", prLink(n))
		}
		text = append(text, b.String())
	}
	if len(waiting) > 0 {
		fmt.Fprintf(a.stdout, "%s: %d originals on #%d change %s and wait for a maintainer\n", l.branch(), len(waiting), pr.Number, workflowsDir)
		var b strings.Builder
		fmt.Fprintf(&b, "These change `%s`, which the batch's token may not push, and wait for a maintainer to bring them over, as [changes to workflows](%s#changes-to-workflows) describes:\n", workflowsDir, policyURL)
		for _, n := range waiting {
			fmt.Fprintf(&b, "\n- %s", prLink(n))
		}
		text = append(text, b.String())
	}
	if !*push {
		return 0
	}
	return a.comment(pr, *mention, today, text)
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
	defer os.Remove(f.Name())
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
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
		fmt.Fprintln(a.stdout, l.branch())
	}
	return 0
}
