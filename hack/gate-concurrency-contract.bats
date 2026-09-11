#!/usr/bin/env bats

# Contract for the invariants that decide who owns, publishes and skips the
# required "E2E Tests" commit status (see docs/agents/e2e-testing.md §10): the
# concurrency keys, the publishing points, and the docs-only decision the two
# lanes have to answer identically.
#
# The gate is a status, not a job, so a run that dies without posting leaves the
# head SHA at "Expected" and blocks the merge forever. That makes the pairing
# below load-bearing: `e2e-fork.yaml`'s `resolve` declines to post for a
# triggering run that concluded `cancelled`/`skipped`, and the workflow's
# concurrency key must put exactly those runs in a group of their own so a run
# that posts nothing cannot cancel the run that does. The two lists live in
# different languages in different halves of the file and can drift apart in a
# way no reviewer diff makes obvious — a third discarded conclusion added to the
# JS guard alone silently restores the wedge. These tests pin executable lines
# only: a commented-out key must never satisfy the contract.
#
# Scope, so the pin is not read as covering more than it does: `resolve` has a
# THIRD silent path, taken when the triggering run succeeded but its `Plan
# build` job skipped, and the concurrency key does not represent it. That is
# deliberate and currently unreachable — every job in pull-requests.yaml either
# carries the discarded-label guard or reaches `plan` through its `needs`, some
# of them only via another job's result rather than by reading plan's outputs
# directly, so a skipped `plan` means a skipped run, which the conclusion check
# already catches. Transitivity is the load-bearing part of that claim. It
# stops being unreachable the moment a job is added that runs independently of
# `plan`, and the extraction below, which matches on `CONCLUSION === '…'`,
# will not notice. Whoever adds such a job owns that.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
FORK="$REPO_ROOT/.github/workflows/e2e-fork.yaml"
PULL_REQUESTS="$REPO_ROOT/.github/workflows/pull-requests.yaml"

# Drop YAML `#` and JavaScript `//` comment lines, so a commented-out key can
# never satisfy a pin. grep exits 1 when it selects nothing, legitimate here,
# and 2 on a real error; the `rc` check turns only the latter into a failure.
# It does so under the bats binary and not under `hack/cozytest.sh`, whose
# translator appends `return 0` to every line that is exactly `}`, file-level
# helpers included, so a helper's last command never decides its status there.
# Either way the tests fail closed: each one asserts its extraction is
# non-empty before comparing, so a swallowed grep error surfaces as empty
# input rather than as a pass.
# Lines of one job's block, so an assertion can be scoped to the job it is
# about instead of matching an identical line in a different one.
job_block() {
  awk -v job="  $1:" '
    $0 == job { inside = 1; next }
    /^  [a-zA-Z0-9_-]+:$/ { inside = 0 }
    inside' "$2"
}

code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' | grep -v '^[[:space:]]*//' || rc=$?
  [ "$rc" -le 1 ]
}

# Every job name in a workflow, so a per-job assertion covers the jobs that
# exist rather than the ones whoever wrote the test remembered. Scoped to the
# `jobs:` mapping: two-space bare keys also occur under `on:`, and one of those
# would otherwise be treated as a job whose "block" runs to the first real one.
job_names() {
  awk '
    /^jobs:$/ { inside = 1; next }
    /^[a-zA-Z0-9_-]+:/ { inside = 0 }
    inside && /^  [a-zA-Z0-9_-]+:$/ { sub(/:[[:space:]]*$/, ""); sub(/^  /, ""); print }
  ' "$1"
}

# Conclusions the concurrency key routes into a per-run group.
group_key_conclusions() {
  code_lines < "$FORK" \
    | grep '^  group: e2e-fork-' \
    | grep -o "workflow_run\.conclusion == '[a-z_]*'" \
    | sed "s/.*'\\(.*\\)'/\\1/" \
    | sort -u
}

