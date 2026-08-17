#!/usr/bin/env bats

# Contract for the halves of the rc-E2E promotion gate that live on THIS branch.
# These tests intentionally pin executable/structural workflow lines, not prose:
# a commented-out gate, job, or step must never satisfy the contract.
#
# Scope, and why it is narrower than main's copy of this file.
#
# The gate itself is not here. `Promote RC` is a workflow_dispatch, so it runs
# from the ref the maintainer dispatches — main — and it is main's promote-rc.yaml
# that composes the expected job name, walks the Actions API for evidence, and
# refuses to promote without it. This branch's promote-rc.yaml predates all of
# that and is deliberately left alone: porting it would drag in a `parse` job, a
# `skip_e2e_gate` input, promote-time changelog and website-docs jobs, and the
# `labeled` pull_request trigger those depend on. main's copy of this file pins
# exactly those things, which is why it cannot be used verbatim here.
#
# What release-1.6 owns is the PRODUCER side of the same contract, and that is
# what these tests pin:
#
#   * a tag push must actually run the full suite, under a job name whose
#     rendered form is what the gate on main searches for. Workflows run from the
#     ref they fire on, so only this file's tags.yaml can do that for an rc cut
#     on this line;
#   * the target base must carry the five files promote-rc.yaml's preflight
#     demands before its first registry write, or the dispatch fails closed with
#     "Target base 'release-1.6' lacks '<path>'";
#   * the candidate verification in finalize must run before anything
#     irreversible, because a guard that runs after the write-once stable tag
#     exists cannot refuse it.
#
# No CI lane exercises tags.yaml (it runs only on tag pushes) and no lane
# exercises finalize's ordering, which is why these are pinned mechanically here.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PULL_REQUESTS="$REPO_ROOT/.github/workflows/pull-requests.yaml"
TAGS="$REPO_ROOT/.github/workflows/tags.yaml"
FINALIZE="$REPO_ROOT/.github/workflows/pull-requests-release.yaml"
E2E_TAG="$REPO_ROOT/.github/workflows/e2e-tag.yaml"

job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
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
code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

# First line number of a named step inside an already-extracted job block, in
# code_lines' comment-stripped view. Top-level rather than nested inside a test
# so its closing brace is the only one the runner's translator has to rewrite.
step_line() {
  printf '%s\n' "$2" | code_lines | grep -nF "      - name: $1" \
    | awk -F: 'NR == 1 { print $1 }'
}

# ── the producer half of the rc-E2E gate ─────────────────────────────────────

@test "an rc tag push calls the full-suite e2e workflow" {
  [ -f "$E2E_TAG" ]

  rc_e2e="$(job_block rc-e2e "$TAGS")"
  [ -n "$rc_e2e" ]

  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'uses: ./.github/workflows/e2e-tag.yaml'

  # The tag under test must be the pushed ref. The gate correlates evidence by a
  # job name that embeds the exact tag, so passing anything else here would
  # produce a green run that no promotion can ever match to its rc.
  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'tag: ${{ github.ref_name }}'

  # rc tags only. A stable tag push must not re-run the suite, and alpha/beta use
  # e2e-tag.yaml's manual dispatch.
  printf '%s\n' "$rc_e2e" | code_lines | grep -qF "contains(github.ref_name, '-rc.')"
}

@test "rc-e2e grants the ceiling e2e-tag.yaml's jobs declare" {
  rc_e2e="$(job_block rc-e2e "$TAGS")"
  [ -n "$rc_e2e" ]

  # Every permission any job in the called workflow declares must be granted by
  # the caller. GitHub validates that ceiling STATICALLY, when it creates the run,
  # before this job's `if:` is evaluated — so a missing grant does not skip the
  # rc lane, it fails the whole tags.yaml run for EVERY tag push, rc and stable
  # alike, building nothing at all. Assert the pair in both files, so removing
  # either side surfaces here instead of at the next release.
  e2e_job="$(job_block e2e "$E2E_TAG")"
  [ -n "$e2e_job" ]
  printf '%s\n' "$e2e_job" | code_lines | grep -qF 'checks: write'

  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'contents: read'
  printf '%s\n' "$rc_e2e" | code_lines | grep -qF 'checks: write'
}

