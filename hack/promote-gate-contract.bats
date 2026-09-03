#!/usr/bin/env bats

# Contract for the rc-E2E promotion gate and the release-PR opt-in path.
# These tests intentionally pin executable/structural workflow lines, not prose:
# a commented-out gate, label, or body note must never satisfy the contract.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PROMOTE="$REPO_ROOT/.github/workflows/promote-rc.yaml"
PULL_REQUESTS="$REPO_ROOT/.github/workflows/pull-requests.yaml"
TAGS="$REPO_ROOT/.github/workflows/tags.yaml"
FINALIZE="$REPO_ROOT/.github/workflows/pull-requests-release.yaml"

job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
}

job_header() {
  job_block "$1" "$2" | awk '
    /^    steps:$/ { exit }
    { print }'
}

input_block() {
  awk -v input="      $1:" '
    $0 == input { inside = 1; print; next }
    inside && /^      [a-zA-Z0-9_-]+:$/ { exit }
    inside { print }' "$2"
}

# Comment-stripping filter for the pins below. POSIX `grep` only: the unit-test
# runner has no ripgrep, and a missing filter used to be swallowed by `|| true`,
# silently reducing every pin to "0 matches" instead of failing on the real
# cause. grep exits 1 when nothing is selected (legitimate for an empty block)
# and 2 on an actual error. Do not read the `rc` check below as what catches
# that: code_lines is never the last stage of a pipeline in this file, so
# ordinary (non-pipefail) pipe semantics discard whatever it itself returns,
# and `hack/cozytest.sh`'s translator appends `return 0` to every line that is
# exactly `}`, code_lines' closing brace included, making its own exit status
# even less trustworthy as a signal. What catches a real grep error is the
# OUTPUT, and only for one class of pin: the error empties code_lines' stream,
# so a pin that requires something to be present in that stream fails on it. A
# pin demanding an absence stays blind, because an exact count of zero is what
# an emptied stream produces anyway, and what the pin was written to assert.
# The `[ -n "$block" ]` guard at the top of a test is not that check: it runs
# on the raw awk extraction, upstream of code_lines, and cannot see it fail.
code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

@test "green rc E2E gate is in parse before promote and gates its DAG edge" {
  parse_block="$(job_block parse "$PROMOTE")"
  promote_block="$(job_block promote "$PROMOTE")"
  [ -n "$parse_block" ]
  [ -n "$promote_block" ]

  count="$(printf '%s\n' "$parse_block" | code_lines | grep -cF '      - name: Verify green RC E2E' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$parse_block" | code_lines | grep -cF '      actions: read' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$promote_block" | code_lines | grep -cF '    needs: parse' || true)"
  [ "${count:-0}" -eq 1 ]

  parse_line="$(code_lines < "$PROMOTE" | grep -n '^  parse:$' | awk -F: 'NR == 1 { print $1 }')"
  promote_line="$(code_lines < "$PROMOTE" | grep -n '^  promote:$' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$parse_line" ]
  [ -n "$promote_line" ]
  [ "$parse_line" -lt "$promote_line" ]
}