# The label names one guard tests for, as a set. `!=` reads the concurrency
# key, whose exclusions are the labels kept OUT of the `-label` group; `==`
# reads a job guard, whose matches are the labels that job runs for. Those two
# sets have to be identical, so they are extracted by one function rather than
# counted separately: a count is satisfied by the wrong labels and needs raising
# by hand every time a publishing label is added, which is how it stops being
# the invariant and becomes a number.
# The character class admits `/` so a namespaced label (`e2e/run`) is seen. A
# class that misses a label name does not fail: both sides simply stop
# mentioning it, the two sets still match, and the complement contract silently
# stops covering it.
label_names() {
  grep -o "github\.event\.label\.name $1 '[a-z0-9/-]*'" \
    | sed "s/.*'\\(.*\\)'/\\1/" \
    | sort -u
}

# One guard's review-axis predicates, normalised to the fact each one asserts.
# The admission side is written with `==`/`contains` and the concurrency key's
# complement with `!=`/`!contains`, so both reduce to the same three tokens and
# the same requirement can be put to either. Normalising rather than matching
# the whole clause is deliberate: the whole-clause pin this replaces lived in
# promote-gate-contract.bats, broke on every unrelated edit to `plan`'s guard,
# and still could not tell a weakened clause from a reworded one.
review_predicates() {
  grep -oE "github\.event\.review\.state [!=]= 'approved'|github\.event\.pull_request\.state [!=]= 'open'|!?contains\(fromJSON\('\[\"OWNER\",\"MEMBER\",\"COLLABORATOR\"\]'\), github\.event\.review\.author_association\)" \
    | sed -e 's/.*review\.state.*/approved/' \
          -e 's/.*pull_request\.state.*/open/' \
          -e 's/.*author_association.*/write-access/' \
    | sort -u
}

# Which way round those predicates are written, so the key cannot pass by
# carrying the admission form. A key that admits what `plan` admits puts the
# publishing run in the `-noop` group and skips the run that was going to post.
review_polarity() {
  grep -oE "github\.event\.review\.state [!=]= 'approved'" \
    | sed -E 's/.*state (..) .*/\1/' \
    | sort -u
}

# Conclusions `resolve` treats as "not a verdict" and posts nothing for.
resolve_silent_conclusions() {
  code_lines < "$FORK" \
    | grep '^ *if (CONCLUSION ===' \
    | grep -o "CONCLUSION === '[a-z_]*'" \
    | sed "s/.*'\\(.*\\)'/\\1/" \
    | sort -u
}

@test "fork gate: a run that posts no status cannot cancel the run that does" {
  group="$(group_key_conclusions)"
  silent="$(resolve_silent_conclusions)"

  # Guard the instrument: an empty side would make the comparison vacuous.
  [ -n "$silent" ]
  [ -n "$group" ]

  [ "$group" = "$silent" ]
}

@test "fork gate: the per-run discriminator is the triggering run id" {
  line="$(code_lines < "$FORK" | grep '^  group: e2e-fork-')"
  [ -n "$line" ]

  count="$(printf '%s\n' "$line" | grep -o 'github\.event\.workflow_run\.id' | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 1 ]
}

