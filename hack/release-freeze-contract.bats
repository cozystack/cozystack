#!/usr/bin/env bats

# Contract for the rc freeze and the backport target resolution that depends on
# it. Like promote-gate-contract.bats, these tests pin executable/structural
# workflow lines rather than prose: a commented-out gate or a guard demoted to a
# comment must never satisfy the contract.
#
# Neither workflow can be exercised by a PR lane. cut-prerelease.yaml runs only
# on workflow_dispatch from main or a release line, and its whole purpose is a
# one-way, write-once side effect — the first vX.Y.0-rc.N freezes the line, and
# there is no dry-run mode and no undo short of deleting the branch by hand.
# backport.yaml runs only on a merged main-targeted PR. So every invariant below
# is one that first reports a slip at a real release, on the one commit nobody
# wants to be debugging: getting the freeze condition wrong re-opens a frozen
# line silently, and getting the comparator wrong aims every backport at the
# wrong branch. Cheap to pin here, expensive to discover there.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CUT="$REPO_ROOT/.github/workflows/cut-prerelease.yaml"
BACKPORT="$REPO_ROOT/.github/workflows/backport.yaml"

# Strip YAML comments. POSIX `grep` only: the unit-test runner has no ripgrep.
# grep exits 1 when nothing is selected (legitimate for an all-comment block) and
# 2 on a real error, so only the latter propagates.
code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

# Strip JavaScript comments. backport.yaml's logic lives in a github-script
# `script: |` block, where comments start with `//` and survive code_lines
# untouched — and that block's comments name the very APIs these tests assert
# are gone, so filtering on `#` alone would let a comment satisfy a pin.
script_lines() {
  local rc=0
  grep -v '^[[:space:]]*//' || rc=$?
  [ "$rc" -le 1 ]
}

# Body of one job, up to the next top-level job key.
job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
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
  [ -f "$BACKPORT" ]
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

# ── backport target resolution ───────────────────────────────────────────────
# Both assertions below run over script_lines: this logic sits in a
# github-script block whose comments discuss getLatestRelease and min-1 by name.

@test "backport targets come from enumerated branches, not the latest release" {
  block="$(job_block prepare "$BACKPORT")"
  [ -n "$block" ]

  # Enumerate real branches. getLatestRelease returns the newest published
  # STABLE, which throughout a freeze window still names the PREVIOUS line — so
  # a `backport` on a fix for the release being stabilised would land on the
  # wrong branch and miss the release it was written for.
  printf '%s\n' "$block" | script_lines | grep -qF 'github.paginate(github.rest.repos.listBranches'
  count="$(printf '%s\n' "$block" | script_lines | grep -cF 'getLatestRelease' || true)"
  [ "${count:-0}" -eq 0 ]

  # Only branches matching release-<major>.<minor> qualify, so per-release
  # staging branches (release-1.6.1) and rc branches never become targets.
  printf '%s\n' "$block" | script_lines | grep -qF 'release-(\d+)\.(\d+)$'

  # previous is the second-newest EXISTING line, never an arithmetic guess. The
  # old min-1 derivation named a non-existent branch whenever a minor was
  # skipped.
  printf '%s\n' "$block" | script_lines | grep -qF 'lines.length > 1 ? lines[1].name'
  count="$(printf '%s\n' "$block" | script_lines | grep -cE 'parseInt\(min\)[[:space:]]*-[[:space:]]*1|min[[:space:]]*-[[:space:]]*1' || true)"
  [ "${count:-0}" -eq 0 ]

  # Asking for a previous line when only one exists must fail, not silently
  # backport nowhere.
  printf '%s\n' "$block" | script_lines | grep -qF 'core.setFailed'
}

@test "release lines sort numerically descending so 1.10 outranks 1.9" {
  block="$(job_block prepare "$BACKPORT")"
  [ -n "$block" ]

  # Numeric descending on major then minor is the ONLY reason release-1.10
  # ranks above release-1.9. A slip to the default lexicographic sort compares
  # the names as strings, puts "release-1.9" first, and aims every backport at
  # a stale line — with nothing going red.
  printf '%s\n' "$block" | script_lines | grep -qF '.sort((a, b) => (b.maj - a.maj) || (b.min - a.min))'

  # The comparands must be numbers, not the captured strings.
  printf '%s\n' "$block" | script_lines | grep -qF 'maj: Number(m[1]), min: Number(m[2])'

  # And the head of that sort is what `backport` uses.
  printf '%s\n' "$block" | script_lines | grep -qF 'lines[0].name'
}