@test "skip_e2e_gate is a boolean emergency override defaulting false" {
  block="$(input_block skip_e2e_gate "$PROMOTE")"
  [ -n "$block" ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF '      skip_e2e_gate:' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF '        default: false' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF '        type: boolean' || true)"
  [ "${count:-0}" -eq 1 ]

  parse_block="$(job_block parse "$PROMOTE")"
  count="$(printf '%s\n' "$parse_block" | code_lines | grep -cF '        if: ${{ !inputs.skip_e2e_gate }}' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$parse_block" | code_lines | grep -cF '        if: ${{ inputs.skip_e2e_gate }}' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "promote PR keeps release label and does not auto-apply full-e2e" {
  block="$(job_block open-pr "$PROMOTE")"
  [ -n "$block" ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF -- '--body "$BODY" --label release' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF -- '--label full-e2e' || true)"
  [ "${count:-0}" -eq 0 ]
}

@test "promote PR body carries verified and bypassed E2E status" {
  promote_block="$(job_block promote "$PROMOTE")"
  open_block="$(job_block open-pr "$PROMOTE")"

  count="$(printf '%s\n' "$promote_block" | code_lines | grep -cF '      e2e_verification: ${{ needs.parse.outputs.e2e_verification }}' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$open_block" | code_lines | grep -cF '          E2E_VERIFICATION: ${{ needs.promote.outputs.e2e_verification }}' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$open_block" | code_lines | grep -cF '            E2E_NOTE="✅ RC full e2e was verified' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$open_block" | code_lines | grep -cF '            E2E_NOTE="⚠️ **RC e2e gate bypassed**' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$open_block" | code_lines | grep -cF '          ${E2E_NOTE}' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "release PR E2E is a working manual full-e2e label opt-in" {
  count="$(code_lines < "$PULL_REQUESTS" | grep -cF '    types: [opened, synchronize, reopened, labeled]' || true)"
  [ "${count:-0}" -eq 1 ]

  plan_header="$(job_header plan "$PULL_REQUESTS")"
  count="$(printf '%s\n' "$plan_header" | code_lines | grep -cF "    if: github.event.action != 'labeled' || github.event.label.name == 'full-e2e'" || true)"
  [ "${count:-0}" -eq 1 ]

  resolve_header="$(job_header resolve_assets "$PULL_REQUESTS")"
  count="$(printf '%s\n' "$resolve_header" | code_lines | grep -cF "github.event.label.name == 'full-e2e'" || true)"
  [ "${count:-0}" -eq 1 ]

  e2e_header="$(job_header e2e "$PULL_REQUESTS")"
  count="$(printf '%s\n' "$e2e_header" | code_lines | grep -cF "needs.resolve_assets.result == 'success'" || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$e2e_header" | code_lines | grep -cF "&& contains(github.event.pull_request.labels.*.name, 'full-e2e')" || true)"
  [ "${count:-0}" -eq 1 ]
}

# ── promote-time website docs contract ──────────────────────────────────────
# The website "update managed apps reference" PR is opened at PROMOTE time from
# the staging branch (via FETCH_REF) instead of only at tag time. These pins are
# executable/structural (code_lines strips comments) so a commented-out step,
# guard, or body line can never satisfy them. The one deliberate exception is the
# tags.yaml backstop-comment pin, which asserts a COMMENT is present.

@test "promote-rc website-docs job depends on parse and promote" {
  block="$(job_block website-docs "$PROMOTE")"
  [ -n "$block" ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF '    needs: [parse, promote]' || true)"
  [ "${count:-0}" -eq 1 ]

  # It must run after the staging branch exists — website-docs is ordered after
  # promote in the file, and promote is what pushes release-X.Y.Z.
  website_line="$(code_lines < "$PROMOTE" | grep -n '^  website-docs:$' | awk -F: 'NR == 1 { print $1 }')"
  promote_line="$(code_lines < "$PROMOTE" | grep -n '^  promote:$' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$website_line" ] && [ -n "$promote_line" ]
  [ "$promote_line" -lt "$website_line" ]
}

@test "website-docs fetches docs from the staging branch via FETCH_REF, not the stable tag" {
  block="$(job_block website-docs "$PROMOTE")"
  [ -n "$block" ]

  # The load-bearing invocation shape: update-all pinned to the staging branch.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'make update-all RELEASE_TAG="$TAG" FETCH_REF="$SRC_REF"' || true)"
  [ "${count:-0}" -eq 1 ]

  # SRC_REF must be the stable staging branch (which exists at promote time), never
  # the stable tag (which finalize only creates post-merge).
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'SRC_REF: ${{ needs.parse.outputs.stable_branch }}' || true)"
  [ "${count:-0}" -ge 1 ]
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'FETCH_REF="${{ needs.parse.outputs.stable_tag }}"' || true)"
  [ "${count:-0}" -eq 0 ]
}

