#!/usr/bin/env bats

# Contract for the rc freeze and the release-line build it dispatches. Like
# promote-gate-contract.bats, these tests pin executable/structural workflow
# lines rather than prose: a commented-out gate or a guard demoted to a comment
# must never satisfy the contract.
#
# No PR lane exercises them. cut-prerelease.yaml runs only on workflow_dispatch
# from main or a release line, and its whole purpose is a one-way, write-once
# side effect — the first vX.Y.0-rc.N freezes the line, and there is no dry-run
# mode and no undo short of deleting the branch by hand. So every invariant
# below is one that first reports a slip at a real release, on the one commit
# nobody wants to be debugging: getting the freeze condition wrong re-opens a
# frozen line silently. Cheap to pin here, expensive to discover there.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CUT="$REPO_ROOT/.github/workflows/cut-prerelease.yaml"

# Strip YAML comments. POSIX `grep` only: the unit-test runner has no ripgrep.
# grep exits 1 when nothing is selected (legitimate for an all-comment block)
# and 2 on a real error. Do not read the `rc` check below as what catches
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

# Body of one `- name:` step, up to the next step at the same indent.
step_block() {
  awk -v want="      - name: $1" '
    $0 == want { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside' "$2"
}

# Line number of a step within the comment-stripped file, for ordering asserts.
# Compared only against other stripped line numbers.
step_line() {
  code_lines < "$2" | grep -nF "      - name: $1" | awk -F: 'NR == 1 { print $1 }'
}

@test "workflows under contract exist" {
  [ -f "$CUT" ]
}

# ── the freeze condition ─────────────────────────────────────────────────────
# The freeze is create-only and one-way. Widening this condition is the single
# change that re-opens a frozen line without any error surfacing: an alpha/beta
# cut, or a patch-line rc, would branch or re-touch release-X.Y off a tree that
# is not the one the line was frozen at.

@test "freeze is gated to the first rc of a minor (kind rc, patch 0)" {
  block="$(step_block 'Freeze the line (create release-X.Y)' "$CUT")"
  [ -n "$block" ]

  # Both halves, as one expression, on a live (non-comment) line, and INSIDE the
  # freeze step. Dropping patch == '0' freezes on every patch-line rc; widening
  # kind freezes on an alpha/beta cut from a still-open main. Scoped to the block
  # rather than the file so that some other step carrying the same condition
  # cannot keep this green after the freeze step loses its own guard.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF "        if: steps.parse.outputs.kind == 'rc' && steps.parse.outputs.patch == '0'" || true)"
  [ "${count:-0}" -eq 1 ]

  # The step id the artifact dispatch below gates on. Losing it makes that gate
  # read an empty output, so it is permanently false and a newly frozen line
  # silently never gets its packages artifact.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF '        id: freeze' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "freeze creates the branch without force, and no-ops when it exists" {
  block="$(step_block 'Freeze the line (create release-X.Y)' "$CUT")"
  [ -n "$block" ]

  # The create push, verbatim and non-forced. A force-push here would move an
  # existing release-X.Y onto the new tip, dragging in everything merged since
  # the freeze — the exact outcome the branch exists to prevent.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'if ! git push origin "HEAD:refs/heads/$BRANCH"; then' || true)"
  [ "${count:-0}" -eq 1 ]

  # No force in any shape: an explicit flag, or a refspec that opens with `+`,
  # which forces that ref alone and needs no flag at all — `git push origin
  # "+HEAD:refs/heads/$BRANCH"` force-moves the branch while reading as an
  # ordinary push.
  count="$(printf '%s\n' "$block" | code_lines | grep -cE 'git push[^|&;]*(--force|--force-with-lease|[[:space:]]-f[[:space:]])' || true)"
  [ "${count:-0}" -eq 0 ]
  count="$(printf '%s\n' "$block" | code_lines | grep -cE "git push[^|&;]*[[:space:]][\"']?[+]" || true)"
  [ "${count:-0}" -eq 0 ]

  # An existing branch is left alone rather than reused as a create target.
  printf '%s\n' "$block" | code_lines | grep -qF 'leaving it untouched'
}

@test "the pre-release tag push is not forced either" {
  block="$(step_block 'Cut and push the pre-release tag' "$CUT")"
  [ -n "$block" ]

  # Non-forced is what makes the tag write-once at the git layer, independently
  # of the ls-remote guard above it.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'git push origin "HEAD:refs/tags/$TAG"' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$block" | code_lines | grep -cE 'git push[^|&;]*(--force|--force-with-lease|[[:space:]]-f[[:space:]])' || true)"
  [ "${count:-0}" -eq 0 ]
  # A leading `+` on the refspec forces the ref with no flag, which for a tag
  # means overwriting a published pre-release in place.
  count="$(printf '%s\n' "$block" | code_lines | grep -cE "git push[^|&;]*[[:space:]][\"']?[+]" || true)"
  [ "${count:-0}" -eq 0 ]
}

# ── the refuse gate ──────────────────────────────────────────────────────────

@test "refusing a frozen line is gated on a main dispatch" {
  block="$(step_block 'Refuse to cut a frozen line from main' "$CUT")"
  [ -n "$block" ]

  # Only a main dispatch can smuggle main's tip into a frozen line; a dispatch
  # from release-X.Y is already the frozen tree. Losing this guard lets rc.2 be
  # cut from main again, which is the regression the freeze was built to fix.
  # Scoped to this step: `github.ref_name == 'main'` is a plausible condition
  # elsewhere in the file, and an unscoped count would accept it as this pin.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF "        if: github.ref_name == 'main'" || true)"
  [ "${count:-0}" -eq 1 ]

  # It must fail closed: an ls-remote that neither found the branch (2) nor
  # succeeded (0) is a transport failure, not proof the line is unfrozen.
  printf '%s\n' "$block" | code_lines | grep -qF 'refusing to proceed'
}

@test "the refuse gate runs before the tag push, and the freeze after it" {
  refuse="$(step_line 'Refuse to cut a frozen line from main' "$CUT")"
  push="$(step_line 'Cut and push the pre-release tag' "$CUT")"
  freeze="$(step_line 'Freeze the line (create release-X.Y)' "$CUT")"
  [ -n "$refuse" ] && [ -n "$push" ] && [ -n "$freeze" ]

  # Refuse first: pre-release tag names are write-once, so a mistaken dispatch
  # must fail before it burns one.
  [ "$refuse" -lt "$push" ]

  # Freeze last: the branch has to be created at the commit that was actually
  # tagged, so it follows the push that proved the tip had not moved.
  [ "$push" -lt "$freeze" ]
}

# ── the frozen line's packages artifact ──────────────────────────────────────

@test "freezing a line dispatches its first build-release run" {
  BUILD_RELEASE="$REPO_ROOT/.github/workflows/build-release.yaml"
  [ -f "$BUILD_RELEASE" ]

  # build-release.yaml publishes cozystack-packages:<line> on push to a line
  # branch, but it carries a paths-ignore filter, and GitHub does not run a
  # path-filtered workflow for a push that changes no files. The freeze push
  # carries no new commits, so it cannot be what builds the new line — the
  # dispatch below is. Keep the button that makes the dispatch possible.
  count="$(code_lines < "$BUILD_RELEASE" | grep -cF '  workflow_dispatch:' || true)"
  [ "${count:-0}" -eq 1 ]

  block="$(step_block "Build the frozen line's packages artifact" "$CUT")"
  [ -n "$block" ]
  printf '%s\n' "$block" | code_lines | grep -qF 'gh workflow run build-release.yaml --ref "$BRANCH"'

  # Only when this run actually created the branch. Re-dispatching for a line
  # that was already frozen would rebuild it for no reason. Scoped to this step,
  # so the pin follows the gate rather than the file.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF "        if: steps.freeze.outputs.created == 'true'" || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "initial line build dispatch has Actions access and preserves failure recovery" {
  step="$(STEP_NAME="Build the frozen line's packages artifact" yq -o=json '.jobs.cut.steps[] | select(.name == strenv(STEP_NAME))' "$CUT")"
  script="$(printf '%s\n' "$step" | yq -r '.run')"
  token="$(printf '%s\n' "$step" | yq -r '.env.GH_TOKEN')"
  [ -n "$script" ] && [ "$script" != null ]
  case "$token" in
    '${{ github.token }}'|'${{ secrets.GITHUB_TOKEN }}') token=workflow-token ;;
    '${{ steps.app-token.outputs.token }}') token=app-token ;;
    *) echo "Unknown dispatch credential: $token"; return 1 ;;
  esac
  actions="$(yq -r '(.jobs.cut.permissions // .permissions).actions // "none"' "$CUT")"

  # The installed CI app has no Actions permission; the workflow token only
  # gets it when the effective job permissions request it.
  gh() {
    [ "$*" = 'workflow run build-release.yaml --ref release-1.7' ] || return 2
    [ "$GH_TOKEN" = workflow-token ] && [ "$ACTIONS_PERMISSION" = write ] || return 1
    return "$DISPATCH_STATUS"
  }
  export -f gh

  run env GH_TOKEN="$token" ACTIONS_PERMISSION="$actions" BRANCH=release-1.7 \
    DISPATCH_STATUS=0 bash -e -c "$script"
  [ "$status" -eq 0 ]
  [[ "$output" == *'Dispatched build-release.yaml for release-1.7;'* ]]

  run env GH_TOKEN="$token" ACTIONS_PERMISSION="$actions" BRANCH=release-1.7 \
    DISPATCH_STATUS=1 bash -e -c "$script"
  [ "$status" -eq 0 ]
  [[ "$output" == *'::warning::Could not dispatch build-release.yaml for release-1.7'* ]]
  [[ "$output" == *'Start it by hand: gh workflow run build-release.yaml --ref release-1.7'* ]]
}

