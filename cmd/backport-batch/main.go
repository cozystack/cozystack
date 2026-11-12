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
  backport-batch run   --line release-X.Y [--onto SHA] [--date YYYY-MM-DD] [--no-workflow-files] [--push] [--policy REF]
  backport-batch run   --line release-X.Y --continue [--no-workflow-files] [--push] [--policy REF]
  backport-batch run   --line release-X.Y --defer N[,N] --reason TEXT [--push] [--policy REF]
  backport-batch run   --line release-X.Y --drop N[,N] --reason TEXT [--push] [--policy REF]
  backport-batch run   --line release-X.Y --undrop N[,N] [--push] [--policy REF]
  backport-batch check --line release-X.Y [--cut] [--policy REF]
  backport-batch fold  --line release-X.Y --original N --commit SHA [--policy REF]
  backport-batch verify --line release-X.Y [--head SHA]
  backport-batch notify --line release-X.Y [--mention org/team|login] [--push]
  backport-batch report --pr N [--id ID] [--json]
  backport-batch lines [--policy REF]

run    Builds backport-batch/<line>-<yyyymmdd> in this working tree, off
       origin/<line>, or on top of the pushed batch branch whose head is
       --onto. Each original goes in as its own commits, picked with -x, and
       a merge commit naming it; a squash-merged one as one pick. --continue
       takes the head from the open batch PR, and starts a batch when none is
       open. Prints the pull request body on stdout.
       --no-workflow-files leaves an original that changes .github/workflows/
       to a maintainer, listed on the batch, for a token that cannot push it.
       --push pushes the branch, fast-forward only, and opens or updates the
       draft batch PR.
       --defer rebuilds the open batch off origin/<line> without those
       originals and records them as deferred, for --reason: the batch keeps
       them out, the next one tries them again. --push replaces the branch,
       leased on the head the rebuild started from; when nothing is left it
       closes the batch PR instead, after recording why in it.
       --drop drops originals from the line for --reason: the line's record
       keeps them out of this batch and every later one. One the open batch
       carries is taken off it as --defer would; otherwise only the record
       changes. --undrop withdraws a drop. Both need an open batch PR whose
       record they change.
check  Exits 1 while a change the policy takes is neither on the line nor on
       the open batch PR, 0 when nothing is, 2 when it cannot tell. With --cut,
       the gate before an rc or a patch is cut, it also exits 1 while a change
       is only on the open batch PR, which the cut would not carry, or waits
       there for a maintainer because it changes workflows.
fold   Checks a hand-made conflict resolution for the open batch PR: for a
       merged pull request a merge of its picked commits, for a squash one
       commit. Prints ok or why not, exiting 0 or 1. It never commits.
verify Checks every original on the batch, from the line's head to --head or
       to the open batch PR's head, against that same shape. Exits 0 when all
       pass, 1 with the reasons when one does not.
notify Prints what waits on the open batch PR for a maintainer: a batch
       open for 3 days or more, its deferred conflicts, and the originals
       that change workflows. --push keeps one comment on the PR that says
       what waits, mentioning --mention.
report Prints a report batch PR N keeps: the one its body carries, or the
       one --id names, read from its comments or its body. --json prints the
       report as it is stored. It writes nothing, so it reads merged and
       closed batches too.
lines  Prints the release lines the batch runs for, newest first.

The policy is docs/release/backports.md. run, check, fold and lines read its
policy block from that page on origin/main, or on --policy REF. Every command
but notify and report fetches origin first. notify reads only GitHub; report
reads GitHub, and for a report that keeps drops also this clone's history,
which has to hold the main and line heads the report was made from: report
never fetches on its own.

BACKPORT_BATCH_BOT names the bot the scheduled batch writes as,
<app>[bot], cozystack-ci[bot] when unset: a batch pull request or a notify
comment it wrote is trusted like a maintainer's. Set and empty, no bot is.
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
	// tool is the commit backport-batch was built from, for the record.
	tool string
	// unwritten is the first error writing to stdout, which fails the run:
	// what it printed never reached its reader.
	unwritten error
}

func main() {
	a := &app{gh: ghCLI{bot: batchBotLogin(os.LookupEnv)}, stdout: os.Stdout, stderr: os.Stderr, now: time.Now, tool: toolVersion()}
	os.Exit(a.main(os.Args[1:]))
}

func (a *app) main(args []string) int {
	a.stdout, a.stderr = textWriter{w: a.stdout, failed: &a.unwritten}, textWriter{w: a.stderr}
	code := a.command(args)
	if a.unwritten != nil {
		_, _ = fmt.Fprintf(a.stderr, "backport-batch: output was lost: %v; anything the run did, such as a push or a publish, still happened\n", a.unwritten)
		if code == 0 {
			code = 2
		}
	}
	return code
}

