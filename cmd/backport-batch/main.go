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

// Command backport-batch assembles the automatic backport batch for a release
// line and gates a patch cut on it. docs/release/backports.md is the policy it
// implements; the README describes the commands.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const usageText = `Assemble and check the automatic backport batch for a release line.

Usage:
  backport-batch run   --line release-X.Y [--onto SHA] [--date YYYY-MM-DD] [--push] [--policy REF]
  backport-batch check --line release-X.Y [--policy REF]
  backport-batch fold  --line release-X.Y --original N --commit SHA [--policy REF]

run    Builds backport-batch/<line>-<yyyymmdd> in this working tree, off
       origin/<line>, or on top of the pushed batch branch whose head is
       --onto. Prints the pull request body on stdout. --push pushes the
       branch, fast-forward only, and opens or updates the draft batch PR.
check  Exits 1 while a change the policy takes is neither on the line nor on
       the open batch PR, 0 when nothing is, 2 when it cannot tell.
fold   Checks a hand-made conflict resolution commit for the open batch PR
       and prints ok or why not, exiting 0 or 1. It never commits.

The policy is docs/release/backports.md. Each command reads the day each line
started under it from that page on origin/main, or on --policy REF.
`

const (
	remote       = "origin"
	upstreamRepo = "cozystack/cozystack"
	policyURL    = "https://github.com/" + upstreamRepo + "/blob/main/docs/release/backports.md"
)

type app struct {
	gh     github
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
	// policyRef is where the policy page is read from; origin/main when empty.
	policyRef string
}

func main() {
	a := &app{gh: ghCLI{}, stdout: os.Stdout, stderr: os.Stderr, now: time.Now}
	os.Exit(a.main(os.Args[1:]))
}

func (a *app) main(args []string) int {
	a.stdout, a.stderr = textWriter{a.stdout}, textWriter{a.stderr}
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usageText)
		return 2
	}
	switch args[0] {
	case "run":
		return a.run(args[1:])
	case "check":
		return a.check(args[1:])
	case "fold":
		return a.fold(args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, usageText)
		return 0
	}
	fmt.Fprintf(a.stderr, "unknown command %q\n\n%s", args[0], usageText)
	return 2
}

// textWriter keeps output fit for a terminal. Titles, file names and the
// messages git relays come from other people, and a control sequence among
// them would move the cursor or rewrite what was printed before.
type textWriter struct{ w io.Writer }

func (t textWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(t.w, clean(string(p), true)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() { fmt.Fprint(a.stderr, usageText) }
	fs.StringVar(&a.policyRef, "policy", "", "ref to read "+policyPage+" from (default: origin/main)")
	return fs
}

// parse parses a subcommand's flags; positional arguments are refused, since
// none is defined and a stray one is a typo for a flag.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "backport-batch: %v\n", err)
	return 2
}

func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.stderr, "warning: "+format+"\n", args...)
}

// verdict is what the policy says about one original for the line.
type verdict struct {
	o      *original
	rule   int // the rule that keeps it out, 0 when it qualifies
	reason string
	// commits are what an -x reference to the original may name, and changes
	// the files it changes; both are read only for what passes rules 2 and 3.
	commits []string
	changes []fileChange
	// onBatch is the commit of the open batch pull request that picks it.
	onBatch string
	// label is the maintainer override the original carries, if any.
	label string
}

// batch is everything read about a line before anything is written.
type batch struct {
	line     line
	newer    *line
	lineHead string
	mainHead string
	base     string
	other    []string
	verdicts []*verdict
	pr       *pullRequest
	prHead   string
	// start is when the line started under the policy, zero for its fork point.
	start time.Time
}

func fetch() error {
	_, err := git("fetch", "--quiet", "--end-of-options", remote)
	return err
}

// prepare reads the line, main and the open batch pull request, each pinned to
// one commit, and judges every original by the policy.
func (a *app) prepare(l line) (*batch, error) {
	b := &batch{line: l}
	var err error
	if b.newer, err = newerLine(l); err != nil {
		return nil, err
	}
	if b.lineHead, err = resolve("refs/remotes/" + remote + "/" + l.branch()); err != nil {
		return nil, err
	}
	if b.mainHead, err = resolve("refs/remotes/" + remote + "/main"); err != nil {
		return nil, err
	}
	policyAt := b.mainHead
	if a.policyRef != "" {
		if strings.HasPrefix(a.policyRef, "-") {
			return nil, fmt.Errorf("--policy %q is not a ref", a.policyRef)
		}
		if policyAt, err = resolve(a.policyRef); err != nil {
			return nil, err
		}
	}
	lineStarts, err := readPolicy(policyAt)
	if err != nil {
		return nil, err
	}
	b.start = lineStarts[l.branch()]
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
	lineHist, err := readHistory(b.lineHead, b.mainHead)
	if err != nil {
		return nil, err
	}
	var newerHist *history
	if b.newer != nil {
		newerHead, err := resolve("refs/remotes/" + remote + "/" + b.newer.branch())
		if err != nil {
			return nil, err
		}
		if newerHist, err = readHistory(newerHead, b.mainHead); err != nil {
			return nil, err
		}
	}
	labels, err := overrides(a.gh)
	if err != nil {
		return nil, err
	}
	prs, err := a.gh.openPulls(l.branch())
	if err != nil {
		return nil, err
	}
	pr, warnings, err := batchPull(a.gh, prs, l)
	for _, w := range warnings {
		a.warn("%s", w)
	}
	if err != nil {
		return nil, err
	}
	var prPicks *history
	if pr != nil {
		b.pr = pr
		if b.prHead, err = batchHead(pr); err != nil {
			return nil, err
		}
		if prPicks, err = readPicks(b.prHead, b.lineHead); err != nil {
			return nil, err
		}
	}
	for _, o := range merges {
		v, err := judge(o, labels[o.number], b.start, lineHist, b.newer, newerHist)
		if err != nil {
			return nil, err
		}
		if v.rule == 0 && prPicks != nil {
			v.onBatch = prPicks.picked(v.commits)
		}
		b.verdicts = append(b.verdicts, v)
	}
	return b, nil
}

// overrides maps each pull request merged into main that carries a backport/
// label to the one that decides; backport/skip wins over backport/include.
func overrides(gh github) (map[int]string, error) {
	labels := map[int]string{}
	for _, label := range []string{labelInclude, labelSkip} {
		numbers, err := gh.labelled(label)
		if err != nil {
			return nil, err
		}
		for _, n := range numbers {
			labels[n] = label
		}
	}
	return labels, nil
}

// judge applies rules 2, 3, 4, 1 and 6, in the order of what each costs to
// read; the first that keeps the original out is the one reported. A line that
// started under the policy after it branched takes nothing merged before that
// start. backport/skip keeps an original out whatever the rules say, and
// backport/include lifts the start and rules 2 to 4, but not what the line or
// the newer line carries.
func judge(o *original, label string, start time.Time, lineHist *history, newer *line, newerHist *history) (*verdict, error) {
	v := &verdict{o: o, label: label}
	if label == labelSkip {
		v.rule, v.reason = ruleSkipLabel, labelSkip
		return v, nil
	}
	if o.merged.Before(start) && label != labelInclude {
		v.rule, v.reason = ruleBeforeStart, "merged "+o.merged.Format(time.DateOnly)
		return v, nil
	}
	if r := typeRule(o.title); r != "" && label != labelInclude {
		v.rule, v.reason = ruleType, r
		return v, nil
	}
	if r := breakingRule(o.title, o.body); r != "" && label != labelInclude {
		v.rule, v.reason = ruleBreaking, r
		return v, nil
	}
	var err error
	if v.changes, err = changedFiles(o.merge+"^1", o.merge); err != nil {
		return nil, err
	}
	if label != labelInclude {
		r, err := touchesRule(o, v.changes)
		if err != nil {
			return nil, err
		}
		if r != "" {
			v.rule, v.reason = ruleTouches, r
			return v, nil
		}
	}
	if v.commits, err = commitsOf(o); err != nil {
		return nil, err
	}
	how, err := lineHist.carries(o, v.commits)
	if err != nil {
		return nil, err
	}
	if how != "" {
		v.rule, v.reason = ruleOnLine, how
		return v, nil
	}
	if newer != nil {
		how, err := newerHist.carries(o, v.commits)
		if err != nil {
			return nil, err
		}
		if how == "" {
			v.rule, v.reason = ruleNewer, "not on "+newer.branch()+" yet"
		}
	}
	return v, nil
}

func (b *batch) qualifying() []*verdict {
	var out []*verdict
	for _, v := range b.verdicts {
		if v.rule == 0 {
			out = append(out, v)
		}
	}
	return out
}

func (b *batch) find(number int) *verdict {
	for _, v := range b.verdicts {
		if v.o.number == number {
			return v
		}
	}
	return nil
}
