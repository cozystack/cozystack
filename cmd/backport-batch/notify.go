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
	"errors"
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

// notify reports what waits on the open batch pull request of a line: a batch
// open since before the previous run, and the conflicts it deferred. With
// --agent ci each conflict goes to the CI agent as a task; with manual the
// comment lists it for a maintainer. One line per finding goes to stdout, for
// the run's summary. --push starts the agent tasks and writes the comment, one
// per pull request, updated in place on every run.
func (a *app) notify(args []string) int {
	fs := a.flags("notify")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	agent := fs.String("agent", "manual", "who resolves a deferred conflict: ci or manual")
	mention := fs.String("mention", "", "team or person the comment mentions, @org/team")
	push := fs.Bool("push", false, "start the agent tasks and write the comment")
	if err := parse(fs, args); err != nil {
		return 2
	}
	l, err := parseLine(*lineArg)
	if err != nil {
		return a.fail(err)
	}
	if *agent != "ci" && *agent != "manual" {
		return a.fail(fmt.Errorf("--agent %q is neither ci nor manual", *agent))
	}
	if *mention != "" {
		m := mentionRE.FindStringSubmatch(*mention)
		if m == nil {
			return a.fail(fmt.Errorf("--mention %q is not @login or @org/team", *mention))
		}
		*mention = "@" + m[1]
	}
	token := os.Getenv("BACKPORT_AGENT_TOKEN")
	if *agent == "ci" && *push && token == "" {
		return a.fail(errors.New("--agent ci needs the agent's token in BACKPORT_AGENT_TOKEN"))
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
	if pr == nil {
		return 0
	}
	opened, err := branchDate(pr.Head.Ref)
	if err != nil {
		return a.fail(err)
	}
	deferred, err := parseDeferred(pr.Body, l)
	if err != nil {
		return a.fail(fmt.Errorf("batch pull request #%d: %w", pr.Number, err))
	}
	var conflicts []int
	for n, e := range deferred {
		if e.Reason == "" {
			conflicts = append(conflicts, n)
		}
	}
	slices.Sort(conflicts)

	today := a.now().UTC().Truncate(24 * time.Hour)
	days := int(today.Sub(opened).Hours() / 24)
	stalled := days >= stalledAfter
	var text []string
	if stalled {
		fmt.Fprintf(a.stdout, "%s: batch pull request #%d has been open since %s, %d days\n", l.branch(), pr.Number, opened.Format(time.DateOnly), days)
		text = append(text, fmt.Sprintf("This batch has been open since %s, %d days, through at least one scheduled run. Merge it, or close it and the next run starts a new one; until then each run adds what the rules take to it.", opened.Format(time.DateOnly), days))
	}
	if len(conflicts) > 0 && *agent == "ci" {
		fmt.Fprintf(a.stdout, "%s: %d deferred conflicts on #%d go to the CI agent\n", l.branch(), len(conflicts), pr.Number)
		for _, n := range conflicts {
			if !*push {
				continue
			}
			prompt := fmt.Sprintf("Resolve the deferred backport conflict of pull request #%d on batch pull request #%d, branch %s, by following docs/release/backport-tasks/resolve-conflict.md on main. Push only to %s, and only a commit that `backport-batch fold --line %s --original %d` accepts.", n, pr.Number, pr.Head.Ref, pr.Head.Ref, l.branch(), n)
			if err := a.gh.startTask(token, l.branch(), pr.Head.Ref, prompt); err != nil {
				return a.fail(err)
			}
		}
	}
	if len(conflicts) > 0 && *agent == "manual" {
		fmt.Fprintf(a.stdout, "%s: %d deferred conflicts on #%d wait for a maintainer\n", l.branch(), len(conflicts), pr.Number)
		var b strings.Builder
		fmt.Fprintf(&b, "These conflicts wait for a maintainer, as [resolving a conflict](%sresolve-conflict.md) describes; unresolved, each goes to the next batch:\n", tasksURL)
		for _, n := range conflicts {
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
		if strings.HasPrefix(c.Body, notifyMarker) && trustedAuthor(a.gh, c.User.Login, c.User.Type) == "" {
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
	fs := a.flags("lines")
	if err := parse(fs, args); err != nil {
		return 2
	}
	if err := fetch(); err != nil {
		return a.fail(err)
	}
	all, err := releaseLines()
	if err != nil {
		return a.fail(err)
	}
	for i := len(all) - 1; i >= 0 && i >= len(all)-2; i-- {
		fmt.Fprintln(a.stdout, all[i].branch())
	}
	return 0
}
