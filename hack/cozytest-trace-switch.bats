#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Behavioural tests for the COZYTEST_TRACE switch in hack/cozytest.sh.
#
# The runner streams every @test body's xtrace live. Across the unit suites that
# was 233,444 of 238,794 lines in one job log, with the single failing assertion
# at line 238,573, so `make unit-tests` now runs with the stream off. What makes
# that safe is a single invariant: run_one tees the raw stream to $log and the
# fail handler prints all of it for whichever test fails, so quiet mode drops
# the output of PASSING tests only.
#
# That invariant fails SILENTLY. It is only exercised when a test fails, so a
# green CI run proves nothing about it: a refactor of run_one's reader loop that
# also broke the dump would ship, and would be discovered by whoever next needed
# a red unit run diagnosed -- with no trace, having already lost the run. Hence
# a suite whose fixtures fail on purpose.
#
# Both directions are pinned, and the verbose one is the positive control for
# the quiet one: "no trace lines" is satisfied just as well by a harness where
# nothing ran at all.
#
# awk-transform constraint (hack/cozytest.sh rewrites this file before sourcing
# it, see the longer note in hack/cozytest-capture-gate.bats): a line beginning
# `@test "` becomes a function header wherever it appears, heredocs included, so
# a fixture written as a heredoc gets its own @test harvested into THIS suite.
# The fixtures below are built with printf, which puts that text inside a format
# string where no rule matches it. Titles must also stay distinctive in their
# [A-Za-z0-9] run, which is all that survives into the function name.
#
# Run with: hack/cozytest.sh hack/cozytest-trace-switch.bats
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
RUNNER="$HACK_DIR/cozytest.sh"
REPO_ROOT="$(cd "$HACK_DIR/.." && pwd)"

# Printed by the fixture from inside the test body. It reaches the job log by
# exactly two routes, the live stream and the fail handler's dump, which is what
# lets the quiet-mode test below tell those routes apart.
MARKER='cozytest-trace-switch-fixture-marker'

# $1 = `pass` or `fail`. The failing fixture exercises the dump path under test.
#
# Named without an e2e- prefix on purpose. That prefix is what arms the runner's
# cluster captures (hack/cozytest-capture-gate.bats owns that gate), and a
# fixture that tripped them would spend the capture timeouts on every test here.
make_fixture() {
  _dir=$(mktemp -d)
  if [ "$1" = pass ]; then
    printf '@test "fixture that passes and prints the marker" {\n  echo %s\n}\n' \
      "$MARKER" >"$_dir/trace-fixture.bats"
  else
    printf '@test "fixture that prints the marker then fails" {\n  echo %s\n  false\n}\n' \
      "$MARKER" >"$_dir/trace-fixture.bats"
  fi
  printf '%s' "$_dir"
}

# Output goes to a file rather than a variable, for the reason spelled out in
# hack/cozytest-capture-gate.bats: these suites can run under `set -x`, and a
# variable holding a fixture's output is expanded into the trace of whatever
# reads it, printing the fixture's deliberate failure into the job log where it
# reads as a real one.
#
# `env -u COZYTEST_TRACE` when $2 is empty, because the Makefile passes the
# variable as a command prefix and that EXPORTS it: a nested runner started from
# a suite that `make unit-tests` is itself running would inherit 0 and the
# default would never be under test. Verified: `sh -c 'COZYTEST_TRACE=0 sh -c
# printenv'` shows it in the grandchild.
run_fixture() {
  _fdir=$1 _mode=$2
  if [ -z "$_mode" ]; then
    env -u COZYTEST_TRACE "$RUNNER" "$_fdir/trace-fixture.bats" >"$_fdir/out" 2>&1 || true
  else
    COZYTEST_TRACE="$_mode" "$RUNNER" "$_fdir/trace-fixture.bats" >"$_fdir/out" 2>&1 || true
  fi
}

count_streamed_marker_lines() {
  # A live line has a [mm:ss] timestamp; the failure dump has the same marker
  # without one. Keep the pattern ASCII-only so tracing this assertion does not
  # put the runner's decorative UTF-8 prefix into its own raw log.
  grep -cE '[[][0-9][0-9]:[0-9][0-9][]].*cozytest-trace-switch-fixture-marker' "$1" || true
}

