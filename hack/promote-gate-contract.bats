#!/usr/bin/env bats

# Contract for the pieces of the rc-E2E promotion gate that live on THIS branch.
# These tests intentionally pin executable/structural workflow lines, not prose:
# a commented-out gate, label, or body note must never satisfy the contract.
#
# BRANCH NOTE (release-1.5): promote-rc.yaml is deliberately NOT on this branch.
# It is dispatched from main and overlays main's `.release-tooling` at the ref it
# is asked to promote, so main owns the gate's asking side — the string
# `E2E ${rcTag} (full suite)` it correlates evidence by, the skip_e2e_gate
# override, and the promote PR body. main's copy of this file pins those. What
# this branch owns is the ANSWERING side: the job name e2e-tag.yaml publishes,
# the permission ceiling tags.yaml must grant to call it, and finalize's
# credential handling. Those are pinned in full below.
#
# A future backport of main's copy will conflict with this file. That is the
# intended outcome: it is the moment to check whether promote-rc.yaml has also
# arrived, and to restore the pins that only make sense once it has.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure.
#
# Run with: hack/cozytest.sh hack/promote-gate-contract.bats

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
TAGS="$REPO_ROOT/.github/workflows/tags.yaml"
FINALIZE="$REPO_ROOT/.github/workflows/pull-requests-release.yaml"
E2E_TAG="$REPO_ROOT/.github/workflows/e2e-tag.yaml"
PULL_REQUESTS="$REPO_ROOT/.github/workflows/pull-requests.yaml"

job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
}

# Everything a job declares before its steps: the `if:`, `needs:`, runner and
# permissions. Scoping a guard pin to this rather than the whole job body keeps
# a step-level `if:` from satisfying a job-level assertion.
job_header() {
  job_block "$1" "$2" | awk '
    /^    steps:$/ { exit }
    { print }'
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

@test "the workflows under contract exist" {
  [ -f "$TAGS" ]
  [ -f "$FINALIZE" ]
  [ -f "$E2E_TAG" ]
  [ -f "$PULL_REQUESTS" ]
}

@test "verify-release-candidate is keyed on author and head branch, never on the release label" {
  header="$(job_header verify-release-candidate "$PULL_REQUESTS")"
  [ -n "$header" ]

  # POSITIVE first, and on the same stream the negative pin below reads. If the
  # job vanished or code_lines broke, these fail rather than letting the
  # absence assertion pass on an empty stream.
  count="$(printf '%s\n' "$header" | code_lines | grep -cF "      github.event.pull_request.user.login == 'cozystack-ci[bot]'" || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$header" | code_lines | grep -cF "      && startsWith(github.head_ref, 'release-')" || true)"
  [ "${count:-0}" -eq 1 ]

  # NEGATIVE: the label key must not come back. main keys this job on the
  # `release` label and can, because its `on.pull_request.types` carries
  # `labeled`. This branch's does not, and `gh pr create --label release`
  # applies the label in a SECOND API call after the PR exists — so a
  # label-keyed guard here fires only if GitHub happens to serialize the
  # `opened` payload after that call. It has been winning that race, which is
  # why the failure would be invisible: the job would keep passing until one
  # release day when it silently did not run at all, while still satisfying
  # promote-rc.yaml's five-file preflight. Written as a counted comparison, not
  # `! grep -q`: both sh and bash exempt a `!`-negated pipeline from errexit, so
  # that form can never fail a cozytest.sh test.
  count="$(printf '%s\n' "$header" | code_lines | grep -cF "github.event.pull_request.labels.*.name, 'release'" || true)"
  [ "${count:-0}" -eq 0 ]

  # A prerelease head can only produce a red check: the verifier's argument is
  # ${HEAD_BRANCH#release-} and it requires X.Y.Z.
  for suffix in '-rc.' '-alpha.' '-beta.'; do
    count="$(printf '%s\n' "$header" | code_lines | grep -cF "&& !contains(github.head_ref, '${suffix}')" || true)"
    [ "${count:-0}" -eq 1 ]
  done

  # The verifier is executed from the trusted BASE checkout, not from the head.
  block="$(job_block verify-release-candidate "$PULL_REQUESTS")"
  printf '%s\n' "$block" | code_lines | grep -qF 'ref: ${{ github.event.pull_request.base.sha }}'
  printf '%s\n' "$block" | code_lines | grep -qF '.release-tooling/hack/verify-promoted-packages.sh "$STABLE_VERSION"'
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

@test "finalize verifies the packages candidate before any write-once name exists" {
  block="$(job_block finalize "$FINALIZE")"
  [ -n "$block" ]

  # The verifier runs from the trusted BASE checkout, against the merged tree —
  # a release PR must not be able to weaken its own final guard.
  printf '%s\n' "$block" | code_lines | grep -qF '.release-tooling/hack/verify-promoted-packages.sh "${TAG#v}"'
  printf '%s\n' "$block" | code_lines | grep -qF 'ref: ${{ github.event.pull_request.base.sha }}'

  # And it precedes the first write-once name: the stable git tag.
  verify_line="$(code_lines < "$FINALIZE" | grep -nF '      - name: Verify stable packages candidate' | awk -F: 'NR == 1 { print $1 }')"
  tag_line="$(code_lines < "$FINALIZE" | grep -nF '      - name: Create tag on merge commit (write-once)' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$verify_line" ] && [ -n "$tag_line" ]
  [ "$verify_line" -lt "$tag_line" ]
}