@test "the e2e job name is the exact string the promote gate searches for" {
  # main's promote-rc.yaml builds `E2E ${rcTag} (full suite)` and matches a job
  # whose name either equals it or ends in " / " plus it (the second form is how
  # GitHub renders a called workflow's job: "<caller job> / <called job>"). This
  # branch supplies the called half, so the literal below is the contract — a
  # rename on either side makes the gate fail closed and blocks promotion until
  # someone notices. Cheap to pin, expensive to debug.
  #
  # -F is not optional: the pattern contains `${{`, and as a basic regular
  # expression that silently matches nothing rather than erroring, which would
  # turn this pin into a test that passes by finding its own subject absent.
  count="$(code_lines < "$E2E_TAG" | grep -cF 'name: E2E ${{ inputs.tag }} (full suite)' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "the tag-push e2e lane runs the full chainsaw suite it claims to" {
  e2e_job="$(job_block e2e "$E2E_TAG")"
  [ -n "$e2e_job" ]

  # Empty CHAINSAW_SUITES is the testing Makefile's full-suite mode. A tag has no
  # PR diff to scope with Test Impact Analysis, so a narrowed suite here would
  # make the gate's evidence weaker than its name advertises.
  printf '%s\n' "$e2e_job" | code_lines | grep -qF 'test-chainsaw CHAINSAW_SUITES=""'

  # The target and the sandbox binary the step above depends on.
  grep -qE '^test-chainsaw' "$REPO_ROOT/packages/core/testing/Makefile"
  grep -qF 'CHAINSAW_VERSION' "$REPO_ROOT/packages/core/testing/images/e2e-sandbox/Dockerfile"
}

# ── the candidate-aware promotion pipeline this base must carry ──────────────

@test "the base carries every file promote-rc.yaml's preflight demands" {
  # promote-rc.yaml resolves the target base (release-X.Y when it exists) and
  # reads each path below off it, refusing to promote unless the content contains
  # the paired marker. It fails closed BEFORE the first registry write, so a base
  # missing any of these cannot be promoted at all — which is exactly the state
  # this branch was in. Keep the list in lockstep with the requiredBaseFiles array
  # in main's promote-rc.yaml.
  grep -qF 'verify-release-candidate:' "$PULL_REQUESTS"
  grep -qF 'Verify stable packages candidate' "$FINALIZE"
  grep -qF 'EXPECTED_PACKAGES_REPOSITORY' "$REPO_ROOT/hack/verify-promoted-packages.sh"
  grep -qF 'collect_image_refs()' "$REPO_ROOT/hack/lib/image-refs.sh"
  grep -qF 'PACKAGES_DIGEST_REF_PATTERN' "$REPO_ROOT/hack/lib/promoted-packages.sh"

  # The verifier sources both libraries by path relative to itself. A base
  # carrying the script without them fails at the PR gate and again at finalize,
  # after the candidate, the staging branch and the draft release already exist.
  [ -x "$REPO_ROOT/hack/verify-promoted-packages.sh" ]
  [ -f "$REPO_ROOT/hack/lib/promoted-packages.sh" ]
  [ -f "$REPO_ROOT/hack/lib/image-refs.sh" ]
}