@test "quiet mode streams no trace for a suite whose tests pass" {
  dir=$(make_fixture pass)
  run_fixture "$dir" 0
  # Positive control first: the test has to have run, or the absence below is
  # satisfied by an empty output.
  grep -qF 'Test OK' "$dir/out"
  n=$(count_streamed_marker_lines "$dir/out")
  # Spelled as an if rather than `! grep`: `set -e` is specified to ignore a
  # command whose status is inverted with `!`, which would make the assertion
  # vacuous -- the exact shape this suite exists to catch elsewhere.
  if [ "$n" -ne 0 ]; then
    echo "quiet mode still streamed $n trace line(s)"
    cat "$dir/out"
    exit 1
  fi
  rm -rf "$dir"
}

@test "the unset default stays verbose so the e2e call sites keep their stream" {
  # The positive control for the test above, and a guard in its own right: the
  # e2e suites in packages/core/testing/Makefile invoke the runner without the
  # variable, and for a long suite that stream is the only progress signal, and
  # the only record at all when a step timeout kills the job before the fail
  # handler can run. Flipping the default silently takes that away.
  dir=$(make_fixture pass)
  run_fixture "$dir" ''
  grep -qF 'Test OK' "$dir/out"
  n=$(count_streamed_marker_lines "$dir/out")
  if [ "$n" -eq 0 ]; then
    echo "the default is no longer verbose; e2e lost its live stream"
    cat "$dir/out"
    exit 1
  fi
  rm -rf "$dir"
}

@test "a failing test still dumps its whole trace when the stream is quiet" {
  # The load-bearing invariant. Quiet mode is only defensible because this dump
  # survives it, and nothing else in the tree exercises the failure path.
  dir=$(make_fixture fail)
  run_fixture "$dir" 0
  grep -qF 'Test failed' "$dir/out"
  grep -qF 'captured output' "$dir/out"
  # The marker reaches the log by two routes and quiet mode closes one of them,
  # so with no streamed copy present its appearance can only have come from the
  # dump. That pairing is the assertion; either half alone is weaker.
  n=$(count_streamed_marker_lines "$dir/out")
  if [ "$n" -ne 0 ]; then
    echo "expected no live trace in quiet mode, got $n line(s)"
    exit 1
  fi
  grep -qF "$MARKER" "$dir/out"
  # The xtrace itself, not just the echoed output: the dump is worth having
  # because it carries the commands, and `+ false` is the failing one.
  grep -qF '+ false' "$dir/out"
  rm -rf "$dir"
}

@test "quiet mode still fails the run when a test fails" {
  dir=$(make_fixture fail)
  COZYTEST_TRACE=0 "$RUNNER" "$dir/trace-fixture.bats" >"$dir/out" 2>&1 && rc=0 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "a failing suite exited 0 under quiet mode, so CI would go green on red"
    exit 1
  fi
  rm -rf "$dir"
}

@test "quiet mode keeps the per test markers so a stuck test is still visible" {
  # With the stream off these two lines are the only evidence of which test is
  # in flight, which is what makes a hang diagnosable: a start with no matching
  # end names the test that never returned.
  dir=$(make_fixture pass)
  run_fixture "$dir" 0
  grep -qF 'Run test:' "$dir/out"
  grep -qF 'Test OK:' "$dir/out"
  rm -rf "$dir"
}

@test "the unit lane recipe asks for the quiet runner" {
  # Without this the 31MB job log comes back the first time somebody tidies the
  # recipe, and every test above keeps passing while it happens.
  grep -qE '^COZYTEST_TRACE \?= 0' "$REPO_ROOT/Makefile"
  grep -qF 'COZYTEST_TRACE=$(COZYTEST_TRACE) hack/cozytest.sh' "$REPO_ROOT/Makefile"
}

@test "the e2e recipes do not silence their runner" {
  # The other half of the design, and the half a well-meaning cleanup would
  # break by making the two lanes consistent with each other.
  # Matches an ASSIGNMENT, not any mention: documenting this design in a comment
  # over there is expected and must not turn the guard red. The recipes are
  # `docker exec ... sh -c '...'`, which does not carry host env either, so the
  # variable structurally cannot reach those suites today -- this pins the
  # recipe text so a future rewrite that does export it is caught.
  e2e_mk="$REPO_ROOT/packages/core/testing/Makefile"
  if grep -nE '^[^#]*COZYTEST_TRACE[[:space:]]*[:?+]?=' "$e2e_mk"; then
    echo "an e2e recipe now sets COZYTEST_TRACE; those suites need the live stream"
    exit 1
  fi
  # Positive control: the file does invoke the runner, so the absence above is
  # about the variable and not about a moved call site.
  grep -qF 'hack/cozytest.sh hack/e2e-' "$e2e_mk"
}
