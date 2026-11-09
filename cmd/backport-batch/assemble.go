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
	"slices"
	"strings"
	"time"
)

// pick is the outcome of picking one original onto the batch branch.
type pick struct {
	v      *verdict
	status string // picked, conflict, empty, held out by --defer, or waiting for a maintainer
	commit string
	files  []string
	// failed is the commit whose pick conflicted.
	failed string
	// more counts the conflicting files a recorded conflict no longer names.
	more   int
	reason string
}

// workflowsDir is where GitHub reads workflows from. A token without the
// workflows permission, a GitHub App's among them, cannot push a commit that
// changes anything under it.
const workflowsDir = ".github/workflows/"

// workflowFiles lists the workflow files the commits change, each against its
// parent: a push carries every pick, so a change one commit makes and a later
// one takes back is in it although the pull request's merge changes nothing
// there.
func workflowFiles(commits []string) ([]string, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	out, err := gitInput(strings.Join(commits, "\n")+"\n", "diff-tree", "--stdin", "--no-commit-id", "--name-only", "-r", "-z", "--no-renames")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range splitNUL(out) {
		if strings.HasPrefix(f, workflowsDir) && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	return files, nil
}

func batchBranch(l line, date time.Time) string {
	return "backport-batch/" + l.branch() + "-" + date.Format("20060102")
}

func (a *app) run(args []string) int {
	fs := a.policyFlags("run")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	onto := fs.String("onto", "", "head of the pushed batch branch to continue")
	cont := fs.Bool("continue", false, "continue the open batch pull request, or start a batch when none is open")
	dateArg := fs.String("date", "", "batch date, YYYY-MM-DD (default: today, UTC)")
	deferArg := fs.String("defer", "", "originals to take out of the open batch, N[,N]; rebuilds it without them")
	noWorkflows := fs.Bool("no-workflow-files", false, "leave an original that changes "+workflowsDir+" to a maintainer, for a token that cannot push one")
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
		if carried, err = readPicks(*onto, b.lineHead, b.textUntil); err != nil {
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
	if err := a.warnReset(branch, start); err != nil {
		return a.fail(err)
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
		case carried != nil && found(carried, v):
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
		if *noWorkflows {
			files, err := workflowFiles(v.commits)
			if err != nil {
				return a.fail(err)
			}
			if len(files) > 0 {
				r.waiting = append(r.waiting, pick{v: v, status: "waiting", files: files})
				continue
			}
		}
		p, err := pickOriginal(v)
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
		descends, err := isAncestor(*onto, head)
		if err != nil {
			return a.fail(err)
		}
		if !descends {
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
		if err := b.load(v); err != nil {
			return nil, err
		}
		if found(carried, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

func found(h *history, v *verdict) bool {
	c, _ := h.found(v.o, v.commits)
	return c != ""
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
	if r.shapes, err = summarize(b, head, r.onBranch); err != nil {
		return a.fail(err)
	}
	title := prTitle(b.line, date.Format(time.DateOnly))
	template, err := lineTemplate(b.lineHead)
	if err != nil {
		return a.fail(err)
	}
	body, left := r.body(template)
	fmt.Fprint(a.stdout, body)
	skipped := 0
	for _, items := range r.skipped {
		skipped += len(items)
	}
	for _, rule := range left {
		for _, v := range r.skipped[rule] {
			fmt.Fprintf(a.stderr, "skipped by %s: %s %s (%s)\n", ruleName(rule), v.o.url(), terminalSafe(v.o.title), terminalSafe(v.reason))
		}
	}
	for _, p := range r.waiting {
		fmt.Fprintf(a.stderr, "waiting for a maintainer: %s %s changes %s\n", p.v.o.url(), terminalSafe(p.v.o.title), terminalSafe(strings.Join(p.files, ", ")))
	}
	fmt.Fprintf(a.stderr, "%s at %.12s: %d on the branch, %d deferred, %d waiting for a maintainer, %d skipped\ntitle: %s\n",
		branch, head, len(r.onBranch), len(r.deferred)+len(r.held), len(r.waiting), skipped, title)
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

// warnReset says, before switch -C moves the local branch to start, which tip
// the move drops commits of and how to get them back: a clean tree says
// nothing about commits only a local branch has.
func (a *app) warnReset(branch, start string) error {
	old, err := lookup("refs/heads/" + branch)
	if err != nil || old == "" || old == start {
		return err
	}
	kept, err := isAncestor(old, start)
	if err != nil || kept {
		return err
	}
	fmt.Fprintf(a.stderr, "resetting the local %s from %s, dropping commits %.12s does not carry; git branch %s-%.12s %s restores them\n", branch, old, start, branch, old, old)
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
	have, err := lookup(onto)
	if err != nil {
		return "", err
	}
	if have == "" {
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
	failed := fmt.Errorf("failed: %s", strings.TrimSpace(stderr))
	if _, err := git("cherry-pick", "--abort"); err != nil {
		return "", nil, errors.Join(failed, err)
	}
	return "", nil, failed
}

// lineTemplate is the line's own pull request template, so the batch body has
// the sections reviewers of that line expect.
func lineTemplate(lineHead string) (string, error) {
	const template = ".github/PULL_REQUEST_TEMPLATE.md"
	out, err := git("--literal-pathspecs", "ls-tree", "--name-only", "--end-of-options", lineHead, "--", template)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "## What this PR does\n\n### Release note\n\n```release-note\n\n```\n", nil
	}
	return git("show", "--end-of-options", lineHead+":"+template)
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
	// A body without the block was written by hand, and the tool's would
	// replace it whole, title included; such a batch is the maintainer's.
	if b.pr != nil && !strings.Contains("\n"+strings.ReplaceAll(b.pr.Body, "\r\n", "\n")+"\n", "\n"+deferredFence+"\n") {
		return fmt.Errorf("batch pull request #%d has no %s block, so backport-batch did not write it; merge or close it, and the next run opens a batch of its own", b.pr.Number, deferredFence)
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
		have, err := lookup(pushed)
		if err != nil {
			return err
		}
		descends := false
		if have != "" {
			if descends, err = isAncestor(pushed, head); err != nil {
				return err
			}
		}
		if !descends {
			refusal := fmt.Sprintf("%s on %s is at %.12s, which %.12s does not descend from; refusing a non-fast-forward push", branch, remote, pushed, head)
			// A run that stopped between its push and opening the pull request
			// leaves the branch, and the next run that day builds it anew.
			if b.pr == nil {
				refusal += fmt.Sprintf("; no batch pull request is open on it, and backport-batch run --line %s --onto %s --push continues it and opens one", b.line.branch(), pushed)
			}
			return errors.New(refusal)
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
		url, err := a.gh.createPull(b.line.branch(), branch, title, f.Name())
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "opened draft %s\n", url)
		return nil
	}
	if err := a.gh.editPull(b.pr.Number, title, f.Name()); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "updated %s\n", pullURL(b.pr))
	return nil
}

// pullURL is where a pull request is on GitHub.
func pullURL(pr *pullRequest) string {
	if pullURLRE.MatchString(pr.HTMLURL) {
		return pr.HTMLURL
	}
	return fmt.Sprintf("%spull/%d", ourRepoURL, pr.Number)
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
