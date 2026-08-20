#!/usr/bin/env bats
# Structural contract for publishing and verifying stable-candidate packages.
# The registry workflow cannot run in a PR lane, so ordering around the first
# write-once stable tag is pinned here. Behaviour lives in the two *_test suites.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PROMOTE="$REPO_ROOT/.github/workflows/promote-rc.yaml"
FINALIZE="$REPO_ROOT/.github/workflows/pull-requests-release.yaml"
PULL_REQUESTS="$REPO_ROOT/.github/workflows/pull-requests.yaml"

code_lines() {
  rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
}

step_block() {
  awk -v want="      - name: $1" '
    $0 == want { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside' "$2"
}

step_line() {
  code_lines < "$2" | grep -nF "      - name: $1" | awk -F: 'NR == 1 { print $1 }'
}

@test "candidate publish and verification helpers exist and are executable" {
  [ -x "$REPO_ROOT/hack/promote-packages-artifact.sh" ]
  [ -x "$REPO_ROOT/hack/verify-promoted-packages.sh" ]
}

@test "promote commits the rewritten tree then publishes and commits its pin" {
  block="$(step_block 'Prepare stable branch' "$PROMOTE")"
  [ -n "$block" ]

  rewrite="$(printf '%s\n' "$block" | code_lines | grep -nF '.release-tooling/hack/promote-rewrite-tags.sh' | awk -F: 'NR == 1 { print $1 }')"
  publish="$(printf '%s\n' "$block" | code_lines | grep -nF '.release-tooling/hack/promote-packages-artifact.sh' | awk -F: 'NR == 1 { print $1 }')"
  content_commit="$(printf '%s\n' "$block" | code_lines | grep -nF 'git commit -s -m "Prepare release' | awk -F: 'NR == 1 { print $1 }')"
  pin_commit="$(printf '%s\n' "$block" | code_lines | grep -nF 'git commit -s -m "Pin promoted packages artifact' | awk -F: 'NR == 1 { print $1 }')"
  [ -n "$rewrite" ] && [ -n "$content_commit" ] && [ -n "$publish" ] && [ -n "$pin_commit" ]
  [ "$rewrite" -lt "$content_commit" ]
  [ "$content_commit" -lt "$publish" ]
  [ "$publish" -lt "$pin_commit" ]

  # The helper comes from the dispatch ref's sparse tooling checkout, so the
  # first release after this fix does not depend on the older rc tree carrying it.
  printf '%s\n' "$block" | code_lines | grep -qF '.release-tooling/hack/promote-packages-artifact.sh'
  printf '%s\n' "$block" | code_lines | grep -qF 'SOURCE_SHA="$(git rev-parse HEAD)"'
  printf '%s\n' "$block" | code_lines | grep -qF 'PROMOTION_ID: ${{ github.run_id }}-${{ github.run_attempt }}'
}

@test "promote authenticates Flux with the same Docker config used by publish" {
  job="$(job_block promote "$PROMOTE")"
  login="$(step_block 'Login to registry (GHCR)' "$PROMOTE")"
  prepare="$(step_block 'Prepare stable branch' "$PROMOTE")"

  printf '%s\n' "$job" | code_lines | grep -qF '      packages: write'
  printf '%s\n' "$job" | code_lines | grep -qF 'FLUX_VERSION: "2.8.6"'
  printf '%s\n' "$job" | code_lines | grep -qF 'flux version --client'
  printf '%s\n' "$login" | code_lines | grep -qF 'DOCKER_CONFIG: ${{ runner.temp }}/.docker'
  printf '%s\n' "$prepare" | code_lines | grep -qF 'DOCKER_CONFIG: ${{ runner.temp }}/.docker'
}

@test "every promotion toolchain install pins its tools" {
  # A release gate whose tooling comes from `releases/latest/download` is not a
  # fixed input: the bytes that decide whether vX.Y.Z may be created, and the
  # yq that rewrites the installer pin, change whenever upstream publishes.
  # Pinned by version, and pinned to STAY pinned — the unpinned one-liner is
  # still the idiom in the workflows outside this pipeline, so it comes back by
  # copy-paste rather than by anyone deciding to unpin a release step.
  for step_and_file in \
    "Set up toolchain (flux, yq)|$PROMOTE" \
    "Set up promotion toolchain (flux, yq)|$PULL_REQUESTS" \
    "Set up promotion toolchain (flux, skopeo, yq, helm)|$FINALIZE"; do
    block="$(step_block "${step_and_file%%|*}" "${step_and_file#*|}")"
    [ -n "$block" ]
    printf '%s\n' "$block" | code_lines | grep -qF 'FLUX_VERSION: "'
    printf '%s\n' "$block" | code_lines | grep -qF 'YQ_VERSION: "'
    count="$(printf '%s\n' "$block" | code_lines | grep -c 'releases/latest/download' || true)"
    [ "${count:-0}" -eq 0 ]

    # yq is pinned by content as well as by version: it is the one downloaded
    # binary that WRITES what a release contains, stamping the candidate digest
    # into installer values and platformVersion into the chart. A version
    # variable authenticates nothing about the bytes it names.
    printf '%s\n' "$block" | code_lines | grep -qF 'YQ_SHA256: "'
    printf '%s\n' "$block" | code_lines | grep -qF 'sha256sum -c -'
  done

  # One version AND one checksum across all three, so the yq that writes the
  # candidate's installer values is the yq that reads them back out of the
  # artifact — and so bumping the version in one file without its checksum, or
  # either without the other two, cannot pass.
  for key in YQ_VERSION YQ_SHA256; do
    values="$(cat "$PROMOTE" "$PULL_REQUESTS" "$FINALIZE" | code_lines \
      | grep -o "$key: \"[0-9a-f.]*\"" | sort -u)"
    [ -n "$values" ]
    [ "$(printf '%s\n' "$values" | wc -l | tr -d ' ')" -eq 1 ]
  done
}

@test "finalize verifies the pinned candidate before the first stable tag" {
  verify="$(step_line 'Verify stable packages candidate' "$FINALIZE")"
  create="$(step_line 'Create tag on merge commit (write-once)' "$FINALIZE")"
  [ -n "$verify" ] && [ -n "$create" ]
  [ "$verify" -lt "$create" ]

  block="$(step_block 'Verify stable packages candidate' "$FINALIZE")"
  printf '%s\n' "$block" | code_lines | grep -qF '.release-tooling/hack/verify-promoted-packages.sh "${TAG#v}"'

  setup="$(step_block 'Set up promotion toolchain (flux, skopeo, yq, helm)' "$FINALIZE")"
  printf '%s\n' "$setup" | code_lines | grep -qF 'FLUX_VERSION: "2.8.6"'
  printf '%s\n' "$setup" | code_lines | grep -qF 'flux version --client'
}

@test "promote rejects an old target base before publishing a candidate" {
  validate="$(step_block 'Validate rc is promotable' "$PROMOTE")"
  prepare_line="$(step_line 'Prepare stable branch' "$PROMOTE")"
  validate_line="$(step_line 'Validate rc is promotable' "$PROMOTE")"
  [ -n "$validate" ] && [ -n "$validate_line" ] && [ -n "$prepare_line" ]
  [ "$validate_line" -lt "$prepare_line" ]

  printf '%s\n' "$validate" | code_lines | grep -qF "['.github/workflows/pull-requests.yaml', 'verify-release-candidate:']"
  printf '%s\n' "$validate" | code_lines | grep -qF "['.github/workflows/pull-requests-release.yaml', 'Verify stable packages candidate']"
  printf '%s\n' "$validate" | code_lines | grep -qF "['hack/verify-promoted-packages.sh', 'EXPECTED_PACKAGES_REPOSITORY']"
  # The verifier resolves its libraries relative to itself at run time, so a
  # base carrying the script without them fails the gate it is meant to be.
  # BOTH of them: the shared image-reference enumeration is sourced through the
  # same `dirname $0` as the promoted-packages library, so a base missing it
  # fails in the same place. Pinned per entry rather than only as a block —
  # dropping any single pair used to leave every assertion here green.
  printf '%s\n' "$validate" | code_lines | grep -qF "['hack/lib/image-refs.sh', 'collect_image_refs()']"
  printf '%s\n' "$validate" | code_lines | grep -qF "['hack/lib/promoted-packages.sh', 'PACKAGES_DIGEST_REF_PATTERN']"
  printf '%s\n' "$validate" | code_lines | grep -qF "core.setOutput('base_branch', baseBranch)"

  open_pr="$(step_block 'Open promote (release) PR' "$PROMOTE")"
  printf '%s\n' "$open_pr" | code_lines | grep -qF 'BASE_BRANCH: ${{ needs.promote.outputs.base_branch }}'
  printf '%s\n' "$open_pr" | code_lines | grep -qF 'BASE="$BASE_BRANCH"'
}

@test "pull request gate verifies the prospective merge with trusted base tooling" {
  job="$(job_block verify-release-candidate "$PULL_REQUESTS")"
  [ -n "$job" ]

  printf '%s\n' "$job" | code_lines | grep -qF "github.event.pull_request.user.login == 'cozystack-ci[bot]'"
  printf '%s\n' "$job" | code_lines | grep -qF 'ref: ${{ github.event.pull_request.base.sha }}'
  printf '%s\n' "$job" | code_lines | grep -qF '.release-tooling/hack/verify-promoted-packages.sh "$STABLE_VERSION"'
  printf '%s\n' "$job" | code_lines | grep -qF 'FLUX_VERSION: "2.8.6"'

  report="$(job_block e2e-report "$PULL_REQUESTS")"
  printf '%s\n' "$report" | code_lines | grep -qF '"verify-release-candidate"'
  printf '%s\n' "$report" | code_lines | grep -qF "IS_RELEASE === 'true' && CANDIDATE_RESULT !== 'success'"

  # `e2e` is the other consumer, and the one that RUNS the suite. Reporting a
  # red is a lesser guarantee than not running a full-e2e promotion suite
  # against a candidate that did not verify, so pin both halves: the dependency
  # and the clause on the release arm. Removing them together left every other
  # assertion in this file green.
  e2e="$(job_block e2e "$PULL_REQUESTS")"
  [ -n "$e2e" ]
  printf '%s\n' "$e2e" | code_lines | grep -qF '"verify-release-candidate"'
  printf '%s\n' "$e2e" | code_lines | grep -qF 'needs.verify-release-candidate.result == '"'"'success'"'"''

  # gh applies --label after opening a PR. The release label event must replace
  # the initial run and publish a fresh status that includes candidate verify.
  grep -qF "github.event.label.name != 'release'" "$PULL_REQUESTS"
  plan="$(job_block plan "$PULL_REQUESTS")"
  printf '%s\n' "$plan" | code_lines | grep -qF "github.event.label.name == 'release'"
  printf '%s\n' "$report" | code_lines | grep -qF "github.event.label.name == 'release'"
}
