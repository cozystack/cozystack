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
	if err := a.warnLeftBranch(b); err != nil {
		return a.fail(err)
	}
	// The scheduled run's mode, as the open batch saved it; with none open,
	// the scheduled run's, which is what the next batch is built in.
	mode := analysisMode{noWorkflowFiles: true}
	if b.src.open {
		mode.noWorkflowFiles = b.src.savedNoWorkflowFiles()
	}
	disp, err := b.dispositions(mode, *cut)
	if err != nil {
		return a.fail(err)
	}
	var missing, failing []string
	for _, v := range b.verdicts {
		d, ok := disp[v]
		switch {
		case !ok:
		case d.kind == outcomeMissing:
			missing = append(missing, d.text)
		case d.kind == outcomeUnmerged || d.kind == outcomeWaiting:
			failing = append(failing, d.text)
		case d.kind == outcomeWarning:
			a.warn("%s", d.text)
		case d.kind == outcomeNote:
			_, _ = fmt.Fprintf(a.stderr, "note: %s\n", d.text)
		}
	}
	if len(missing)+len(failing) == 0 {
		if *cut {
			_, _ = fmt.Fprintf(a.stdout, "%s: no change the policy takes is missing before the cut\n", l.branch())
		} else {
			_, _ = fmt.Fprintf(a.stdout, "%s: every change the policy takes is on the line or on the batch\n", l.branch())
		}
		return 0
	}
	where := "and no batch pull request is open"
	if b.pr != nil {
		where = "nor on batch pull request #" + strconv.Itoa(b.pr.Number)
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(a.stdout, "%s: %d changes the policy takes are not on the line, %s:\n", l.branch(), len(missing), where)
		for _, text := range missing {
			_, _ = fmt.Fprintf(a.stdout, "  %s\n", text)
		}
	}
	for _, text := range failing {
		_, _ = fmt.Fprintf(a.stdout, "%s: %s\n", l.branch(), text)
	}
	return 1
}

// warnLeftBranch names the branch a closed batch's close left behind.
func (a *app) warnLeftBranch(b *batch) error {
	if b.src.open || b.src.rec == nil || b.src.rec.Closing == nil {
		return nil
	}
	c := b.src.rec.Closing
	heads, err := remoteBatchHeads(b.line)
	if err != nil {
		return err
	}
	switch h, ok := heads[c.Branch]; {
	case !ok:
	case takenBranch(b, *c) != nil:
		a.warn("%v", takenBranch(b, *c))
	case h == c.Head:
		a.warn("batch pull request #%d is closed, but its branch %s is still at %.12s; backport-batch run --line %s --push deletes it", c.PR, c.Branch, h, b.line.branch())
	default:
		a.warn("%v", movedBranch(*c, h))
	}
	return nil
}

// outcome is what check makes of one original.
type outcome int

const (
	// outcomeMissing fails check and the cut: nothing accounts for an
	// original the line lacks.
	outcomeMissing outcome = iota + 1
	// outcomeUnmerged and outcomeWaiting fail the cut only: the cut would not
	// carry what is only on the open batch, nor what waits there.
	outcomeUnmerged
	outcomeWaiting
	outcomeWarning
	outcomeNote
)

type disposition struct {
	kind outcome
	text string
}

