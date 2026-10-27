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
	"strings"
)

// verify checks every original a batch carries, from the line's head to the
// batch head, against the shape the batch carries it in: what fold checks for
// one resolution, for all of them and for the clean picks too. fold runs only
// where someone resolved a conflict and chose to run it; CI runs verify on
// every push to a batch pull request.
//
// With --head it reads that commit and nothing from GitHub, which is how CI
// runs it: the tool built from main, the batch head fetched as data.
func (a *app) verify(args []string) int {
	fs := a.flags("verify")
	lineArg := fs.String("line", "", "release line, release-X.Y")
	headArg := fs.String("head", "", "the batch head to verify (default: the head of the open batch PR)")
	if err := parse(fs, args); err != nil {
		return 2
	}
	l, err := parseLine(*lineArg)
	if err != nil {
		return a.fail(err)
	}
	if *headArg != "" {
		if err := checkSHA(*headArg); err != nil {
			return a.fail(fmt.Errorf("--head: %w", err))
		}
	}
	if err := fetch(); err != nil {
		return a.fail(err)
	}
	b, err := shapeOf(l)
	if err != nil {
		return a.fail(err)
	}
	head := *headArg
	if head == "" {
		prs, err := a.gh.openPulls(l.branch())
		if err != nil {
			return a.fail(err)
		}
		pr, warnings, err := batchPull(a.gh, prs, l)
		for _, w := range warnings {
			a.warn("%s", w)
		}
		if err != nil {
			return a.fail(err)
		}
		if pr == nil {
			fmt.Fprintf(a.stdout, "%s: no batch pull request is open; nothing to verify\n", l.branch())
			return 0
		}
		if head, err = batchHead(pr); err != nil {
			return a.fail(err)
		}
	} else if !gitOK("cat-file", "-e", "--end-of-options", head+"^{commit}") {
		return a.fail(fmt.Errorf("--head %.12s is not in this repository; fetch it first", head))
	}
	units, err := batchUnits(head, b.lineHead)
	if err != nil {
		return a.fail(err)
	}
	seen := map[*verdict]string{}
	var refused []string
	for _, u := range units {
		why, v, err := checkUnit(b, u)
		if err != nil {
			return a.fail(err)
		}
		if why == "" && seen[v] != "" {
			why = fmt.Sprintf("carries #%d again, which %.12s already carries", v.o.number, seen[v])
		}
		if why != "" {
			refused = append(refused, why)
			fmt.Fprintf(a.stdout, "refused %.12s: %s\n", u.commit, why)
			continue
		}
		seen[v] = u.commit
		n := len(u.picks)
		if !u.merge() {
			n = 1
		}
		fmt.Fprintf(a.stdout, "ok %.12s #%d, %d %s\n", u.commit, v.o.number, n, plural(n, "commit"))
	}
	if len(refused) > 0 {
		fmt.Fprintf(a.stdout, "%s: %d of %d units at %.12s are not originals in the batch's shape\n", l.branch(), len(refused), len(units), head)
		return 1
	}
	fmt.Fprintf(a.stdout, "%s: %d %s at %.12s, each one original in the batch's shape\n", l.branch(), len(units), plural(len(units), "unit"), head)
	return 0
}

// shapeOf reads what verify needs of a line, from git alone: its head, and
// every pull request merged into main since it branched, unjudged.
func shapeOf(l line) (*batch, error) {
	b := &batch{line: l}
	var err error
	if b.lineHead, err = resolve("refs/remotes/" + remote + "/" + l.branch()); err != nil {
		return nil, err
	}
	if b.mainHead, err = resolve("refs/remotes/" + remote + "/main"); err != nil {
		return nil, err
	}
	out, err := git("merge-base", "--end-of-options", b.mainHead, b.lineHead)
	if err != nil {
		return nil, fmt.Errorf("%s shares no history with main: %w", l.branch(), err)
	}
	b.base = strings.TrimSpace(out)
	merges, other, err := mainMerges(b.base, b.mainHead)
	if err != nil {
		return nil, err
	}
	b.other = other
	for _, o := range merges {
		b.verdicts = append(b.verdicts, &verdict{o: o})
	}
	return b, nil
}
