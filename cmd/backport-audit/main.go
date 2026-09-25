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

// Command backport-audit answers, before a release or an rc is cut, whether
// every backport-labelled PR actually landed on the release branch, and prints
// the URLs of the ones that did not. It shells out to git and gh (both resolved
// from PATH) and writes nothing anywhere.
//
// A PR is a candidate for release-X.Y when it merged to main carrying the
// `kind/backport` label while X.Y was the current release line, or the
// `kind/backport-previous` label while X.Y was one minor behind it. That
// mirrors .github/workflows/backport.yaml, which resolves its target branches
// at merge time rather than from the label itself, so the same label means a
// different branch depending on when the PR merged. What "the current line"
// meant changed once, at the freeze contract; lineOpenDates carries both
// rules and the reason a single cutover reproduces them faithfully.
//
// Landing is established from three independent kinds of evidence: the PR's
// merge commit already being reachable from the release branch (it merged
// before the branch was cut, so no backport was ever needed); a MERGED backport
// PR for it on that branch; or the branch's own history carrying the change
// (the bot's merge subject, an identical commit subject, or an -x cherry-pick
// reference). Anything left over is reported as MISSING, pending (a backport PR
// is open, including the draft PRs korthout/backport-action opens with the
// conflict committed) or dropped (a backport PR was closed unmerged, with
// whatever reason someone recorded on it).
//
// The labels are not the only record of what a branch received, so the backport
// PRs on it are also read from the side of the originals they name. One whose
// original is not a candidate for the branch is listed as unlabelled, and does
// not move the exit code: the gate answers whether everything labelled landed,
// and an unlabelled backport can only add to a branch, never leave a labelled
// change off it. An original claimed by two open backport PRs, or by an open
// one after another already merged, is a duplicate and does count as
// outstanding. One of them is redundant and should be closed, or the open one
// is the unfinished half of a split backport; either way a human decides
// before the cut, and a verdict cannot say so, because it settles on the first
// merged backport PR it finds or reports the first open one as pending.
//
// Known limits: a hand-backport that is squash-merged, rewords its subject and
// references no original PR is unprovable from either side and reads as
// MISSING; a `kind/backport` label added after the release line moved on
// resolves to the newer line; and a backport PR that names its original
// neither by the bot's head branch nor in a "Backport of" phrase is invisible
// to the unlabelled and duplicate checks.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const usageText = `Audit whether every backport-labelled PR actually landed on a release branch.

Exits 0 when nothing is outstanding and 1 when something is, so it can gate a release.
Backports of PRs that carry no label for the line are listed without affecting the
exit code; an original with more than one live backport PR counts as outstanding.

Usage:
  backport-audit [OPTIONS] [release-X.Y ...]

Arguments:
  release-X.Y ...       Release lines to audit (default: the two newest)

Options:
      --remote NAME     Git remote holding the release branches (default: origin)
      --limit N         Max merged PRs to scan per label (default and ceiling: 1000)
      --no-fetch        Trust the local refs as-is, skipping git fetch
      --json            Machine-readable output
      --no-color        Disable color output (auto-disabled on non-TTY)
  -h, --help            Show this help
`

// A reference to a PR, optionally qualified by its repository, and a list of
// them as prose writes one: "#1 and #2", "#1, #2 and #3", "#1, #2, and #3".
const (
	prRef     = `(?:[\w.-]+/[\w.-]+)?#\d+`
	prRefList = prRef + `(?:(?:\s*,\s*(?:and\s+)?|\s+and\s+)` + prRef + `)*`
)

// A release line named in prose, bare or in backticks, as a whole word:
// release-1.6-fixes, release-1.6.1 and release-1.6.fixes start with a line name
// without being one, so punctuation ends the name only when a space or the end
// of the text follows it.
const releaseLineName = `(?:\x60release-\d+\.\d+\x60|release-\d+\.\d+)(?:$|\s|\)|[.,;:](?:\s|$))`

// upstreamRepo is the repository an unqualified reference is read against when
// a PR's URL does not say which repository it belongs to.
const upstreamRepo = "cozystack/cozystack"