@test "build-release refuses a dispatch from anything but a line branch" {
  BUILD_RELEASE="$REPO_ROOT/.github/workflows/build-release.yaml"
  [ -f "$BUILD_RELEASE" ]

  # The push trigger is filtered to release-X.Y, but workflow_dispatch takes any
  # ref and the Run-workflow UI preselects the default branch — which is where
  # this workflow's own recovery advice sends an operator. IMAGE_TAG is
  # github.ref_name, so a dispatch from main spends two hours republishing
  # cozystack-packages:main and every main image tag, racing build-main.yaml for
  # the same tags: the 409 collision class #2711 fixed.
  block="$(step_block 'Refuse a build for a non-line ref' "$BUILD_RELEASE")"
  [ -n "$block" ]

  # The pattern must be anchored at both ends. Unanchored, release-1.6.1 (a
  # per-release staging branch) and release-1.6.0-rc.4 both match, and building
  # those republishes a published release's tags from a staging tree.
  printf '%s\n' "$block" | code_lines | grep -qF '^release-[0-9]+\.[0-9]+$'

  # And the ref must be a BRANCH. A TAG named release-1.6 satisfies the pattern
  # on its own, and building from one publishes cozystack-packages:release-1.6
  # from whatever commit the tag froze rather than from the line's tip. Pinned as
  # the comparison, not just the env wiring, so passing REF_TYPE in without
  # testing it does not satisfy this.
  printf '%s\n' "$block" | code_lines | grep -qF 'REF_TYPE: ${{ github.ref_type }}'
  printf '%s\n' "$block" | code_lines | grep -qF '"$REF_TYPE" != "branch"'

  # It has to fail, not skip: a job that quietly no-ops looks in the run list
  # exactly like one that built the artifact the operator is waiting for.
  printf '%s\n' "$block" | code_lines | grep -qF 'exit 1'

  # And it has to run before the work: a guard placed after checkout and the
  # OCIR login has already spent the credentials it exists to protect.
  guard="$(step_line 'Refuse a build for a non-line ref' "$BUILD_RELEASE")"
  checkout="$(step_line 'Checkout code' "$BUILD_RELEASE")"
  [ -n "$guard" ] && [ -n "$checkout" ]
  [ "$guard" -lt "$checkout" ]
}
