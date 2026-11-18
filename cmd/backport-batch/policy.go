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
	"strconv"
	"strings"
	"time"
)

// policyPage is the policy the batch applies. The block in it that names when
// each line started under the policy is read from the page itself, on the ref
// the run pins, so the page and the batch cannot drift apart.
const policyPage = "docs/release/backports.md"

const policyFence = "```backport-policy"

var (
	policyLineRE     = regexp.MustCompile(`^(release-\d+\.\d+|` + batchOnlyKey + `):[ \t]*(\d{4}-\d{2}-\d{2})[ \t]*$`)
	policyReleasedRE = regexp.MustCompile(`^` + releasedKey + `:[ \t]*(\d+)[ \t]*$`)
)

const (
	// batchOnlyKey names the day from which the batch is the only way onto a
	// line, so that from then on only its picks show what a line carries.
	batchOnlyKey = "batch-only"
	// releasedKey names how many of the lines that have shipped a stable
	// release the batch runs for, the newest of them; defaultReleased when
	// the block does not say.
	releasedKey     = "released-lines"
	defaultReleased = 2
)

// The rule 2 types and the scopes that never go into a patch release, as the
// policy page lists them; policy_test.go holds the page to these.
var (
	takenTypes     = []string{"fix", "security"}
	excludedScopes = []string{"deps"}
)

// starts maps a release line to the day it started under the policy. A line
// that is not listed started at its fork point from main.
type starts map[string]time.Time

// policy is what the backport-policy block says: when each line started, the
// batch-only day, and how many released lines the batch runs for.
type policy struct {
	starts    starts
	batchOnly time.Time
	released  int
}

// parsePolicy reads the one backport-policy block of the page: a line per
// release line, `release-X.Y: YYYY-MM-DD`, exactly one `batch-only:
// YYYY-MM-DD`, at most one `released-lines: N`, and comments starting with #.
func parsePolicy(page string) (*policy, error) {
	var blocks [][]string
	var cur []string
	in := false
	for _, ln := range strings.Split(strings.ReplaceAll(page, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case !in && t == policyFence:
			in, cur = true, nil
		case in && t == "```":
			in = false
			blocks = append(blocks, cur)
		case in:
			cur = append(cur, t)
		}
	}
	if in {
		return nil, fmt.Errorf("%s: the %s block is not closed", policyPage, policyFence)
	}
	if len(blocks) != 1 {
		return nil, fmt.Errorf("%s holds %d %s blocks, not one", policyPage, len(blocks), policyFence)
	}
	out := &policy{starts: starts{}, released: defaultReleased}
	seen := map[string]bool{}
	for _, t := range blocks[0] {
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if m := policyReleasedRE.FindStringSubmatch(t); m != nil {
			n, err := strconv.Atoi(m[1])
			switch {
			case err != nil || n < 1:
				return nil, fmt.Errorf("%s: %s is %q, not a number of lines", policyPage, releasedKey, m[1])
			case seen[releasedKey]:
				return nil, fmt.Errorf("%s: %s is listed twice", policyPage, releasedKey)
			}
			seen[releasedKey], out.released = true, n
			continue
		}
		m := policyLineRE.FindStringSubmatch(t)
		if m == nil {
			return nil, fmt.Errorf("%s: %q in the %s block is not `release-X.Y: YYYY-MM-DD`, `%s: YYYY-MM-DD` or `%s: N`", policyPage, t, policyFence, batchOnlyKey, releasedKey)
		}
		day, err := time.Parse(time.DateOnly, m[2])
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a date: %w", policyPage, m[2], err)
		}
		if seen[m[1]] {
			return nil, fmt.Errorf("%s: %s is listed twice", policyPage, m[1])
		}
		seen[m[1]] = true
		if m[1] == batchOnlyKey {
			out.batchOnly = day
			continue
		}
		if _, err := parseLine(m[1]); err != nil {
			return nil, fmt.Errorf("%s: %w", policyPage, err)
		}
		out.starts[m[1]] = day
	}
	if !seen[batchOnlyKey] {
		return nil, fmt.Errorf("%s: the %s block gives no %s day", policyPage, policyFence, batchOnlyKey)
	}
	return out, nil
}

func readPolicy(commit string) (*policy, error) {
	page, err := git("show", "--end-of-options", commit+":"+policyPage)
	if err != nil {
		return nil, fmt.Errorf("reading the policy at %.12s: %w", commit, err)
	}
	return parsePolicy(page)
}