func (a *app) command(args []string) int {
	a.gh = &permCache{github: a.gh, perms: map[string]string{}}
	if len(args) == 0 {
		_, _ = fmt.Fprint(a.stderr, usageText)
		return 2
	}
	if bot := a.gh.batchBot(); bot != "" && !botLoginRE.MatchString(bot) {
		return a.fail(fmt.Errorf("%s %q is not a bot login, <app>[bot]", batchBotEnv, bot))
	}
	switch args[0] {
	case "run":
		return a.run(args[1:])
	case "check":
		return a.check(args[1:])
	case "fold":
		return a.fold(args[1:])
	case "verify":
		return a.verify(args[1:])
	case "notify":
		return a.notify(args[1:])
	case "report":
		return a.report(args[1:])
	case "lines":
		return a.lines(args[1:])
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(a.stdout, usageText)
		return 0
	}
	_, _ = fmt.Fprintf(a.stderr, "unknown command %q\n\n%s", args[0], usageText)
	return 2
}

// textWriter keeps output fit for a terminal. Titles, file names and the
// messages git relays come from other people, and a control sequence among
// them would move the cursor or rewrite what was printed before.
type textWriter struct {
	w      io.Writer
	failed *error
}

func (t textWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(t.w, clean(string(p), true)); err != nil {
		if t.failed != nil && *t.failed == nil {
			*t.failed = err
		}
		return 0, err
	}
	return len(p), nil
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(a.stderr, usageText) }
	return fs
}

func (a *app) policyFlags(name string) *flag.FlagSet {
	fs := a.flags(name)
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
	_, _ = fmt.Fprintf(a.stderr, "backport-batch: %v\n", err)
	return 2
}

func (a *app) warn(format string, args ...any) {
	_, _ = fmt.Fprintf(a.stderr, "warning: "+format+"\n", args...)
}

// verdict is what the policy says about one original for the line.
type verdict struct {
	o      *original
	rule   int // the rule that keeps it out, 0 when it qualifies
	reason string
	// commits are the commits the original brings, one pick each, and changes
	// the files it changes; both are read only for what passes rules 2 and 3,
	// and by load for anything else.
	commits []string
	changes []fileChange
	// onBatch is the commit of the open batch pull request that picks it,
	// and batched the unit of the open batch that carries it whatever the
	// rules now say of it.
	onBatch string
	batched string
	// label is the maintainer override the original carries, if any.
	label string
	// partial lists what its clean net merge onto the line would leave
	// without effect, and park why the batch leaves it out, if it does.
	partial []string
	park    *parkedEntry
	// deps is what it builds on that the line lacks.
	deps []dependency
	// diag is what could not be judged about it, shown whatever became of
	// it.
	diag []string
}

// batch is everything read about a line before anything is written.
type batch struct {
	line     line
	newer    *line
	lineHead string
	mainHead string
	// newerHead is the newer line's head, and policyAt the commit the
	// policy was read from.
	newerHead string
	policyAt  string
	base      string
	other     []string
	verdicts  []*verdict
	pr        *pullRequest
	prHead    string
	// picks is what the open batch pull request carries beyond the line.
	picks *history
	// src is the line's record and where it was found, and drops its drops
	// that name an original of this batch.
	src   recordSource
	drops map[int]droppedEntry
	// lineHist and newerHist are what the line and the newer line carry,
	// read once for every original judged.
	lineHist, newerHist *history
	// start is when the line started under the policy, zero for its fork point.
	start time.Time
	// textUntil is the batch-only day: what a commit's text says counts as
	// evidence only on commits from before it.
	textUntil time.Time
	// partialUnits are the originals whose unit on the open batch leaves
	// what it lists without effect.
	partialUnits map[*verdict][]string
	// What one command read once and asks again: renames by commit pair,
	// units by batch head, and how often git merge-file ran.
	renameCache map[string]renameSet
	unitCache   map[string]map[*verdict]string
	mergeFiles  int
	// renameLimit is the -l rename detection runs with; 0, git's widest.
	renameLimit int
	// brought maps main's commits since the fork point to the first-parent
	// commit that brought each, and ancestors says which commits blame
	// named are older than the fork point.
	brought   map[string]string
	ancestors map[string]bool
	// detecting is how long judging partial units and dependencies took.
	detecting time.Duration
	depCache  map[string][]dependency
	// lineCarries says, for a prerequisite the rules keep out, whether the
	// line carries it all the same.
	lineCarries map[*verdict]bool
}