var (
	releaseLineRE  = regexp.MustCompile(`^release-(\d+)\.(\d+)$`)
	backportHeadRE = regexp.MustCompile(`^backport-(\d+)-to-(release-\d+\.\d+)$`)
	// A hand backport that carries more than one change names all of them in
	// its "Backport of" phrase, either as a list or as the "to release-X.Y,
	// together with #N" this repository writes for a dependency pulled along.
	backportOfRE = regexp.MustCompile(`[Bb]ackport of\s+(` + prRefList + `)` +
		`(?:\s+to\s+\x60?release-\d+\.\d+\x60?,?\s+together with\s+(` + prRefList + `))?`)
	// What a list of references may be followed by for every item in it to be
	// an original: the end of the line, the end of a sentence, or the "to
	// release-X.Y" that names the target. Anything else may be the list going
	// on to say something about its later items -- "#10, #20 is not included",
	// "#10, #20 to follow in a separate PR" -- so an unterminated list keeps
	// only its first item, the one the phrase names directly.
	prRefListEndRE = regexp.MustCompile(`^(?:[ \t]*(?:\r?\n|$)|\.(?:\s|$)|\s+to\s+` + releaseLineName + `)`)
	prRefRE        = regexp.MustCompile(`(?:([\w.-]+/[\w.-]+))?#(\d+)`)
	cherryPickRE   = regexp.MustCompile(`cherry picked from commit ([0-9a-f]{7,40})`)
	releaseTagRE   = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	// The tag whose cut creates release-X.Y. cut-prerelease.yaml gates the
	// freeze on kind == rc and patch == 0, so alpha, beta and patch-line rcs
	// deliberately do not match.
	freezeTagRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.0-rc\.\d+$`)
)

// Caps on the three listings the audit makes. gh truncates at --limit in
// silence, so each one is checked against its cap by truncated() and the
// numbers are set well clear of the current populations rather than close to
// them: at the time of writing, 290 labelled PRs, 217 releases, and at most 54
// PRs on any one release branch. Raising a cap costs nothing when it is not
// reached -- gh stops as soon as the result set is exhausted -- so the reason
// they are bounded at all is to keep a runaway query from paginating forever.
const (
	releaseListCap = 1000
	branchPRCap    = 1000
)

// searchResultCap is the most results GitHub search returns for one query,
// however far it is paginated. gh pr list answers a --label filter through
// search, so the candidate listing stops here whatever --limit asks for, and
// gh's --json output does not warn that it did. The other two listings do not
// go through search and are bounded by their own caps alone.
const searchResultCap = 1000

// candidateCeiling is the listing length at which a label query with --limit
// limit has to be taken as truncated, and what to do about it.
func candidateCeiling(limit int) (int, string) {
	if limit < searchResultCap {
		return limit, "re-run with a higher --limit"
	}
	return searchResultCap, "GitHub search returns at most 1000 results per query; split the label query in cmd/backport-audit, e.g. by merge date"
}

// freezeContractLandedAt is when cutting an rc started creating release-X.Y,
// and with it when backport.yaml stopped resolving its targets from the last
// published stable.
//
// One moment, not two: 53a7fc4dc ("cutting an rc freezes the line into
// release-X.Y") and 6ff5b98d5 ("target the newest existing release line")
// landed on main in the same push, which is what makes a single cutover
// faithful. Before it a release-X.Y branch was created at promote time, so the
// newest branch and the newest published stable named the same line and the
// two rules could not disagree. After it they disagree for the length of every
// freeze window, which is precisely when a backport has to reach the branch.
var freezeContractLandedAt = time.Date(2026, 8, 3, 16, 31, 59, 0, time.UTC)

// The two backport requests .github/workflows/backport.yaml acts on, named by
// the label that carries them. These names are what the audit reports.
const (
	labelCurrent  = "kind/backport"
	labelPrevious = "kind/backport-previous"
)

// backportLabels maps each request to every label spelling that expresses it.
//
// TRANSITIONAL, matching the same allowance in backport.yaml: the labels were
// namespaced under kind/ in 7b71053a0, carrying the bare names as label-sync
// aliases so the rename applied in place. Historical PRs therefore read back as
// `kind/backport` today, but the org-level dosubot still applies the bare names
// on new PRs, so a candidate can carry either. Both are queried and folded onto
// the canonical name above. When dosubot's PR labelling is switched off, the
// legacy spellings here and the ones in backport.yaml can be dropped together.
var backportLabels = []struct {
	canonical string
	spellings []string
}{
	{labelCurrent, []string{labelCurrent, "backport"}},
	{labelPrevious, []string{labelPrevious, "backport-previous"}},
}

// Statuses, most to least in need of attention. outstanding is the prefix of
// this order that makes the audit exit non-zero.
const (
	statusMissing    = "MISSING"
	statusPending    = "pending"
	statusDropped    = "dropped"
	statusBackported = "backported"
	statusInBranch   = "in-branch"
)

var (
	statusOrder = []string{statusMissing, statusPending, statusDropped, statusBackported, statusInBranch}
	outstanding = []string{statusMissing, statusPending, statusDropped}
	headings    = map[string]string{
		statusMissing: "MISSING -- labelled for this line, no trace of it here",
		statusPending: "PENDING -- backport PR open, not merged",
		statusDropped: "DROPPED -- backport PR closed without merging",
	}
)

// The two ways one original can be claimed by more than one live backport PR on
// a branch. Both are outstanding.
const (
	dupOpenTwice      = "open-twice"
	dupOpenAfterMerge = "open-after-merge"
)

var dupDescriptions = map[string]string{
	dupOpenTwice:      "more than one backport PR open",
	dupOpenAfterMerge: "a backport PR still open after another one merged",
}

type config struct {
	remote   string
	limit    int
	fetch    bool
	asJSON   bool
	useColor bool
	branches []string

	red, green, yellow, cyan, dim, bold, reset string
}

// line is a release line, i.e. a major.minor pair with a release-X.Y branch.
type line struct{ major, minor int }

func (l line) branch() string { return fmt.Sprintf("release-%d.%d", l.major, l.minor) }
func (l line) less(o line) bool {
	if l.major != o.major {
		return l.major < o.major
	}
	return l.minor < o.minor
}

// mainPR is a merged-to-main PR carrying at least one backport label.
type mainPR struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	MergedAt    time.Time `json:"mergedAt"`
	MergeCommit struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`

	labels map[string]bool
}

// backportPR is a PR opened against a release branch.
type backportPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	Body        string `json:"body"`
	IsDraft     bool   `json:"isDraft"`
	Comments    []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body string `json:"body"`
	} `json:"comments"`
}

// linkedPR is the trimmed view of a backport PR carried in the output.
type linkedPR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft,omitempty"`
}

func trimPR(b backportPR) linkedPR {
	return linkedPR{Number: b.Number, State: b.State, URL: b.URL, Draft: b.IsDraft}
}

// verdict is one audited candidate. The JSON field names are the tool's
// contract with jq one-liners, so they are spelled explicitly.
type verdict struct {
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Author      string     `json:"author"`
	MergedAt    string     `json:"mergedAt"`
	Label       string     `json:"label"`
	BackportPRs []linkedPR `json:"backport_prs"`
	Status      string     `json:"status"`
	Evidence    string     `json:"evidence"`
	Reason      string     `json:"reason,omitempty"`
}

