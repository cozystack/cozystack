#!/usr/bin/env bats

# Contract for the rc freeze, the backport target resolution that depends on it,
# and the triggering and concurrency rules that decide whether a backport runs at
# all. Like promote-gate-contract.bats, these tests pin executable/structural
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

# ── backport concurrency ─────────────────────────────────────────────────────
# A run joins its concurrency group before `prepare`'s guard is evaluated, so
# nothing in that guard can keep a label event from disturbing the run already
# backporting the PR. The group key and `cancel-in-progress` close different
# halves of that and are pinned separately: either one alone still leaves a
# live failure, and neither reads load-bearing on its own.

@test "the trigger surface the concurrency key is built for does not widen" {
  # Both halves of the key are written against one trigger delivering `closed` or
  # `labeled`, and neither degrades safely if anything else arrives. The group key
  # routes only `labeled` aside, so a new event lands in the main group;
  # `cancel-in-progress` is positive, so it does not cancel there; and `prepare`
  # does not qualify it. The result occupies the single pending slot, evicts a
  # genuine backport request, and delivers nothing.
  #
  # Pinned as the whole surface rather than just the types list, because a SIBLING
  # trigger reaches the same harm without touching `types:` at all: anything
  # carrying a `pull_request` payload resolves the PR number and so lands in the
  # same per-PR group. `pull_request_review` is the realistic one.
  #
  # Deliberately strict: this also fails on a widening that would be harmless,
  # `workflow_dispatch` for instance, whose payload carries no `pull_request` so
  # the key degenerates to a shared group holding no genuine request. Separating
  # the harmless case needs a list of which triggers carry that payload, and that
  # list drifts. A red here means read the block above, not that the pin is wrong.
  triggers="$(awk '/^on:/{inside=1;next} /^[a-z]/{inside=0} inside && /^  [a-z_]+:/{print $1}' "$BACKPORT")"
  [ -n "$triggers" ]
  [ "$(printf '%s\n' "$triggers" | wc -l | tr -d ' ')" -eq 1 ]
  printf '%s\n' "$triggers" | grep -qF 'pull_request_target:'

  printf '%s\n' "$(code_lines < "$BACKPORT")" | grep -qF 'types: [closed, labeled]'
}

@test "a label event that requests no backport gets its own concurrency group" {
  line="$(code_lines < "$BACKPORT" | grep '^  group: backport-')"
  [ -n "$line" ]

  # The per-PR discriminator, first: without it every PR's backport shares one
  # group, and since a merge still cancels in that group, merging one PR kills
  # another's in-flight backport. The assertions below all describe how a label
  # event is separated from a merge WITHIN one PR, and every one of them stays
  # green with the PR number deleted, so the widest failure of the three is the
  # one nothing else here would notice.
  printf '%s\n' "$line" | grep -qF 'backport-${{ github.workflow }}-${{ github.event.pull_request.number }}${{'

  count="$(printf '%s\n' "$line" | grep -o "github\.event\.action == 'labeled'" | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 1 ]

  # The split is on the label NAMES, not on the action alone. Routing EVERY label
  # event aside would move `backport` and `backport-previous` out of the group
  # holding the run they have to queue behind, and one request could then evict
  # the other.
  #
  # Pinned as the literal pair, not as a count of `label.name != '`. That count
  # measures the operator and says nothing about the operands, so renaming one
  # name here and not in `prepare` — the half-finished rename — left it at two
  # and stayed green, while a real `backport-previous` event took the `-label`
  # branch and became free to cherry-pick alongside the merge run.
  # Including the conjunction that joins the action check to the name checks.
  # `&&` binds tighter than `||` in GitHub expressions, so flipping this one
  # operator reads as `action == 'labeled' || (name != … && name != …)`, and on a
  # `closed` event `github.event.label` is absent, both name checks are true, and
  # EVERY event takes the suffix. A constant suffix is exactly as vacuous as no
  # split at all. Every operand here was pinned before this line existed; the
  # operator joining them was not.
  printf '%s\n' "$line" | grep -qF "github.event.action == 'labeled' && github.event.label.name != 'backport' && github.event.label.name != 'backport-previous'"

  # And exactly two of them. The literal above is a substring check, so appending
  # a third exclusion satisfies it unchanged — and a third exclusion is not
  # cosmetic: the named label stops being routed aside, lands in the main group,
  # takes its single pending slot, skips in `prepare`, and evicts a genuine
  # backport request while delivering nothing. Same harm as widening `types:`.
  # Presence and count answer different questions; this test needs both.
  count="$(printf '%s\n' "$line" | grep -o "github\.event\.label\.name != '" | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 2 ]

  # The suffix the condition selects, not only the condition. Asserting the
  # operands alone leaves the whole split deletable — collapsing the tail to
  # `&& '' || ''` puts every label back in the main group with the condition
  # still reading correctly above it. Inverting it to `&& '' || '-label'` is
  # worse than deleting it, because that moves the two backport requests aside
  # and leaves the irrelevant labels sharing the group with the run in flight,
  # which is the arrangement this file exists to prevent. Both survive every
  # other assertion here.
  printf '%s\n' "$line" | grep -qF "&& '-label' || ''"
}