@test "fork gate: real runs still supersede each other on fork repo + branch" {
  line="$(code_lines < "$FORK" | grep '^  group: e2e-fork-')"
  [ -n "$line" ]

  count="$(printf '%s\n' "$line" | grep -o 'workflow_run\.head_repository\.full_name' | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 1 ]

  count="$(printf '%s\n' "$line" | grep -o 'workflow_run\.head_branch' | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 1 ]

  count="$(code_lines < "$FORK" | grep -c '^  cancel-in-progress: true$' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "same-repo gate: only a label run that publishes nothing is moved aside" {
  line="$(code_lines < "$PULL_REQUESTS" | grep '^  group: pr-')"
  [ -n "$line" ]

  count="$(printf '%s\n' "$line" | grep -o "github\.event\.action == 'labeled'" | wc -l | tr -d ' ')"
  [ "${count:-0}" -eq 1 ]

  # The group key must EXCLUDE the publishing labels, not every label: a run
  # that posts `E2E Tests` has to stay in the main group and supersede the head
  # run, or both publish the same context and the later narrower green erases
  # the earlier full-suite failure. The pin is the SET, not its size — the key's
  # condition is written as the exact complement of `plan`'s guard, so excluding
  # a label `plan` skips for (or admitting one it runs for) is the drift that
  # reintroduces the wedge, at any number of labels.
  excluded="$(printf '%s\n' "$line" | label_names '!=')"
  admitted="$(job_block plan "$PULL_REQUESTS" | code_lines | label_names '==')"
  [ -n "$excluded" ]
  [ -n "$admitted" ]
  [ "$excluded" = "$admitted" ]

  # One publishing label is same-repo only, so both halves carry a fork term as
  # well — `release` on a fork PR is always a mislabel, and letting such a run
  # into the main group makes e2e-fork.yaml post red on a suite that was never
  # going to run. Complementary again: NOT(… OR (C AND NOT F)) keeps `OR F`.
  # Dropping the term from one side alone is what re-opens the wedge this whole
  # test is about, and neither side reads wrong on its own.
  count="$(printf '%s\n' "$line" | grep -c 'github\.event\.pull_request\.head\.repo\.fork' || true)"
  [ "${count:-0}" -eq 1 ]
  # `plan`'s own guard, not its steps': one of those carries the same bare
  # expression for an unrelated reason (see the opener assertion below).
  count="$(job_block plan "$PULL_REQUESTS" | code_lines | grep '^    if:' \
    | grep -c 'github\.event\.pull_request\.head\.repo\.fork' || true)"
  [ "${count:-0}" -eq 1 ]

  count="$(code_lines < "$PULL_REQUESTS" | grep -c '^  cancel-in-progress: true$' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "both lanes open the status before they conclude it" {
  # A commit status has no expiry: whatever was last written for (sha, context)
  # stands until something overwrites it. So a run starting against a head SHA
  # that already went green must claim the context up front, or the PR stays
  # mergeable on the previous run's verdict for the whole of this one. Each
  # lane therefore writes `E2E Tests` from two places — an opener and a
  # terminal report — and dropping either half is invisible until someone
  # merges during a re-run.
  #
  # Scope: this pins that each lane HAS an opener, not that the opener lands
  # before the window opens. Only the same-repo one does, being `plan`'s first
  # step. The fork opener sits in the privileged workflow, which starts only
  # after the unprivileged build has finished, so on that lane the window is
  # bounded by the build rather than closed. A grep cannot express the
  # difference; read this test as "both openers exist" and §10 of
  # docs/agents/e2e-testing.md for when each of them lands.
  #
  # Every assertion is scoped to the job that owns it. Counting writers per
  # FILE does not work and looked like it did: on the fork lane the context
  # string occurs in the shared `setStatus` helper and in `report`, so the
  # opener — which calls that helper rather than naming the context itself —
  # adds no occurrence, and deleting it left the count at two. A pin has to
  # name the call it is protecting, not a string that happens to appear twice.

  # Same-repo lane: `plan` opens, `e2e-report` concludes.
  plan_block="$(job_block plan "$PULL_REQUESTS" | code_lines)"
  [ -n "$plan_block" ]

  # The opener must be pending, or it publishes a verdict it has not reached.
  count="$(printf '%s\n' "$plan_block" | grep -c "state: 'pending'" || true)"
  [ "${count:-0}" -eq 1 ]

  # …and same-repo only: a fork's pull_request token cannot write a status, so
  # an unguarded call would 403 and fail this job on every fork PR. Matched as
  # the whole step guard, not on the identifier: the same bare guard sits on
  # steps in several other jobs, and `plan`'s own `if:` now names the same
  # expression too, so counting the identifier inside this block would be
  # satisfied by that one with the step's guard deleted.
  #
  # The second half of that guard is what stops the opener wedging the gate on
  # the good path. A run that neither re-verifies the SHA nor concludes the
  # context — a second approving review, a reopen, a re-run of a run that
  # passed — has to leave the existing green alone, because `e2e-report`
  # deliberately posts nothing for it and the `pending` this call would write
  # would then have nothing to conclude it. Pinned together with the fork term
  # rather than beside it: the two halves are one decision about whether this
  # run owns the status.
  count="$(printf '%s\n' "$plan_block" \
    | grep -cF "if: \${{ !github.event.pull_request.head.repo.fork && !(steps.heavy.outputs.heavy != 'true' && steps.heavy.outputs.prior == 'success') }}" || true)"
  [ "${count:-0}" -eq 1 ]

  block="$(job_block e2e-report "$PULL_REQUESTS" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" | grep -c "context: 'E2E Tests'" || true)"
  [ "${count:-0}" -eq 1 ]

  # Fork lane: `resolve` opens, `report` concludes. The opener goes through the
  # helper, so pin the call.
  block="$(job_block resolve "$FORK" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" | grep -c "setStatus('pending'" || true)"
  [ "${count:-0}" -eq 1 ]

  block="$(job_block report "$FORK" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" | grep -c "context: 'E2E Tests'" || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "neither terminal reporter publishes from a cancelled run" {
  # `always()` includes cancellation; `!cancelled()` does not. A cancelled run
  # has been superseded, so the run that replaced it owns the head SHA, and a
  # verdict posted from the dying one lands on a suite that never finished with
  # nothing obliged to correct it. Both lanes had this wrong at different times,
  # in the same shape, which is why it is pinned rather than described.
  block="$(job_block e2e-report "$PULL_REQUESTS" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" | grep -c '!cancelled()' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$block" | grep -c 'if: .*always()' || true)"
  [ "${count:-0}" -eq 0 ]

  # The fork reporter's guard only; `always()` is legitimate on cleanup steps in
  # other jobs, which is why this is scoped to `report` and anchored on `if:`.
  block="$(job_block report "$FORK" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" | grep -c '^    if: .*!cancelled()' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$block" | grep -c '^    if: .*always()' || true)"
  [ "${count:-0}" -eq 0 ]
}