// unlabelledBackport is an original that backport PRs on the branch claim
// although it is not a candidate for the branch. Labels holds the backport
// requests it does carry, which then resolved to some other line.
type unlabelledBackport struct {
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Labels      []string   `json:"labels"`
	BackportPRs []linkedPR `json:"backport_prs"`
}

// duplicateBackport is an original claimed by more than one live backport PR
// on the branch. Label is the request it was audited under here, empty when it
// is not a candidate for the branch.
type duplicateBackport struct {
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Label       string     `json:"label"`
	Kind        string     `json:"kind"`
	BackportPRs []linkedPR `json:"backport_prs"`
}

// branchReport is everything the audit found on one release branch.
type branchReport struct {
	Candidates []verdict            `json:"candidates"`
	Unlabelled []unlabelledBackport `json:"unlabelled"`
	Duplicates []duplicateBackport  `json:"duplicates"`
}

// clean reports whether the branch passes the gate. Unlabelled backports do not
// enter into it; see the package comment.
func (r *branchReport) clean() bool {
	for _, v := range r.Candidates {
		if slices.Contains(outstanding, v.Status) {
			return false
		}
	}
	return len(r.Duplicates) == 0
}

func main() { os.Exit(run()) }

func run() int {
	cfg, code, done := parseArgs(os.Args[1:])
	if done {
		return code
	}

	if cfg.fetch {
		fmt.Fprintln(os.Stderr, "fetching refs...")
		if _, err := capture("git", "fetch", "--quiet", "--tags", cfg.remote); err != nil {
			fmt.Fprintf(os.Stderr, "git fetch failed: %v\n", err)
			return 2
		}
	}

	if len(cfg.branches) == 0 {
		lines, err := releaseLines(cfg.remote)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		if len(lines) == 0 {
			fmt.Fprintf(os.Stderr, "no release-X.Y branches found on %s\n", cfg.remote)
			return 2
		}
		// Newest first, at most two.
		for i := len(lines) - 1; i >= 0 && len(cfg.branches) < 2; i-- {
			cfg.branches = append(cfg.branches, lines[i].branch())
		}
		fmt.Fprintf(os.Stderr, "auditing %s\n", strings.Join(cfg.branches, ", "))
	}

	for _, b := range cfg.branches {
		if !releaseLineRE.MatchString(b) {
			fmt.Fprintf(os.Stderr, "not a release line: %s (expected release-<major>.<minor>)\n", b)
			return 2
		}
		if !gitOK("rev-parse", "--verify", "--quiet", cfg.remote+"/"+b) {
			fmt.Fprintf(os.Stderr, "no such branch: %s/%s\n", cfg.remote, b)
			return 2
		}
	}

	opened, err := lineOpenDates()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	cands, err := candidates(cfg.limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	fmt.Fprintf(os.Stderr, "%d merged PRs carry a backport label\n", len(cands))

	out := map[string]*branchReport{}
	clean := true
	for _, branch := range cfg.branches {
		rep, err := cfg.audit(branch, cands, opened)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		out[branch] = rep
		// The exit code is the whole point of running this in a gate, so it is
		// computed from the results, not as a side effect of printing them.
		if !rep.clean() {
			clean = false
		}
	}

	// A title is decoration, so a failed lookup costs the titles and nothing
	// else: the verdicts and the exit code are already settled above.
	if err := fillTitles(out); err != nil {
		fmt.Fprintf(os.Stderr, "could not look up titles of unlabelled originals: %v\n", err)
	}

	if cfg.asJSON {
		blob, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		fmt.Println(string(blob))
	} else {
		for _, branch := range cfg.branches {
			cfg.report(branch, out[branch])
		}
		fmt.Println()
	}

	if clean {
		return 0
	}
	return 1
}

func parseArgs(args []string) (*config, int, bool) {
	cfg := &config{remote: "origin", limit: 1000, fetch: true, useColor: true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, bool) {
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "%s requires a value\n", a)
				return "", false
			}
			i++
			return args[i], true
		}
		switch a {
		case "-h", "--help":
			fmt.Print(usageText)
			return nil, 0, true
		case "--remote":
			v, ok := next()
			if !ok {
				return nil, 2, true
			}
			cfg.remote = v
		case "--limit":
			v, ok := next()
			if !ok {
				return nil, 2, true
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "invalid --limit value: %s\n", v)
				return nil, 2, true
			}
			cfg.limit = n
		case "--no-fetch":
			cfg.fetch = false
		case "--json":
			cfg.asJSON = true
		case "--no-color":
			cfg.useColor = false
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "Unknown argument: %s\n", a)
				fmt.Fprint(os.Stderr, usageText)
				return nil, 2, true
			}
			cfg.branches = append(cfg.branches, a)
		}
	}

	// Color is for a human reading a terminal; --json and pipes get none.
	if cfg.asJSON {
		cfg.useColor = false
	}
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		cfg.useColor = false
	}
	cfg.setColors()
	return cfg, 0, false
}

func (cfg *config) setColors() {
	if cfg.useColor {
		cfg.red = "\033[0;31m"
		cfg.green = "\033[0;32m"
		cfg.yellow = "\033[1;33m"
		cfg.cyan = "\033[0;36m"
		cfg.dim = "\033[2m"
		cfg.bold = "\033[1m"
		cfg.reset = "\033[0m"
	}
}

