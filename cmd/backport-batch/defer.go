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
	"slices"
	"strconv"
	"strings"
)

// parseDefer reads --defer N[,N] and the --reason that has to come with it.
func parseDefer(list, reason string) ([]int, string, error) {
	if list == "" {
		return nil, "", errors.New("--reason goes with --defer")
	}
	reason = capped(oneLine(clean(reason, false)), titleLimit)
	if reason == "" {
		return nil, "", errors.New("--defer needs a --reason, which the batch body records")
	}
	var numbers []int
	for _, s := range strings.Split(list, ",") {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(s), "#"))
		if err != nil || n <= 0 {
			return nil, "", fmt.Errorf("--defer %q is not a list of pull request numbers", list)
		}
		if !slices.Contains(numbers, n) {
			numbers = append(numbers, n)
		}
	}
	return numbers, reason, nil
}

// rebuild takes the originals in numbers out of the open batch: it builds the
// batch branch again off the line's head without their commits, and records
// them in the body as deferred, for reason, next to what earlier deferrals
// recorded. It adds no new original; the next run does.
func (a *app) rebuild(b *batch, numbers []int, reason string, push bool) int {
	if b.pr == nil {
		return a.fail(fmt.Errorf("no batch pull request is open on %s; --defer rebuilds the open one", b.line.branch()))
	}
	branch := b.pr.Head.Ref
	date, err := branchDate(branch)
	if err != nil {
		return a.fail(err)
	}
	recorded, err := parseDeferred(b.pr.Body, b.line)
	if err != nil {
		return a.fail(fmt.Errorf("batch pull request #%d: %w", b.pr.Number, err))
	}
	hold := map[int]string{}
	for n, e := range recorded {
		if e.Reason != "" {
			hold[n] = e.Reason
		}
	}
	carried, err := readPicks(b.prHead, b.lineHead)
	if err != nil {
		return a.fail(err)
	}
	stale, err := keptOut(b, carried)
	if err != nil {
		return a.fail(err)
	}
	for _, n := range numbers {
		switch v := b.find(n); {
		case v == nil:
			return a.fail(fmt.Errorf("#%d did not merge into main after %s branched", n, b.line.branch()))
		case v.rule != 0 && !slices.Contains(stale, v):
			return a.fail(fmt.Errorf("#%d is not one the batch takes: rule %d, %s", n, v.rule, terminalSafe(v.reason)))
		}
		hold[n] = reason
	}
	start, replay, err := rebuildPlan(b, hold)
	if err != nil {
		return a.fail(err)
	}
	if _, err := git("switch", "--quiet", "--no-track", "-C", branch, "--end-of-options", start); err != nil {
		return a.fail(err)
	}
	for _, c := range replay {
		status, files, err := applyPick("-c", "core.hooksPath=/dev/null", "cherry-pick", "--end-of-options", c)
		switch {
		case err != nil:
			return a.fail(fmt.Errorf("picking %.12s again: %w", c, err))
		case status == "conflict":
			return a.fail(fmt.Errorf("without what is deferred, %.12s%s no longer applies (%s); defer it as well", c, b.originOf(c), strings.Join(files, ", ")))
		}
	}
	head, err := resolve("HEAD")
	if err != nil {
		return a.fail(err)
	}
	picks, err := readPicks(head, b.lineHead)
	if err != nil {
		return a.fail(err)
	}
	r := newReport(b)
	if r.stale, err = keptOut(b, picks); err != nil {
		return a.fail(err)
	}
	for _, v := range b.qualifying() {
		e := recorded[v.o.number]
		switch {
		case picks.picked(v.commits) != "":
			r.onBranch = append(r.onBranch, v)
		case hold[v.o.number] != "":
			r.held = append(r.held, pick{v: v, status: "held", reason: hold[v.o.number]})
		case len(e.Files) > 0:
			r.deferred = append(r.deferred, pick{v: v, status: "conflict", files: e.Files, more: e.More})
		}
	}
	return a.finish(b, r, branch, date, push, true)
}

// rebuildPlan is where the rebuild of the open batch without the originals in
// drop starts, and the batch commits it picks again on top, oldest first.
// While the batch still sits on the line's head, everything before the first
// commit it drops stays as it is.
func rebuildPlan(b *batch, drop map[int]string) (string, []string, error) {
	out, err := git("rev-list", "--reverse", "--topo-order", "--parents", "--end-of-options", b.prHead, "^"+b.lineHead)
	if err != nil {
		return "", nil, err
	}
	var commits []string
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		for _, sha := range f {
			if checkSHA(sha) != nil {
				return "", nil, fmt.Errorf("rev-list: %q is not a commit", sha)
			}
		}
		if len(f) != 2 {
			return "", nil, fmt.Errorf("batch pull request #%d carries merge commit %.12s, which a rebuild cannot pick again; take the original out by hand", b.pr.Number, f[0])
		}
		commits = append(commits, f[0])
	}
	msgs, err := commitMessages(commits)
	if err != nil {
		return "", nil, err
	}
	dropped := func(c string) bool {
		v := b.pickedBy(xSources(msgs[c].message))
		return v != nil && drop[v.o.number] != ""
	}
	var keep []string
	for _, c := range commits {
		if !dropped(c) {
			keep = append(keep, c)
		}
	}
	first := slices.IndexFunc(commits, dropped)
	switch {
	case !isAncestor(b.lineHead, b.prHead):
		return b.lineHead, keep, nil
	case first < 0:
		return b.prHead, nil, nil
	case first == 0:
		return b.lineHead, keep, nil
	}
	return commits[first-1], keep[first:], nil
}

// pickedBy is the original whose commits the -x sources name, or nil.
func (b *batch) pickedBy(sources []string) *verdict {
	for _, v := range b.verdicts {
		for _, s := range sources {
			if slices.Contains(v.commits, s) {
				return v
			}
		}
	}
	return nil
}

// originOf names the original a batch commit picks, for an error message.
func (b *batch) originOf(commit string) string {
	msgs, err := commitMessages([]string{commit})
	if err != nil {
		return ""
	}
	if v := b.pickedBy(xSources(msgs[commit].message)); v != nil {
		return fmt.Sprintf(", which picks #%d,", v.o.number)
	}
	return ""
}
