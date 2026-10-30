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
	"os"
	"slices"
	"strconv"
	"strings"
)

// A batch carries each original as one unit on its first-parent line. A
// merged pull request is a merge commit whose first parent is the batch before
// it and whose second parent ends a straight line of picks of the pull
// request's own commits, oldest first, each keeping its author and sign-offs;
// the merge says which merge commit on main it carries in a Backported-from
// trailer. A squash-merged original is one pick of its commit on main.

// pickOriginal picks one original onto HEAD in that shape. A conflict on any
// of its commits takes the branch back to where it was before the original,
// so nothing of it is left half applied. Hooks are off so that each commit
// carries its message and -x line as git writes them; signing is left to the
// user's configuration.
func pickOriginal(v *verdict) (pick, error) {
	p := pick{v: v}
	base, err := resolve("HEAD")
	if err != nil {
		return p, err
	}
	for _, c := range v.commits {
		status, files, err := applyPick("-c", "core.hooksPath=/dev/null", "cherry-pick", "-x", "--signoff", "--end-of-options", c)
		if err != nil {
			if rerr := resetTo(base); rerr != nil {
				return p, rerr
			}
			return p, fmt.Errorf("cherry-pick of %.12s of #%d: %w", c, v.o.number, err)
		}
		if status == "conflict" {
			p.status, p.files, p.failed = "conflict", files, c
			return p, resetTo(base)
		}
	}
	head, err := resolve("HEAD")
	switch {
	case err != nil:
		return p, err
	case head == base:
		p.status = "empty"
		return p, nil
	case v.o.parents < 2:
		p.status, p.commit = "picked", head
		return p, nil
	}
	msgs, err := commitMessages([]string{v.o.merge})
	if err != nil {
		return p, err
	}
	if p.commit, err = commitMerge(base, head, mergeMessage(msgs[v.o.merge].message, v.o.merge)); err != nil {
		if rerr := resetTo(base); rerr != nil {
			return p, rerr
		}
		return p, err
	}
	p.status = "picked"
	return p, nil
}

// mergeMessage is the message of a batch merge: the one of the merge commit
// on main, which says which pull request it was, and a paragraph of its own
// naming that merge commit, so nothing the original's text ends with can pass
// for the trailer.
func mergeMessage(message, merge string) string {
	return strings.TrimRight(message, " \t\r\n") + "\n\n" + batchTrailer + ": " + merge + "\n"
}

// commitMerge makes the merge of tip onto base, whose tree is tip's since tip
// descends from base, and moves the current branch from tip to it.
func commitMerge(base, tip, message string) (string, error) {
	f, err := os.CreateTemp("", "backport-batch-merge-*.txt")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(message); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	out, err := git("commit-tree", "-p", base, "-p", tip, "-F", f.Name(), tip+"^{tree}")
	if err != nil {
		return "", err
	}
	merge := strings.TrimSpace(out)
	if err := checkSHA(merge); err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	if _, err := git("update-ref", "-m", "backport-batch: merge", "HEAD", merge, tip); err != nil {
		return "", err
	}
	return merge, nil
}

// resetTo takes the current branch, its index and its tree back to commit,
// which run owns: it starts only in a clean tree.
func resetTo(commit string) error {
	_, err := git("reset", "--quiet", "--hard", commit)
	return err
}

// unit is one original as a batch carries it: a commit on the batch's
// first-parent line, and for a merge the picks its second parent brings.
type unit struct {
	commit  string
	parents []string
	message string
	// picks are the commits reachable from the second parent and not from the
	// first, oldest first.
	picks []string
}

func (u unit) merge() bool { return len(u.parents) == 2 }

// readUnit reads the unit whose first-parent commit is commit.
func readUnit(commit string) (unit, error) {
	msgs, err := commitMessages([]string{commit})
	if err != nil {
		return unit{}, err
	}
	u := unit{commit: commit, parents: msgs[commit].parents, message: msgs[commit].message}
	if u.merge() {
		if u.picks, err = revList("--reverse", "--topo-order", "--end-of-options", u.parents[1], "^"+u.parents[0]); err != nil {
			return unit{}, err
		}
	}
	return u, nil
}

