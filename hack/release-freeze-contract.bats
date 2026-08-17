#!/usr/bin/env bats

# Contract for the rc freeze, as implemented by cut-prerelease.yaml. These tests
# pin executable/structural workflow lines rather than prose: a commented-out
# gate or a guard demoted to a comment must never satisfy the contract.
#
# cut-prerelease.yaml cannot be exercised by a PR lane. It runs only on
# workflow_dispatch from main or a release line, and its whole purpose is a
# one-way, write-once side effect — the first vX.Y.0-rc.N freezes the line, and
# there is no dry-run mode and no undo short of deleting the branch by hand. So
# every invariant below is one that first reports a slip at a real release, on
# the one commit nobody wants to be debugging: getting the freeze condition
# wrong re-opens a frozen line silently. Cheap to pin here, expensive to
# discover there.
#
# BRANCH NOTE (release-1.5): main's copy of this file also covers backport.yaml's
# target resolution and the build-release.yaml dispatch the freeze fires. Neither
# is portable here — this branch's backport.yaml predates that rework, and
# build-release.yaml does not exist on this line at all (which is why the freeze
# step's artifact dispatch is deliberately NOT pinned below; it is unreachable
# here, since freezing requires `patch == '0'` and release-1.5 already exists).
# The freeze invariants themselves are the same on both branches and are pinned
# in full.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure.
#
# Run with: hack/cozytest.sh hack/release-freeze-contract.bats

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

@test "the workflow under contract exists" {
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
  #
  # On release-1.5 this is also what makes the freeze inert: a patch cut carries
  # patch != 0, so the step is skipped outright, and the only tag shape that
  # would reach it (v1.5.0-*) is already taken and refused by the write-once
  # guard.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF "        if: steps.parse.outputs.kind == 'rc' && steps.parse.outputs.patch == '0'" || true)"
  [ "${count:-0}" -eq 1 ]

  # The step id the artifact dispatch downstream gates on. Losing it makes that
  # gate read an empty output, so it is permanently false.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF '        id: freeze' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "freeze creates the branch without force, and no-ops when it exists" {
  block="$(step_block 'Freeze the line (create release-X.Y)' "$CUT")"
  [ -n "$block" ]

  # The create push, verbatim and non-forced. A force-push here would move an
  # existing release-X.Y onto the new tip, dragging in everything merged since
  # the freeze — the exact outcome the branch exists to prevent. On this branch
  # that would mean moving release-1.5 itself.
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
  # of the ls-remote guard above it. This is the property auto-release.yaml
  # broke on this line: it deleted and re-created stable tags on every run.
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

# ── the dispatch-branch validation this line depends on ──────────────────────
# release-1.5 is a frozen line, so every cut for it is dispatched from the
# branch. The line/branch agreement check is what stops a v1.6.x tag being cut
# from here (and a v1.5.x patch tag being cut from main, where the tree is a
# different generation entirely).

@test "a patch-line pre-release may not be cut from main, and the line must match the branch" {
  block="$(step_block 'Validate dispatch branch' "$CUT")"
  [ -n "$block" ]

  # From main only vX.Y.0-* is allowed. Without this a v1.5.4-rc.1 dispatched
  # from main would tag main's tip and stage it as release-1.5.4-rc.1 — a tree
  # from a different minor entirely.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'if [ "$PATCH" != "0" ]; then' || true)"
  [ "${count:-0}" -eq 1 ]

  # From a release line the tag's line must equal the branch.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF 'if [ "release-$LINE" != "$REF_NAME" ]; then' || true)"
  [ "${count:-0}" -eq 1 ]

  # And a stable vX.Y.Z is refused outright by the parse step: stable tags are
  # cut only by finalize, at a promote PR's merge commit.
  parse="$(step_block 'Parse and validate tag' "$CUT")"
  [ -n "$parse" ]
  printf '%s\n' "$parse" | code_lines | grep -qF 'const m = tag.match(/^v(\d+)\.(\d+)\.(\d+)-(alpha|beta|rc)\.(\d+)$/);'
}