@test "both lanes answer docs-only with both sides of a rename" {
  # The two lanes decide docs-only from different sources — a git diff in
  # `plan`, the PR file list in `resolve` — and they must agree. `git diff
  # --name-only` reports a rename as its new path alone, so a code file moved
  # into docs/ reads as docs-only there while the fork lane, which sees
  # `previous_filename`, reads it as code. Disagreement is not a near-miss: the
  # fork lane then requires an artifact the same-repo lane never built, and the
  # PR is unfixably red. `plan` therefore reads the rename-blind list.
  plan_block="$(job_block plan "$PULL_REQUESTS" | code_lines)"
  [ -n "$plan_block" ]

  count="$(printf '%s\n' "$plan_block" | grep -c "grep -qvE '\^docs/' /tmp/changed_norenames.txt" || true)"
  [ "${count:-0}" -eq 1 ]

  # The rename-detected list may still feed the build matrix, but never this.
  count="$(printf '%s\n' "$plan_block" | grep -c "grep -qvE '\^docs/' /tmp/changed.txt" || true)"
  [ "${count:-0}" -eq 0 ]

  # The fork half of the same agreement: both sides of a rename are inspected.
  # Pinned on the docs-only expression itself — `previous_filename` also appears
  # in the TIA file list a few lines down, so counting the identifier would stay
  # satisfied by that one with this check deleted.
  block="$(job_block resolve "$FORK" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" \
    | grep -c 'f\.previous_filename && !isDocs(f\.previous_filename)' || true)"
  [ "${count:-0}" -eq 1 ]
}

