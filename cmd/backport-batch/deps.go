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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What an original builds on that its line lacks is read from git alone, in
// two ways: a file it modifies or deletes that the line does not have, named
// by the first-parent commit on main that added it (V1), and a line it
// removes or changes that git blame gives to a commit the line lacks, kept
// only while that line's text is absent from the line's file (V2c). Either
// names the first-parent commit that brought the evidence to main: the merge
// of another original, or a commit that merged no pull request.

// Bounds per original, past which the evidence is incomplete rather than
// read at any cost.
const (
	maxDepFiles    = 256
	maxBlamedLines = 20000
	maxDepBlob     = 8 << 20
	gitDeadline    = 2 * time.Minute
)

// dependency is a first-parent commit on main an original builds on, with
// the original's paths the evidence is in.
type dependency struct {
	merge string
	// v is the original merge merged, nil for a commit that merged none.
	v     *verdict
	paths []string
	// added says some of the evidence is a file the line lacks (V1).
	added bool
}

// errDeadline is a git call that ran out of time: incomplete evidence, not
// a failure.
var errDeadline = errors.New("git took longer than " + gitDeadline.String())

// gitTimed runs git under the deadline the detector gives each call.
func gitTimed(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(cmd.Environ(), commandEnv...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return "", errDeadline
	case err != nil:
		return "", fmt.Errorf("git %s: %w: %s", quotedArgs(args), err, terminalSafe(errOut.String()))
	}
	return out.String(), nil
}

// quotedArgs names a git command's arguments for an error, each quoted: a
// file name can hold a newline, and the error reaches a terminal and a CI
// log, which would take the rest for a line of their own.
func quotedArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strconv.Quote(a)
	}
	return strings.Join(out, " ")
}

// gitStatusTimed is gitStatus under the detector's deadline: a non-zero
// exit is an answer, and running out of time is errDeadline.
func gitStatusTimed(args ...string) (stdout, stderr string, code int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(cmd.Environ(), commandEnv...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return "", "", -1, errDeadline
	case errors.As(err, &ee):
		return out.String(), errOut.String(), ee.ExitCode(), nil
	case err != nil:
		return "", "", -1, fmt.Errorf("git %s: %w", quotedArgs(args), err)
	}
	return out.String(), errOut.String(), 0, nil
}

// broughtBy maps every commit in base..mainHead to the first-parent commit
// that brought it to main: each first-parent commit claims itself, then
// every commit of the range reachable from its other parents that no
// earlier one claimed. A commit outside the map is an ancestor of base.
func broughtBy(base, mainHead string) (map[string]string, error) {
	first, err := revList("--first-parent", "--reverse", "--end-of-options", base+".."+mainHead)
	if err != nil {
		return nil, err
	}
	out, err := git("rev-list", "--parents", "--topo-order", "--end-of-options", base+".."+mainHead)
	if err != nil {
		return nil, err
	}
	parents := map[string][]string{}
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		if len(f) == 0 {
			continue
		}
		for _, sha := range f {
			if checkSHA(sha) != nil {
				return nil, fmt.Errorf("rev-list: %q is not a commit", sha)
			}
		}
		parents[f[0]] = f[1:]
	}
	by := map[string]string{}
	for _, fp := range first {
		if _, ok := parents[fp]; !ok {
			return nil, fmt.Errorf("rev-list: %s is on main's first-parent line but not in its range", fp)
		}
		by[fp] = fp
		stack := slices.Clone(parents[fp][min(1, len(parents[fp])):])
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, inRange := parents[c]; !inRange || by[c] != "" {
				continue
			}
			by[c] = fp
			stack = append(stack, parents[c]...)
		}
	}
	return by, nil
}