// colorFor is the accent a status is reported in.
func (cfg *config) colorFor(status string) string {
	switch status {
	case statusMissing:
		return cfg.red
	case statusPending:
		return cfg.yellow
	case statusDropped:
		return cfg.cyan
	case statusBackported, statusInBranch:
		return cfg.green
	}
	return ""
}

func capture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// git runs a git command whose failure means the audit cannot be trusted.
func git(args ...string) (string, error) { return capture("git", args...) }

func gitOK(args ...string) bool {
	return exec.Command("git", args...).Run() == nil
}

// truncated turns a saturated `gh ... --limit N` query into an error instead of
// a quietly shorter answer.
//
// Every list this tool makes is a completeness claim -- these are all the
// candidates, this is every backport PR on the branch -- and gh stops at
// --limit without saying so. A saturated query therefore does not make the
// audit partial, it makes its verdicts wrong: the PRs past the cut are
// reported nowhere, and the exit code says clean. That is the same failure
// class the tool exists to catch, so it fails loudly instead.
func truncated(got, limit int, what, remedy string) error {
	if got < limit {
		return nil
	}
	return fmt.Errorf("%s hit its cap of %d results, so the list is truncated and the audit would be unsound: %s", what, limit, remedy)
}

func ghJSON(target any, args ...string) error {
	out, err := capture("gh", args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), target)
}

// releaseLines returns the two-component release branches on the remote,
// oldest first. release-1.6.0-rc.1 and the like are staging branches, not
// maintenance lines, so they are excluded.
func releaseLines(remote string) ([]line, error) {
	out, err := git("branch", "-r", "--list", remote+"/release-*")
	if err != nil {
		return nil, err
	}
	seen := map[line]bool{}
	for raw := range strings.SplitSeq(out, "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(raw), remote+"/")
		if m := releaseLineRE.FindStringSubmatch(name); m != nil {
			major, _ := strconv.Atoi(m[1])
			minor, _ := strconv.Atoi(m[2])
			seen[line{major, minor}] = true
		}
	}
	lines := make([]line, 0, len(seen))
	for l := range seen {
		lines = append(lines, l)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].less(lines[j]) })
	return lines, nil
}

type lineOpen struct {
	line line
	when time.Time
}