@test "a labeled event queues behind the backport in flight instead of cancelling it" {
  # Labels set with the default GITHUB_TOKEN start no run, but labels set by
  # third-party GitHub Apps do, and they arrive in bursts. Under a plain `true`
  # each one killed the backport the merge had just started, leaving a cancelled
  # run next to a green one and the work repeated by whichever label came last.
  # With `prepare` now narrowed, no burst label requalifies to repeat it,
  # so dropping this arm does not restore the old noisy delivery — it loses the
  # backport. `cancel-in-progress: true` is the obvious thing for the next reader
  # to normalise this back to, so pin its absence too.
  count="$(code_lines < "$BACKPORT" | grep -cF "  cancel-in-progress: \${{ github.event.action == 'closed' }}" || true)"
  [ "${count:-0}" -eq 1 ]

  # Positively, not as `!= 'labeled'`. The two are equivalent only while `types:`
  # holds exactly `closed` and `labeled`; the negative form hands cancellation to
  # any third type added later, in the main group, which is the behaviour this
  # arm removes.
  count="$(code_lines < "$BACKPORT" | grep -c "cancel-in-progress:.*!= 'labeled'" || true)"
  [ "${count:-0}" -eq 0 ]

  count="$(code_lines < "$BACKPORT" | grep -c '^  cancel-in-progress: true$' || true)"
  [ "${count:-0}" -eq 0 ]
}

@test "both backport jobs bound how long a queued request can wait" {
  # These ceilings exist because of the queuing above, not for their own sake. A
  # cancelling key disposed of a stuck run by killing it; queuing makes the next
  # genuine request wait behind it instead, and an unbounded job on the 6-hour
  # default turns one wedged job into a six-hour hole in the release line.
  # Nothing else in this file makes them look load-bearing, so a tidy-up that
  # drops them reads as harmless.
  #
  # Scope, since the test name says "how long a queued request can wait": what is
  # bounded here is a stuck JOB. A run can wedge at the RUN level with every job
  # already finished, and `timeout-minutes` is job-level with no run-level
  # equivalent, so that state holds the group until the run is cancelled. These
  # ceilings do not reach it and are not meant to.
  # Asserted as a bound, not as presence. The hazard is the SIZE of the wait, so
  # a pin that only checks the key exists is satisfied by `timeout-minutes: 355`,
  # which restores the hole to within five minutes of the default it was written
  # against.
  #
  # Bounded on BOTH sides, because both directions break it. Too high restores
  # the hole; too low cancels a real backport that was only waiting for a runner,
  # and an upper-bound-only assertion stays green at `timeout-minutes: 1`.
  #
  # The floor is judgement, not measurement, and says so. `timeout-minutes`
  # bounds execution, and every execution measured on this workflow has been
  # seconds, so no observation argues for any particular floor. No maximum is
  # quoted deliberately: a superlative over a growing set goes stale on the next
  # slower run. What the floor guards is a ceiling set low enough to cancel a
  # cherry-pick much larger than anything seen yet; ten minutes is far above
  # every leg measured. Per JOB, not per run, and not over the runner queue,
  # which no `timeout-minutes` reaches.
  for job in prepare backport; do
    block="$(job_block "$job" "$BACKPORT" | code_lines)"
    [ -n "$block" ]
    minutes="$(printf '%s\n' "$block" | grep -o '^    timeout-minutes: [0-9][0-9]*' | grep -o '[0-9][0-9]*$')"
    [ -n "$minutes" ]
    [ "$minutes" -ge 10 ]
    [ "$minutes" -le 30 ]
  done
}

@test "each backport trigger reads only the labels it is entitled to" {
  block="$(job_block prepare "$BACKPORT" | code_lines)"
  [ -n "$block" ]

  # The cumulative label set answers for the merge and for nothing else. Left
  # ungated it re-enters this job on every later unrelated label — an automated
  # size/* or kind/* — for a merged PR still carrying `backport`, redoing a
  # backport that has already been delivered.
  printf '%s\n' "$block" | grep -qF "(github.event.action == 'closed' && (contains(github.event.pull_request.labels.*.name, 'backport') || contains(github.event.pull_request.labels.*.name, 'backport-previous')))"

  # And the guard reads that set exactly twice, both of them inside the gated
  # disjunct above, so it cannot be joined by an ungated one that quietly
  # restores the old behaviour while the assertion above stays green.
  #
  # Counted as OCCURRENCES, not lines. `grep -c` counts matching lines, and both
  # reads live on one line here, so it answers 1 and keeps answering 1 when a
  # third read is appended to that same line — which is exactly the restoration
  # this is meant to catch. Only an append on a NEW line would have moved it.
  count="$(printf '%s\n' "$block" | grep -o 'contains(github\.event\.pull_request\.labels' | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 2 ]

  # A label event answers for the label it carries.
  printf '%s\n' "$block" | grep -qF "(github.event.action == 'labeled' && (github.event.label.name == 'backport' || github.event.label.name == 'backport-previous'))"

  # The two conjuncts the label logic hangs off, which the label assertions above
  # would not miss. `base.ref == 'main'` is what stops the bot backporting its own
  # output: its PRs target release-X.Y, so they cannot satisfy it however often a
  # labeler re-applies `backport` to a `[Backport release-X.Y]` title. docs/release.md
  # calls that architectural protection, which is only true while the line is here.
  # With their trailing conjunctions, for the reason the group-key test spells
  # out: flipping either `&&` to `||` turns the guard into a disjunction that
  # fires on every merge to main and on labels applied to open PRs. That one does
  # not produce a wrong backport, because the `backport` job re-checks both
  # independently, but it runs the job when nothing asked for it.
  # In BOTH jobs, because docs/release.md rests the architectural protection on
  # both `if:` blocks. Losing either copy alone is survivable (the other still
  # skips, and `backport` needs `prepare`), so this pins a duplicate rather than
  # a hole — but an untested duplicate is how a duplicate stops being one.
  for job in prepare backport; do
    b="$(job_block "$job" "$BACKPORT" | code_lines)"
    [ -n "$b" ]
    printf '%s\n' "$b" | grep -qF "github.event.pull_request.base.ref == 'main' &&"
    printf '%s\n' "$b" | grep -qF 'github.event.pull_request.merged == true &&'
  done
}
