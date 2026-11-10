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
		return nil, "", errors.New("--reason goes with --defer or --drop")
	}
	reason = capped(oneLine(clean(reason, false)), titleLimit)
	if reason == "" {
		return nil, "", errors.New("--defer needs a --reason, which the batch body records")
	}
	numbers, err := parseNumbers("--defer", list)
	return numbers, reason, err
}

// parseNumbers reads the N[,N] list a flag takes, each once.
func parseNumbers(flag, list string) ([]int, error) {
	var numbers []int
	for _, s := range strings.Split(list, ",") {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(s), "#"))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s %q is not a list of pull request numbers", flag, list)
		}
		if !slices.Contains(numbers, n) {
			numbers = append(numbers, n)
		}
	}
	return numbers, nil
}

// rebuild takes the originals in numbers out of the open batch: it builds the
// batch branch again off the line's head without their commits, and records
// them in the body as deferred, for reason, next to what earlier deferrals
// recorded. It adds no new original; the next run does.
func (a *app) rebuild(b *batch, numbers []int, reason string, push bool) int {
	if b.pr == nil {
		return a.fail(fmt.Errorf("no batch pull request is open on %s; --defer rebuilds the open one", b.line.branch()))
	}
	hold := map[int]string{}
	for n, e := range b.recorded() {
		if e.Reason != "" {
			hold[n] = e.Reason
		}
	}
	carried, err := readPicks(b.prHead, b.lineHead, b.textUntil)
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
		case v.rule != 0 && !slices.Contains(stale, v) && b.partialUnits[v] == nil:
			return a.fail(fmt.Errorf("#%d is not one the batch takes: %s, %s", n, ruleName(v.rule), terminalSafe(v.reason)))
		}
		hold[n] = reason
	}
	return a.reassemble(b, hold, hold, b.src.rec.dropped(), modeDefer, push)
}

// reassemble builds the open batch again off the line's head without the
// units of the originals in out, and reports it with hold deferred for their
// reasons and drops kept.
func (a *app) reassemble(b *batch, out, hold map[int]string, drops []droppedEntry, mode string, push bool) int {
	branch := b.pr.Head.Ref
	start, replay, err := rebuildPlan(b, out)
	if err != nil {
		return a.fail(err)
	}
	if err := a.warnReset(branch, start); err != nil {
		return a.fail(err)
	}
	if _, err := git("switch", "--quiet", "--no-track", "-C", branch, "--end-of-options", start); err != nil {
		return a.fail(err)
	}
	for _, u := range replay {
		failed, files, err := replayUnit(u)
		switch {
		case err != nil:
			return a.fail(err)
		case failed != "":
			return a.fail(fmt.Errorf("without what is deferred, %.12s%s no longer applies (%s); defer it as well", failed, originName(b, u), terminalSafe(strings.Join(files, ", "))))
		}
	}
	head, err := resolve("HEAD")
	if err != nil {
		return a.fail(err)
	}
	picks, err := readPicks(head, b.lineHead, b.textUntil)
	if err != nil {
		return a.fail(err)
	}
	return a.reclassify(b, picks, hold, drops, mode, push)
}

// reclassify reports the open batch as picks shows it, after an operation
// that adds no original: what the record held stays held, and an original
// the batch neither carries nor records is missing until a run tries it.
func (a *app) reclassify(b *batch, picks *history, hold map[int]string, drops []droppedEntry, mode string, push bool) int {
	date, err := branchDate(b.pr.Head.Ref)
	if err != nil {
		return a.fail(err)
	}
	recorded := b.recorded()
	r := newReport(b)
	// None of these changes how the batch is assembled, so the record keeps
	// the flag the batch was assembled with.
	r.opts = runOptions{NoWorkflowFiles: b.src.savedNoWorkflowFiles(), Mode: mode}
	r.dropped = drops
	if r.stale, err = keptOut(b, picks); err != nil {
		return a.fail(err)
	}
	for _, v := range b.qualifying() {
		e := recorded[v.o.number]
		on, err := b.onBranch(picks, v)
		if err != nil {
			return a.fail(err)
		}
		switch {
		case on:
			r.onBranch = append(r.onBranch, v)
		case hold[v.o.number] != "":
			r.held = append(r.held, pick{v: v, status: "held", reason: hold[v.o.number]})
		case v.park != nil:
			r.parked = append(r.parked, v)
		case e.Workflows:
			r.waiting = append(r.waiting, pick{v: v, status: "waiting", files: e.Files, more: e.More})
		case len(e.Files) > 0:
			r.deferred = append(r.deferred, pick{v: v, status: "conflict", files: e.Files, more: e.More, failed: e.Commit})
		default:
			// Merged into main since the batch was assembled, or recorded
			// nowhere: a rebuild adds no original, the next run tries it.
			r.missing = append(r.missing, v)
		}
	}
	if err := b.markPartial(r, picks.head); err != nil {
		return a.fail(err)
	}
	return a.finish(b, r, b.pr.Head.Ref, date, push, true)
}

// recorded is what the open batch's record defers; a closed batch's record
// holds nothing back on this one.
func (b *batch) recorded() map[int]deferredEntry {
	if !b.src.open {
		return map[int]deferredEntry{}
	}
	return b.src.rec.deferred()
}

// rebuildPlan is where the rebuild of the open batch without the originals in
// drop starts, and the units of the batch it picks again on top, oldest first,
// each in its own shape. While the batch still sits on the line's head,
// everything before the first unit it drops stays as it is.
func rebuildPlan(b *batch, drop map[int]string) (string, []unit, error) {
	units, err := batchUnits(b.prHead, b.lineHead)
	if err != nil {
		return "", nil, err
	}
	for _, u := range units {
		if len(u.parents) > 2 || u.merge() && len(batchedFrom(u.message)) != 1 {
			return "", nil, fmt.Errorf("batch pull request #%d carries merge commit %.12s, which is no batch merge of an original and which a rebuild cannot pick again; take the original out by hand", b.pr.Number, u.commit)
		}
	}
	dropped := func(u unit) bool {
		v := b.unitOriginal(u)
		return v != nil && drop[v.o.number] != ""
	}
	var keep []unit
	for _, u := range units {
		if !dropped(u) {
			keep = append(keep, u)
		}
	}
	first := slices.IndexFunc(units, dropped)
	onLineHead, err := isAncestor(b.lineHead, b.prHead)
	if err != nil {
		return "", nil, err
	}
	switch {
	case !onLineHead:
		return b.lineHead, keep, nil
	case first < 0:
		return b.prHead, nil, nil
	case first == 0:
		return b.lineHead, keep, nil
	}
	return units[first-1].commit, keep[first:], nil
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

// originName names the original a unit carries, for an error message.
func originName(b *batch, u unit) string {
	if v := b.unitOriginal(u); v != nil {
		return fmt.Sprintf(", of #%d,", v.o.number)
	}
	return ""
}
