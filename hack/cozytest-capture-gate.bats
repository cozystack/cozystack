#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Behavioural tests for the cluster-capture gate in hack/cozytest.sh.
#
# The runner arms its on-failure cluster captures from an EXIT trap. Those
# captures belong to the e2e suites, which run against a live cluster; a unit
# suite has no cluster to snapshot, so arming them there buys an empty capture
# and pays the capture's timeouts for it. The gate therefore keys on which kind
# of suite is running rather than on whether a kubectl binary happens to be
# installed, because a unit runner has the binary and no cluster.
#
# Both directions are asserted, and they share one fixture generator that
# differs only in the fixture's filename. That pairing is the point: the
# unit-side assertion is a negative one, and a negative assertion passes just as
# well when the harness is broken and nothing ran at all. The e2e-side test is
# the positive control for exactly that failure, and the unit-side test
# additionally pins that the run did fail, so "no captures" cannot be satisfied
# by "no test".
#
# The stub kubectl is what makes this a real test of the gate: with no kubectl
# on PATH every capture leg is skipped for a second reason and both directions
# would agree for the wrong cause.
#
# awk-transform constraint (hack/cozytest.sh rewrites this file before sourcing
# it): it turns any line beginning `@test "` into a function header, and any
# line that is exactly `}` into `return 0` plus the brace. Neither rule knows
# what a heredoc is. So a fixture written as a heredoc here has its own `@test`
# line harvested into THIS suite: the runner registers a phantom extra test,
# runs it first, and the fixture's `false` ends the run before any real test
# executes. Checked by building that variant rather than reasoned about -- it
# fails loudly rather than silently, but it fails the wrong thing, and the gate
# is then covered by nothing. The fixtures are built with printf, which puts the
# `@test` text inside a format string where no rule matches it.
#
# Title syntax constraints (also inherited from that parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/cozytest-capture-gate.bats
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
RUNNER="$HACK_DIR/cozytest.sh"

# Marker printed by the runner's data-plane capture leg. Chosen as one probe
# because the runner echoes it before invoking the capture, so the assertion
# reads the gate's decision rather than the capture's outcome.
DATAPLANE_MARKER='capturing host->pod data-plane'

# The gate covers three legs, and each leaves a distinct artefact under the
# snapshot directory. Pinning all three from the report tree is what keeps any
# one of them from being ungated without a test noticing: the echo above covers
# only the data-plane leg, and the crust-gather leg is the one whose cost is
# worst, since it needs no reachable cluster to fire, only an ambient
# KUBECONFIG.
assert_legs_armed() {
  _rdir=$1 _name=$2
  [ -f "$_rdir/snapshots/$_name/crust-gather.log" ]
  [ -d "$_rdir/snapshots/$_name/dataplane" ]
  [ -f "$_rdir/snapshots/$_name/previous-logs/capture-notes.txt" ]
}

assert_legs_disarmed() {
  _rdir=$1 _name=$2
  for _leg in crust-gather.log dataplane previous-logs; do
    if [ -e "$_rdir/snapshots/$_name/$_leg" ]; then
      echo "cluster capture leg $_leg fired for a unit suite"
      exit 1
    fi
  done
}

# Build a scratch dir holding stub cluster tools and a single-test fixture whose
# test fails. $1 is the fixture's path relative to the scratch dir, which is the
# only thing that differs between the directions under test.
#
# Both kubectl and crust-gather are stubbed, and crust-gather is not optional:
# the runner invokes it directly rather than through a script that needs
# kubectl output first, so a kubectl-only stub leaves that leg reaching for the
# real binary against the ambient KUBECONFIG. On a machine that has both -- a
# maintainer's laptop, the e2e sandbox image -- `make unit-tests` would then
# snapshot whatever cluster happens to be current, for up to the leg's 390s.
# That is the same bill this change exists to stop paying.
make_fixture_dir() {
  _dir=$(mktemp -d)
  mkdir -p "$_dir/bin" "$_dir/$(dirname "$1")"
  for _tool in kubectl crust-gather; do
    printf '#!/bin/sh\nexit 0\n' >"$_dir/bin/$_tool"
    chmod +x "$_dir/bin/$_tool"
  done
  printf '@test "fixture fails on purpose" {\n  false\n}\n' >"$_dir/$1"
  printf '%s' "$_dir"
}

# Run the runner against a fixture with the stub kubectl first on PATH and the
# report directory redirected into the scratch dir, so a capture that does fire
# writes there instead of into the worktree. The output goes to a file rather
# than a variable on purpose: these suites run under `set -x`, and a variable
# holding the fixture's output gets expanded into the trace of whatever command
# reads it, printing the fixture's deliberate "Test failed" line into the job
# log where it reads as a real failure.
run_fixture() {
  _fdir=$1 _fname=$2
  PATH="$_fdir/bin:$PATH" COZY_REPORT_DIR="$_fdir/report" \
    "$RUNNER" "$_fdir/$_fname" >"$_fdir/out" 2>&1 || true
}