# ── skip a target whose backport already merged ─────────────────────────────
# A second label event on a PR whose first backport already merged (e.g.
# backport-previous added after backport merged) re-enters this job for the
# already-delivered target. The cherry-pick is then empty, which the action's
# draft_commit_conflicts handling misreads as a real conflict and reports as a
# failed backport on the original PR — this guard skips the re-run instead.

@test "the guard checks merged, not just closed, PRs on the deterministic branch" {
  block="$(job_block backport "$BACKPORT")"
  [ -n "$block" ]

  # The branch name the action itself would have used. Checking anything else
  # answers a different question than "did THIS backport already land".
  printf '%s\n' "$block" | script_lines | grep -qF 'backport-${context.payload.pull_request.number}-to-${targetBranch}'

  # Closed alone is not enough: a closed-without-merge PR is a stale or
  # abandoned attempt, not proof the change reached the target. Loosening this
  # to any closed PR would skip a real retry after a conflict was closed
  # unmerged.
  printf '%s\n' "$block" | script_lines | grep -qF "state: 'closed'"
  printf '%s\n' "$block" | script_lines | grep -qF 'p.merged_at !== null'
}

@test "both the checkout and the backport-action step are gated on the guard" {
  block="$(job_block backport "$BACKPORT")"
  [ -n "$block" ]

  # Losing the gate on either step still runs the action against a target that
  # already has the change, reproducing the false comment the guard exists to
  # prevent.
  count="$(printf '%s\n' "$block" | code_lines | grep -cF "steps.guard.outputs.already_merged != 'true'" || true)"
  [ "${count:-0}" -eq 2 ]
}

@test "the guard runs before the checkout and the backport-action step" {
  guard="$(step_line 'Check for existing merged backport' "$BACKPORT")"
  checkout="$(step_line 'Checkout repository' "$BACKPORT")"
  create="$(step_line 'Create back‑port PR' "$BACKPORT")"
  [ -n "$guard" ] && [ -n "$checkout" ] && [ -n "$create" ]

  [ "$guard" -lt "$checkout" ]
  [ "$guard" -lt "$create" ]
}

# Mirrors release-changelog-behaviour.bats's convention for a github-script
# snippet: extract the decision expression verbatim in shape and execute it
# under node, because a grep for "merged_at" cannot tell an `.some()` from an
# `.every()` or a flipped comparison. Two cases are the ones that matter and
# are NOT interchangeable: the first-ever run (branch never existed, so the
# API returns an empty list) and a real retry (someone closed a conflicting
# backport PR without merging it). Both must NOT skip, or either the very
# first backport attempt or a legitimate re-creation after a manual close
# never runs.
@test "the guard's merged decision is correct on empty, closed-unmerged and closed-merged lists" {
  command -v node >/dev/null || { echo "node unavailable; skipping"; return 0; }
  tmp="$(mktemp -d)"

  cat > "$tmp/guard.js" <<'JS'
function alreadyMerged(prs) {
  return prs.some(p => p.merged_at !== null);
}
const cases = {
  'first-run-empty': [],
  'closed-unmerged': [{ merged_at: null }],
  'closed-merged':   [{ merged_at: '2026-01-01T00:00:00Z' }],
};
const lines = Object.entries(cases).map(([name, prs]) => name + '=' + alreadyMerged(prs));
require('fs').writeFileSync(1, lines.join('\n') + '\n');
JS

  node "$tmp/guard.js" > "$tmp/out.txt" 2>"$tmp/err.txt" || {
    echo "node failed:" >&2; cat "$tmp/err.txt" >&2; exit 1; }

  want="first-run-empty=false
closed-unmerged=false
closed-merged=true"
  got="$(cat "$tmp/out.txt")"
  [ "$got" = "$want" ] || {
    echo "guard decision table is wrong." >&2
    echo "want:" >&2; printf '%s\n' "$want" | sed 's/^/  /' >&2
    echo "got:"  >&2; printf '%s\n' "$got"  | sed 's/^/  /' >&2
    exit 1
  }

  # The mirror must match the real guard's exact decision expression, not just
  # the presence of "merged_at" somewhere in the step. Wrapped like the other
  # tests in this file's final assertion: a bare failing command here, under a
  # trap-installed EXIT handler, has been observed to abort the whole bats run
  # instead of just this test on macOS.
  block="$(job_block backport "$BACKPORT")"
  [ -n "$block" ]
  printf '%s\n' "$block" | script_lines | grep -qF 'prs.some(p => p.merged_at !== null)' || {
    echo "backport.yaml's guard no longer uses prs.some(p => p.merged_at !== null)." >&2; exit 1; }

  rm -rf "$tmp"
}
