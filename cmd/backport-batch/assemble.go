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
	// needs are the originals still to come it waits on.
	needs []int
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
	dropArg := fs.String("drop", "", "originals to drop from the line, N[,N]; recorded on the open batch, and taken off it")
	undropArg := fs.String("undrop", "", "originals whose drop to withdraw, N[,N]")
	noWorkflows := fs.Bool("no-workflow-files", false, "leave an original that changes "+workflowsDir+" to a maintainer, for a token that cannot push one")
	reasonArg := fs.String("reason", "", "why --defer takes them out or --drop drops them, recorded in the batch body")
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
	var deferred, drops []int
	reason, ops := "", 0
	for _, op := range []string{*deferArg, *dropArg, *undropArg} {
		if op != "" {
			ops++
		}
	}
	switch {
	case ops > 1:
		return a.fail(errors.New("--defer, --drop and --undrop each change the open batch pull request on their own; give one"))
	case *dropArg != "" || *undropArg != "":
		if *onto != "" || *cont || *dateArg != "" {
			return a.fail(errors.New("--drop and --undrop change the open batch pull request; each takes no --onto, --continue or --date"))
		}
		if *undropArg != "" && *reasonArg != "" {
			return a.fail(errors.New("--undrop takes no --reason"))
		}
		list := *dropArg + *undropArg
		if drops, err = parseNumbers(map[bool]string{true: "--drop", false: "--undrop"}[*dropArg != ""], list); err != nil {
			return a.fail(err)
		}
		if reason = capped(oneLine(clean(*reasonArg, false)), titleLimit); *dropArg != "" && reason == "" {
			return a.fail(errors.New("--drop needs a --reason, which the line's record keeps"))
		}
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
	started := time.Now()
	b, err := a.prepare(l)
	if err != nil {
		return a.fail(err)
	}
	if goOn, code := a.resumeClose(b, *push, deferred != nil || drops != nil); !goOn {
		return code
	}
	if err := b.analyze(); err != nil {
		return a.fail(err)
	}
	// The scheduled job has half an hour for every line; this says where it
	// goes.
	_, _ = fmt.Fprintf(a.stderr, "prepared %s in %s, %s of it judging partial units and what originals build on\n", l.branch(), time.Since(started).Round(time.Millisecond), b.detecting.Round(time.Millisecond))
	if drops != nil {
		return a.redrop(b, drops, reason, *undropArg != "", *push)
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
	if carried != nil && b.src.open && b.pr.Head.Ref == branch {
		held = b.src.rec.deferred()
	}
	if err := a.warnReset(branch, start); err != nil {
		return a.fail(err)
	}
	if _, err := git("switch", "--quiet", "--no-track", "-C", branch, "--end-of-options", start); err != nil {
		return a.fail(err)
	}

	r := newReport(b)
	r.opts = runOptions{NoWorkflowFiles: *noWorkflows, Mode: modeNew}
	// A drop is a decision about the line, not about one batch: a new batch
	// keeps the ones the closed batch recorded.
	r.dropped = b.src.rec.dropped()
	if carried != nil {
		r.opts.Mode = modeContinue
		if r.stale, err = keptOut(b, carried); err != nil {
			return a.fail(err)
		}
	}
	// cls is what each original became in this run, to an original that
	// builds on it; the rules decide the rest.
	mode := analysisMode{noWorkflowFiles: *noWorkflows}
	cls := map[*verdict]edgeClass{}
	classOf := func(f *verdict) edgeClass {
		if c, ok := b.ruleClass(f); ok {
			return c
		}
		if c, ok := cls[f]; ok {
			return c
		}
		return edgeMissing
	}
	for _, v := range b.qualifying() {
		on := false
		if carried != nil {
			if on, err = b.onBranch(carried, v); err != nil {
				return a.fail(err)
			}
		}
		switch {
		case on:
			r.onBranch = append(r.onBranch, v)
			cls[v] = edgeSatisfied
			continue
		case held[v.o.number].Reason != "":
			r.held = append(r.held, pick{v: v, status: "held", reason: held[v.o.number].Reason})
			cls[v] = edgeDeferred
			continue
		case v.onBatch != "":
			v.rule, v.reason = ruleOnLine, fmt.Sprintf("on the open batch pull request #%d", b.pr.Number)
			r.skipped[ruleOnLine] = append(r.skipped[ruleOnLine], v)
			continue
		}
		if v.partial != nil {
			// Judged at the line's head; what the batch picked since may
			// supply what it lacked there.
			if err := a.partialAtHead(b, v); err != nil {
				return a.fail(err)
			}
			if v.partial != nil {
				r.parked = append(r.parked, v)
				cls[v] = edgeNever
				continue
			}
		}
		if len(v.deps) > 0 {
			kind, err := a.dependsAt(b, r, v, mode, classOf)
			if err != nil {
				return a.fail(err)
			}
			if kind != edgeSatisfied {
				cls[v] = kind
				continue
			}
		}
		if *noWorkflows {
			files, err := workflowFiles(v.commits)
			if err != nil {
				return a.fail(err)
			}
			if len(files) > 0 {
				r.waiting = append(r.waiting, pick{v: v, status: "waiting", files: files})
				cls[v] = edgeWorkflow
				continue
			}
		}
		p, err := pickOriginal(v, func(base, head string) ([]string, error) {
			omitted, diag, err := b.omittedEffects(v, base, head)
			v.addDiag(diag...)
			return omitted, err
		})
		if err != nil {
			return a.fail(err)
		}
		switch p.status {
		case "partial":
			v.partial = p.files
			v.park = &parkedEntry{Original: v.o.number, Merge: v.o.merge, Kind: parkPartial, Files: sortedClean(p.files)}
			r.parked = append(r.parked, v)
			cls[v] = edgeNever
		case "picked":
			r.onBranch = append(r.onBranch, v)
			cls[v] = edgeSatisfied
		case "conflict":
			r.deferred = append(r.deferred, p)
			cls[v] = edgeDeferred
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
		if err := b.markPartial(r, carried.head); err != nil {
			return a.fail(err)
		}
	}
	return a.finish(b, r, branch, date, *push, false)
}

// dependsAt decides, from what v builds on that the branch lacks, whether
// the run leaves it out: waiting for a maintainer in the scheduled mode
// when only tests or docs build on what never comes, parked when anything
// else never comes, or waiting on prerequisites still to come. What is
// there, the line's, the batch's, or what this run picked or found empty,
// counts for nothing; once earlier picks moved the branch, v is judged at
// its head. It returns what v became to an original that builds on it,
// edgeSatisfied when the run goes on to pick it.
func (a *app) dependsAt(b *batch, r *report, v *verdict, mode analysisMode, classOf func(*verdict) edgeClass) (edgeClass, error) {
	deps := v.deps
	head, err := resolve("HEAD")
	if err != nil {
		return 0, err
	}
	if head != b.lineHead {
		if deps, err = b.depsAt(v, head); err != nil {
			return 0, err
		}
	}
	never, pending := edges(deps, classOf)
	primary, err := workflowPrimary(mode, v, never, pending)
	switch {
	case err != nil:
		return 0, err
	case primary:
		files, err := workflowFiles(v.commits)
		if err != nil {
			return 0, err
		}
		v.addDiag(testsOnly(v, never, b.line))
		r.waiting = append(r.waiting, pick{v: v, status: "waiting", files: files})
		return edgeWorkflow, nil
	case len(never) > 0:
		v.park = dependencyPark(v, never)
		r.parked = append(r.parked, v)
		return edgeNever, nil
	case len(pending) > 0:
		class, needs := strongest(pending)
		r.needs = append(r.needs, pick{v: v, status: "waiting", needs: numbers(needs)})
		return class, nil
	}
	return edgeSatisfied, nil
}

// partialAtHead judges v again where the run has got to, when earlier picks
// moved the branch past the line's head.
func (a *app) partialAtHead(b *batch, v *verdict) error {
	head, err := resolve("HEAD")
	if err != nil || head == b.lineHead {
		return err
	}
	return b.partialAt(v, head)
}

// keptOut lists the originals with backport/skip, or dropped, that the batch
// still carries: a run never takes a commit off the batch, --defer does.
func keptOut(b *batch, carried *history) ([]*verdict, error) {
	var out []*verdict
	for _, v := range b.verdicts {
		if v.rule != ruleSkipLabel && v.rule != ruleDropped {
			continue
		}
		if err := b.load(v); err != nil {
			return nil, err
		}
		on, err := b.onBranch(carried, v)
		if err != nil {
			return nil, err
		}
		if on {
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
	r := &report{line: b.line, newer: b.newer, skipped: map[int][]*verdict{}, other: len(b.other), start: b.start, carried: b.src.rec}
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
	if rebuilt && head == b.lineHead && b.pr != nil {
		// Deferring everything the batch carries leaves nothing to push: the
		// batch is closed, and its record says so first, with the head the
		// close is leased on.
		r.closing = &closing{PR: b.pr.Number, Branch: branch, Head: b.prHead}
		r.opts.Mode = modeClose
	}
	r.built = builtFrom{Main: b.mainHead, Line: b.lineHead, Newer: b.newerHead, Policy: b.policyAt, Batch: head, Tool: a.tool,
		Run: runURL(os.LookupEnv), At: a.now().UTC().Format(time.RFC3339), Options: r.opts}
	doc, err := buildReport(b, r)
	if err != nil {
		return a.fail(err)
	}
	p := publication{branch: branch, head: head, title: prTitle(b.line, date.Format(time.DateOnly)), report: storedReport{doc: doc, raw: reportJSON(doc)}}
	p.record = r.lineRecord(doc)
	// A record the next command would refuse is never written.
	if err := p.record.validate(b.line); err != nil {
		return a.fail(fmt.Errorf("the record this run made %w", err))
	}
	r.blocks = recordBlock(p.record) + "\n\n" + reportBlock(p.report.raw)
	template, err := lineTemplate(b.lineHead)
	if err != nil {
		return a.fail(err)
	}
	body, left := r.body(template)
	p.body = body
	_, _ = fmt.Fprint(a.stdout, body)
	skipped := 0
	for _, items := range r.skipped {
		skipped += len(items)
	}
	for _, rule := range left {
		for _, v := range r.skipped[rule] {
			_, _ = fmt.Fprintf(a.stderr, "skipped by %s: %s %s (%s)\n", ruleName(rule), v.o.url(), terminalSafe(v.o.title), terminalSafe(v.reason))
		}
	}
	for _, p := range r.waiting {
		_, _ = fmt.Fprintf(a.stderr, "waiting for a maintainer: %s %s changes %s\n", p.v.o.url(), terminalSafe(p.v.o.title), terminalSafe(strings.Join(p.files, ", ")))
	}
	_, _ = fmt.Fprintf(a.stderr, "%s at %.12s: %d on the branch, %d deferred, %d waiting for a maintainer, %d skipped\ntitle: %s\nreport: %s\n",
		branch, head, len(r.onBranch), len(r.deferred)+len(r.held), len(r.waiting), skipped, p.title, doc.ID)
	if !push {
		return 0
	}
	if head == b.lineHead && b.pr == nil {
		// Picking nothing is not a failure of the run; check still reports
		// whatever the line lacks. Merged and closed batches are never
		// written, so there is nowhere to keep what the run found.
		_, _ = fmt.Fprintf(a.stderr, "%s carries nothing beyond %s; nothing to push\n", branch, b.line.branch())
		_, _ = fmt.Fprintf(a.stderr, "no batch pull request is open on %s; this run's classification is not retained\n", b.line.branch())
		return 0
	}
	if err := a.publish(b, p, rebuilt); err != nil {
		return a.fail(err)
	}
	return 0
}

// lineRecord is the record a run leaves for the line: what later commands
// act on, with the full classification left to the report it names.
func (r *report) lineRecord(doc reportDoc) lineRecord {
	built := doc.Built
	dropped := slices.Clone(r.dropped)
	if dropped == nil {
		dropped = []droppedEntry{}
	}
	slices.SortFunc(dropped, func(a, b droppedEntry) int { return a.Original - b.Original })
	return lineRecord{Version: recordVersion, Line: doc.Line, Built: &built, Report: doc.ID, Closing: r.closing, Deferred: slices.Clone(doc.Primary.Deferred), Dropped: dropped, Stale: slices.Clone(doc.Overlays.Stale),
		Parked: carryAnnounced(doc.Primary.Parked, r.carried), Partial: slices.Clone(doc.Overlays.Partial), Changed: slices.Clone(doc.Overlays.Changed)}
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
	_, _ = fmt.Fprintf(a.stderr, "resetting the local %s from %s, dropping commits %.12s does not carry; git branch %s-%.12s %s restores them\n", branch, old, start, branch, old, old)
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

// publication is what a run publishes: the branch at head, and the body that
// carries the record and the report.
type publication struct {
	branch, head, title, body string
	report                    storedReport
	record                    lineRecord
}

// publish pushes the batch branch, then opens the draft batch pull request or
// updates the open one. A run pushes only a fast-forward of what the remote
// has. A rebuild replaces the branch, and only the head it was rebuilt from:
// the lease is that head, as ls-remote reads it now, so a commit pushed since
// is never dropped.
//
// The report goes up as a comment before the push and the body that names
// it, so no write leaves a body naming a report nobody can read; a new batch
// carries the report in the body it is opened with, and the comment follows.
// Everything is rendered and measured before the first write.
func (a *app) publish(b *batch, p publication, rebuilt bool) error {
	if b.pr != nil && b.pr.Head.Ref != p.branch {
		return fmt.Errorf("the open batch pull request #%d is on %s; continue it with --onto %s", b.pr.Number, b.pr.Head.Ref, b.prHead)
	}
	// A body without the block was written by hand, and the tool's would
	// replace it whole, title included; such a batch is the maintainer's.
	if b.pr != nil && !b.src.open {
		return fmt.Errorf("batch pull request #%d has no %s block, so backport-batch did not write it; merge or close it, and the next run opens a batch of its own", b.pr.Number, deferredFence)
	}
	if p.head == b.lineHead {
		if rebuilt && p.record.Closing != nil {
			return a.closeBatch(b, p)
		}
		return fmt.Errorf("%s carries nothing beyond %s; nothing to push", p.branch, b.line.branch())
	}
	heads, err := remoteBatchHeads(b.line)
	if err != nil {
		return err
	}
	pushed, onRemote := heads[p.branch]
	lease := ""
	switch {
	case rebuilt:
		if !onRemote || pushed != b.prHead {
			return fmt.Errorf("%s on %s is at %.12s, not at %.12s, which the rebuild started from; run again", p.branch, remote, pushed, b.prHead)
		}
		lease = pushed
	case onRemote && pushed != p.head:
		have, err := lookup(pushed)
		if err != nil {
			return err
		}
		descends := false
		if have != "" {
			if descends, err = isAncestor(pushed, p.head); err != nil {
				return err
			}
		}
		if !descends {
			refusal := fmt.Sprintf("%s on %s is at %.12s, which %.12s does not descend from; refusing a non-fast-forward push", p.branch, remote, pushed, p.head)
			// A run that stopped between its push and opening the pull request
			// leaves the branch, and the next run that day builds it anew.
			switch c := b.src.carrier; {
			case b.pr != nil:
			case c != nil && !b.src.open && c.Head.Ref == p.branch:
				// Continuing a batch that merged or closed would publish
				// what nobody can merge any more.
				refusal += fmt.Sprintf("; it is the branch of batch pull request #%d, which is %s; the next run picks again what the tool put on it, but not a commit someone pushed there, such as a resolved conflict: check what %.12s carries, then delete it with git push origin --delete %s and run again", c.Number, prState(c), pushed, p.branch)
			default:
				refusal += fmt.Sprintf("; no batch pull request is open on it, and backport-batch run --line %s --onto %s --push continues it and opens one", b.line.branch(), pushed)
			}
			return errors.New(refusal)
		}
	}
	if err := p.fits(); err != nil {
		return err
	}
	if b.pr != nil {
		if err := a.keepReports(b, &p); err != nil {
			return err
		}
	}
	if pushed != p.head {
		if _, err := git(pushArgs(p.branch, p.head, lease)...); err != nil {
			return err
		}
	}
	name, err := writeTemp("backport-batch-body-*.md", p.body)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()
	if b.pr == nil {
		n, url, err := a.gh.createPull(b.line.branch(), p.branch, p.title, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(a.stderr, "opened draft %s\n", url)
		if err := a.postReport(n, p.report); err != nil {
			return fmt.Errorf("posting report %.12s on #%d: %w; the body keeps it, and the next run --push posts it", p.report.doc.ID, n, err)
		}
		return nil
	}
	if err := a.stillOpen(b.pr.Number); err != nil {
		return err
	}
	if err := a.gh.editPull(b.pr.Number, p.title, name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.stderr, "updated %s\n", pullURL(b.pr))
	return nil
}

// fits says why GitHub would not take the comment or the body.
func (p *publication) fits() error {
	if _, err := reportComment(p.report); err != nil {
		return err
	}
	if len(p.body) > bodyLimit {
		return fmt.Errorf("the body is %d bytes with only what it cannot leave out, over GitHub's %d; nothing is published", len(p.body), bodyLimit)
	}
	return nil
}

// keepReports makes sure the open batch's comments keep both the report its
// body carries now and the one this run made, in that order, each once.
// When this run's report is one already kept, the body carries the kept
// bytes, which differ only in when and in which job it was made.
func (a *app) keepReports(b *batch, p *publication) error {
	pr := b.pr
	posted, bad, err := findReports(a.gh, pr.Number, b.line)
	if err != nil {
		return err
	}
	for _, why := range bad {
		a.warn("batch pull request #%d: %s; it does not count as a report", pr.Number, why)
	}
	earlier, err := bodyReport(pr.Body, b.line)
	if err != nil {
		return fmt.Errorf("batch pull request #%d: %w", pr.Number, err)
	}
	kept, ok := posted[p.report.doc.ID]
	if !ok && earlier != nil && earlier.doc.ID == p.report.doc.ID {
		kept, ok = *earlier, true
	}
	if ok {
		p.body = strings.Replace(p.body, reportBlock(p.report.raw), reportBlock(kept.raw), 1)
		p.report = kept
		if err := p.fits(); err != nil {
			return err
		}
	}
	if earlier != nil {
		if _, ok := posted[earlier.doc.ID]; !ok {
			if err := a.postReport(pr.Number, *earlier); err != nil {
				return fmt.Errorf("posting the earlier report %.12s on #%d: %w", earlier.doc.ID, pr.Number, err)
			}
			posted[earlier.doc.ID] = *earlier
		}
	}
	if _, ok := posted[p.report.doc.ID]; !ok {
		if err := a.postReport(pr.Number, p.report); err != nil {
			return fmt.Errorf("posting report %.12s on #%d: %w", p.report.doc.ID, pr.Number, err)
		}
	}
	return nil
}

// pullURL is where a pull request is on GitHub.
func pullURL(pr *pullRequest) string {
	if pullURLRE.MatchString(pr.HTMLURL) {
		return pr.HTMLURL
	}
	return fmt.Sprintf("%spull/%d", ourRepoURL, pr.Number)
}

// closeBatch closes the open batch that --defer left carrying nothing. Every
// write happens while it is open: the report, then the body whose record
// says it is closing and at which head. The close and the deletion of its
// branch then go only while both are still at that head.
func (a *app) closeBatch(b *batch, p publication) error {
	c := *p.record.Closing
	if err := p.fits(); err != nil {
		return err
	}
	if err := a.keepReports(b, &p); err != nil {
		return err
	}
	name, err := writeTemp("backport-batch-body-*.md", p.body)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()
	if err := a.stillOpen(c.PR); err != nil {
		return err
	}
	if err := a.gh.editPull(c.PR, p.title, name); err != nil {
		return fmt.Errorf("recording the close of #%d: %w", c.PR, err)
	}
	return a.finishClose(b.line, pullURL(b.pr), c)
}

// finishClose closes the batch pull request a record says is closing, and
// deletes its branch, each only while it is at the head the record froze.
func (a *app) finishClose(l line, url string, c closing) error {
	pr, err := a.gh.pull(c.PR)
	if err != nil {
		return fmt.Errorf("closing #%d, reading it again: %w", c.PR, err)
	}
	if st := prState(&pr); st != "open" || pr.Head.SHA != c.Head {
		return fmt.Errorf("closing #%d: it is %s at %.12s, not open at %.12s, which its record froze", c.PR, st, terminalSafe(pr.Head.SHA), c.Head)
	}
	heads, err := remoteBatchHeads(l)
	if err != nil {
		return err
	}
	if h := heads[c.Branch]; h != c.Head {
		return fmt.Errorf("closing #%d: %s on %s is at %.12s, not at %.12s, which its record froze", c.PR, c.Branch, remote, h, c.Head)
	}
	if err := a.gh.closePull(c.PR); err != nil {
		return fmt.Errorf("closing #%d: %w; backport-batch run --line %s --push finishes it", c.PR, err, l.branch())
	}
	if err := a.deleteBranch(l, c); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.stderr, "closed %s: without what is deferred it carries nothing beyond %s; the next batch tries them again\n", url, l.branch())
	return nil
}

// deleteBranch deletes the branch of a closed batch, leased on the head it
// was closed at: one someone pushed since is a person's to delete or keep.
func (a *app) deleteBranch(l line, c closing) error {
	heads, err := remoteBatchHeads(l)
	if err != nil {
		return err
	}
	switch h, ok := heads[c.Branch]; {
	case !ok:
		return nil
	case h != c.Head:
		return movedBranch(c, h)
	}
	if _, err := git(deleteArgs(c.Branch, c.Head)...); err != nil {
		return fmt.Errorf("deleting %s, left by the close of #%d: %w; backport-batch run --line %s --push deletes it", c.Branch, c.PR, err, l.branch())
	}
	return nil
}

// takenBranch is the refusal for a branch a close left that an open batch
// pull request now uses, whatever it holds, or nil.
func takenBranch(b *batch, c closing) error {
	if b.pr == nil || b.pr.Head.Ref != c.Branch {
		return nil
	}
	return fmt.Errorf("batch pull request #%d is open on %s, which the close of #%d left; the branch is that pull request's now, and the close leaves it alone", b.pr.Number, c.Branch, c.PR)
}

func movedBranch(c closing, h string) error {
	return fmt.Errorf("batch pull request #%d was closed at %.12s, but %s is at %.12s now; the next run picks again what the tool put on it, but not a commit someone pushed there, such as a resolved conflict: check what %.12s carries, then delete it with git push origin --delete %s, and run again", c.PR, c.Head, c.Branch, h, h, c.Branch)
}

// resumeClose finishes a close the record says is under way, before a run
// does anything else, and only under --push. It says whether the run may go
// on: only once the batch is closed and its branch gone. A run that finishes
// one stops there, and fails when it was asked to change the batch.
func (a *app) resumeClose(b *batch, push, changing bool) (bool, int) {
	if b.src.rec == nil || b.src.rec.Closing == nil {
		return true, 0
	}
	c := *b.src.rec.Closing
	heads, err := remoteBatchHeads(b.line)
	if err != nil {
		return false, a.fail(err)
	}
	h, onRemote := heads[c.Branch]
	switch {
	case b.src.open && h != c.Head:
		return false, a.fail(fmt.Errorf("batch pull request #%d is being closed, but %s is at %.12s, not at %.12s, which its record froze; a person closes it, or pushes the branch back, by hand", c.PR, c.Branch, h, c.Head))
	case b.src.open && !push:
		return false, a.fail(fmt.Errorf("batch pull request #%d is being closed: closing it and deleting %s are left; backport-batch run --line %s --push finishes it", c.PR, c.Branch, b.line.branch()))
	case b.src.open:
		if err := a.finishClose(b.line, pullURL(b.src.carrier), c); err != nil {
			return false, a.fail(err)
		}
		return false, a.finished(c, changing)
	case !onRemote:
		return true, 0
	case takenBranch(b, c) != nil:
		return false, a.fail(takenBranch(b, c))
	case h != c.Head:
		return false, a.fail(movedBranch(c, h))
	case !push:
		return false, a.fail(fmt.Errorf("batch pull request #%d was closed, but %s is still at %.12s; backport-batch run --line %s --push deletes it", c.PR, c.Branch, h, b.line.branch()))
	}
	if err := a.deleteBranch(b.line, c); err != nil {
		return false, a.fail(err)
	}
	_, _ = fmt.Fprintf(a.stderr, "deleted %s, which the close of #%d left\n", c.Branch, c.PR)
	return false, a.finished(c, changing)
}

// finished is the exit of a run that finished a close: a change it was asked
// for needs an open batch pull request, and none is left to make it on.
func (a *app) finished(c closing, changing bool) int {
	if !changing {
		return 0
	}
	return a.fail(fmt.Errorf("finished the close of #%d and made no other change: what this run was asked to change needs an open batch pull request, which the next run opens", c.PR))
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

// deleteArgs deletes branch, only while the remote has it at lease.
func deleteArgs(branch, lease string) []string {
	return []string{"push", "--quiet", "--force-with-lease=refs/heads/" + branch + ":" + lease, "--end-of-options", remote, ":refs/heads/" + branch}
}