// dispositions says what check reports about each original that needs a line
// of its own; the others pass in silence. The text is what follows the line's
// name, or the original alone for one that is missing. Originals are taken in
// merge order, each by the first of: a unit on the batch that does only part
// of it, a reason a person deferred it for, a park, a wait for a maintainer
// or on other originals, and what the record says of it.
func (b *batch) dispositions(mode analysisMode, cut bool) (map[*verdict]disposition, error) {
	if c := b.src.closingOpen(); c != nil {
		return nil, fmt.Errorf("batch pull request #%d is being closed; backport-batch run --line %s --push finishes it", c.PR, b.line.branch())
	}
	var stale []*verdict
	if b.pr != nil {
		var err error
		if stale, err = keptOut(b, b.picks); err != nil {
			return nil, err
		}
	}
	// A record a closed batch left still says what a maintainer decided, but
	// nothing waits on a batch that is gone.
	deferred, on := b.src.rec.deferred(), ""
	if c := b.src.carrier; c != nil {
		on = fmt.Sprintf("#%d", c.Number)
		if !b.src.open {
			on += " (" + prState(c) + ")"
		}
	}
	if err := b.analyze(); err != nil {
		return nil, err
	}
	if b.pr != nil {
		if err := b.keepRecordedPartials(b.prHead); err != nil {
			return nil, err
		}
	}
	held := map[int]string{}
	for n, e := range deferred {
		if e.Reason != "" {
			held[n] = e.Reason
		}
	}
	states, err := b.classify(mode, held, deferred, b.src.open)
	if err != nil {
		return nil, err
	}
	// waitsOn names what an original waits on, as check says it.
	waitsOn := func(s *state) string {
		var names []string
		for _, f := range s.needs {
			names = append(names, fmt.Sprintf("#%d", f.o.number))
		}
		return strings.Join(names, " and ")
	}
	out := map[*verdict]disposition{}
	for _, v := range b.verdicts {
		named := v.o.url() + " " + terminalSafe(v.o.title)
		e, isDeferred := deferred[v.o.number]
		s := states[v]
		if s == nil {
			s = &state{kind: stOwn}
		}
		switch {
		case b.partialUnits[v] != nil && cut:
			out[v] = disposition{outcomeUnmerged, fmt.Sprintf("#%d is on batch #%d, but only part of it applies to %s: it leaves %s without effect; take it out with backport-batch run --defer before the cut: %s", v.o.number, b.pr.Number, b.line.branch(), quotedFiles(b.partialUnits[v]), named)}
		case b.partialUnits[v] != nil:
			out[v] = disposition{outcomeWarning, fmt.Sprintf("#%d is on batch #%d, but only part of it applies to %s: it leaves %s without effect; take it out with backport-batch run --defer: %s", v.o.number, b.pr.Number, b.line.branch(), quotedFiles(b.partialUnits[v]), named)}
		case slices.Contains(stale, v):
			out[v] = disposition{outcomeWarning, fmt.Sprintf("#%d is still on batch #%d; take it out with backport-batch run --defer: %s", v.o.number, b.pr.Number, named)}
		case v.rule == ruleDropped:
			d := b.drops[v.o.number]
			out[v] = disposition{outcomeNote, fmt.Sprintf("#%d is dropped from %s on #%d (%s): %s", v.o.number, b.line.branch(), d.On, terminalSafe(d.Reason), named)}
		case v.rule != 0:
		case v.onBatch != "":
			if cut {
				out[v] = disposition{outcomeUnmerged, fmt.Sprintf("#%d is only on open batch PR #%d, merge it before the cut: %s", v.o.number, b.pr.Number, named)}
			}
		case s.kind == stHeld:
			out[v] = disposition{outcomeWarning, fmt.Sprintf("deferred on %s to the next batch: %s (%s)", on, named, terminalSafe(e.Reason))}
		case s.kind == stPartial:
			out[v] = disposition{outcomeNote, fmt.Sprintf("#%d is parked: only part of it applies to %s, which would leave %s without effect; port what applies as a pull request of its own: %s", v.o.number, b.line.branch(), quotedFiles(v.park.Files), named)}
		case s.kind == stWorkflowPrimary && s.class == edgeMissing:
			out[v] = disposition{outcomeMissing, named}
		case s.kind == stParked:
			out[v] = disposition{outcomeNote, fmt.Sprintf("#%d is parked: it builds on %s, which never reaches %s; port what applies as a pull request of its own, or drop it: %s", v.o.number, parkNames(*v.park), b.line.branch(), named)}
		case s.kind == stNeeds && s.wait == edgeMissing:
			out[v] = disposition{outcomeMissing, fmt.Sprintf("%s (it waits on %s, which is not on the line either)", named, waitsOn(s))}
		case s.kind == stNeeds && s.wait == edgeWorkflow && cut:
			out[v] = disposition{outcomeWaiting, fmt.Sprintf("#%d waits on %s, which changes %s and waits on batch PR %s for a maintainer; bring it over, or defer both, before the cut: %s", v.o.number, waitsOn(s), workflowsDir, on, named)}
		case s.kind == stNeeds && s.wait == edgeWorkflow:
			out[v] = disposition{outcomeWarning, fmt.Sprintf("#%d waits on %s, which waits on %s for a maintainer: %s", v.o.number, waitsOn(s), on, named)}
		case s.kind == stNeeds && s.wait == edgeDeferred:
			what := "a deferred conflict on " + on
			if f := s.needs[0]; deferred[f.o.number].Reason != "" {
				what = "deferred on " + on + " to the next batch"
			} else if states[f] != nil && states[f].kind == stNeeds {
				what = "which waits itself"
			}
			out[v] = disposition{outcomeWarning, fmt.Sprintf("#%d waits on %s, %s: %s", v.o.number, waitsOn(s), what, named)}
		case s.kind == stNeeds:
			newer := "the newer line"
			if b.newer != nil {
				newer = b.newer.branch()
			}
			out[v] = disposition{outcomeNote, fmt.Sprintf("#%d waits on %s, which is not on %s yet: %s", v.o.number, waitsOn(s), newer, named)}
		case isDeferred && e.Workflows && !b.src.open:
			out[v] = disposition{outcomeMissing, named}
		case isDeferred && e.Workflows && cut:
			out[v] = disposition{outcomeWaiting, fmt.Sprintf("#%d changes %s and waits on batch PR %s for a maintainer, bring it over or defer it before the cut: %s", v.o.number, workflowsDir, on, named)}
		case isDeferred && e.Workflows:
			out[v] = disposition{outcomeWarning, fmt.Sprintf("waiting on %s for a maintainer, since it changes %s: %s", on, workflowsDir, named)}
		case isDeferred && len(e.Needs) == 0:
			out[v] = disposition{outcomeWarning, fmt.Sprintf("deferred conflict on %s: %s", on, named)}
		default:
			out[v] = disposition{outcomeMissing, named}
		}
	}
	return out, nil
}