// lineOpenDates reports when each release line came into existence as a
// backport target, oldest first, reproducing whichever rule backport.yaml was
// running at the time.
//
// Since freezeContractLandedAt a line exists from the moment its release-X.Y
// branch is pushed, which is what the workflow's branch enumeration sees; see
// freezeDates for how that moment is read exactly. Before it, a line existed
// from the publication of its first non-prerelease release, which is what
// getLatestRelease returned; drafts and prereleases are skipped, as that API
// does.
//
// The two are combined per line rather than by era, because a PR merged after
// the cutover is judged against every branch that exists by then, including
// the ones created long before it. That is sound in both directions: a line
// created under the freeze contract cannot predate the cutover, and a line
// created before it had a branch well before the enumeration rule ever ran, so
// which of the two dates is used for an old line cannot change a verdict.
// release-1.4 is the case that proves it — its branch was cut at v1.4.0-rc.2,
// five days before v1.4.0 published — and both dates sit months on the far
// side of the cutover.
func lineOpenDates() ([]lineOpen, error) {
	var releases []struct {
		TagName      string    `json:"tagName"`
		PublishedAt  time.Time `json:"publishedAt"`
		IsPrerelease bool      `json:"isPrerelease"`
		IsDraft      bool      `json:"isDraft"`
	}
	if err := ghJSON(&releases, "release", "list", "--limit", strconv.Itoa(releaseListCap),
		"--json", "tagName,publishedAt,isPrerelease,isDraft"); err != nil {
		return nil, err
	}
	if err := truncated(len(releases), releaseListCap, "the release listing",
		"raise releaseListCap in cmd/backport-audit"); err != nil {
		return nil, err
	}

	earliest := map[line]time.Time{}
	for _, rel := range releases {
		if rel.IsPrerelease || rel.IsDraft {
			continue
		}
		m := releaseTagRE.FindStringSubmatch(rel.TagName)
		if m == nil {
			continue
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		l := line{major, minor}
		if prev, ok := earliest[l]; !ok || rel.PublishedAt.Before(prev) {
			earliest[l] = rel.PublishedAt
		}
	}

	frozen, err := freezeDates()
	if err != nil {
		return nil, err
	}
	for l, when := range frozen {
		earliest[l] = when
	}

	opened := make([]lineOpen, 0, len(earliest))
	for l, when := range earliest {
		opened = append(opened, lineOpen{l, when})
	}
	sort.Slice(opened, func(i, j int) bool { return opened[i].when.Before(opened[j].when) })
	return opened, nil
}

// freezeDates reports, for each line whose release-X.Y branch was created by
// the freeze, the exact moment it started existing.
//
// The moment is read as the committer date of the commit the first
// vX.Y.0-rc.N tag points at, which is exact rather than approximate. The
// freeze creates the branch AT the tagged commit, in the same job and
// immediately after the tag push, and the stale-tip guard just above that push
// refuses to proceed unless the dispatched commit is still main's tip. So
// nothing merges to main between the tagged commit and the branch appearing:
// every PR merged after that commit merged after the branch existed, and every
// PR merged before it merged before. There is no window for the two to
// disagree, which the rc release's publishedAt could not offer -- that lands
// hours later, once tags.yaml has finished building.
//
// Lines whose first such tag predates the cutover are dropped: their branches
// were not created by the freeze, so the tag says nothing about when they
// appeared, and lineOpenDates falls back to the published-stable rule that was
// actually in force for them.
func freezeDates() (map[line]time.Time, error) {
	out, err := git("for-each-ref", "--format=%(refname:strip=2) %(committerdate:iso-strict)", "refs/tags/v*")
	if err != nil {
		return nil, err
	}
	return parseFreezeDates(out)
}

// parseFreezeDates is the half of freezeDates that does not need a repository:
// `<tag> <iso-8601 date>` per line, in, freeze moments out.
func parseFreezeDates(out string) (map[line]time.Time, error) {
	frozen := map[line]time.Time{}
	for raw := range strings.SplitSeq(out, "\n") {
		name, date, ok := strings.Cut(strings.TrimSpace(raw), " ")
		if !ok {
			continue
		}
		m := freezeTagRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		when, err := time.Parse(time.RFC3339, date)
		if err != nil {
			return nil, fmt.Errorf("tag %s: unparseable date %q: %w", name, date, err)
		}
		if when.Before(freezeContractLandedAt) {
			continue
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		l := line{major, minor}
		if prev, ok := frozen[l]; !ok || when.Before(prev) {
			frozen[l] = when
		}
	}
	return frozen, nil
}

// targetsAt reports the current and previous release lines as of when, which is
// what the `kind/backport` and `kind/backport-previous` labels meant at that
// moment.
//
// The lines that exist at that point are ranked by version, not by when they
// opened, matching the numeric descending sort backport.yaml applies to its
// branch list. The two orders normally coincide, and stop coinciding as soon
// as a freeze overlaps the previous line's stabilisation: cut vX.(Y+1).0-rc.1
// while vX.Y.0 is still unpublished and the newer line opens first in time
// while still being the newer line. Ranking by version is also what keeps
// release-1.10 above release-1.9.
func targetsAt(opened []lineOpen, when time.Time) (current, previous *line) {
	var live []line
	for _, o := range opened {
		if !o.when.After(when) {
			live = append(live, o.line)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[j].less(live[i]) })
	if n := len(live); n > 0 {
		current = &live[0]
		if n > 1 {
			previous = &live[1]
		}
	}
	return current, previous
}

// candidates returns the merged-to-main PRs carrying a backport label, keyed by
// PR number so a PR matched under two spellings of the same request, or under
// both requests, is one entry carrying both canonical names.
func candidates(limit int) (map[int]*mainPR, error) {
	found := map[int]*mainPR{}
	for _, req := range backportLabels {
		for _, spelling := range req.spellings {
			var prs []mainPR
			if err := ghJSON(&prs, "pr", "list", "--base", "main", "--state", "merged",
				"--label", spelling, "--limit", strconv.Itoa(limit),
				"--json", "number,title,url,mergedAt,mergeCommit,labels,author"); err != nil {
				return nil, err
			}
			ceiling, remedy := candidateCeiling(limit)
			if err := truncated(len(prs), ceiling, "the merged-PR query for label "+spelling, remedy); err != nil {
				return nil, err
			}
			for i := range prs {
				pr := prs[i]
				existing, ok := found[pr.Number]
				if !ok {
					pr.labels = map[string]bool{}
					found[pr.Number] = &pr
					existing = &pr
				}
				existing.labels[req.canonical] = true
			}
		}
	}
	return found, nil
}

// branchHistory is everything the audit needs to read out of one release
// branch, collected in a single walk of its history.
type branchHistory struct {
	branch       string
	ref          string
	commits      map[string]bool
	subjects     map[string]bool
	cherryPicked []string
}

func newBranchHistory(remote, branch string) (*branchHistory, error) {
	h := &branchHistory{
		branch:   branch,
		ref:      remote + "/" + branch,
		commits:  map[string]bool{},
		subjects: map[string]bool{},
	}
	// One walk yields all three things: the reachable commits (a set lookup
	// answers exactly what `merge-base --is-ancestor` would, per commit, for
	// free), the subjects, and the -x cherry-pick references in the bodies.
	out, err := git("log", "--format=%x00%H%x01%s%x01%b", h.ref)
	if err != nil {
		return nil, err
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		parts := strings.SplitN(entry, "\x01", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		h.commits[strings.TrimSpace(parts[0])] = true
		h.subjects[strings.TrimSpace(parts[1])] = true
		for _, m := range cherryPickRE.FindAllStringSubmatch(parts[2], -1) {
			h.cherryPicked = append(h.cherryPicked, m[1])
		}
	}
	return h, nil
}

func (h *branchHistory) hasSubject(subject string) bool {
	return h.subjects[strings.TrimSpace(subject)]
}

func (h *branchHistory) contains(commit string) bool {
	return commit != "" && h.commits[commit]
}

// hasCherryPickOf reports whether the branch records an -x cherry-pick of any
// of the given commits. Abbreviations on either side are compared by prefix.
func (h *branchHistory) hasCherryPickOf(oids []string) bool {
	for _, oid := range oids {
		for _, picked := range h.cherryPicked {
			if strings.HasPrefix(oid, picked) || strings.HasPrefix(picked, oid) {
				return true
			}
		}
	}
	return false
}

// backportPRsFor indexes every PR on branch by the main PRs it claims to
// backport; see claimedOrigins.
func backportPRsFor(branch string) (map[int][]backportPR, error) {
	var prs []backportPR
	if err := ghJSON(&prs, "pr", "list", "--base", branch, "--state", "all", "--limit", strconv.Itoa(branchPRCap),
		"--json", "number,title,url,state,headRefName,body,isDraft,comments"); err != nil {
		return nil, err
	}
	if err := truncated(len(prs), branchPRCap, "the PR listing for "+branch,
		"raise branchPRCap in cmd/backport-audit"); err != nil {
		return nil, err
	}
	return indexByOrigin(prs, branch), nil
}

func indexByOrigin(prs []backportPR, branch string) map[int][]backportPR {
	index := map[int][]backportPR{}
	for _, pr := range prs {
		for _, n := range claimedOrigins(pr, branch) {
			index[n] = append(index[n], pr)
		}
	}
	return index
}

// claimedOrigins reads the main PRs a PR on branch says it backports: the one
// in the bot's head branch name, and every one in the body's "Backport of"
// phrases, which is what a hand-written backport carries. Hand backports often
// reuse the bot's head naming, so the two usually agree and are counted once.
func claimedOrigins(pr backportPR, branch string) []int {
	var origins []int
	if m := backportHeadRE.FindStringSubmatch(pr.HeadRefName); m != nil && m[2] == branch {
		n, _ := strconv.Atoi(m[1])
		origins = append(origins, n)
	}
	for _, n := range bodyOrigins(pr.Body, repoOf(pr.URL)) {
		if !slices.Contains(origins, n) {
			origins = append(origins, n)
		}
	}
	return origins
}

// bodyOrigins returns the PR numbers named in a body's "Backport of" phrases, in
// order of appearance and without repeats.
//
// A reference qualified with any repository but repo is skipped rather than
// read as a local number: "other/repo#20" linked as #20 would let an unrelated
// backport vouch for whatever local PR happens to carry that number, and a
// merged one would turn its MISSING verdict into backported.
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
				if !slices.Contains(origins, n) {
					origins = append(origins, n)
				}
			}
		}
	}
	return origins
}

