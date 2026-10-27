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
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A reference to a PR, optionally qualified by its repository, and a list of
// them as prose writes one: "#1 and #2", "#1, #2 and #3", "#1, #2, and #3".
const (
	prRef     = `(?:[\w.-]+/[\w.-]+)?#\d+`
	prRefList = prRef + `(?:(?:\s*,\s*(?:and\s+)?|\s+and\s+)` + prRef + `)*`
)

// A release line named in prose, bare or in backticks, as a whole word.
const releaseLineName = `(?:\x60release-\d+\.\d+\x60|release-\d+\.\d+)(?:$|\s|\)|[.,;:](?:\s|$))`

var (
	// A hand backport that carries more than one change names all of them in
	// its "Backport of" phrase, either as a list or as the "to release-X.Y,
	// together with #N" this repository writes for a dependency pulled along.
	backportOfRE = regexp.MustCompile(`[Bb]ackport of\s+(` + prRefList + `)` +
		`(?:\s+to\s+\x60?release-\d+\.\d+\x60?,?\s+together with\s+(` + prRefList + `))?`)
	// What a list of references may be followed by for every item in it to be
	// an original. Anything else may be the list going on to say something
	// about its later items -- "#10, #20 is not included" -- so an
	// unterminated list keeps only its first item.
	prRefListEndRE = regexp.MustCompile(`^(?:[ \t]*(?:\r?\n|$)|\.(?:\s|$)|\s+to\s+` + releaseLineName + `)`)
	prRefRE        = regexp.MustCompile(`(?:([\w.-]+/[\w.-]+))?#(\d+)`)
	xLineRE        = regexp.MustCompile(`^\(cherry picked from commit ([0-9a-f]{40})\)$`)
	batchLineRE    = regexp.MustCompile(`^` + batchTrailer + `: ([0-9a-f]{40})$`)
	trailerRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*[ \t]*:[ \t]`)
	paragraphRE    = regexp.MustCompile(`\n[ \t]*\n`)
)

// batchTrailer names, in the merge commit a batch makes for an original, the
// merge commit on main it carries.
const batchTrailer = "Backported-from"

// trailerBlock is the last paragraph of a message when every line of it is a
// trailer, a trailer's continuation or an -x line, which is where git writes
// both; nil otherwise. The same lines anywhere else are prose, which anyone
// can write. interpret-trailers does not report -x lines, so the block is read
// here the way git finds it.
func trailerBlock(message string) []string {
	var paras []string
	for _, p := range paragraphRE.Split(strings.ReplaceAll(message, "\r\n", "\n"), -1) {
		if strings.TrimSpace(p) != "" {
			paras = append(paras, p)
		}
	}
	if len(paras) < 2 {
		return nil
	}
	var block []string
	for _, ln := range strings.Split(paras[len(paras)-1], "\n") {
		switch t := strings.TrimSpace(ln); {
		case t == "":
		case xLineRE.MatchString(t):
			block = append(block, t)
		case strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t"):
			// a trailer's continuation line
		case !trailerRE.MatchString(ln):
			return nil
		default:
			block = append(block, t)
		}
	}
	return block
}

// trailerValues lists what the lines of a message's trailer block that re
// matches name.
func trailerValues(message string, re *regexp.Regexp) []string {
	var values []string
	for _, ln := range trailerBlock(message) {
		if m := re.FindStringSubmatch(ln); m != nil {
			values = append(values, m[1])
		}
	}
	return values
}

// xSources lists the commits the -x lines of a message name.
func xSources(message string) []string { return trailerValues(message, xLineRE) }

// batchedFrom lists the merge commits on main a batch merge says it carries.
func batchedFrom(message string) []string { return trailerValues(message, batchLineRE) }

// bodyOrigins returns the PR numbers named in a message's "Backport of"
// phrases, in order of appearance and without repeats. A reference qualified
// with another repository is not a local number.
func bodyOrigins(body, repo string) []int {
	var origins []int
	for _, m := range backportOfRE.FindAllStringSubmatchIndex(body, -1) {
		for g := 1; 2*g+1 < len(m); g++ {
			start, end := m[2*g], m[2*g+1]
			if start < 0 {
				continue
			}
			refs := prRefRE.FindAllStringSubmatch(body[start:end], -1)
			if !prRefListEndRE.MatchString(body[end:]) {
				refs = refs[:1]
			}
			for _, ref := range refs {
				if ref[1] != "" && !strings.EqualFold(ref[1], repo) {
					continue
				}
				n, _ := strconv.Atoi(ref[2])
				if n > 0 && !slices.Contains(origins, n) {
					origins = append(origins, n)
				}
			}
		}
	}
	return origins
}

// pickRef is an -x reference: the commit it names, and the commit carrying it.
type pickRef struct{ source, commit string }

// history is what a branch carries beyond a base it shares with main.
//
// Only the commits outside the base supply -x, batch and "Backport of"
// evidence: shared history can name an original without carrying it on this
// branch.
type history struct {
	head      string
	reachable map[string]bool
	picks     []pickRef
	// batches maps a merge commit on main to the batch merge that names it.
	batches   map[string]string
	backports map[int]string
}

func readHistory(head, base string) (*history, error) {
	h, err := readPicks(head, base)
	if err != nil {
		return nil, err
	}
	all, err := revList("--end-of-options", head)
	if err != nil {
		return nil, err
	}
	for _, oid := range all {
		h.reachable[oid] = true
	}
	return h, nil
}

// readPicks reads only the messages of head's commits outside base, which is
// all a batch branch is asked about.
func readPicks(head, base string) (*history, error) {
	h := &history{head: head, reachable: map[string]bool{}, batches: map[string]string{}, backports: map[int]string{}}
	oids, err := revList("--end-of-options", head, "^"+base)
	if err != nil {
		return nil, err
	}
	msgs, err := commitMessages(oids)
	if err != nil {
		return nil, err
	}
	for _, oid := range oids {
		msg := msgs[oid].message
		for _, source := range xSources(msg) {
			h.picks = append(h.picks, pickRef{source: source, commit: oid})
		}
		for _, merge := range batchedFrom(msg) {
			if _, ok := h.batches[merge]; !ok {
				h.batches[merge] = oid
			}
		}
		for _, n := range bodyOrigins(msg, upstreamRepo) {
			if _, ok := h.backports[n]; !ok {
				h.backports[n] = oid
			}
		}
	}
	return h, nil
}

// pickOf returns the commit whose -x line names source, or "".
func (h *history) pickOf(source string) string {
	for _, r := range h.picks {
		if r.source == source {
			return r.commit
		}
	}
	return ""
}

// found returns the commit that carries an original on the branch and how,
// or "" when none does: a batch merge that names its merge commit, an -x line
// naming that merge commit, which is how a batch picked a merged pull request
// with -m 1 before and how a hand backport does, or -x lines naming every one
// of commits, the commits of its own.
func (h *history) found(o *original, commits []string) (string, string) {
	if c, ok := h.batches[o.merge]; ok {
		return c, "batched in " + c[:12]
	}
	if c := h.pickOf(o.merge); c != "" {
		return c, "picked in " + c[:12]
	}
	if o.parents < 2 || len(commits) == 0 {
		return "", ""
	}
	last := ""
	for _, source := range commits {
		if last = h.pickOf(source); last == "" {
			return "", ""
		}
	}
	return last, fmt.Sprintf("its %d %s picked, the last in %s", len(commits), plural(len(commits), "commit"), last[:12])
}

// carries says how the branch carries an original, or "" when it does not.
// The no-op pick goes last: it is the one check that asks git to merge.
func (h *history) carries(o *original, commits []string) (string, error) {
	if h.reachable[o.merge] {
		return "merged before the line forked", nil
	}
	if c, how := h.found(o, commits); c != "" {
		return how, nil
	}
	if c, ok := h.backports[o.number]; ok {
		return `named by "Backport of" in ` + c[:12], nil
	}
	noop, err := noopPick(h.head, o)
	if err != nil {
		return "", err
	}
	if noop {
		return "a pick would change nothing", nil
	}
	return "", nil
}

// noopPick reports whether cherry-picking the original onto head would leave
// head's tree as it is: the content is there, whatever names it.
func noopPick(head string, o *original) (bool, error) {
	stdout, stderr, code, err := gitStatus("merge-tree", "--write-tree", "--no-messages", "--merge-base="+o.merge+"^1", "--end-of-options", head, o.merge)
	switch {
	case err != nil:
		return false, err
	case code == 1:
		return false, nil
	case code != 0:
		return false, fmt.Errorf("git merge-tree for #%d: exit %d: %s", o.number, code, strings.TrimSpace(stderr))
	}
	tree, _, _ := strings.Cut(stdout, "\n")
	headTree, err := resolveTree(head)
	if err != nil {
		return false, err
	}
	return tree == headTree, nil
}

func resolveTree(commit string) (string, error) {
	out, err := git("rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