// parkNames names what a park waits on for a terminal.
func parkNames(p parkedEntry) string {
	var out []string
	for _, n := range p.Needs {
		out = append(out, fmt.Sprintf("#%d", n))
	}
	for _, c := range p.Commits {
		out = append(out, c[:12])
	}
	return strings.Join(out, ", ")
}

// quotedFiles names files for a terminal, each quoted, the first few of them.
func quotedFiles(files []string) string {
	var out []string
	for _, f := range files[:min(len(files), filesListed)] {
		out = append(out, strconv.Quote(f))
	}
	if more := len(files) - len(out); more > 0 {
		out = append(out, fmt.Sprintf("%d more", more))
	}
	return strings.Join(out, ", ")
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
	if c := b.src.closingOpen(); c != nil {
		return a.fail(fmt.Errorf("batch pull request #%d is being closed; backport-batch run --line %s --push finishes it", c.PR, l.branch()))
	}
	if err := a.warnLeftBranch(b); err != nil {
		return a.fail(err)
	}
	why, err := refuseFold(b, *number, *commit)
	if err != nil {
		return a.fail(err)
	}
	if why != "" {
		_, _ = fmt.Fprintf(a.stdout, "refused: %s\n", why)
		return 1
	}
	_, _ = fmt.Fprintln(a.stdout, "ok")
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
		return fmt.Sprintf("#%d is not one the batch takes: %s, %s", n, ruleName(v.rule), terminalSafe(v.reason)), nil
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
	omitted, _, err := b.omittedEffects(v, u.parents[0], u.commit)
	if err != nil || len(omitted) == 0 {
		return "", err
	}
	return leftOut(v, b.line, omitted), nil
}
