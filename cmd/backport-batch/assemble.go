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
	"strings"
	"time"
)

// pick is the outcome of picking one original onto the batch branch.
type pick struct {
	v      *verdict
	status string // picked, conflict, empty, or held out by --defer
	commit string
	files  []string
	// more counts the conflicting files a recorded conflict no longer names.
	more   int
	reason string
}

func batchBranch(l line, date time.Time) string {
	return "backport-batch/" + l.branch() + "-" + date.Format("20060102")
}

func (a *app) run(args []string) int {
	fs := a.flags("run")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	onto := fs.String("onto", "", "head of the pushed batch branch to continue")
	cont := fs.Bool("continue", false, "continue the open batch pull request, or start a batch when none is open")
	dateArg := fs.String("date", "", "batch date, YYYY-MM-DD (default: today, UTC)")
	deferArg := fs.String("defer", "", "originals to take out of the open batch, N[,N]; rebuilds it without them")
	reasonArg := fs.String("reason", "", "why --defer takes them out, recorded in the batch body")
	push := fs.Bool("push", false, "push the branch and open or update the draft batch PR")
	if err := parse(fs, args); err != nil {
		return 2
	}
	l, err := parseLine(*lineArg)
	if err != nil {
		return a.fail(err)
	}
	date := a.now().UTC()
	if *dateArg != "" {
		if date, err = time.Parse(time.DateOnly, *dateArg); err != nil {
			return a.fail(fmt.Errorf("--date %q is not YYYY-MM-DD", *dateArg))
		}
	}
	if *onto != "" {
		if err := checkSHA(*onto); err != nil {
			return a.fail(fmt.Errorf("--onto: %w", err))
		}
	}
	var deferred []int
	reason := ""
	switch {
	case *deferArg != "" || *reasonArg != "":
		if deferred, reason, err = parseDefer(*deferArg, *reasonArg); err != nil {
			return a.fail(err)
		}
		if *onto != "" || *cont || *dateArg != "" {
			return a.fail(errors.New("--defer rebuilds the open batch pull request; it takes no --onto, --continue or --date"))
		}
	case *cont && (*onto != "" || *dateArg != ""):
		return a.fail(errors.New("--continue reads the batch to continue from the open batch pull request; it takes no --onto or --date"))
	}
	if err := cleanTree(); err != nil {
		return a.fail(err)
	}
	if err := fetch(); err != nil {
		return a.fail(err)
	}
	b, err := a.prepare(l)
	if err != nil {
		return a.fail(err)
	}
	if deferred != nil {
		return a.rebuild(b, deferred, reason, *push)
	}
	if *cont && b.pr != nil {
		*onto = b.prHead
	}

	branch, start := batchBranch(l, date), b.lineHead
	var carried *history
	if *onto != "" {
		if branch, err = ontoBranch(l, *onto, *dateArg != "", branch); err != nil {
			return a.fail(err)
		}
		if carried, err = readPicks(*onto, b.lineHead); err != nil {
			return a.fail(err)
		}
		start = *onto
		// The batch keeps the date it was opened under, in its title too.
		if date, err = branchDate(branch); err != nil {
			return a.fail(err)
		}
	}
	if err := checkBranch(branch); err != nil {
		return a.fail(err)
	}
	// Continuing the open batch keeps out what was deferred from it by hand;
	// a new batch tries those again.
	held := map[int]deferredEntry{}
	if carried != nil && b.pr != nil && b.pr.Head.Ref == branch {
		if held, err = parseDeferred(b.pr.Body, l); err != nil {
			return a.fail(fmt.Errorf("batch pull request #%d: %w", b.pr.Number, err))
		}
	}
	if old, err := resolve("refs/heads/" + branch); err == nil && old != start {
		fmt.Fprintf(a.stderr, "resetting the local %s from %.12s\n", branch, old)
	}
	if _, err := git("switch", "--quiet", "--no-track", "-C", branch, "--end-of-options", start); err != nil {
		return a.fail(err)
	}

	r := newReport(b)
	if carried != nil {
		if r.stale, err = keptOut(b, carried); err != nil {
			return a.fail(err)
		}
	}
	for _, v := range b.qualifying() {
		switch {
		case carried != nil && carried.picked(v.commits) != "":
			r.onBranch = append(r.onBranch, v)
			continue
		case held[v.o.number].Reason != "":
			r.held = append(r.held, pick{v: v, status: "held", reason: held[v.o.number].Reason})
			continue
		case v.onBatch != "":
			v.rule, v.reason = ruleOnLine, fmt.Sprintf("on the open batch pull request #%d", b.pr.Number)
			r.skipped[ruleOnLine] = append(r.skipped[ruleOnLine], v)
			continue
		}
		p, err := cherryPick(v)
		if err != nil {
			return a.fail(err)
		}
		switch p.status {
		case "picked":
			r.onBranch = append(r.onBranch, v)
		case "conflict":
			r.deferred = append(r.deferred, p)
		case "empty":
			v.rule, v.reason = ruleOnLine, "the earlier picks already carry it"
			r.skipped[ruleOnLine] = append(r.skipped[ruleOnLine], v)
		}
	}
	if *onto != "" {
		head, err := resolve("HEAD")
		if err != nil {
			return a.fail(err)
		}
		if !isAncestor(*onto, head) {
			return a.fail(fmt.Errorf("%.12s does not descend from --onto %.12s", head, *onto))
		}
	}
	return a.finish(b, r, branch, date, *push, false)
}

