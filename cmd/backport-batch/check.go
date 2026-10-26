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
	"slices"
	"strconv"
	"strings"
)

// check is the gate before a patch cut: every original the policy takes is on
// the line, on the open batch pull request, or listed there as deferred, a
// conflict or one taken out with --defer, which warns without failing.
func (a *app) check(args []string) int {
	fs := a.flags("check")
	lineArg := fs.String("line", "", "release line, release-X.Y")
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
	var missing []*verdict
	for _, v := range b.qualifying() {
		e, isDeferred := deferred[v.o.number]
		switch {
		case v.onBatch != "":
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
	if len(missing) == 0 {
		fmt.Fprintf(a.stdout, "%s: every change the policy takes is on the line or on the batch\n", l.branch())
		return 0
	}
	fmt.Fprintf(a.stdout, "%s: %d changes the policy takes are not on the line, %s:\n", l.branch(), len(missing), where)
	for _, v := range missing {
		fmt.Fprintf(a.stdout, "  %s %s\n", v.o.url(), terminalSafe(v.o.title))
	}
	return 1
}

// terminalSafe keeps a title from carrying escape sequences into a terminal.
func terminalSafe(s string) string { return capped(oneLine(clean(s, false)), titleLimit) }

// fold checks a hand-made resolution of a deferred conflict before anyone
// pushes it onto the batch: one commit, on the batch head, porting one
// original, inside the files that original touches.
func (a *app) fold(args []string) int {
	fs := a.flags("fold")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	number := fs.Int("original", 0, "number of the original pull request")
	commit := fs.String("commit", "", "the resolution commit")
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
// when it is one.
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
	if !gitOK("cat-file", "-e", "--end-of-options", commit+"^{commit}") {
		return fmt.Sprintf("%.12s is not a commit in this repository", commit), nil
	}
	out, err := git("rev-list", "--parents", "-n", "1", "--end-of-options", commit)
	if err != nil {
		return "", err
	}
	parents := strings.Fields(out)[1:]
	if len(parents) != 1 {
		return fmt.Sprintf("%.12s has %d parents; a fold is one commit with one parent", commit, len(parents)), nil
	}
	if parents[0] != b.prHead {
		return fmt.Sprintf("%.12s is on %.12s, not on the head of batch pull request #%d (%.12s)", commit, parents[0], b.pr.Number, b.prHead), nil
	}
	changes, err := changedFiles(commit+"^", commit)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return fmt.Sprintf("%.12s changes nothing", commit), nil
	}
	allowed := paths(v.changes)
	var extra []string
	for _, p := range paths(changes) {
		if !slices.Contains(allowed, p) {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		for i, p := range extra {
			extra[i] = strconv.Quote(p)
		}
		return fmt.Sprintf("%.12s touches %s, which #%d does not touch", commit, strings.Join(extra, ", "), n), nil
	}
	msgs, err := commitMessages([]string{commit})
	if err != nil {
		return "", err
	}
	sources := xSources(msgs[commit].message)
	if len(sources) == 0 {
		return fmt.Sprintf("%.12s carries no (cherry picked from commit %s) line in its trailers", commit, v.o.merge), nil
	}
	for _, source := range sources {
		if !slices.Contains(v.commits, source) {
			return fmt.Sprintf("%.12s names %s, which is not a commit of #%d", commit, source, n), nil
		}
	}
	return "", nil
}