// repoOf is the owner/name of the repository a PR URL, as gh prints it, points
// into.
func repoOf(prURL string) string {
	i := strings.LastIndex(prURL, "/pull/")
	if i < 0 {
		return upstreamRepo
	}
	parts := strings.Split(prURL[:i], "/")
	if len(parts) < 2 {
		return upstreamRepo
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// lastHumanComment is the last comment on a PR not written by the backport bot.
func lastHumanComment(pr backportPR) string {
	for i := len(pr.Comments) - 1; i >= 0; i-- {
		login := pr.Comments[i].Author.Login
		if login == "github-actions" || login == "github-actions[bot]" {
			continue
		}
		text := strings.Join(strings.Fields(pr.Comments[i].Body), " ")
		if text == "" {
			continue
		}
		if len(text) > 200 {
			text = text[:200]
		}
		return login + ": " + text
	}
	return "no reason recorded"
}

// prCommitSubjects returns the subjects of the commits a PR contributed, read
// locally. main takes PRs as merge commits, so the branch side is ^1..^2. A
// squashed or rebased merge has no second parent; then the commit itself is the
// change.
func prCommitSubjects(mergeCommit string) []string {
	rng, ok := prCommitRange(mergeCommit)
	if !ok {
		return nil
	}
	out, err := git("log", "--format=%s", rng)
	if err != nil {
		return nil
	}
	var subjects []string
	for s := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(s) != "" {
			subjects = append(subjects, s)
		}
	}
	return subjects
}

func prCommitOids(mergeCommit string) []string {
	rng, ok := prCommitRange(mergeCommit)
	if !ok {
		return nil
	}
	out, err := git("log", "--format=%H", rng)
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

func prCommitRange(mergeCommit string) (string, bool) {
	if mergeCommit == "" || !gitOK("cat-file", "-e", mergeCommit+"^{commit}") {
		return "", false
	}
	if gitOK("rev-parse", "--verify", "--quiet", mergeCommit+"^2") {
		return mergeCommit + "^1.." + mergeCommit + "^2", true
	}
	return mergeCommit + "^.." + mergeCommit, true
}

func (cfg *config) audit(branch string, cands map[int]*mainPR, opened []lineOpen) (*branchReport, error) {
	hist, err := newBranchHistory(cfg.remote, branch)
	if err != nil {
		return nil, err
	}
	backports, err := backportPRsFor(branch)
	if err != nil {
		return nil, err
	}

	m := releaseLineRE.FindStringSubmatch(branch)
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	want := line{major, minor}

	numbers := make([]int, 0, len(cands))
	for n := range cands {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	results := []verdict{}
	audited := map[int]string{}
	for _, n := range numbers {
		pr := cands[n]
		current, previous := targetsAt(opened, pr.MergedAt)
		label := ""
		switch {
		case pr.labels[labelCurrent] && current != nil && *current == want:
			label = labelCurrent
		case pr.labels[labelPrevious] && previous != nil && *previous == want:
			label = labelPrevious
		default:
			continue
		}
		audited[n] = label
		results = append(results, classify(pr, label, hist, backports[pr.Number]))
	}
	unlabelled, duplicates := crossCheck(backports, audited, cands)
	return &branchReport{Candidates: results, Unlabelled: unlabelled, Duplicates: duplicates}, nil
}

// crossCheck reads the backport PRs on a branch from the side of the originals
// they claim, which is the only side an unlabelled backport can be seen from.
// audited maps each candidate for the branch to the request it was audited
// under; cands supplies the title and labels of any original that carries a
// backport label at all.
func crossCheck(index map[int][]backportPR, audited map[int]string, cands map[int]*mainPR) ([]unlabelledBackport, []duplicateBackport) {
	numbers := make([]int, 0, len(index))
	for n := range index {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	unlabelled := []unlabelledBackport{}
	duplicates := []duplicateBackport{}
	for _, n := range numbers {
		claims := index[n]
		linked := make([]linkedPR, 0, len(claims))
		for _, b := range claims {
			linked = append(linked, trimPR(b))
		}
		sort.Slice(linked, func(i, j int) bool { return linked[i].Number < linked[j].Number })

		// The URL is derived rather than looked up, so it is there even when the
		// title lookup fails; GitHub redirects /pull/N to the issue when N is one.
		title, url, labels := "", originURL(claims[0].URL, n), []string{}
		if pr, ok := cands[n]; ok {
			title, url = pr.Title, pr.URL
			for _, req := range backportLabels {
				if pr.labels[req.canonical] {
					labels = append(labels, req.canonical)
				}
			}
		}

		label, isCandidate := audited[n]
		if !isCandidate {
			unlabelled = append(unlabelled, unlabelledBackport{
				Number: n, Title: title, URL: url, Labels: labels, BackportPRs: linked,
			})
		}
		if kind := duplicateKind(claims); kind != "" {
			duplicates = append(duplicates, duplicateBackport{
				Number: n, Title: title, URL: url, Label: label, Kind: kind, BackportPRs: linked,
			})
		}
	}
	return unlabelled, duplicates
}

// duplicateKind classifies the backport PRs claiming one original, returning
// "" unless more than one of them is live. A closed PR next to an open one is
// the normal shape of a conflicting bot backport redone by hand, and two merged
// ones are history the branch already carries, so neither is flagged.
func duplicateKind(claims []backportPR) string {
	open, merged := 0, 0
	for _, b := range claims {
		switch b.State {
		case "OPEN":
			open++
		case "MERGED":
			merged++
		}
	}
	switch {
	case open > 0 && merged > 0:
		return dupOpenAfterMerge
	case open > 1:
		return dupOpenTwice
	}
	return ""
}

// originURL is the URL of PR n in the repository that sibling, the URL of
// another PR, belongs to.
func originURL(sibling string, n int) string {
	i := strings.LastIndex(sibling, "/pull/")
	if i < 0 {
		return ""
	}
	return sibling[:i] + "/pull/" + strconv.Itoa(n)
}

// fillTitles sets the title of every unlabelled or duplicate original that no
// listing carried, with one request covering every branch.
func fillTitles(out map[string]*branchReport) error {
	var missing []int
	for _, rep := range out {
		for _, u := range rep.Unlabelled {
			if u.Title == "" && !slices.Contains(missing, u.Number) {
				missing = append(missing, u.Number)
			}
		}
		for _, d := range rep.Duplicates {
			if d.Title == "" && !slices.Contains(missing, d.Number) {
				missing = append(missing, d.Number)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Ints(missing)
	titles, err := titlesOf(missing)
	for _, rep := range out {
		for i := range rep.Unlabelled {
			if t, ok := titles[rep.Unlabelled[i].Number]; ok && rep.Unlabelled[i].Title == "" {
				rep.Unlabelled[i].Title = t
			}
		}
		for i := range rep.Duplicates {
			if t, ok := titles[rep.Duplicates[i].Number]; ok && rep.Duplicates[i].Title == "" {
				rep.Duplicates[i].Title = t
			}
		}
	}
	return err
}

// titlesOf fetches the titles of the given issue or PR numbers in a single
// GraphQL request, one alias per number, rather than one gh call each.
//
// gh exits non-zero when any one number fails to resolve -- a reference to a
// deleted PR, say -- but still prints the partial response, so stdout is read
// whatever the exit status and every title that did resolve is kept.
func titlesOf(numbers []int) (map[int]string, error) {
	var q strings.Builder
	q.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
	for _, n := range numbers {
		fmt.Fprintf(&q, "n%d:issueOrPullRequest(number:%d){...on PullRequest{title} ...on Issue{title}}", n, n)
	}
	q.WriteString("}}")

	stdout, runErr := exec.Command("gh", "api", "graphql",
		"-F", "owner={owner}", "-F", "name={repo}", "-f", "query="+q.String()).Output()
	if ee, ok := runErr.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		runErr = fmt.Errorf("gh api graphql: %w: %s", runErr, strings.TrimSpace(string(ee.Stderr)))
	}
	var resp struct {
		Data struct {
			Repository map[string]*struct {
				Title string `json:"title"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		if runErr != nil {
			return nil, runErr
		}
		return nil, err
	}
	titles := map[int]string{}
	for alias, node := range resp.Data.Repository {
		if n, err := strconv.Atoi(strings.TrimPrefix(alias, "n")); err == nil && node != nil {
			titles[n] = node.Title
		}
	}
	if runErr != nil && len(titles) < len(numbers) {
		return titles, fmt.Errorf("%d of %d did not resolve: %w", len(numbers)-len(titles), len(numbers), runErr)
	}
	return titles, nil
}

func classify(pr *mainPR, label string, hist *branchHistory, linked []backportPR) verdict {
	v := verdict{
		Number:      pr.Number,
		Title:       pr.Title,
		URL:         pr.URL,
		Author:      pr.Author.Login,
		MergedAt:    pr.MergedAt.Format(time.RFC3339),
		Label:       label,
		BackportPRs: []linkedPR{},
	}
	for _, b := range linked {
		v.BackportPRs = append(v.BackportPRs, trimPR(b))
	}

	mergeCommit := pr.MergeCommit.Oid
	if hist.contains(mergeCommit) {
		v.Status, v.Evidence = statusInBranch, "merged before branch cut"
		return v
	}

	for _, b := range linked {
		if b.State == "MERGED" {
			v.Status = statusBackported
			v.Evidence = fmt.Sprintf("backport PR #%d merged", b.Number)
			return v
		}
	}

	if hist.hasSubject(fmt.Sprintf("[Backport %s] %s", hist.branch, pr.Title)) {
		v.Status, v.Evidence = statusBackported, "bot merge commit on branch"
		return v
	}
	for _, s := range prCommitSubjects(mergeCommit) {
		if hist.hasSubject(s) {
			v.Status = statusBackported
			v.Evidence = fmt.Sprintf("identical commit subject on branch: %q", s)
			return v
		}
	}
	if hist.hasCherryPickOf(prCommitOids(mergeCommit)) {
		v.Status, v.Evidence = statusBackported, "-x cherry-pick reference"
		return v
	}

	for _, b := range linked {
		if b.State == "OPEN" {
			draft := ""
			if b.IsDraft {
				draft = " (draft/conflict)"
			}
			v.Status = statusPending
			v.Evidence = fmt.Sprintf("backport PR #%d open%s", b.Number, draft)
			return v
		}
	}
	for _, b := range linked {
		if b.State == "CLOSED" {
			// A closed backport is usually a deliberate "not needed on this
			// line". Carry the reason someone left, so the next release does
			// not re-open the same investigation from scratch.
			v.Status = statusDropped
			v.Evidence = fmt.Sprintf("backport PR #%d closed unmerged", b.Number)
			v.Reason = lastHumanComment(b)
			return v
		}
	}

	v.Status, v.Evidence = statusMissing, "no backport PR, nothing on branch"
	return v
}

func (cfg *config) report(branch string, rep *branchReport) {
	results := rep.Candidates
	byStatus := map[string][]verdict{}
	for _, r := range results {
		byStatus[r.Status] = append(byStatus[r.Status], r)
	}

	var counts []string
	for _, s := range statusOrder {
		if n := len(byStatus[s]); n > 0 {
			counts = append(counts, fmt.Sprintf("%s%s=%d%s", cfg.colorFor(s), s, n, cfg.reset))
		}
	}
	summary := strings.Join(counts, " ")
	if summary == "" {
		summary = "no candidates"
	}
	var others []string
	if n := len(rep.Duplicates); n > 0 {
		others = append(others, fmt.Sprintf("%sDUPLICATE=%d%s", cfg.red, n, cfg.reset))
	}
	if n := len(rep.Unlabelled); n > 0 {
		others = append(others, fmt.Sprintf("unlabelled=%d", n))
	}
	if len(others) > 0 {
		summary += " | " + strings.Join(others, " ")
	}
	fmt.Printf("\n%s=== %s ===%s %d candidate PRs: %s\n",
		cfg.bold, branch, cfg.reset, len(results), summary)

	for _, status := range outstanding {
		rows := byStatus[status]
		if len(rows) == 0 {
			continue
		}
		accent := cfg.colorFor(status)
		fmt.Printf("\n  %s%s%s (%d):\n", accent, headings[status], cfg.reset, len(rows))
		for _, r := range rows {
			fmt.Printf("    %s%s%s\n", accent, r.URL, cfg.reset)
			fmt.Printf("      %s#%d%s %s\n", cfg.bold, r.Number, cfg.reset, r.Title)
			fmt.Printf("      %slabel=%s author=%s merged=%s -- %s%s\n",
				cfg.dim, r.Label, r.Author, r.MergedAt[:10], r.Evidence, cfg.reset)
			if r.Reason != "" {
				fmt.Printf("      %sreason: %s%s\n", cfg.dim, r.Reason, cfg.reset)
			}
		}
	}

	if len(rep.Duplicates) > 0 {
		fmt.Printf("\n  %sDUPLICATE -- one original, more than one live backport PR%s (%d):\n",
			cfg.red, cfg.reset, len(rep.Duplicates))
		for _, d := range rep.Duplicates {
			label := d.Label
			if label == "" {
				label = "none for this line"
			}
			cfg.printOrigin(cfg.red, d.URL, d.Number, d.Title)
			fmt.Printf("      %slabel=%s -- %s%s\n", cfg.dim, label, dupDescriptions[d.Kind], cfg.reset)
			cfg.printBackportPRs(d.BackportPRs)
		}
	}

	if rep.clean() {
		fmt.Printf("  %snothing outstanding: every candidate is on this branch%s\n", cfg.green, cfg.reset)
	}

	if len(rep.Unlabelled) > 0 {
		fmt.Printf("\n  %sUNLABELLED -- backport PRs here for originals not labelled for this line%s (%d, informational):\n",
			cfg.bold, cfg.reset, len(rep.Unlabelled))
		for _, u := range rep.Unlabelled {
			cfg.printOrigin("", u.URL, u.Number, u.Title)
			fmt.Printf("      %s%s%s\n", cfg.dim, claimState(u.BackportPRs), cfg.reset)
			if len(u.Labels) > 0 {
				fmt.Printf("      %slabels=%s, which did not target this line at merge%s\n",
					cfg.dim, strings.Join(u.Labels, ","), cfg.reset)
			}
			cfg.printBackportPRs(u.BackportPRs)
		}
	}
}

// claimState says what the backport PRs claiming an original have done on the
// branch, so an open claim does not read as a delivered one.
func claimState(prs []linkedPR) string {
	state := "claimed here, closed unmerged"
	for _, b := range prs {
		switch b.State {
		case "MERGED":
			return "backported here"
		case "OPEN":
			state = "claimed here, not merged"
		}
	}
	return state
}

func (cfg *config) printOrigin(accent, url string, number int, title string) {
	fmt.Printf("    %s%s%s\n", accent, url, cfg.reset)
	fmt.Printf("      %s#%d%s %s\n", cfg.bold, number, cfg.reset, title)
}

func (cfg *config) printBackportPRs(prs []linkedPR) {
	for _, b := range prs {
		draft := ""
		if b.Draft {
			draft = " (draft)"
		}
		fmt.Printf("      %sbackport #%d %s%s %s%s\n", cfg.dim, b.Number, b.State, draft, b.URL, cfg.reset)
	}
}