@test "same-repo gate: every publishing guard names the same label set" {
  # `plan`, `verify-release-candidate`, `e2e-report` and the concurrency key
  # each decide from the label name whether this run owns the status. Renaming
  # a label in one of them, or teaching one about a label the others do not
  # know, splits the decision without changing a line that reads load-bearing.
  # `plan` is the reference: the key's condition is its exact complement, and
  # the other two are required to run if and only if it does.
  expected="$(job_block plan "$PULL_REQUESTS" | code_lines | label_names '==')"
  [ -n "$expected" ]

  # Agreement among the guards that exist is not the same as the guard being
  # present, and the two jobs fail in opposite directions. Deleting
  # `e2e-report`'s copy leaves the rest agreeing with each other while a
  # discarded label event starts publishing again and clobbers the verdict of
  # the run that actually tested the SHA. Narrowing `verify-release-candidate`'s
  # instead leaves it SKIPPED under an `e2e-report` that still runs, which reads
  # the non-success candidate result and posts a red on a release PR nobody
  # touched. Require the guard per job, not just consistency across the file.
  for job in plan verify-release-candidate e2e-report; do
    block="$(job_block "$job" "$PULL_REQUESTS" | code_lines)"
    [ -n "$block" ]
    names="$(printf '%s\n' "$block" | label_names '==')"
    [ -n "$names" ]
    [ "$names" = "$expected" ]
  done

  # …and every OTHER job that decides from a label name decides from the same
  # set. Enumerated from the file rather than listed here, because the list is
  # what went wrong: `release` was added to `plan`, `e2e-report` and the key,
  # and `resolve_assets` — which sits on `e2e`'s release arm — was missed. A
  # named-job loop cannot notice a job nobody thought to name. A job with NO
  # label guard is not a violation: most of the file legitimately has none, and
  # requiring one everywhere would pin an unrelated design decision.
  for job in $(job_names "$PULL_REQUESTS"); do
    names="$(job_block "$job" "$PULL_REQUESTS" | code_lines | label_names '==')"
    [ -z "$names" ] || [ "$names" = "$expected" ]
  done

  # Nothing outside a job block may introduce a name none of them know.
  all="$(code_lines < "$PULL_REQUESTS" | label_names '==')"
  [ "$all" = "$expected" ]
}