// batchUnits lists the units of the batch from base to head, oldest first.
func batchUnits(head, base string) ([]unit, error) {
	oids, err := revList("--reverse", "--first-parent", "--end-of-options", head, "^"+base)
	if err != nil {
		return nil, err
	}
	var units []unit
	for _, oid := range oids {
		u, err := readUnit(oid)
		if err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, nil
}

// unitOriginal is the original a unit says it carries, or nil: the one its
// Backported-from trailer names, or the one its appended -x line picks.
func (b *batch) unitOriginal(u unit) *verdict {
	if u.merge() {
		if merges := batchedFrom(u.message); len(merges) == 1 {
			return b.findMerge(merges[0])
		}
		return nil
	}
	source := appendedSource(u.message)
	if v := b.findMerge(source); v != nil {
		return v
	}
	return b.pickedBy([]string{source})
}

// ownerOf is the original one of whose own commits source is, or nil. It reads
// every original's commits, so it is for explaining a refusal only.
func (b *batch) ownerOf(source string) (*verdict, error) {
	for _, v := range b.verdicts {
		if v.commits == nil {
			commits, err := prCommits(v.o)
			if err != nil {
				return nil, err
			}
			v.commits = commits
		}
		if v.o.parents > 1 && slices.Contains(v.commits, source) {
			return v, nil
		}
	}
	return nil, nil
}

// checkUnit says why u is not one original carried in the batch's shape, or
// "" when it is, and which original it carries: a merge whose second parent
// is a straight line from its first, of picks of the pull request's own
// commits in their order, each with the -x line of the commit it came from,
// that names the original's merge commit in its trailer; or for a squash one
// pick of its commit on main. Either changes something, and only files the
// original changes. A pick is known by the -x line git appended, not by
// lines its message brought along, and one that also claims another commit
// of the same original is refused.
func checkUnit(b *batch, u unit) (string, *verdict, error) {
	switch len(u.parents) {
	case 1:
		return checkPick(b, u)
	case 2:
		return checkMerge(b, u)
	}
	return fmt.Sprintf("%.12s has %d parents", u.commit, len(u.parents)), nil, nil
}

func checkPick(b *batch, u unit) (string, *verdict, error) {
	source := appendedSource(u.message)
	if source == "" {
		return fmt.Sprintf("%.12s ends in no (cherry picked from commit …) line; a batch carries originals only", u.commit), nil, nil
	}
	v := b.findMerge(source)
	if v == nil {
		owner, err := b.ownerOf(source)
		if err != nil {
			return "", nil, err
		}
		if owner != nil {
			return fmt.Sprintf("%.12s picks one commit of #%d on its own; a merged pull request goes in as a merge of all its commits", u.commit, owner.o.number), owner, nil
		}
		return fmt.Sprintf("%.12s picks %s, which merged no pull request into main after %s branched", u.commit, source, b.line.branch()), nil, nil
	}
	if v.o.parents > 1 {
		return fmt.Sprintf("%.12s picks the merge commit of #%d as one commit; a merged pull request goes in as a merge of its own commits", u.commit, v.o.number), v, nil
	}
	why, err := contained(b, v, u.parents[0], u.commit)
	return why, v, err
}

func checkMerge(b *batch, u unit) (string, *verdict, error) {
	merges := batchedFrom(u.message)
	if len(merges) != 1 {
		return fmt.Sprintf("%.12s is a merge with %d %s trailers, not one", u.commit, len(merges), batchTrailer), nil, nil
	}
	v := b.findMerge(merges[0])
	switch {
	case v == nil:
		return fmt.Sprintf("%.12s names %s, which merged no pull request into main after %s branched", u.commit, merges[0], b.line.branch()), nil, nil
	case v.o.parents < 2:
		return fmt.Sprintf("%.12s merges #%d, which main took as one commit; it goes in as one pick", u.commit, v.o.number), v, nil
	case len(u.picks) == 0:
		return fmt.Sprintf("%.12s merges nothing", u.commit), v, nil
	}
	if err := b.load(v); err != nil {
		return "", nil, err
	}
	msgs, err := commitMessages(u.picks)
	if err != nil {
		return "", nil, err
	}
	prev, last := u.parents[0], -1
	for _, c := range u.picks {
		ps := msgs[c].parents
		if len(ps) != 1 || ps[0] != prev {
			return fmt.Sprintf("%.12s is not on a straight line of picks from %.12s", c, u.parents[0]), v, nil
		}
		source := appendedSource(msgs[c].message)
		if source == "" {
			return fmt.Sprintf("%.12s ends in no (cherry picked from commit …) line", c), v, nil
		}
		i := slices.Index(v.commits, source)
		if i < 0 {
			owner, err := b.ownerOf(source)
			switch {
			case err != nil:
				return "", nil, err
			case owner != nil:
				return fmt.Sprintf("%.12s picks a commit of #%d into the merge of #%d", c, owner.o.number, v.o.number), v, nil
			}
			return fmt.Sprintf("%.12s picks %s, which is not a commit of #%d", c, source, v.o.number), v, nil
		}
		if i <= last {
			return fmt.Sprintf("%.12s picks %.12s out of the order of #%d's commits", c, source, v.o.number), v, nil
		}
		for _, claimed := range claimedSources(msgs[c].message) {
			if claimed != source && (claimed == v.o.merge || slices.Contains(v.commits, claimed)) {
				return fmt.Sprintf("%.12s claims more than one commit of #%d", c, v.o.number), v, nil
			}
		}
		prev, last = c, i
	}
	why, err := contained(b, v, u.parents[0], u.commit)
	return why, v, err
}

// contained says why the change from base to tip is not inside the files v
// changes, or "" when it is.
func contained(b *batch, v *verdict, base, tip string) (string, error) {
	if err := b.load(v); err != nil {
		return "", err
	}
	changes, err := changedFiles(base, tip)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return fmt.Sprintf("%.12s changes nothing", tip), nil
	}
	allowed := paths(v.changes)
	var extra []string
	for _, p := range paths(changes) {
		if !slices.Contains(allowed, p) {
			extra = append(extra, strconv.Quote(p))
		}
	}
	if len(extra) > 0 {
		return fmt.Sprintf("%.12s touches %s, which #%d does not touch", tip, strings.Join(extra, ", "), v.o.number), nil
	}
	return "", nil
}

