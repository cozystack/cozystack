#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the pure selection helpers in
# hack/e2e-capture-previous-logs.sh -- the logic that decides WHICH restarted
# containers get their previous instance dumped, and in what order:
#
#   - prevlog_filter_restarted -- keep only containers with restartCount > 0;
#   - prevlog_prioritize       -- put the failing test's namespace first;
#   - prevlog_cap              -- bound the dump so a wedged cluster cannot
#                                 explode the catch;
#   - prevlog_logfile_name     -- per-container artifact filename.
#
# The `kubectl logs --previous` capture itself is not unit-testable (it needs a
# live cluster with a crash-looping pod); these tests pin the derivations that
# decide what is worth asking for at all. The restart filter is the one that
# matters most: without it every healthy container would be asked for a
# previous instance that does not exist, turning the catch into a page of noise.
#
# Strategy: the script is sourced once with E2E_CAPTURE_PREVLOGS_LIB set, which
# the script's sourcing guard honours by defining the helpers and returning
# before it touches $1 or runs any capture -- so no cluster is required and the
# capture body never executes. Each @test then calls the helpers directly and
# asserts with `[ ... ]`, matching this repo's plain-shell bats convention (no
# `run` helper).
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/capture-previous-logs.bats
#           (or `bats hack/capture-previous-logs.bats` if the bats binary is
#           installed; cozytest.sh is the CI path.)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/e2e-capture-previous-logs.sh"

# Load the pure helpers. The guard returns before the capture body, so this is
# side-effect-free and needs no cluster.
E2E_CAPTURE_PREVLOGS_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

@test "filter restarted keeps only containers whose restart count is above zero" {
  rows="$(printf '%s\n' \
    'tenant-test|mariadb-test-0|mariadb|container|3' \
    'tenant-test|mariadb-test-1|mariadb|container|0' \
    'cozy-system|healthy|app|container|0' \
    'tenant-test|mariadb-test-0|init-datadir|init|2')"

  out="$(printf '%s\n' "$rows" | prevlog_filter_restarted)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
  printf '%s\n' "$out" | grep -q '^tenant-test|mariadb-test-0|mariadb|container|3$'
  printf '%s\n' "$out" | grep -q '^tenant-test|mariadb-test-0|init-datadir|init|2$'
  # `! cmd` is vacuous under cozytest's `set -e` (errexit is suppressed for a
  # `!`-negated pipeline), so a filter regression that let these rows through
  # would not fail the test. Assert the absence via `if cmd; then ...; false`.
  if printf '%s\n' "$out" | grep -q 'mariadb-test-1'; then echo "FAIL: must drop the zero-restart replica"; false; fi
  if printf '%s\n' "$out" | grep -q 'cozy-system'; then echo "FAIL: must drop the zero-restart system pod"; false; fi
}

@test "filter restarted drops rows with a malformed or empty restart count" {
  # A non-numeric count must be dropped rather than compared. The row would be
  # skipped either way (`[ "<none>" -gt 0 ]` exits 2, which the filter's
  # `|| continue` catches), so what this pins is the contract -- malformed rows
  # never reach the capture -- not a crash.
  rows="$(printf '%s\n' \
    'tenant-test|pod-a|app|container|' \
    'tenant-test|pod-b|app|container|<none>' \
    'tenant-test|pod-c|app|container|-1' \
    'tenant-test|pod-d|app|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_filter_restarted)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  printf '%s\n' "$out" | grep -q '^tenant-test|pod-d|'
}

@test "filter restarted drops rows missing a namespace pod or container field" {
  rows="$(printf '%s\n' \
    '|pod-a|app|container|4' \
    'tenant-test||app|container|4' \
    'tenant-test|pod-c||container|4' \
    'tenant-test|pod-d|app|container|4')"

  out="$(printf '%s\n' "$rows" | prevlog_filter_restarted)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  printf '%s\n' "$out" | grep -q '^tenant-test|pod-d|'
}