@test "verify-release-candidate fires on the promote PR without needing a label" {
  block="$(job_block verify-release-candidate "$PULL_REQUESTS")"
  [ -n "$block" ]

  # This branch's on.pull_request.types has no `labeled` event, and the promote
  # PR's `release` label is applied a moment AFTER the PR opens, so the `opened`
  # payload carries no labels at all. A label-keyed guard would therefore never
  # fire here. Pin the author + head-branch predicate that does, and pin the
  # absence of the label dependency so nobody "restores" it from main and
  # silently switches the job off.
  printf '%s\n' "$block" | code_lines | grep -qF "github.event.pull_request.user.login == 'cozystack-ci[bot]'"
  printf '%s\n' "$block" | code_lines | grep -qF "startsWith(github.head_ref, 'release-')"

  count="$(printf '%s\n' "$block" | code_lines | grep -cF "contains(github.event.pull_request.labels.*.name, 'release')" || true)"
  [ "${count:-0}" -eq 0 ]

  # The verifier must come from the trusted base, not from the PR's own tree: a
  # promote PR must not be able to weaken the guard that judges it.
  printf '%s\n' "$block" | code_lines | grep -qF 'ref: ${{ github.event.pull_request.base.sha }}'
  printf '%s\n' "$block" | code_lines | grep -qF '.release-tooling/hack/verify-promoted-packages.sh'
}

@test "the candidate verification runs before every irreversible finalize step" {
  block="$(job_block finalize "$FINALIZE")"
  [ -n "$block" ]

  verify="$(step_line 'Verify stable packages candidate' "$block")"
  [ -n "$verify" ]

  # Each of these creates or moves a name that cannot be taken back: the
  # write-once stable git tag, the API submodule tag, the published release, the
  # stable image tags (and :latest), and the stable installer chart. The
  # verification is only a gate if it precedes all of them.
  tag_step="$(step_line 'Create tag on merge commit (write-once)' "$block")"
  [ -n "$tag_step" ]
  [ "$verify" -lt "$tag_step" ]

  submodule_step="$(step_line 'Tag API submodule (write-once)' "$block")"
  [ -n "$submodule_step" ]
  [ "$verify" -lt "$submodule_step" ]

  publish_step="$(step_line 'Publish draft release' "$block")"
  [ -n "$publish_step" ]
  [ "$verify" -lt "$publish_step" ]

  retag_step="$(step_line 'Retag rc images to stable' "$block")"
  [ -n "$retag_step" ]
  [ "$verify" -lt "$retag_step" ]

  chart_step="$(step_line 'Publish stable cozy-installer chart' "$block")"
  [ -n "$chart_step" ]
  [ "$verify" -lt "$chart_step" ]

  # The toolchain and the registry login the verification needs must precede it
  # too. They used to sit after "Publish draft release"; moving the verification
  # up without them would make it fail on a missing `flux` instead of verifying.
  toolchain_step="$(step_line 'Set up promotion toolchain (flux, skopeo, yq, helm)' "$block")"
  [ -n "$toolchain_step" ]
  [ "$toolchain_step" -lt "$verify" ]

  login_step="$(step_line 'Login to registry (GHCR)' "$block")"
  [ -n "$login_step" ]
  [ "$login_step" -lt "$verify" ]

  tooling_step="$(step_line 'Checkout release tooling from base' "$block")"
  [ -n "$tooling_step" ]
  [ "$tooling_step" -lt "$verify" ]
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

# ── the tag-time changelog backstop ──────────────────────────────────────────

@test "the tag-time changelog is validated and ported rather than regenerated" {
  block="$(job_block generate-changelog "$TAGS")"
  [ -n "$block" ]

  # The promote PR merges into release-1.6, so its reviewed changelog never
  # reaches main and `exists` alone reads as "absent". at_tag is what stops a
  # second AI run from overwriting an already-published release body.
  printf '%s\n' "$block" | code_lines | grep -qF 'at_tag=true'
  printf '%s\n' "$block" | code_lines | grep -qF "steps.check_changelog.outputs.at_tag == 'false'"

  # Neither a ported nor a generated file has been checked at this point, and the
  # generating step is allowed to fail, so a truncated fragment must not pass.
  printf '%s\n' "$block" | code_lines | grep -qF 'hack/validate-changelog.sh'
  [ -x "$REPO_ROOT/hack/validate-changelog.sh" ]
}