@test "same-repo gate: every publishing guard names the same review term" {
  # The review axis is the second dimension of the same decision the label
  # tests above pin, and it was invisible to all of them: `label_names()` reads
  # `github.event.label.name` and a review submission carries none, so a guard
  # could gain, lose or weaken its review clause without moving a single
  # assertion. `plan` is the reference here as it is there.
  expected="$(job_block plan "$PULL_REQUESTS" | code_lines | review_predicates)"
  [ -n "$expected" ]

  # Three facts, not one. `approved` alone admits a review from any account
  # with read access, which on a public repository is everyone, and each
  # submission re-fires the event, so it would let anyone start the 215-minute
  # job on any open same-repo PR as often as they liked; `pull_request_review`
  # also fires on a closed PR as readily as an open one. The ruleset counts
  # approvals from write access only, so a guard that admits less than all
  # three is looser than the gate it stands in for.
  [ "$(printf '%s\n' "$expected" | wc -l | tr -d ' ')" -eq 3 ]

  # Required per job, for the reason the label test gives: `verify-release-candidate`
  # and `resolve_assets` reach `plan` through no `needs`, so a missing clause
  # there is not a disagreement between guards but a job that runs when `plan`
  # does not. `e2e-report` is the one that shipped without it and posted red on
  # a `changes_requested` submission (run 33750114876).
  for job in plan verify-release-candidate resolve_assets e2e-report; do
    block="$(job_block "$job" "$PULL_REQUESTS" | code_lines)"
    [ -n "$block" ]

    names="$(printf '%s\n' "$block" | review_predicates)"
    [ "$names" = "$expected" ]

    polarity="$(printf '%s\n' "$block" | review_polarity)"
    [ "$polarity" = "==" ]

    count="$(printf '%s\n' "$block" | grep -c "github\.event_name != 'pull_request_review'" || true)"
    [ "${count:-0}" -eq 1 ]
  done

  # …and the key is the complement, term for term and the other way round.
  line="$(code_lines < "$PULL_REQUESTS" | grep '^  group: pr-')"
  [ -n "$line" ]

  names="$(printf '%s\n' "$line" | review_predicates)"
  [ "$names" = "$expected" ]

  polarity="$(printf '%s\n' "$line" | review_polarity)"
  [ "$polarity" = "!=" ]

  count="$(printf '%s\n' "$line" | grep -c "github\.event_name == 'pull_request_review'" || true)"
  [ "${count:-0}" -eq 1 ]

  # Every OTHER job either carries the same term or reaches `plan` through its
  # `needs`. Unlike the label loop this requires one or the other rather than
  # tolerating neither, which closes the enumeration hole that loop documents:
  # a job added with no guard and no dependency on `plan` is exactly the job
  # that runs on a discarded submission, and nobody has to remember to name it
  # here.
  for job in $(job_names "$PULL_REQUESTS"); do
    block="$(job_block "$job" "$PULL_REQUESTS" | code_lines)"
    names="$(printf '%s\n' "$block" | review_predicates)"
    if [ -n "$names" ]; then
      [ "$names" = "$expected" ]
    else
      count="$(printf '%s\n' "$block" | grep -c '^    needs:.*"plan"' || true)"
      [ "${count:-0}" -eq 1 ]
    fi
  done
}

@test "the expensive lane is gated on plan's decision, and that decision is computed" {
  # What moves the suite off "every push" is one term in `e2e`'s guard reading
  # one output of `plan`. Both halves are pinned because either can be undone
  # without touching the other, and undoing either restores per-push running —
  # the change this whole contract exists to hold in place — while every
  # assertion above stays green.
  block="$(job_block e2e "$PULL_REQUESTS" | code_lines)"
  [ -n "$block" ]
  count="$(printf '%s\n' "$block" | grep -cF "needs.plan.outputs.heavy == 'true'" || true)"
  [ "${count:-0}" -eq 1 ]

  plan_block="$(job_block plan "$PULL_REQUESTS" | code_lines)"
  [ -n "$plan_block" ]
  count="$(printf '%s\n' "$plan_block" | grep -cF 'heavy: ${{ steps.heavy.outputs.heavy }}' || true)"
  [ "${count:-0}" -eq 1 ]

  # …and the step decides rather than asserts. Every write of the output is
  # computed — there are two, one per axis, and both go through `String()` — so
  # replacing the decision with a constant cannot leave both counts intact.
  # That mutation is the one worth pinning: it silently restores per-push
  # running and reads like a simplification.
  count="$(printf '%s\n' "$plan_block" | grep -cF "core.setOutput('heavy'" || true)"
  [ "${count:-0}" -eq 2 ]
  count="$(printf '%s\n' "$plan_block" | grep -cF "core.setOutput('heavy', String(" || true)"
  [ "${count:-0}" -eq 2 ]

  # The two axes it decides from: the label EVENT, and whether this head SHA
  # already carries a green verdict to reuse. The status read happens once and
  # is published as `prior`, because the opener's guard needs the same reading
  # and the opener has already overwritten it by the time anything else could
  # look.
  count="$(printf '%s\n' "$plan_block" | grep -cF "context.eventName !== 'pull_request_review'" || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$plan_block" | grep -cF 'getCombinedStatusForRef' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$plan_block" | grep -cF "prior !== 'success'" || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$plan_block" | grep -cF "core.setOutput('prior', prior)" || true)"
  [ "${count:-0}" -eq 1 ]
}
