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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A clean pick can still carry only part of an original: git deletes a file
// the line never had without a word, and leaves alone a file the line lacks.
// What follows tells such a unit from a whole one, by what it leaves without
// effect of the files the original modifies or deletes.

// treeEntry is one record of git ls-tree.
type treeEntry struct{ mode, kind, oid string }

// regular says whether git can merge the entry as text: a file, not a
// symlink, a submodule or a tree.
func (e treeEntry) regular() bool {
	return e.kind == "blob" && (e.mode == "100644" || e.mode == "100755")
}

// treeEntries reads the entries paths have at treeish, from full ls-tree
// records: a path that is a tree there, or absent, has none.
func treeEntries(treeish string, paths []string) (map[string]treeEntry, error) {
	out := map[string]treeEntry{}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	const chunk = 500
	for len(paths) > 0 {
		n := min(chunk, len(paths))
		args := append([]string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--full-tree", "--end-of-options", treeish, "--"}, paths[:n]...)
		raw, stderr, code, err := gitStatus(args...)
		switch {
		case err != nil:
			return nil, fmt.Errorf("git ls-tree %s did not run", terminalSafe(treeish))
		case code != 0:
			// The paths stay out of the error: they are file names someone
			// chose, and the error reaches a terminal.
			return nil, fmt.Errorf("git ls-tree %s: exit %d: %s", terminalSafe(treeish), code, terminalSafe(stderr))
		}
		for _, rec := range splitNUL(raw) {
			meta, path, ok := strings.Cut(rec, "\t")
			f := strings.Fields(meta)
			if !ok || len(f) != 3 || checkSHA(f[2]) != nil {
				return nil, fmt.Errorf("git ls-tree %s: unreadable record %q", treeish, terminalSafe(rec))
			}
			if want[path] {
				out[path] = treeEntry{mode: f[0], kind: f[1], oid: f[2]}
			}
		}
		paths = paths[n:]
	}
	return out, nil
}

// renameSet is what git diff -M found renamed between two commits, old path
// to new, and whether it looked at every candidate.
type renameSet struct {
	moved    map[string]string
	complete bool
}

// renames reads the renames between from and to, once per pair. git says on
// stderr when it skipped inexact detection for too many files; complete is
// then false, and no path may be called absent for want of a rename.
func (b *batch) renames(from, to string) (renameSet, error) {
	key := from + ".." + to
	if s, ok := b.renameCache[key]; ok {
		return s, nil
	}
	// -l0 lifts git's own limit, which the picker's merge does not share:
	// a rename the pick follows has to be one the checks of its shape see.
	stdout, stderr, code, err := gitStatusTimed("diff", "--no-ext-diff", "--no-textconv", "-M", "-l"+strconv.Itoa(b.renameLimit), "--name-status", "-z", "--diff-filter=R", "--end-of-options", from, to)
	switch {
	case errors.Is(err, errDeadline):
		// Out of time is rename detection skipped: no path is called
		// absent for want of it.
		s := renameSet{moved: map[string]string{}}
		if b.renameCache == nil {
			b.renameCache = map[string]renameSet{}
		}
		b.renameCache[key] = s
		return s, nil
	case err != nil:
		return renameSet{}, err
	case code != 0:
		return renameSet{}, fmt.Errorf("git diff -M %s %s: exit %d: %s", from, to, code, strings.TrimSpace(stderr))
	}
	f := splitNUL(stdout)
	if len(f)%3 != 0 {
		return renameSet{}, fmt.Errorf("unreadable renames between %s and %s", from, to)
	}
	s := renameSet{moved: map[string]string{}, complete: !strings.Contains(stderr, "detection was skipped")}
	for i := 0; i < len(f); i += 3 {
		if !strings.HasPrefix(f[i], "R") {
			return renameSet{}, fmt.Errorf("unreadable renames between %s and %s: %q", from, to, terminalSafe(f[i]))
		}
		s.moved[f[i+1]] = f[i+2]
	}
	if b.renameCache == nil {
		b.renameCache = map[string]renameSet{}
	}
	b.renameCache[key] = s
	return s, nil
}

// effectPaths are the paths v modifies or deletes; what it adds has nothing
// to correspond to.
func effectPaths(v *verdict) []string {
	var out []string
	for _, c := range v.changes {
		if c.status == 'M' || c.status == 'D' {
			out = append(out, c.path)
		}
	}
	return out
}

// correspondence maps a path of the original (at M^1) to the path holding the
// same file on target, through a main-side rename base..M^1, a line-side
// rename base..target, or both composed through the base path. A path with
// no counterpart maps to itself, and one git skipped rename detection for is
// left out and named in unknown: it is not known to be absent.
func (b *batch) correspondence(v *verdict, target string) (map[string]string, []string, error) {
	paths := effectPaths(v)
	have, err := treeEntries(target, paths)
	if err != nil {
		return nil, nil, err
	}
	corr := map[string]string{}
	var missing []string
	for _, p := range paths {
		if _, ok := have[p]; ok {
			corr[p] = p
		} else {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return corr, nil, nil
	}
	onMain, err := b.renames(b.base, v.o.merge+"^1")
	if err != nil {
		return nil, nil, err
	}
	onLine, err := b.renames(b.base, target)
	if err != nil {
		return nil, nil, err
	}
	fromBase := map[string]string{}
	for old, now := range onMain.moved {
		fromBase[now] = old
	}
	candidates := map[string]string{}
	var names []string
	for _, p := range missing {
		q := p
		if old, ok := fromBase[p]; ok {
			q = old
		}
		r := q
		if now, ok := onLine.moved[q]; ok {
			r = now
		}
		if r != p {
			candidates[p] = r
			names = append(names, r)
		}
	}
	found, err := treeEntries(target, names)
	if err != nil {
		return nil, nil, err
	}
	var unknown []string
	for _, p := range missing {
		if r, ok := candidates[p]; ok {
			if _, ok := found[r]; ok {
				corr[p] = r
				continue
			}
		}
		if !onMain.complete || !onLine.complete {
			unknown = append(unknown, p)
			continue
		}
		corr[p] = p
	}
	return corr, unknown, nil
}

// omittedEffects lists the paths v modifies or deletes that the change from
// before to after leaves without effect, each judged at its corresponding
// path c(p) on before. A modified path is omitted when c(p) exists on neither
// side, applied when the change touches it, and when the change leaves an
// existing file alone, satisfied only if the line already has what the
// original makes of it. A deleted path is omitted when c(p) is absent before:
// a deletion the line already did is no change this unit makes. diag says
// what could not be judged, which is never counted as omitted.
func (b *batch) omittedEffects(v *verdict, before, after string) ([]string, []string, error) {
	if err := b.load(v); err != nil {
		return nil, nil, err
	}
	corr, unknown, err := b.correspondence(v, before)
	if err != nil {
		return nil, nil, err
	}
	var diag []string
	for _, p := range unknown {
		diag = append(diag, "git skipped rename detection, so where "+strconv.Quote(p)+" is on the line is not known")
	}
	added := map[string]bool{}
	for _, c := range v.changes {
		if c.status == 'A' {
			added[c.path] = true
		}
	}
	// A deleted path whose counterpart the original adds itself is a rename
	// the line already made the same way: it is judged as a modified file,
	// against what the original makes of that path.
	var at, own, moved []string
	movedTo := map[string]string{}
	for _, c := range v.changes {
		cp, ok := corr[c.path]
		if !ok {
			continue
		}
		at = append(at, cp)
		switch {
		case c.status == 'M':
			own = append(own, c.path)
		case c.status == 'D' && cp != c.path && added[cp]:
			own, moved = append(own, c.path), append(moved, cp)
			movedTo[c.path] = cp
		}
	}
	bef, err := treeEntries(before, at)
	if err != nil {
		return nil, nil, err
	}
	aft, err := treeEntries(after, at)
	if err != nil {
		return nil, nil, err
	}
	pre, err := treeEntries(v.o.merge+"^1", own)
	if err != nil {
		return nil, nil, err
	}
	post, err := treeEntries(v.o.merge, append(slices.Clone(own), moved...))
	if err != nil {
		return nil, nil, err
	}
	// A change that makes none is judged by its trees, whatever names them:
	// a commit and the tree a net merge wrote can be the same change.
	same := before == after
	if !same {
		bt, err := resolveTree(before)
		if err != nil {
			return nil, nil, err
		}
		at, err := resolveTree(after)
		if err != nil {
			return nil, nil, err
		}
		same = bt == at
	}
	var omitted []string
	// modified judges a path whose counterpart cp should end up as the
	// original's postimage p1 of it.
	modified := func(path, cp string, p0, p1 treeEntry) error {
		be, inBefore := bef[cp]
		ae, inAfter := aft[cp]
		switch {
		case !inBefore && !inAfter:
			omitted = append(omitted, path)
			return nil
		case inBefore != inAfter || be != ae:
			return nil
		case be.oid == p1.oid && (p0.mode == p1.mode || be.mode == p1.mode):
			return nil
		case !be.regular() || !p0.regular() || !p1.regular() || p0.mode != p1.mode:
			diag = append(diag, "the mode or type of "+strconv.Quote(path)+" changes, which only a person can judge")
			return nil
		}
		applied, unsupported, err := b.alreadyApplied(p0.oid, be.oid, p1.oid)
		switch {
		case err != nil:
			return fmt.Errorf("judging %s of #%d: %w", strconv.Quote(path), v.o.number, err)
		case unsupported:
			diag = append(diag, "git cannot merge "+strconv.Quote(path)+" as text, so whether the line has its change is not known")
		case !applied:
			omitted = append(omitted, path)
		}
		return nil
	}
	for _, c := range v.changes {
		cp, ok := corr[c.path]
		if !ok {
			continue
		}
		switch {
		case c.status == 'D' && movedTo[c.path] != "":
			if err := modified(c.path, cp, pre[c.path], post[cp]); err != nil {
				return nil, nil, err
			}
		case c.status == 'D':
			// Absent before: the line already made the deletion, which is
			// no change of a unit that makes others, and is all of one
			// that makes none. Still there after: the deletion did not
			// happen, whatever the unit changed in the file; keeping what
			// the line made of it is a port of its own.
			_, inBefore := bef[cp]
			_, inAfter := aft[cp]
			if !inBefore && !same || inAfter {
				omitted = append(omitted, c.path)
			}
		case c.status == 'M':
			if err := modified(c.path, cp, pre[c.path], post[c.path]); err != nil {
				return nil, nil, err
			}
		}
	}
	for i := range diag {
		diag[i] = oneLineText(diag[i])
	}
	return omitted, diag, nil
}

// alreadyApplied says whether cur, a file the unit leaves alone, already
// holds the original's change from pre to post: it is post itself, or git
// merge-file of the three merges cleanly back into cur. unsupported is
// content merge-file refuses, such as differing binary files, which says
// nothing either way.
func (b *batch) alreadyApplied(pre, cur, post string) (applied, unsupported bool, err error) {
	if cur == post {
		return true, false, nil
	}
	b.mergeFiles++
	blobs, err := readObjects([]string{pre, cur, post}, "blob")
	if err != nil {
		return false, false, err
	}
	dir, err := os.MkdirTemp("", "backport-batch-merge-file-*")
	if err != nil {
		return false, false, err
	}
	defer os.RemoveAll(dir)
	names := map[string]string{}
	for _, f := range []struct{ name, oid string }{{"cur", cur}, {"pre", pre}, {"post", post}} {
		names[f.name] = filepath.Join(dir, f.name)
		if err := os.WriteFile(names[f.name], blobs[f.oid], 0o600); err != nil {
			return false, false, err
		}
	}
	stdout, stderr, code, err := gitStatus("merge-file", "-p", names["cur"], names["pre"], names["post"])
	switch {
	case err != nil:
		return false, false, err
	case code == 0:
		return stdout == string(blobs[cur]), false, nil
	case code > 0 && code < 128:
		// The number of conflicts.
		return false, false, nil
	case strings.Contains(stderr, "Cannot merge binary files"):
		return false, true, nil
	}
	return false, false, fmt.Errorf("git merge-file: exit %d: %s", code, strings.TrimSpace(stderr))
}

// unitsOn maps each original the batch head carries a unit of to that unit,
// by its Backported-from trailer or appended -x line: found needs every
// commit of a merged original, and misses one whose first pick came out
// empty because the line already had that change.
func (b *batch) unitsOn(head string) (map[*verdict]string, error) {
	if m, ok := b.unitCache[head]; ok {
		return m, nil
	}
	units, err := batchUnits(head, b.lineHead)
	if err != nil {
		return nil, err
	}
	m := map[*verdict]string{}
	for _, u := range units {
		if v := b.unitOriginal(u); v != nil && m[v] == "" {
			m[v] = u.commit
		}
	}
	if b.unitCache == nil {
		b.unitCache = map[string]map[*verdict]string{}
	}
	b.unitCache[head] = m
	return m, nil
}

// onBranch says whether the batch head h carries v.
func (b *batch) onBranch(h *history, v *verdict) (bool, error) {
	if found(h, v) {
		return true, nil
	}
	m, err := b.unitsOn(h.head)
	return m[v] != "", err
}

// partialStates finds what applies only in part on the line. Every unit on
// the open batch is checked against its own first parent, whatever the rules
// now say of its original: one that leaves an effect out fails the cut until
// it is taken out. Every original the policy takes and nothing carries is
// checked against the tree its clean net merge onto the line writes, so a
// partial one is found with no record and no adder; a conflict is never
// called partial.
func (b *batch) partialStates() error {
	defer b.timed(time.Now())
	b.partialUnits = map[*verdict][]string{}
	if b.pr != nil {
		var err error
		if b.partialUnits, err = b.unitsPartial(b.prHead); err != nil {
			return err
		}
		on, err := b.unitsOn(b.prHead)
		if err != nil {
			return err
		}
		for v, commit := range on {
			v.batched = commit
			if v.rule == 0 && v.onBatch == "" {
				v.onBatch = commit
			}
		}
	}
	for _, v := range b.qualifying() {
		if v.onBatch != "" {
			continue
		}
		if err := b.partialAt(v, b.lineHead); err != nil {
			return err
		}
	}
	return nil
}

// unitsPartial maps each original whose unit between the line's head and
// head leaves part of it without effect to what it leaves.
func (b *batch) unitsPartial(head string) (map[*verdict][]string, error) {
	units, err := batchUnits(head, b.lineHead)
	if err != nil {
		return nil, err
	}
	out := map[*verdict][]string{}
	for _, u := range units {
		v := b.unitOriginal(u)
		if v == nil || len(u.parents) == 0 {
			continue
		}
		omitted, diag, err := b.omittedEffects(v, u.parents[0], u.commit)
		if err != nil {
			return nil, err
		}
		v.addDiag(diag...)
		if len(omitted) > 0 {
			out[v] = append(out[v], omitted...)
		}
	}
	return out, nil
}

// markPartial puts every original on the branch at head whose unit there
// does only part of it among the partial units, and so among the stale
// ones: still on the batch, and to be taken out before the cut.
func (b *batch) markPartial(r *report, head string) error {
	units := b.partialUnits
	if b.pr == nil || head != b.prHead {
		var err error
		if units, err = b.unitsPartial(head); err != nil {
			return err
		}
	}
	r.partialFiles = map[*verdict][]string{}
	for _, v := range b.verdicts {
		files := units[v]
		if len(files) == 0 {
			continue
		}
		r.partial = append(r.partial, v)
		r.partialFiles[v] = files
		if !slices.Contains(r.stale, v) {
			r.stale = append(r.stale, v)
		}
	}
	return nil
}

// keepRecordedPartials judges again, at head, what the batch carries now,
// each original the open record parks as partial that the line's head
// judges whole: the run's picker found it partial after the picks before
// it, which check and an operation that picks nothing do not replay. It
// stays parked while its net merge onto head still leaves something
// without effect, and is the line's again once that went, as when the pick
// that made it partial was taken off.
func (b *batch) keepRecordedPartials(head string) error {
	if !b.src.open || b.src.rec == nil {
		return nil
	}
	for _, p := range b.src.rec.Parked {
		v := b.find(p.Original)
		if p.Kind != parkPartial || v == nil || v.rule != 0 || v.onBatch != "" || v.park != nil || v.o.merge != p.Merge {
			continue
		}
		if err := b.partialAt(v, head); err != nil {
			return err
		}
	}
	return nil
}

// rejudged reads again what partialStates and analyze read for v once its
// rules changed, as a withdrawn drop changes them: it is back where the
// line puts it.
func (b *batch) rejudged(v *verdict) error {
	if b.pr != nil {
		on, err := b.unitsOn(b.prHead)
		if err != nil {
			return err
		}
		v.batched = on[v]
		if v.rule == 0 && v.onBatch == "" {
			v.onBatch = on[v]
		}
	}
	if v.rule != 0 || v.onBatch != "" {
		return nil
	}
	if err := b.partialAt(v, b.lineHead); err != nil {
		return err
	}
	deps, diag, err := b.dependencies(v, b.lineHead)
	if err != nil {
		return err
	}
	v.deps = deps
	v.addDiag(diag...)
	return nil
}

// partialAt judges v's clean net merge onto head, and parks it, or clears
// the park, by what that merge leaves without effect.
func (b *batch) partialAt(v *verdict, head string) error {
	tree, err := b.netTree(head, v.o)
	if err != nil {
		return err
	}
	v.partial = nil
	if v.park != nil && v.park.Kind == parkPartial {
		v.park = nil
	}
	if tree == "" {
		return nil
	}
	omitted, diag, err := b.omittedEffects(v, head, tree)
	if err != nil {
		return err
	}
	v.addDiag(diag...)
	if len(omitted) > 0 {
		v.partial = omitted
		v.park = &parkedEntry{Original: v.o.number, Merge: v.o.merge, Kind: parkPartial, Files: sortedClean(omitted)}
	}
	return nil
}

// netTree is the tree a clean net merge of o onto head writes, "" for a
// conflict; the line's head reuses what judging the originals merged.
func (b *batch) netTree(head string, o *original) (string, error) {
	if head == b.lineHead && b.lineHist != nil {
		return b.lineHist.netTree(o)
	}
	tree, _, err := noopPick(head, o)
	return tree, err
}

// addDiag keeps what could not be judged of v, each line once.
func (v *verdict) addDiag(lines ...string) {
	for _, ln := range lines {
		if ln != "" && !slices.Contains(v.diag, ln) {
			v.diag = append(v.diag, ln)
		}
	}
}

func sortedClean(files []string) []string {
	out := cleanFiles(files)
	slices.Sort(out)
	return slices.Compact(out)
}

// leftOut is the refusal for a unit that leaves paths of v without effect.
func leftOut(v *verdict, l line, omitted []string) string {
	quoted := make([]string, len(omitted))
	for i, p := range omitted {
		quoted[i] = strconv.Quote(p)
	}
	return fmt.Sprintf("#%d leaves %s without effect on %s; port only what the line has as a pull request of its own", v.o.number, strings.Join(quoted, ", "), l.branch())
}
