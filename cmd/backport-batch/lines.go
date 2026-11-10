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
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	releaseLineRE = regexp.MustCompile(`^release-(\d+)\.(\d+)$`)
	stableTagRE   = regexp.MustCompile(`^refs/tags/v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)
	prNumberRE    = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	mergePRRE     = regexp.MustCompile(`^Merge pull request #(\d+) from \S+$`)
)

// line is a release line, i.e. a major.minor pair with a release-X.Y branch.
type line struct{ major, minor int }

func (l line) branch() string { return fmt.Sprintf("release-%d.%d", l.major, l.minor) }
func (l line) less(o line) bool {
	if l.major != o.major {
		return l.major < o.major
	}
	return l.minor < o.minor
}

// parseLine accepts exactly the branch name of a line: release-01.6 parses to
// the same numbers as release-1.6 without being that branch.
func parseLine(s string) (line, error) {
	m := releaseLineRE.FindStringSubmatch(s)
	if m == nil {
		return line{}, fmt.Errorf("not a release line: %q (expected release-<major>.<minor>)", s)
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	l := line{major, minor}
	if err1 != nil || err2 != nil || l.branch() != s {
		return line{}, fmt.Errorf("not a release line: %q (expected release-<major>.<minor>)", s)
	}
	return l, nil
}

// releaseLines returns the two-component release branches on the remote,
// oldest first. release-1.6.0-rc.1 and the like are staging branches, not
// maintenance lines, so they are excluded.
func releaseLines() ([]line, error) {
	out, err := git("for-each-ref", "--format=%(refname:strip=3)", "refs/remotes/"+remote+"/release-*")
	if err != nil {
		return nil, err
	}
	var lines []line
	for name := range strings.FieldsSeq(out) {
		if l, err := parseLine(name); err == nil {
			lines = append(lines, l)
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].less(lines[j]) })
	return lines, nil
}

// shippedLines reads which lines have a stable release on the remote: a
// vX.Y.Z tag with no pre-release suffix, which only the finalize job of
// pull-requests-release.yaml creates, when the promoted release merges.
func shippedLines() (map[line]bool, error) {
	out, err := git("ls-remote", "--tags", "--refs", "--end-of-options", remote, "refs/tags/v*")
	if err != nil {
		return nil, err
	}
	shipped := map[line]bool{}
	for ln := range strings.Lines(out) {
		_, ref, _ := strings.Cut(strings.TrimSpace(ln), "\t")
		m := stableTagRE.FindStringSubmatch(ref)
		if m == nil {
			continue
		}
		major, err1 := strconv.Atoi(m[1])
		minor, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil {
			shipped[line{major, minor}] = true
		}
	}
	return shipped, nil
}

// supportedLines picks from all, oldest first, the lines the batch runs for:
// the newest released lines that have shipped a stable release, and every
// line branched after the newest of them, whose rc window is open.
func supportedLines(all []line, shipped map[line]bool, released int) []line {
	var out []line
	kept := 0
	for i := len(all) - 1; i >= 0 && kept < released; i-- {
		switch {
		case shipped[all[i]]:
			kept++
		case kept > 0:
			// Older than a release and never released itself: abandoned.
			continue
		}
		out = append(out, all[i])
	}
	slices.Reverse(out)
	return out
}

// batchLines are the lines the batch runs for, oldest first.
func batchLines(released int) ([]line, error) {
	all, err := releaseLines()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no release-X.Y branches on %s", remote)
	}
	shipped, err := shippedLines()
	if err != nil {
		return nil, err
	}
	return supportedLines(all, shipped, released), nil
}

// newerLine returns the line after want among lines, oldest first, and nil
// for the newest. Rule 6 of the policy holds a line to what the next newer one
// carries, and the batch runs for no line outside them.
func newerLine(want line, lines []line) (*line, error) {
	i := slices.Index(lines, want)
	switch {
	case i < 0:
		names := make([]string, len(lines))
		for j, l := range lines {
			names[j] = l.branch()
		}
		return nil, fmt.Errorf("%s is not a line the batch runs for; it runs for %s", want.branch(), strings.Join(names, ", "))
	case i == len(lines)-1:
		return nil, nil
	}
	return &lines[i+1], nil
}

// original is a pull request merged into main, read from its merge commit.
//
// The title and body come from the commit message, not from GitHub: the
// commit holds them as they were when a maintainer merged the pull request,
// while the live pull request stays editable by its author.
type original struct {
	number  int
	title   string
	body    string
	merge   string
	parents int
	merged  time.Time
}

func (o *original) url() string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", upstreamRepo, o.number)
}

// mainMerges reads main's first-parent history after base, oldest first: one
// original per pull request merge, and the commits that name no pull request.
func mainMerges(base, mainHead string) ([]*original, []string, error) {
	out, err := git("rev-list", "--first-parent", "--reverse", "--parents", "--end-of-options", base+".."+mainHead)
	if err != nil {
		return nil, nil, err
	}
	type merge struct {
		oid     string
		parents int
	}
	var list []merge
	var oids []string
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		for _, sha := range f {
			if checkSHA(sha) != nil {
				return nil, nil, fmt.Errorf("rev-list: %q is not a commit", sha)
			}
		}
		if len(f) < 2 {
			return nil, nil, fmt.Errorf("rev-list: %s has no parent", f[0])
		}
		list = append(list, merge{f[0], len(f) - 1})
		oids = append(oids, f[0])
	}
	msgs, err := commitMessages(oids)
	if err != nil {
		return nil, nil, err
	}
	var merges []*original
	var other []string
	for _, m := range list {
		// The subject is the first paragraph, its lines joined, as git reads it.
		msg := strings.TrimRight(msgs[m.oid].message, "\n")
		subject, body, _ := strings.Cut(msg, "\n\n")
		subject = strings.ReplaceAll(subject, "\n", " ")
		o := &original{merge: m.oid, parents: m.parents, merged: msgs[m.oid].committed}
		if pr := mergePRRE.FindStringSubmatch(subject); pr != nil {
			o.number, _ = strconv.Atoi(pr[1])
			body = strings.TrimLeft(body, "\n")
			o.title, body, _ = strings.Cut(body, "\n")
		} else if pr := prNumberRE.FindStringSubmatchIndex(subject); pr != nil {
			o.number, _ = strconv.Atoi(subject[pr[2]:pr[3]])
			o.title = strings.TrimSpace(subject[:pr[0]])
		}
		if o.number <= 0 || o.parents > 2 {
			other = append(other, m.oid)
			continue
		}
		o.body = strings.TrimLeft(body, "\n")
		merges = append(merges, o)
	}
	return merges, other, nil
}

// prCommits lists what an original brings to a line, one pick each: for a
// merged pull request every commit of its own, oldest first, leaving out the
// merges that brought main into its branch, whose content main already has;
// for a squash, its one commit on main.
func prCommits(o *original) ([]string, error) {
	if o.parents < 2 {
		return []string{o.merge}, nil
	}
	oids, err := revList("--reverse", "--topo-order", "--no-merges", "--end-of-options", o.merge+"^1.."+o.merge+"^2")
	if err != nil {
		return nil, fmt.Errorf("reading the commits of #%d: %w", o.number, err)
	}
	return oids, nil
}