# ── rc-e2e's reusable-workflow permission ceiling ────────────────────────────
# tags.yaml runs only on tag pushes, so no PR lane can ever exercise these two
# facts together. They are pinned here because getting them wrong does not
# degrade gracefully: a caller that grants less than the called workflow's jobs
# declare fails GitHub's STATIC validation at run creation, taking down the whole
# tags.yaml run — build, draft release and staging branch included — for every
# tag push, rc or stable.

@test "rc-e2e grants the ceiling e2e-tag.yaml's jobs declare" {
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

@test "e2e-tag.yaml publishes the exact job name the promote gate correlates on" {
  # The gate on main correlates evidence by exact job name, which makes rc.1 vs
  # rc.11 unambiguous — and makes a rename on either side fail the gate closed,
  # blocking promotion until someone notices. main's `E2E ${rcTag} (full suite)`
  # is the asking side; this is the answering side, and it is the half that lives
  # here. Byte-identical to main's e2e-tag.yaml on purpose: an adapted suite step
  # is fine, an adapted job NAME makes the evidence unfindable and leaves
  # skip_e2e_gate=true as the only route to promotion.
  count="$(code_lines < "$E2E_TAG" | grep -cF 'E2E ${{ inputs.tag }} (full suite)' || true)"
  [ "${count:-0}" -eq 1 ]

  # And the caller passes the tag that name is built from, so the published job
  # name is the rc tag being promoted rather than a branch or a SHA.
  rc_e2e="$(job_block rc-e2e "$TAGS")"
  [ -n "$rc_e2e" ]
  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'tag: ${{ github.ref_name }}'
}

# ── the tag lane's suite step actually exists on this branch ─────────────────

@test "e2e-tag.yaml's full-suite step calls a target packages/core/testing defines" {
  # The one adaptation this branch makes to e2e-tag.yaml. main runs
  # `test-chainsaw`, which packages/core/testing/Makefile does not define here —
  # a straight verbatim port would have failed at the suite step on every rc,
  # after the sandbox was already built. Pin both halves so the swap cannot rot
  # back: the workflow must call test-apps-, and the Makefile must define it.
  block="$(job_block e2e "$E2E_TAG")"
  [ -n "$block" ]
  printf '%s\n' "$block" | code_lines | grep -qF 'make -C packages/core/testing SANDBOX_NAME=$SANDBOX_NAME test-apps-$app'
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'test-chainsaw' || true)"
  [ "${count:-0}" -eq 0 ]

  grep -qE '^test-apps-%:' "$REPO_ROOT/packages/core/testing/Makefile"
}
