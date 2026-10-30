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
	"strings"
	"time"
)

// policyPage is the policy the batch applies. The block in it that names when
// each line started under the policy is read from the page itself, on the ref
// the run pins, so the page and the batch cannot drift apart.
const policyPage = "docs/release/backports.md"

const policyFence = "```backport-policy"

var policyLineRE = regexp.MustCompile(`^(release-\d+\.\d+|` + batchOnlyKey + `):[ \t]*(\d{4}-\d{2}-\d{2})[ \t]*$`)

// batchOnlyKey names the day from which the batch is the only way onto a
// line, so that from then on only its picks show what a line carries.
const batchOnlyKey = "batch-only"

// The rule 2 types and the scopes that never go into a patch release, as the
// policy page lists them; policy_test.go holds the page to these.
var (
	takenTypes     = []string{"fix", "security"}
	excludedScopes = []string{"deps"}
)

// starts maps a release line to the day it started under the policy. A line
// that is not listed started at its fork point from main.
type starts map[string]time.Time

// policy is what the backport-policy block says: when each line started, and
// the batch-only day.
type policy struct {
	starts    starts
	batchOnly time.Time
}

// parsePolicy reads the one backport-policy block of the page: a line per
// release line, `release-X.Y: YYYY-MM-DD`, exactly one `batch-only:
// YYYY-MM-DD`, and comments starting with #.
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
	out := &policy{starts: starts{}}
	seen := map[string]bool{}
	for _, t := range blocks[0] {
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		m := policyLineRE.FindStringSubmatch(t)
		if m == nil {
			return nil, fmt.Errorf("%s: %q in the %s block is not `release-X.Y: YYYY-MM-DD` or `%s: YYYY-MM-DD`", policyPage, t, policyFence, batchOnlyKey)
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

// readPolicy reads the block from the policy page at commit.
func readPolicy(commit string) (*policy, error) {
	page, err := git("show", "--end-of-options", commit+":"+policyPage)
	if err != nil {
		return nil, fmt.Errorf("reading the policy at %.12s: %w", commit, err)
	}
	return parsePolicy(page)
}