// fetch moves to the top of the working tree first: every path the batch
// hands git is relative to it.
func fetch() error {
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if err := os.Chdir(strings.TrimSpace(top)); err != nil {
		return err
	}
	_, err = git("fetch", "--quiet", "--end-of-options", remote)
	return err
}

// policy reads the policy and says from which commit.
func (a *app) policy(mainHead string) (*policy, string, error) {
	at := mainHead
	if a.policyRef != "" {
		if strings.HasPrefix(a.policyRef, "-") {
			return nil, "", fmt.Errorf("--policy %q is not a ref", a.policyRef)
		}
		var err error
		if at, err = resolve(a.policyRef); err != nil {
			return nil, "", err
		}
	}
	pol, err := readPolicy(at)
	return pol, at, err
}

// prepare reads the line, main and the open batch pull request, each pinned to
// one commit, and judges every original by the policy.
func (a *app) prepare(l line) (*batch, error) {
	b := &batch{line: l}
	var err error
	if b.mainHead, err = resolve("refs/remotes/" + remote + "/main"); err != nil {
		return nil, err
	}
	pol, policyAt, err := a.policy(b.mainHead)
	if err != nil {
		return nil, err
	}
	lines, err := batchLines(pol.released)
	if err != nil {
		return nil, err
	}
	if b.newer, err = newerLine(l, lines); err != nil {
		return nil, err
	}
	if b.lineHead, err = resolve("refs/remotes/" + remote + "/" + l.branch()); err != nil {
		return nil, err
	}
	b.start, b.textUntil, b.policyAt = pol.starts[l.branch()], pol.batchOnly, policyAt
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
	if b.lineHist, err = readHistory(b.lineHead, b.mainHead, b.textUntil); err != nil {
		return nil, err
	}
	if b.newer != nil {
		if b.newerHead, err = resolve("refs/remotes/" + remote + "/" + b.newer.branch()); err != nil {
			return nil, err
		}
		if b.newerHist, err = readHistory(b.newerHead, b.mainHead, b.textUntil); err != nil {
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
	if pr != nil {
		b.pr = pr
		if b.prHead, err = batchHead(pr); err != nil {
			return nil, err
		}
		if b.picks, err = readPicks(b.prHead, b.lineHead, b.textUntil); err != nil {
			return nil, err
		}
	}
	if b.src, err = findLineRecord(a.gh, l, pr); err != nil {
		return nil, err
	}
	if b.drops, err = b.bindDrops(merges); err != nil {
		return nil, err
	}
	if err := b.waitOrder(merges); err != nil {
		return nil, err
	}
	for _, o := range merges {
		v := &verdict{o: o, label: labels[o.number]}
		if err := b.judge(v, b.drops); err != nil {
			return nil, err
		}
		b.verdicts = append(b.verdicts, v)
	}
	if err := b.partialStates(); err != nil {
		return nil, err
	}
	return b, nil
}

// judge judges v again under drops, and finds it on the open batch when the
// policy takes it.
func (b *batch) judge(v *verdict, drops map[int]droppedEntry) error {
	j, err := judge(v.o, v.label, b.start, b.lineHist, b.newer, b.newerHist, drops)
	if err != nil {
		return err
	}
	*v = *j
	if v.rule == 0 && b.picks != nil {
		v.onBatch, _ = b.picks.found(v.o, v.commits)
	}
	return nil
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
// the newer line carries. A maintainer's drop keeps out what the line does
// not carry, include or not.
func judge(o *original, label string, start time.Time, lineHist *history, newer *line, newerHist *history, drops map[int]droppedEntry) (*verdict, error) {
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
	if v.commits, err = prCommits(o); err != nil {
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
	if d, ok := drops[o.number]; ok {
		v.rule, v.reason = ruleDropped, fmt.Sprintf("dropped on #%d: %s", d.On, d.Reason)
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

// load reads what the rules did not need to read for an original: its
// commits and the files it changes.
func (b *batch) load(v *verdict) error {
	var err error
	if v.changes == nil {
		if v.changes, err = changedFiles(v.o.merge+"^1", v.o.merge); err != nil {
			return err
		}
	}
	if v.commits == nil {
		v.commits, err = prCommits(v.o)
	}
	return err
}

// findMerge is the original merged as merge commit sha, or nil.
func (b *batch) findMerge(sha string) *verdict {
	for _, v := range b.verdicts {
		if v.o.merge == sha {
			return v
		}
	}
	return nil
}

func (b *batch) find(number int) *verdict {
	for _, v := range b.verdicts {
		if v.o.number == number {
			return v
		}
	}
	return nil
}