// hunk is an old range of a diff, a,+b in git blame's terms.
type hunk struct{ start, count int }

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+\d+(?:,\d+)? @@`)

// oldRanges reads the removed or changed line ranges of a -U0 diff of one
// file, and whether git called it binary. A header it cannot read is an
// error, never evidence of absence.
func oldRanges(diff string) ([]hunk, bool, error) {
	var out []hunk
	for ln := range strings.Lines(diff) {
		ln = strings.TrimSuffix(ln, "\n")
		switch {
		case strings.HasPrefix(ln, "Binary files ") && strings.HasSuffix(ln, " differ"):
			return nil, true, nil
		case strings.HasPrefix(ln, "@@"):
			m := hunkRE.FindStringSubmatch(ln)
			if m == nil {
				return nil, false, fmt.Errorf("unreadable hunk header %q", terminalSafe(ln))
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count > 0 {
				out = append(out, hunk{start, count})
			}
		}
	}
	return out, false, nil
}

// blameArgs blames ranges of file at rev, a full commit, which git takes for
// a revision whatever follows: blame refuses --end-of-options.
func blameArgs(rev, file string, ranges []hunk) ([]string, error) {
	if err := checkSHA(rev); err != nil {
		return nil, err
	}
	args := []string{"blame", "--porcelain", "-w"}
	for _, r := range ranges {
		args = append(args, "-L", fmt.Sprintf("%d,+%d", r.start, r.count))
	}
	return append(args, rev, "--", file), nil
}

// blamedLine is one line git blame gives to a commit.
type blamedLine struct{ commit, text string }

// parseBlame reads git blame --porcelain: a header per line, the commit's
// fields the first time it appears, and the line itself after a tab.
// Anything else is an error, never evidence of absence.
func parseBlame(out string) ([]blamedLine, error) {
	var lines []blamedLine
	commit := ""
	for ln := range strings.Lines(out) {
		ln = strings.TrimSuffix(ln, "\n")
		switch {
		case strings.HasPrefix(ln, "\t"):
			if commit == "" {
				return nil, errors.New("git blame: a line with no header")
			}
			lines = append(lines, blamedLine{commit: commit, text: ln[1:]})
			commit = ""
		case commit == "":
			f := strings.Fields(ln)
			if len(f) < 3 || len(f) > 4 || checkSHA(f[0]) != nil {
				return nil, fmt.Errorf("git blame: unreadable header %q", terminalSafe(ln))
			}
			for _, n := range f[1:] {
				if v, err := strconv.Atoi(n); err != nil || v <= 0 {
					return nil, fmt.Errorf("git blame: unreadable header %q", terminalSafe(ln))
				}
			}
			commit = f[0]
		}
	}
	if commit != "" {
		return nil, errors.New("git blame: a header with no line")
	}
	return lines, nil
}

var generatedMarkerRE = regexp.MustCompile(`Code generated .* DO NOT EDIT`)

// generatedFiles are written whole by a tool, whatever they hold.
var generatedFiles = []string{"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"}

// cozyRDGenerated are the keys hack/update-crd.sh writes into an
// ApplicationDefinition, the rest of which people write.
var cozyRDGenerated = []string{
	"apiVersion", "kind", "metadata.name", "spec.application.openAPISchema", "spec.release.prefix",
	"spec.release.chartRef.kind", "spec.release.chartRef.name", "spec.release.chartRef.namespace",
	"spec.dashboard.description", "spec.dashboard.icon", "spec.dashboard.keysOrder",
}

var yamlKeyRE = regexp.MustCompile(`^( *)([A-Za-z0-9_.-]+):(?:\s|$)`)

// generatedLines says which lines of a file at a commit a tool wrote: all of
// them for a generated file, the keys hack/update-crd.sh sets for an
// ApplicationDefinition, found by indentation, and none otherwise. ok is
// false for an ApplicationDefinition the scan cannot read, whose lines are
// then not blamed at all.
func generatedLines(file string, blob []byte) (skip map[int]bool, whole, ok bool) {
	base := path.Base(file)
	if strings.HasPrefix(base, "zz_generated") || slices.Contains(generatedFiles, base) {
		return nil, true, true
	}
	lines := strings.Split(string(blob), "\n")
	for _, ln := range lines[:min(len(lines), 100)] {
		if generatedMarkerRE.MatchString(ln) {
			return nil, true, true
		}
	}
	if rd, _ := path.Match(cozyRDGlob, file); !rd {
		return nil, false, true
	}
	skip = map[int]bool{}
	type key struct {
		indent int
		name   string
	}
	var stack []key
	genIndent := -1
	for i, ln := range lines {
		n := i + 1
		trimmed := strings.TrimLeft(ln, " ")
		indent := len(ln) - len(trimmed)
		switch {
		case strings.HasPrefix(trimmed, "\t") || strings.HasPrefix(ln, "---") && i > 0:
			return nil, false, false
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			if genIndent >= 0 {
				skip[n] = true
			}
			continue
		case genIndent >= 0 && indent > genIndent:
			skip[n] = true
			continue
		}
		genIndent = -1
		m := yamlKeyRE.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, key{indent, m[2]})
		var names []string
		for _, k := range stack {
			names = append(names, k.name)
		}
		if slices.Contains(cozyRDGenerated, strings.Join(names, ".")) {
			skip[n] = true
			genIndent = indent
		}
	}
	return skip, false, true
}

// trimmedLines is the set of a blob's lines with their edges trimmed, which
// is all a reformat of indentation or trailing space changes.
func trimmedLines(blob []byte) map[string]bool {
	set := map[string]bool{}
	for ln := range strings.Lines(string(blob)) {
		set[strings.TrimSpace(ln)] = true
	}
	return set
}

// shown is a path as a diagnostic names it.
func shown(p string) string { return strconv.Quote(p) }

// incomplete is a diagnostic about evidence the detector could not read.
func incomplete(format string, args ...any) string {
	return oneLineText("incomplete evidence: " + fmt.Sprintf(format, args...))
}

// dependencies reads what v builds on that target lacks, with what it could
// not read. V1 looks at the paths whose counterpart target lacks, V2c at
// the ones it has.
func (b *batch) dependencies(v *verdict, target string) ([]dependency, []string, error) {
	if err := b.load(v); err != nil {
		return nil, nil, err
	}
	if b.brought == nil {
		var err error
		if b.brought, err = broughtBy(b.base, b.mainHead); err != nil {
			return nil, nil, err
		}
	}
	corr, unknown, err := b.correspondence(v, target)
	if err != nil {
		return nil, nil, err
	}
	var diag []string
	for _, p := range unknown {
		diag = append(diag, incomplete("git skipped rename detection for %s", shown(p)))
	}
	var at []string
	for _, c := range corr {
		at = append(at, c)
	}
	have, err := treeEntries(target, at)
	if err != nil {
		return nil, nil, err
	}
	parent, err := resolve(v.o.merge + "^1")
	if err != nil {
		return nil, nil, err
	}
	byMerge := map[string]*dependency{}
	add := func(merge, p string, added bool) {
		d := byMerge[merge]
		if d == nil {
			d = &dependency{merge: merge, v: b.findMerge(merge)}
			byMerge[merge] = d
		}
		if !slices.Contains(d.paths, p) {
			d.paths = append(d.paths, p)
		}
		d.added = d.added || added
	}
	files, blamed := 0, 0
	for _, c := range v.changes {
		cp, ok := corr[c.path]
		if !ok || c.status != 'M' && c.status != 'D' {
			continue
		}
		if !utf8.ValidString(c.path) {
			diag = append(diag, incomplete("a path that is not UTF-8, %s", shown(c.path)))
			continue
		}
		if files++; files > maxDepFiles {
			diag = append(diag, incomplete("more than %d files to read", maxDepFiles))
			break
		}
		cur, present := have[cp]
		if !present {
			out, err := gitTimed("--literal-pathspecs", "log", "--first-parent", "--diff-merges=first-parent", "--no-renames", "--diff-filter=A", "--format=%H", "--no-patch", "-1", "--end-of-options", b.base+".."+parent, "--", c.path)
			switch {
			case errors.Is(err, errDeadline):
				diag = append(diag, incomplete("%s, finding what added %s", err, shown(c.path)))
				continue
			case err != nil:
				return nil, nil, err
			}
			adder := strings.TrimSpace(out)
			if adder == "" {
				diag = append(diag, oneLineText(fmt.Sprintf("%s is not on the line, and nothing merged into main since it branched added it", shown(c.path))))
				continue
			}
			if checkSHA(adder) != nil {
				return nil, nil, fmt.Errorf("git log for %s: %q is not a commit", shown(c.path), terminalSafe(adder))
			}
			add(adder, c.path, true)
			continue
		}
		found, more, err := b.blamed(v, parent, c.path, cur, target, maxBlamedLines-blamed)
		if err != nil {
			return nil, nil, err
		}
		blamed += more.lines
		diag = append(diag, more.diag...)
		for merge := range found {
			add(merge, c.path, false)
		}
		if more.capped {
			break
		}
	}
	var deps []dependency
	for _, d := range byMerge {
		slices.Sort(d.paths)
		deps = append(deps, *d)
		// The rules decide before the line is looked at, so whether the
		// line carries a prerequisite they keep out is asked here.
		if f := d.v; f != nil && f.rule != 0 && f.rule != ruleOnLine && f.rule != ruleNewer && b.lineHist != nil {
			if _, asked := b.lineCarries[f]; !asked {
				if err := b.load(f); err != nil {
					return nil, nil, err
				}
				how, err := b.lineHist.carries(f.o, f.commits)
				if err != nil {
					return nil, nil, err
				}
				if b.lineCarries == nil {
					b.lineCarries = map[*verdict]bool{}
				}
				b.lineCarries[f] = how != ""
			}
		}
	}
	slices.SortFunc(deps, func(x, y dependency) int { return strings.Compare(x.merge, y.merge) })
	return deps, diag, nil
}

// blameResult is what blaming one file read: how many lines, what it could
// not read, and whether the original's line budget ran out.
type blameResult struct {
	lines  int
	diag   []string
	capped bool
}

// blamed gives each line of file that v removes or changes, at parent, to
// the first-parent commit that brought it, and keeps the commits one of
// whose lines target's counterpart of the file, cur, lacks.
func (b *batch) blamed(v *verdict, parent, file string, cur treeEntry, target string, budget int) (map[string]bool, blameResult, error) {
	var res blameResult
	entries, err := treeEntries(parent, []string{file})
	if err != nil {
		return nil, res, err
	}
	pre := entries[file]
	switch {
	case pre.mode == "120000" || cur.mode == "120000":
		res.diag = append(res.diag, incomplete("%s is a symlink", shown(file)))
		return nil, res, nil
	case pre.kind == "commit" || cur.kind == "commit":
		res.diag = append(res.diag, incomplete("%s is a submodule", shown(file)))
		return nil, res, nil
	case !pre.regular() || !cur.regular():
		res.diag = append(res.diag, incomplete("%s is no file git can blame", shown(file)))
		return nil, res, nil
	}
	// A file the line already has as the original makes it builds on
	// nothing the line lacks, whoever wrote the lines it replaced.
	if v.o.merge != "" && cur.oid != pre.oid {
		post, err := treeEntries(v.o.merge, []string{file})
		if err != nil {
			return nil, res, err
		}
		if p1, ok := post[file]; ok && p1.regular() {
			if cur.oid == p1.oid {
				return nil, res, nil
			}
			applied, _, err := b.alreadyApplied(pre.oid, cur.oid, p1.oid)
			if err != nil {
				return nil, res, err
			}
			if applied {
				return nil, res, nil
			}
		}
	}
	for _, oid := range []string{pre.oid, cur.oid} {
		out, err := git("cat-file", "-s", oid)
		if err != nil {
			return nil, res, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return nil, res, fmt.Errorf("git cat-file -s %s: %q is no size", oid, terminalSafe(out))
		}
		if size > maxDepBlob {
			res.diag = append(res.diag, incomplete("%s is larger than %d bytes", shown(file), maxDepBlob))
			return nil, res, nil
		}
	}
	diff, err := gitTimed("--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-U0", "--end-of-options", parent, v.o.merge, "--", file)
	if errors.Is(err, errDeadline) {
		res.diag = append(res.diag, incomplete("%s, diffing %s", err, shown(file)))
		return nil, res, nil
	} else if err != nil {
		return nil, res, err
	}
	ranges, binary, err := oldRanges(diff)
	switch {
	case err != nil:
		return nil, res, fmt.Errorf("the diff of %s in #%d: %w", shown(file), v.o.number, err)
	case binary:
		res.diag = append(res.diag, incomplete("%s is binary", shown(file)))
		return nil, res, nil
	case len(ranges) == 0:
		return nil, res, nil
	}
	blobs, err := readObjects([]string{pre.oid, cur.oid}, "blob")
	if err != nil {
		return nil, res, err
	}
	skip, whole, ok := generatedLines(file, blobs[pre.oid])
	switch {
	case whole:
		return nil, res, nil
	case !ok:
		res.diag = append(res.diag, incomplete("%s, whose generated keys could not be told apart", shown(file)))
		return nil, res, nil
	}
	var kept []hunk
	for _, r := range ranges {
		for n := r.start; n < r.start+r.count; n++ {
			if skip[n] {
				continue
			}
			if l := len(kept) - 1; l >= 0 && kept[l].start+kept[l].count == n {
				kept[l].count++
			} else {
				kept = append(kept, hunk{n, 1})
			}
			res.lines++
		}
	}
	if res.lines > budget {
		res.diag = append(res.diag, incomplete("more than %d lines to blame", maxBlamedLines))
		res.capped = true
		return nil, res, nil
	}
	if len(kept) == 0 {
		return nil, res, nil
	}
	args, err := blameArgs(parent, file, kept)
	if err != nil {
		return nil, res, err
	}
	out, err := gitTimed(args...)
	if errors.Is(err, errDeadline) {
		res.diag = append(res.diag, incomplete("%s, blaming %s", err, shown(file)))
		return nil, res, nil
	} else if err != nil {
		return nil, res, err
	}
	lines, err := parseBlame(out)
	if err != nil {
		return nil, res, fmt.Errorf("blaming %s for #%d: %w", shown(file), v.o.number, err)
	}
	onTarget := trimmedLines(blobs[cur.oid])
	found := map[string]bool{}
	unmapped := false
	for _, ln := range lines {
		merge, inRange := b.brought[ln.commit]
		switch {
		case !inRange:
			// An ancestor of the line's fork point, which the line has, or
			// a commit main does not have at all.
			before, err := b.beforeBase(ln.commit)
			if err != nil {
				return nil, res, err
			}
			unmapped = unmapped || !before
		case merge == v.o.merge:
		case !onTarget[strings.TrimSpace(ln.text)]:
			found[merge] = true
		}
	}
	if unmapped {
		res.diag = append(res.diag, incomplete("git blame gives lines of %s to a commit that did not come through main", shown(file)))
	}
	return found, res, nil
}

// timed adds what has passed since start to the time the detector took.
func (b *batch) timed(start time.Time) { b.detecting += time.Since(start) }

// beforeBase says whether commit is an ancestor of the line's fork point,
// once per commit.
func (b *batch) beforeBase(commit string) (bool, error) {
	if ok, seen := b.ancestors[commit]; seen {
		return ok, nil
	}
	ok, err := isAncestor(commit, b.base)
	if err == nil {
		if b.ancestors == nil {
			b.ancestors = map[string]bool{}
		}
		b.ancestors[commit] = ok
	}
	return ok, err
}

// analyze reads what every original the policy takes and nothing carries
// builds on that the line lacks, and keeps it with the original, for the
// parks and waits that follow from it.
func (b *batch) analyze() error {
	defer b.timed(time.Now())
	for _, v := range b.qualifying() {
		if v.onBatch != "" {
			continue
		}
		deps, diag, err := b.dependencies(v, b.lineHead)
		if err != nil {
			return err
		}
		v.deps = deps
		v.addDiag(diag...)
	}
	return nil
}

// analysisMode is how a classification treats an original that changes a
// workflow: as the scheduled run does, whose token cannot push one, or as a
// maintainer's run.
type analysisMode struct{ noWorkflowFiles bool }

// edgeClass is what a prerequisite is to an original that builds on it,
// from what never matters to what decides first.
type edgeClass int

const (
	edgeSatisfied edgeClass = iota
	// edgeNewer is held by rule 6 on this line.
	edgeNewer
	// edgeDeferred is a deferred conflict, or deferred for a reason.
	edgeDeferred
	// edgeWorkflow waits on the open batch for a maintainer.
	edgeWorkflow
	// edgeMissing is on neither the line, the batch nor the record.
	edgeMissing
	// edgeNever never reaches the line.
	edgeNever
)

// edge is a dependency whose prerequisite has not reached the line, with
// what that prerequisite is.
type edge struct {
	dep   dependency
	class edgeClass
}

// ruleClass is what the rules alone make of a prerequisite, or ok false when
// it depends on what this classification makes of it: one the line or the
// open batch carries is there, whatever its label, drop or rule says, and a
// direct push and an original the rules keep out never come.
func (b *batch) ruleClass(f *verdict) (edgeClass, bool) {
	switch {
	case f == nil:
		return edgeNever, true
	case f.rule == ruleOnLine || f.onBatch != "" || f.batched != "" || b.lineCarries[f]:
		return edgeSatisfied, true
	case f.rule == ruleNewer:
		return edgeNewer, true
	case f.rule != 0:
		return edgeNever, true
	}
	return 0, false
}

// edges sorts dependencies by what classOf says of each prerequisite into
// those that never come and those still to come, dropping the ones there.
func edges(deps []dependency, classOf func(*verdict) edgeClass) (never, pending []edge) {
	for _, d := range deps {
		switch c := classOf(d.v); c {
		case edgeSatisfied:
		case edgeNever:
			never = append(never, edge{d, c})
		default:
			pending = append(pending, edge{d, c})
		}
	}
	return never, pending
}

// strongest is the class of prerequisite that decides a wait, and the
// prerequisites of that class, in merge order.
func strongest(pending []edge) (edgeClass, []*verdict) {
	top := edgeSatisfied
	for _, e := range pending {
		top = max(top, e.class)
	}
	var out []*verdict
	for _, e := range pending {
		if e.class == top && !slices.Contains(out, e.dep.v) {
			out = append(out, e.dep.v)
		}
	}
	return top, out
}

// auxiliary says whether a path holds tests or documentation, which an
// original can change without building on what it needs to work.
func auxiliary(p string) bool {
	parts := strings.Split(p, "/")
	base := parts[len(parts)-1]
	for _, d := range parts[:len(parts)-1] {
		if d == "test" || d == "tests" || d == "testdata" {
			return true
		}
	}
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.Contains(base, ".test."), strings.Contains(base, ".spec."), strings.HasSuffix(base, ".bats"):
		return true
	case len(parts) > 1 && parts[0] == "docs":
		return true
	}
	return len(parts) == 1 && strings.HasSuffix(base, ".md")
}

// workflowPrimary says whether v waits for a maintainer rather than parks:
// in the scheduled mode, an original that changes a workflow, all of whose
// missing dependencies that never come are tests or docs, and whose
// workflow lines build on nothing still missing. The workflow is what it is
// for, and a maintainer brings over what applies. Only what is still
// missing counts: never and pending are what is left once what is there was
// taken out.
func workflowPrimary(mode analysisMode, v *verdict, never, pending []edge) (bool, error) {
	if !mode.noWorkflowFiles || len(never) == 0 {
		return false, nil
	}
	for _, e := range never {
		if slices.ContainsFunc(e.dep.paths, func(p string) bool { return !auxiliary(p) }) {
			return false, nil
		}
	}
	for _, e := range append(slices.Clone(never), pending...) {
		if slices.ContainsFunc(e.dep.paths, func(p string) bool { return strings.HasPrefix(p, workflowsDir) }) {
			return false, nil
		}
	}
	files, err := workflowFiles(v.commits)
	return len(files) > 0, err
}

// testsOnly is the note a workflow original gets that waits for a
// maintainer although its tests build on what never comes.
func testsOnly(v *verdict, never []edge, l line) string {
	var who []string
	for _, e := range never {
		if e.dep.v != nil {
			who = append(who, fmt.Sprintf("#%d", e.dep.v.o.number))
		} else {
			who = append(who, e.dep.merge[:12])
		}
	}
	slices.Sort(who)
	return oneLineText(fmt.Sprintf("only its tests or docs build on %s, which never reaches %s", strings.Join(slices.Compact(who), ", "), l.branch()))
}

// dependencyPark is the park of v for prerequisites that never come.
func dependencyPark(v *verdict, never []edge) *parkedEntry {
	p := &parkedEntry{Original: v.o.number, Merge: v.o.merge, Kind: parkDependency}
	for _, e := range never {
		if e.dep.v != nil {
			p.Needs = append(p.Needs, e.dep.v.o.number)
		} else {
			p.Commits = append(p.Commits, e.dep.merge)
		}
	}
	slices.Sort(p.Needs)
	p.Needs = slices.Compact(p.Needs)
	slices.Sort(p.Commits)
	p.Commits = slices.Compact(p.Commits)
	return p
}

// depsAt is what v builds on that target lacks, read once per target: the
// run asks again at its head once earlier picks moved the branch.
func (b *batch) depsAt(v *verdict, target string) ([]dependency, error) {
	key := target + " " + v.o.merge
	if d, ok := b.depCache[key]; ok {
		return d, nil
	}
	deps, diag, err := b.dependencies(v, target)
	if err != nil {
		return nil, err
	}
	v.addDiag(diag...)
	if b.depCache == nil {
		b.depCache = map[string][]dependency{}
	}
	b.depCache[key] = deps
	return deps, nil
}

// stateKind is what a classification makes of an original the policy takes
// that nothing carries, in the order that decides it.
type stateKind int

const (
	// stHeld is deferred for a reason a person gave.
	stHeld stateKind = iota + 1
	// stPartial would leave part of it without effect on the line.
	stPartial
	// stWorkflowPrimary waits for a maintainer, though its tests build on
	// what never comes.
	stWorkflowPrimary
	// stParked builds on what never comes.
	stParked
	// stNeeds waits on prerequisites still to come.
	stNeeds
	// stOwn is what the record says of it, or missing.
	stOwn
)

// state is a classification of one original: what it is, what it is to an
// original that builds on it, and for a wait the class that decides it and
// the prerequisites of that class.
type state struct {
	kind  stateKind
	class edgeClass
	wait  edgeClass
	needs []*verdict
}

// classify decides, in merge order, what becomes of every original the
// policy takes that neither the line nor the open batch carries, as an
// operation that picks nothing sees it: check, and the ones that only
// change the record. held are the reasons people deferred originals for,
// recorded what the record defers, and listed says the record's workflow
// waits still wait on an open batch. Parks it finds go into each verdict.
func (b *batch) classify(mode analysisMode, held map[int]string, recorded map[int]deferredEntry, listed bool) (map[*verdict]*state, error) {
	states := map[*verdict]*state{}
	classOf := func(f *verdict) edgeClass {
		if c, ok := b.ruleClass(f); ok {
			return c
		}
		if s := states[f]; s != nil {
			return s.class
		}
		return edgeMissing
	}
	for _, v := range b.qualifying() {
		if v.onBatch != "" {
			continue
		}
		s := &state{}
		states[v] = s
		e := recorded[v.o.number]
		never, pending := edges(v.deps, classOf)
		primary, err := workflowPrimary(mode, v, never, pending)
		if err != nil {
			return nil, err
		}
		switch {
		case held[v.o.number] != "":
			s.kind, s.class = stHeld, edgeDeferred
		case v.park != nil && v.park.Kind == parkPartial:
			s.kind, s.class = stPartial, edgeNever
		case primary:
			s.kind, s.class = stWorkflowPrimary, edgeMissing
			if listed && e.Workflows {
				s.class = edgeWorkflow
			}
		case len(never) > 0:
			v.park = dependencyPark(v, never)
			s.kind, s.class = stParked, edgeNever
		case len(pending) > 0:
			s.kind = stNeeds
			s.wait, s.needs = strongest(pending)
			s.class = s.wait
		case listed && e.Workflows:
			s.kind, s.class = stOwn, edgeWorkflow
		case !e.Workflows && len(e.Needs) == 0 && (e.Commit != "" || len(e.Files) > 0):
			s.kind, s.class = stOwn, edgeDeferred
		default:
			s.kind, s.class = stOwn, edgeMissing
		}
	}
	return states, nil
}
