#!/usr/bin/env bats

# Contract for the milestone release workflow. Like issue-triage-contract.bats,
# these tests pin executable workflow lines, not prose, so the JS block is
# filtered on `//` as well as YAML on `#`: a guard demoted to a comment must
# never satisfy a pin.
#
# No PR lane runs this workflow. It fires on closed issues, published releases,
# a daily cron and workflow_dispatch, so its first real run already writes to
# the live milestones. The pins below are the lines that bound what it can do
# there: it never cuts a tag or starts another workflow, it touches only vX.Y.Z
# milestones, it opens at most one tracking issue per milestone, an empty
# milestone never counts as done, it closes a milestone only on a published
# stable release, and a human dispatch is dry unless asked otherwise.
#
# Behavioural coverage of the script would need a JS runtime, which the unit
# lane does not guarantee; see issue-triage-contract.bats for the same ceiling.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WF="$REPO_ROOT/.github/workflows/milestone-release.yaml"
STALE="$REPO_ROOT/.github/workflows/stale.yaml"
LABELS="$REPO_ROOT/.github/labels.yml"
DOC="$REPO_ROOT/docs/release.md"

# Workflow lines with YAML and JS comments dropped. grep exits 1 on an empty
# selection, which is not an error here.
code() {
  grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*//' "$WF" || true
}

@test "the workflow under contract exists" {
  [ -f "$WF" ]
}

@test "it never cuts a tag, writes a ref or starts another workflow" {
  # The first rc freezes release-X.Y, and cut-prerelease.yaml keeps that a
  # maintainer's dispatch. This workflow only opens and closes issues.
  for forbidden in createWorkflowDispatch createRef updateRef deleteRef 'git push' 'actions: write' 'contents: write'; do
    if code | grep -qF "$forbidden"; then
      echo "FAIL: milestone-release.yaml must not use '$forbidden'"
      false
    fi
  done
}

@test "the token holds contents: read and issues: write and nothing else" {
  perms="$(code | awk '/^    permissions:$/ { p = 1; next } p && /^      [a-z-]+:/ { print $1 $2 } p && !/^      / { p = 0 }')"
  [ "$perms" = "$(printf 'contents:read\nissues:write')" ] || {
    echo "FAIL: job permissions are:"; echo "$perms"; false
  }
  code | grep -qE '^permissions:$'
  code | grep -A1 -E '^permissions:$' | grep -qE '^  contents: read$'
}

@test "the github-script action is pinned by commit SHA" {
  code | grep -qE 'uses: actions/github-script@[0-9a-f]{40} # v[0-9]+'
  if code | grep -E 'uses: ' | grep -vqE '@[0-9a-f]{40}'; then
    echo "FAIL: every action must be pinned by SHA"; false
  fi
}

@test "only milestones titled exactly vX.Y.Z are considered" {
  code | grep -qF 'const VERSION = /^v(\d+)\.(\d+)\.(\d+)$/;'
  code | grep -qF 'const m = ms.title.match(VERSION);'
  code | grep -A1 -F 'if (!m) {' | grep -q 'not a vX.Y.Z milestone'
}

@test "one tracking issue per milestone: an existing one stops the run before create" {
  code | grep -qF 'i.title === title &&'
  code | grep -qF 'const work = items.filter((i) => i !== tracking);'
  tracking_line="$(code | grep -nF 'if (tracking) {' | cut -d: -f1)"
  create_line="$(code | grep -nF 'github.rest.issues.create({' | cut -d: -f1)"
  [ -n "$tracking_line" ] && [ -n "$create_line" ]
  [ "$tracking_line" -lt "$create_line" ]
  code | sed -n "${tracking_line},\$p" | sed -n '2,3p' | grep -qF 'continue;'
}

@test "an empty milestone is not done" {
  code | grep -qF 'const complete = open.length === 0 && done.length > 0;'
}

@test "the milestone closes only on a published stable release" {
  code | grep -qF 'getReleaseByTag({ owner, repo, tag: version })'
  code | grep -qF 'if (release && !release.prerelease) {'
}

@test "a dispatch is dry by default and only the literal false writes" {
  code | grep -A4 -E '^      dry_run:$' | grep -qE '^        default: true$'
  code | grep -qF "!['issues', 'release', 'schedule'].includes(context.eventName) &&"
  code | grep -qF "String(context.payload.inputs?.dry_run ?? 'true') !== 'false';"
}

@test "every label the tracking issue gets exists, and triage/accepted keeps it from going stale" {
  for label in area/release triage/accepted; do
    code | grep -qF "'$label'"
    grep -qE "^- name: $label$" "$LABELS"
  done
  grep -qF 'triage/accepted' "$STALE"
}

@test "the docs anchor the issue links to exists" {
  code | grep -qF 'docs/release.md#milestones'
  grep -qE '^## Milestones$' "$DOC"
}