@test "an e2e suite failing still arms the cluster captures" {
  dir=$(make_fixture_dir e2e-gate-fixture.bats)
  run_fixture "$dir" e2e-gate-fixture.bats
  # Positive control for the negative assertion in the last test: same fixture,
  # same stubs, e2e filename.
  grep -qF "$DATAPLANE_MARKER" "$dir/out"
  # The capture ran to its own end here -- the stub kubectl answers instantly --
  # so the incompleteness line the next test pins must NOT appear. Without this
  # the same line would satisfy that test whether it tracked the leg's status or
  # was printed unconditionally.
  if grep -qF 'data-plane capture INCOMPLETE' "$dir/out"; then
    echo "a capture that finished was reported as cut short:"
    cat "$dir/out"
    exit 1
  fi
  assert_legs_armed "$dir/report" e2e-gate-fixture
  rm -rf "$dir"
}

@test "a data-plane capture cut short by its backstop says so" {
  # The collector names every read of its own that could not finish, which
  # leaves the outer backstop here as the one way its leg can still die without
  # a word -- and a truncated dataplane/ then reads exactly like a complete one
  # that found little. The sibling previous-logs leg has reported this since it
  # was added; this is the same discipline at the third leg.
  #
  # The backstop is simulated with a `timeout` on PATH that exits 124 rather
  # than by making the collector overrun a real 600s bound. The call site
  # cannot tell the difference: it sees a non-zero status from `timeout` and
  # whatever had been written before the kill, which is the whole input to the
  # behaviour under test.
  dir=$(make_fixture_dir e2e-backstop-fixture.bats)
  printf '#!/bin/sh\nexit 124\n' >"$dir/bin/timeout"
  chmod +x "$dir/bin/timeout"
  run_fixture "$dir" e2e-backstop-fixture.bats
  # Positive control: the leg was armed and reached, so the assertion below is
  # about what the leg said and not about the gate having skipped it.
  grep -qF "$DATAPLANE_MARKER" "$dir/out"
  grep -qF 'data-plane capture INCOMPLETE (exit 124)' "$dir/out"
  rm -rf "$dir"
}

@test "a data-plane collector that is not there is not reported as a capture" {
  # This leg is gated on kubectl alone, while the previous-logs leg beside it
  # requires its collector to be executable and so does the Chainsaw caller.
  # The asymmetry cost nothing while the status was discarded; now that a
  # non-zero status prints "kept what landed in <dir>", a tree without the
  # collector claims a partial capture in a directory nothing ever created.
  dir=$(make_fixture_dir e2e-nocollector-fixture.bats)
  # The runner resolves both collectors relative to its own path, so copying it
  # somewhere without its siblings is what makes them absent -- nothing in the
  # worktree is touched.
  mkdir -p "$dir/hack"
  cp "$RUNNER" "$dir/hack/cozytest.sh"
  PATH="$dir/bin:$PATH" COZY_REPORT_DIR="$dir/report" \
    "$dir/hack/cozytest.sh" "$dir/e2e-nocollector-fixture.bats" >"$dir/out" 2>&1 || true
  # Positive controls: the fixture failed, so the handler ran, and the gate
  # armed -- otherwise the absence below would be satisfied by nothing running.
  grep -qF 'Test failed' "$dir/out"
  [ -f "$dir/report/snapshots/e2e-nocollector-fixture/crust-gather.log" ]
  if grep -qF 'data-plane capture INCOMPLETE' "$dir/out"; then
    echo "a collector that was never there was reported as a capture cut short:"
    cat "$dir/out"
    exit 1
  fi
  rm -rf "$dir"
}

@test "a live cluster suite under an e2e directory keeps its captures" {
  # hack/e2e-apps/ holds suites whose own filenames carry no prefix. Matching
  # the basename alone disarms them, which takes the captures away from a suite
  # that runs against a real cluster and therefore has state worth capturing.
  dir=$(make_fixture_dir e2e-apps/plain-named-suite.bats)
  run_fixture "$dir" e2e-apps/plain-named-suite.bats
  grep -qF "$DATAPLANE_MARKER" "$dir/out"
  assert_legs_armed "$dir/report" plain-named-suite
  rm -rf "$dir"
}

@test "a unit suite failing does not arm the cluster captures" {
  dir=$(make_fixture_dir plain-gate-fixture.bats)
  run_fixture "$dir" plain-gate-fixture.bats
  # The run must have failed, or "no captures" would be satisfied by "no test".
  grep -qF 'Test failed' "$dir/out"
  # Spelled as an if rather than `! grep ...`: `set -e` is specified to ignore a
  # command whose status is inverted with `!`, so the negated form would report
  # success even when the marker is present, which is the whole thing this test
  # exists to catch.
  if grep -qF "$DATAPLANE_MARKER" "$dir/out"; then
    echo "cluster captures were armed for a unit suite"
    exit 1
  fi
  assert_legs_disarmed "$dir/report" plain-gate-fixture
  rm -rf "$dir"
}
