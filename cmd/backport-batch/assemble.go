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
	status string // picked, conflict or empty
	commit string
	files  []string
}

func batchBranch(l line, date time.Time) string {
	return "backport-batch/" + l.branch() + "-" + date.Format("20060102")
}

func (a *app) run(args []string) int {
	fs := a.flags("run")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	onto := fs.String("onto", "", "head of the pushed batch branch to continue")
	dateArg := fs.String("date", "", "batch date, YYYY-MM-DD (default: today, UTC)")
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
		if date, err = time.Parse("20060102", batchBranchRE.FindStringSubmatch(branch)[2]); err != nil {
			return a.fail(err)
		}
	}
	if err := checkBranch(branch); err != nil {
		return a.fail(err)
	}
	if old, err := resolve("refs/heads/" + branch); err == nil && old != start {
		fmt.Fprintf(a.stderr, "resetting the local %s from %.12s\n", branch, old)
	}
	if _, err := git("switch", "--quiet", "--no-track", "-C", branch, "--end-of-options", start); err != nil {
		return a.fail(err)
	}

	r := &report{line: l, newer: b.newer, skipped: map[int][]*verdict{}, other: len(b.other)}
	for _, v := range b.verdicts {
		if v.rule != 0 {
			r.skipped[v.rule] = append(r.skipped[v.rule], v)
		}
	}
	for _, v := range b.qualifying() {
		switch {
		case carried != nil && carried.picked(v.commits) != "":
			r.onBranch = append(r.onBranch, v)
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
	head, err := resolve("HEAD")
	if err != nil {
		return a.fail(err)
	}
	if *onto != "" && !isAncestor(*onto, head) {
		return a.fail(fmt.Errorf("%.12s does not descend from --onto %.12s", head, *onto))
	}

	title := prTitle(l, date.Format(time.DateOnly))
	body, left := r.body(lineTemplate(b.lineHead))
	fmt.Fprint(a.stdout, body)
	for _, rule := range left {
		for _, v := range r.skipped[rule] {
			fmt.Fprintf(a.stderr, "skipped by rule %d: %s %s (%s)\n", rule, v.o.url(), terminalSafe(v.o.title), terminalSafe(v.reason))
		}
	}
	fmt.Fprintf(a.stderr, "%s at %.12s: %d on the branch, %d deferred, %d skipped\ntitle: %s\n",
		branch, head, len(r.onBranch), len(r.deferred), len(b.verdicts)-len(r.onBranch)-len(r.deferred), title)
	if !*push {
		return 0
	}
	if err := a.publish(b, branch, head, title, body); err != nil {
		return a.fail(err)
	}
	return 0
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
	stdout, stderr, code, err := gitStatus(args...)
	if err != nil {
		return p, err
	}
	if code == 0 {
		p.status = "picked"
		p.commit, err = resolve("HEAD")
		return p, err
	}
	out, err := git("diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return p, err
	}
	if files := splitNUL(out); len(files) > 0 {
		p.status, p.files = "conflict", files
		_, err := git("cherry-pick", "--abort")
		return p, err
	}
	if msg := stdout + stderr; strings.Contains(msg, "is now empty") || strings.Contains(msg, "nothing to commit") {
		p.status = "empty"
		_, err := git("cherry-pick", "--skip")
		return p, err
	}
	_, _ = git("cherry-pick", "--abort")
	return p, fmt.Errorf("cherry-pick of #%d (%s) failed: %s", v.o.number, v.o.merge, strings.TrimSpace(stderr))
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

// publish pushes the batch branch, only ever as a fast-forward of what the
// remote has, then opens the draft batch pull request or updates the open one.
func (a *app) publish(b *batch, branch, head, title, body string) error {
	if b.pr != nil && b.pr.Head.Ref != branch {
		return fmt.Errorf("the open batch pull request #%d is on %s; continue it with --onto %s", b.pr.Number, b.pr.Head.Ref, b.prHead)
	}
	if head == b.lineHead {
		return fmt.Errorf("%s carries nothing beyond %s; nothing to push", branch, b.line.branch())
	}
	heads, err := remoteBatchHeads(b.line)
	if err != nil {
		return err
	}
	if pushed, ok := heads[branch]; ok && pushed != head {
		if !gitOK("cat-file", "-e", "--end-of-options", pushed+"^{commit}") || !isAncestor(pushed, head) {
			return fmt.Errorf("%s on %s is at %.12s, which %.12s does not descend from; refusing a non-fast-forward push", branch, remote, pushed, head)
		}
	}
	if heads[branch] != head {
		if _, err := git("push", "--quiet", "--end-of-options", remote, head+":refs/heads/"+branch); err != nil {
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