@test "website-docs guards against a website checkout that predates FETCH_REF" {
  block="$(job_block website-docs "$PROMOTE")"
  guard="$(printf '%s\n' "$block" | awk '
    /^      - name: Require FETCH_REF support/ { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside')"
  [ -n "$guard" ]

  # It detects support by probing the website Makefile, and fails loudly when
  # absent — never warn-and-continue into stub-doc generation.
  printf '%s\n' "$guard" | code_lines | grep -qF "grep -q '^FETCH_REF' Makefile"
  printf '%s\n' "$guard" | code_lines | grep -qF 'exit 1'
}

@test "website-docs checkout does not persist credentials (app-token push, extraheader trap)" {
  block="$(job_block website-docs "$PROMOTE")"
  checkout="$(printf '%s\n' "$block" | awk '
    /^      - name: Checkout website repo$/ { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside')"
  [ -n "$checkout" ]

  count="$(printf '%s\n' "$checkout" | code_lines | grep -cF 'persist-credentials: false' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$checkout" | code_lines | grep -cF 'repository: cozystack/website' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "website-docs PR body carries the DO-NOT-MERGE-until-finalize contract" {
  block="$(job_block website-docs "$PROMOTE")"
  # Explicit merge-timing wording in the body the job opens on cozystack/website.
  printf '%s\n' "$block" | code_lines | grep -qF 'DO NOT MERGE until'
}

@test "promote PR body carries a website-docs ✅/⚠️ status line" {
  block="$(job_block open-pr "$PROMOTE")"
  [ -n "$block" ]

  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'WEBSITE_DOCS_RESULT: ${{ needs.website-docs.result }}' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$block" | code_lines | grep -cF '          ${WEBSITE_NOTE}' || true)"
  [ "${count:-0}" -eq 1 ]
  # Both outcomes must be expressible.
  printf '%s\n' "$block" | code_lines | grep -qF 'WEBSITE_NOTE="✅'
  printf '%s\n' "$block" | code_lines | grep -qF 'WEBSITE_NOTE="⚠️'
}

@test "tags.yaml update-website-docs documents that it is now the backstop" {
  # Deliberately a COMMENT-presence pin (not code_lines): the backstop status is
  # documented in a comment above the tag-time job so a maintainer reading it
  # knows the promote flow opens the PR earlier.
  grep -qF 'promote-rc.yaml::website-docs' "$TAGS"
  grep -qiF 'backstop' "$TAGS"
}

@test "finalize checkout does not persist credentials so the app-token tag push triggers tags.yaml" {
  block="$(job_block finalize "$FINALIZE")"
  [ -n "$block" ]
  checkout="$(printf '%s\n' "$block" | awk '
    /^      - name: Checkout repo$/ { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside')"
  [ -n "$checkout" ]

  # The one-line root-cause fix. Without persist-credentials:false the checkout
  # persists GITHUB_TOKEN as http.extraheader, which silently defeats the app token
  # each later `git remote set-url` injects onto the tag pushes — and a
  # GITHUB_TOKEN-authenticated push creates no workflow run (anti-recursion), so
  # tags.yaml's stable-tag backstops never fire (v1.6.0's tag never triggered it).
  count="$(printf '%s\n' "$checkout" | code_lines | grep -cF 'persist-credentials: false' || true)"
  [ "${count:-0}" -eq 1 ]
}

# ── rc-e2e's reusable-workflow permission ceiling ────────────────────────────
# tags.yaml runs only on tag pushes, so no PR lane can ever exercise these two
# facts together. They are pinned here because getting them wrong does not
# degrade gracefully: a caller that grants less than the called workflow's jobs
# declare fails GitHub's STATIC validation at run creation, taking down the whole
# tags.yaml run — build, draft release and staging branch included — for every
# tag push, rc or stable.

@test "rc-e2e grants the ceiling e2e-tag.yaml's jobs declare" {
  E2E_TAG="$REPO_ROOT/.github/workflows/e2e-tag.yaml"
  [ -f "$E2E_TAG" ]

  rc_e2e="$(job_block rc-e2e "$TAGS")"
  [ -n "$rc_e2e" ]
  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'uses: ./.github/workflows/e2e-tag.yaml'

  # Every permission any job in the called workflow declares must be granted by
  # the caller. Assert the pair explicitly, in both files, so removing either side
  # surfaces here instead of at the next release.
  e2e_job="$(job_block e2e "$E2E_TAG")"
  [ -n "$e2e_job" ]
  printf '%s\n' "$e2e_job" | code_lines | grep -qF 'checks: write'

  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'contents: read'
  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'checks: write'
}

@test "the promote gate's expected job name matches e2e-tag.yaml's job name" {
  E2E_TAG="$REPO_ROOT/.github/workflows/e2e-tag.yaml"

  # The gate correlates evidence by exact job name, which makes rc.1 vs rc.11
  # unambiguous — and makes a rename on either side fail the gate closed, blocking
  # promotion until someone notices. Cheap to pin, expensive to debug.
  count="$(code_lines < "$PROMOTE" | grep -cF 'E2E ${rcTag} (full suite)' || true)"
  [ "${count:-0}" -ge 1 ]
  count="$(code_lines < "$E2E_TAG" | grep -cF 'E2E ${{ inputs.tag }} (full suite)' || true)"
  [ "${count:-0}" -eq 1 ]
}