// keptOut lists the originals with backport/skip that the batch picked before
// the label came: a run never takes a commit off the batch, --defer does.
func keptOut(b *batch, carried *history) ([]*verdict, error) {
	var out []*verdict
	for _, v := range b.verdicts {
		if v.label != labelSkip {
			continue
		}
		if v.commits == nil {
			commits, err := commitsOf(v.o)
			if err != nil {
				return nil, err
			}
			v.commits = commits
		}
		if carried.picked(v.commits) != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// newReport starts the report of a run with what the rules left out and what
// the labels overrode.
func newReport(b *batch) *report {
	r := &report{line: b.line, newer: b.newer, skipped: map[int][]*verdict{}, other: len(b.other), start: b.start}
	for _, v := range b.verdicts {
		if v.rule != 0 {
			r.skipped[v.rule] = append(r.skipped[v.rule], v)
		}
		if v.label != "" {
			r.overrides = append(r.overrides, v)
		}
	}
	return r
}

// finish prints the body and what the run did, and publishes the branch when
// asked to.
func (a *app) finish(b *batch, r *report, branch string, date time.Time, push, rebuilt bool) int {
	head, err := resolve("HEAD")
	if err != nil {
		return a.fail(err)
	}
	title := prTitle(b.line, date.Format(time.DateOnly))
	body, left := r.body(lineTemplate(b.lineHead))
	fmt.Fprint(a.stdout, body)
	skipped := 0
	for _, items := range r.skipped {
		skipped += len(items)
	}
	for _, rule := range left {
		for _, v := range r.skipped[rule] {
			fmt.Fprintf(a.stderr, "skipped by rule %d: %s %s (%s)\n", rule, v.o.url(), terminalSafe(v.o.title), terminalSafe(v.reason))
		}
	}
	fmt.Fprintf(a.stderr, "%s at %.12s: %d on the branch, %d deferred, %d skipped\ntitle: %s\n",
		branch, head, len(r.onBranch), len(r.deferred)+len(r.held), skipped, title)
	if !push {
		return 0
	}
	if head == b.lineHead && b.pr == nil {
		// Picking nothing is not a failure of the run; check still reports
		// whatever the line lacks.
		fmt.Fprintf(a.stderr, "%s carries nothing beyond %s; nothing to push\n", branch, b.line.branch())
		return 0
	}
	if err := a.publish(b, branch, head, title, body, rebuilt); err != nil {
		return a.fail(err)
	}
	return 0
}

// branchDate is the date a batch branch was opened under.
func branchDate(branch string) (time.Time, error) {
	m := batchBranchRE.FindStringSubmatch(branch)
	if m == nil {
		return time.Time{}, fmt.Errorf("%q is not a batch branch", branch)
	}
	return time.Parse("20060102", m[2])
}

// cleanTree refuses a working tree with changes or an operation in progress:
// run switches it to the batch branch.
func cleanTree() error {
	out, err := git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("the working tree has changes; run in a clean worktree")
	}
	for _, name := range []string{"CHERRY_PICK_HEAD", "MERGE_HEAD", "REVERT_HEAD", "REBASE_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		p, err := git("rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(strings.TrimSpace(p)); err == nil {
			return fmt.Errorf("an operation is in progress (%s); finish or abort it first", name)
		}
	}
	return nil
}

// ontoBranch is the pushed batch branch whose head --onto names, read from the
// remote itself: a head that moved since, or that no batch branch has, is
// refused rather than continued.
func ontoBranch(l line, onto string, dated bool, dateBranch string) (string, error) {
	heads, err := remoteBatchHeads(l)
	if err != nil {
		return "", err
	}
	var found []string
	for branch, sha := range heads {
		if sha == onto && (!dated || branch == dateBranch) {
			found = append(found, branch)
		}
	}
	switch {
	case len(found) == 0 && dated:
		return "", fmt.Errorf("--onto %.12s is not the head of %s on %s (%.12s)", onto, dateBranch, remote, heads[dateBranch])
	case len(found) == 0:
		return "", fmt.Errorf("--onto %.12s is not the head of any batch branch for %s on %s", onto, l.branch(), remote)
	case len(found) > 1:
		return "", fmt.Errorf("--onto %.12s heads more than one batch branch; name one with --date", onto)
	}
	if !gitOK("cat-file", "-e", "--end-of-options", onto+"^{commit}") {
		return "", fmt.Errorf("--onto %.12s is not in this repository; fetch it first", onto)
	}
	return found[0], nil
}

// remoteBatchHeads lists the batch branches for l on the remote, read with
// ls-remote rather than from the possibly stale remote-tracking refs.
func remoteBatchHeads(l line) (map[string]string, error) {
	out, err := git("ls-remote", "--refs", "--heads", "--end-of-options", remote)
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for ln := range strings.Lines(out) {
		sha, ref, ok := strings.Cut(strings.TrimSpace(ln), "\t")
		branch, isHead := strings.CutPrefix(ref, "refs/heads/")
		if !ok || !isHead || checkSHA(sha) != nil {
			continue
		}
		if m := batchBranchRE.FindStringSubmatch(branch); m != nil && m[1] == l.branch() {
			heads[branch] = sha
		}
	}
	return heads, nil
}

// cherryPick picks one original onto HEAD. Hooks are off so that the commit
// carries the original message and the -x line as git writes them; signing is
// left to the user's configuration.
func cherryPick(v *verdict) (pick, error) {
	p := pick{v: v}
	args := []string{"-c", "core.hooksPath=/dev/null", "cherry-pick", "-x", "--signoff"}
	if v.o.parents > 1 {
		args = append(args, "-m", "1")
	}
	args = append(args, "--end-of-options", v.o.merge)
	status, files, err := applyPick(args...)
	if err != nil {
		return p, fmt.Errorf("cherry-pick of #%d (%s): %w", v.o.number, v.o.merge, err)
	}
	p.status, p.files = status, files
	if status == "picked" {
		p.commit, err = resolve("HEAD")
	}
	return p, err
}

// applyPick runs one cherry-pick onto HEAD and leaves no operation in progress
// behind it: the outcome is picked, conflict, with the conflicting files, or
// empty.
func applyPick(args ...string) (string, []string, error) {
	stdout, stderr, code, err := gitStatus(args...)
	if err != nil {
		return "", nil, err
	}
	if code == 0 {
		return "picked", nil, nil
	}
	out, err := git("diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return "", nil, err
	}
	if files := splitNUL(out); len(files) > 0 {
		_, err := git("cherry-pick", "--abort")
		return "conflict", files, err
	}
	if msg := stdout + stderr; strings.Contains(msg, "is now empty") || strings.Contains(msg, "nothing to commit") {
		_, err := git("cherry-pick", "--skip")
		return "empty", nil, err
	}
	_, _ = git("cherry-pick", "--abort")
	return "", nil, fmt.Errorf("failed: %s", strings.TrimSpace(stderr))
}

// lineTemplate is the line's own pull request template, so the batch body has
// the sections reviewers of that line expect.
func lineTemplate(lineHead string) string {
	out, err := git("show", "--end-of-options", lineHead+":.github/PULL_REQUEST_TEMPLATE.md")
	if err != nil {
		return "## What this PR does\n\n### Release note\n\n```release-note\n\n```\n"
	}
	return out
}

// publish pushes the batch branch, then opens the draft batch pull request or
// updates the open one. A run pushes only a fast-forward of what the remote
// has. A rebuild replaces the branch, and only the head it was rebuilt from:
// the lease is that head, as ls-remote reads it now, so a commit pushed since
// is never dropped.
func (a *app) publish(b *batch, branch, head, title, body string, rebuilt bool) error {
	if b.pr != nil && b.pr.Head.Ref != branch {
		return fmt.Errorf("the open batch pull request #%d is on %s; continue it with --onto %s", b.pr.Number, b.pr.Head.Ref, b.prHead)
	}
	if head == b.lineHead {
		if rebuilt {
			return fmt.Errorf("without what is deferred, %s carries nothing beyond %s; close batch pull request #%d instead", branch, b.line.branch(), b.pr.Number)
		}
		return fmt.Errorf("%s carries nothing beyond %s; nothing to push", branch, b.line.branch())
	}
	heads, err := remoteBatchHeads(b.line)
	if err != nil {
		return err
	}
	pushed, onRemote := heads[branch]
	lease := ""
	switch {
	case rebuilt:
		if !onRemote || pushed != b.prHead {
			return fmt.Errorf("%s on %s is at %.12s, not at %.12s, which the rebuild started from; run again", branch, remote, pushed, b.prHead)
		}
		lease = pushed
	case onRemote && pushed != head:
		if !gitOK("cat-file", "-e", "--end-of-options", pushed+"^{commit}") || !isAncestor(pushed, head) {
			return fmt.Errorf("%s on %s is at %.12s, which %.12s does not descend from; refusing a non-fast-forward push", branch, remote, pushed, head)
		}
	}
	if pushed != head {
		if _, err := git(pushArgs(branch, head, lease)...); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp("", "backport-batch-body-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if b.pr == nil {
		return a.gh.createPull(b.line.branch(), branch, title, f.Name())
	}
	return a.gh.editPull(b.pr.Number, title, f.Name())
}

// pushArgs pushes head to branch: as a fast-forward, which the remote checks,
// or, given a lease, over exactly that commit and no other.
func pushArgs(branch, head, lease string) []string {
	args := []string{"push", "--quiet"}
	if lease != "" {
		args = append(args, "--force-with-lease=refs/heads/"+branch+":"+lease)
	}
	return append(args, "--end-of-options", remote, head+":refs/heads/"+branch)
}