// replayUnit picks a unit of the batch again onto HEAD in its own shape: a
// pushed resolution keeps what it resolved. On a conflict it takes the branch
// back to where it was before the unit and names the commit that failed.
func replayUnit(u unit) (string, []string, error) {
	base, err := resolve("HEAD")
	if err != nil {
		return "", nil, err
	}
	picks := u.picks
	if !u.merge() {
		picks = []string{u.commit}
	}
	for _, c := range picks {
		status, files, err := applyPick("-c", "core.hooksPath=/dev/null", "cherry-pick", "--end-of-options", c)
		if err == nil && status == "conflict" {
			return c, files, resetTo(base)
		}
		if err != nil {
			if rerr := resetTo(base); rerr != nil {
				return "", nil, rerr
			}
			return "", nil, fmt.Errorf("picking %.12s again: %w", c, err)
		}
	}
	tip, err := resolve("HEAD")
	if err != nil || !u.merge() || tip == base {
		return "", nil, err
	}
	_, err = commitMerge(base, tip, u.message)
	return "", nil, err
}

// summarize says, for each original the batch head carries in units, how many
// commits it brought and which of them differ from the commit they were picked
// from: a resolution, or a pick git had to fit to the line.
func summarize(b *batch, head string, on []*verdict) (map[*verdict]string, error) {
	units, err := batchUnits(head, b.lineHead)
	if err != nil {
		return nil, err
	}
	out := map[*verdict]string{}
	for _, u := range units {
		v := b.unitOriginal(u)
		if v == nil || !slices.Contains(on, v) {
			continue
		}
		picks := u.picks
		if !u.merge() {
			picks = []string{u.commit}
		}
		msgs, err := commitMessages(picks)
		if err != nil {
			return nil, err
		}
		var changed []string
		for _, c := range picks {
			source := appendedSource(msgs[c].message)
			if source == "" {
				continue
			}
			same, err := samePatch(source, c)
			if err != nil {
				return nil, err
			}
			if !same {
				changed = append(changed, codeSpan(c[:12]))
			}
		}
		s := "1 commit"
		if len(picks) != 1 {
			s = fmt.Sprintf("%d commits", len(picks))
		}
		if len(changed) > 0 {
			s += ", changed in the pick: " + strings.Join(changed, ", ")
		}
		out[v] = s
	}
	return out, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// samePatch says whether two commits make the same change to their first
// parents, by git patch-id of their diffs without context, so that a pick
// that only lands among other lines on the line counts as the same.
func samePatch(a, b string) (bool, error) {
	pa, err := patchID(a)
	if err != nil {
		return false, err
	}
	pb, err := patchID(b)
	return pa == pb, err
}

func patchID(c string) (string, error) {
	diff, err := git("diff", "--no-ext-diff", "--no-textconv", "--unified=0", "--end-of-options", c+"^", c)
	if err != nil {
		return "", err
	}
	out, err := gitInput(diff, "patch-id", "--stable")
	if err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out), " ")
	return id, nil
}
