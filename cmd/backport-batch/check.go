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
	"strconv"
)

// check says whether every original the policy takes is on the line, on the
// open batch pull request, or listed there as deferred, a conflict or one
// taken out with --defer, or as waiting for a maintainer because it changes
// workflows, each of which warns without failing. With --cut it is the gate
// before a cut, which tags the line: an original only on the open batch pull
// request fails it too, since the cut would not carry it, and so does one that
// waits, which nobody decided to leave out.
func (a *app) check(args []string) int {
	fs := a.policyFlags("check")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	cut := fs.Bool("cut", false, "fail too while a change is only on the open batch PR, as before a cut")
	if err := parse(fs, args); err != nil {
		return 2
	}
	l, err := parseLine(*lineArg)
	if err != nil {
		return a.fail(err)
	}
	if err := fetch(); err != nil {
		return a.fail(err)
	}
	b, err := a.prepare(l)
	if err != nil {
		return a.fail(err)
	}
	deferred := map[int]deferredEntry{}
	if b.pr != nil {
		if deferred, err = parseDeferred(b.pr.Body, l); err != nil {
			return a.fail(fmt.Errorf("batch pull request #%d: %w", b.pr.Number, err))
		}
	}
	var missing, unmerged, waiting []*verdict
	for _, v := range b.qualifying() {
		e, isDeferred := deferred[v.o.number]
		switch {
		case v.onBatch != "":
			if *cut {
				unmerged = append(unmerged, v)
			}
		case isDeferred && e.Workflows && *cut:
			waiting = append(waiting, v)
		case isDeferred && e.Workflows:
			a.warn("waiting on #%d for a maintainer, since it changes %s: %s %s", b.pr.Number, workflowsDir, v.o.url(), terminalSafe(v.o.title))
		case isDeferred && e.Reason != "":
			a.warn("deferred on #%d to the next batch: %s %s (%s)", b.pr.Number, v.o.url(), terminalSafe(v.o.title), terminalSafe(e.Reason))
		case isDeferred:
			a.warn("deferred conflict on #%d: %s %s", b.pr.Number, v.o.url(), terminalSafe(v.o.title))
		default:
			missing = append(missing, v)
		}
	}
	where := "and no batch pull request is open"
	if b.pr != nil {
		where = "nor on batch pull request #" + strconv.Itoa(b.pr.Number)
	}
	switch {
	case len(missing) == 0 && len(unmerged) == 0 && len(waiting) == 0 && *cut:
		fmt.Fprintf(a.stdout, "%s: every change the policy takes is on the line\n", l.branch())
		return 0
	case len(missing) == 0 && len(unmerged) == 0:
		fmt.Fprintf(a.stdout, "%s: every change the policy takes is on the line or on the batch\n", l.branch())
		return 0
	}
	if len(missing) > 0 {
		fmt.Fprintf(a.stdout, "%s: %d changes the policy takes are not on the line, %s:\n", l.branch(), len(missing), where)
		for _, v := range missing {
			fmt.Fprintf(a.stdout, "  %s %s\n", v.o.url(), terminalSafe(v.o.title))
		}
	}
	for _, v := range unmerged {
		fmt.Fprintf(a.stdout, "%s: #%d is only on open batch PR #%d, merge it before the cut: %s %s\n", l.branch(), v.o.number, b.pr.Number, v.o.url(), terminalSafe(v.o.title))
	}
	for _, v := range waiting {
		fmt.Fprintf(a.stdout, "%s: #%d changes %s and waits on batch PR #%d for a maintainer, bring it over or defer it before the cut: %s %s\n", l.branch(), v.o.number, workflowsDir, b.pr.Number, v.o.url(), terminalSafe(v.o.title))
	}
	return 1
}

// terminalSafe keeps a title from carrying escape sequences into a terminal.
func terminalSafe(s string) string { return capped(oneLine(clean(s, false)), titleLimit) }

// fold checks a hand-made resolution of a deferred conflict before anyone
// pushes it onto the batch: one original on the batch head, in the shape the
// batch carries it, inside the files that original touches.
func (a *app) fold(args []string) int {
	fs := a.policyFlags("fold")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	number := fs.Int("original", 0, "number of the original pull request")
	commit := fs.String("commit", "", "the resolution: the merge commit, or for a squash-merged original its one commit")
	if err := parse(fs, args); err != nil {
		return 2
	}
	l, err := parseLine(*lineArg)
	if err != nil {
		return a.fail(err)
	}
	if *number <= 0 {
		return a.fail(fmt.Errorf("--original %d is not a pull request number", *number))
	}
	if err := checkSHA(*commit); err != nil {
		return a.fail(fmt.Errorf("--commit: %w", err))
	}
	if err := fetch(); err != nil {
		return a.fail(err)
	}
	b, err := a.prepare(l)
	if err != nil {
		return a.fail(err)
	}
	why, err := refuseFold(b, *number, *commit)
	if err != nil {
		return a.fail(err)
	}
	if why != "" {
		fmt.Fprintf(a.stdout, "refused: %s\n", why)
		return 1
	}
	fmt.Fprintln(a.stdout, "ok")
	return 0
}

// refuseFold says why commit is not a fold of original n onto the batch, or ""
// when it is one: for a merged pull request a merge whose first parent is the
// batch head and whose second parent ends a straight line of picks of the pull
// request's own commits, in order, each with its -x line, naming the original
// in its Backported-from trailer; for a squash-merged one a single pick of its
// commit on main. Pushed as it is, either is a fast-forward of the batch.
func refuseFold(b *batch, n int, commit string) (string, error) {
	v := b.find(n)
	switch {
	case v == nil:
		return fmt.Sprintf("#%d did not merge into main after %s branched", n, b.line.branch()), nil
	case v.rule == ruleSkipLabel:
		return fmt.Sprintf("#%d carries %s", n, labelSkip), nil
	case v.rule != 0:
		return fmt.Sprintf("#%d is not one the batch takes: rule %d, %s", n, v.rule, v.reason), nil
	case b.pr == nil:
		return fmt.Sprintf("no batch pull request is open on %s", b.line.branch()), nil
	case v.onBatch != "":
		return fmt.Sprintf("batch pull request #%d already carries #%d in %.12s", b.pr.Number, n, v.onBatch), nil
	}
	have, err := lookup(commit)
	if err != nil {
		return "", err
	}
	if have == "" {
		return fmt.Sprintf("%.12s is not a commit in this repository", commit), nil
	}
	u, err := readUnit(commit)
	if err != nil {
		return "", err
	}
	want := 1
	if v.o.parents > 1 {
		want = 2
	}
	switch {
	case len(u.parents) != want && want == 2:
		return fmt.Sprintf("%.12s has %d parents; #%d is a merged pull request, so a fold is a merge of its picked commits onto the batch head", commit, len(u.parents), n), nil
	case len(u.parents) != want:
		return fmt.Sprintf("%.12s has %d parents; #%d merged as one commit, so a fold is one commit with one parent", commit, len(u.parents), n), nil
	case u.parents[0] != b.prHead:
		return fmt.Sprintf("%.12s is on %.12s, not on the head of batch pull request #%d (%.12s)", commit, u.parents[0], b.pr.Number, b.prHead), nil
	}
	why, carried, err := checkUnit(b, u)
	switch {
	case err != nil || why != "":
		return why, err
	case carried != v:
		return fmt.Sprintf("%.12s carries #%d, not #%d", commit, carried.o.number, n), nil
	}
	return "", nil
}