@test "prioritize puts the failing test namespace ahead of everything else" {
  rows="$(printf '%s\n' \
    'cozy-linstor|satellite-x|linstor|container|5' \
    'tenant-test|mariadb-test-0|mariadb|container|3' \
    'cozy-cilium|agent-y|cilium|container|2' \
    'tenant-test|mariadb-test-1|mariadb|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize tenant-test)"

  # All four survive -- prioritisation reorders, it never drops.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 4 ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)" = "mariadb-test-0" ]
  [ "$(printf '%s\n' "$out" | sed -n '2p' | cut -d'|' -f2)" = "mariadb-test-1" ]
  # Non-matching rows keep their relative input order behind the matches.
  [ "$(printf '%s\n' "$out" | sed -n '3p' | cut -d'|' -f2)" = "satellite-x" ]
  [ "$(printf '%s\n' "$out" | sed -n '4p' | cut -d'|' -f2)" = "agent-y" ]
}

@test "prioritize with an empty namespace passes every row through unchanged" {
  rows="$(printf '%s\n' \
    'cozy-linstor|satellite-x|linstor|container|5' \
    'tenant-test|mariadb-test-0|mariadb|container|3')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize '')"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)" = "satellite-x" ]
}

@test "prioritize matches the namespace field only and not a pod name substring" {
  # The match must anchor on the LEADING namespace field. The first row is the
  # fixture that makes this test bite: a pod named exactly like the namespace,
  # in a different namespace, so the row contains `tenant-test|` but does not
  # start with it. Drop the `^` from prevlog_prioritize and that row is promoted
  # ahead of the genuine namespace match below, flipping the first two
  # assertions. (A pod merely named `tenant-test-runner` does NOT distinguish
  # the two -- it lacks the trailing `|` and so matches neither form -- which is
  # why it is kept only as the third row rather than carrying the test.)
  # The second row is the other half of the anchor: a SIBLING NAMESPACE sharing
  # the prefix, which nested tenants make routine. It pins the trailing `|` --
  # drop that from the pattern and `tenant-test-child` is treated as the failing
  # test's own namespace and promoted, which is how the cap would get spent on
  # an unrelated tenant's restarts.
  rows="$(printf '%s\n' \
    'cozy-system|tenant-test|app|container|1' \
    'tenant-test-child|neighbour|app|container|1' \
    'cozy-system|tenant-test-runner|app|container|1' \
    'tenant-test|real|app|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize tenant-test)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 4 ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f1)" = "tenant-test" ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)" = "real" ]
  # Everything else keeps input order behind the single genuine match.
  [ "$(printf '%s\n' "$out" | sed -n '2p' | cut -d'|' -f2)" = "tenant-test" ]
  [ "$(printf '%s\n' "$out" | sed -n '3p' | cut -d'|' -f2)" = "neighbour" ]
  [ "$(printf '%s\n' "$out" | sed -n '4p' | cut -d'|' -f2)" = "tenant-test-runner" ]
}

@test "cap keeps at most the requested number of rows" {
  rows="$(printf '%s\n' \
    'ns|p1|c|container|1' 'ns|p2|c|container|1' 'ns|p3|c|container|1' \
    'ns|p4|c|container|1' 'ns|p5|c|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_cap 3)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 3 ]
  printf '%s\n' "$out" | grep -q '^ns|p1|'
  if printf '%s\n' "$out" | grep -q '^ns|p4|'; then echo "FAIL: cap must drop rows past the limit"; false; fi
}

@test "cap leaves a shorter input untouched" {
  rows="$(printf '%s\n' 'ns|p1|c|container|1' 'ns|p2|c|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_cap 12)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
}

@test "cap falls back to the default limit when given a malformed value" {
  # COZY_PREVLOG_MAX is operator-supplied, so a typo must degrade to the default
  # rather than make `head -n` fail and drop the whole capture.
  rows="$(i=1; while [ "$i" -le 20 ]; do printf 'ns|p%s|c|container|1\n' "$i"; i=$((i + 1)); done)"
  [ "$(printf '%s\n' "$rows" | grep -c .)" -eq 20 ]

  out="$(printf '%s\n' "$rows" | prevlog_cap 'twelve')"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 12 ]
}

@test "logfile name joins namespace pod and container with underscores" {
  name="$(prevlog_logfile_name tenant-test mariadb-test-0 mariadb)"

  [ "$name" = "tenant-test_mariadb-test-0_mariadb.log" ]
}
