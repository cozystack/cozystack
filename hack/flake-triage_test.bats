#!/usr/bin/env bats
# Unit tests for hack/flake-triage.sh — the pure `parse` and `classify` halves
# (the `triage` subcommand is GitHub I/O and is not unit-tested here).
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats (see the same
# note in hack/nightly-mirror_test.bats / hack/select-e2e_test.bats). There is
# no bats `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each @test
# is a shell function run under `set -eu -x`, so any non-zero exit aborts the
# test (that IS the exit-0 assertion). Capture output with `out=$(cmd)` (a
# failing cmd aborts under set -e) and assert with plain `[ … ]` / `grep`; a
# negative assertion uses `if cmd; then …; false; fi` (set -e ignores a `!`-
# negated pipeline). Run with: hack/cozytest.sh hack/flake-triage_test.bats

SCRIPT=hack/flake-triage.sh
# POSIX tab: cozytest.sh runs this file under `#!/bin/sh` (dash on CI), which has
# no ANSI-C `$'\t'` quoting. Command substitution strips trailing newlines only,
# so the tab survives.
TAB=$(printf '\t')

# Write a run file (parse-format PASS/FAIL lines) from "FAIL:a PASS:b …" tokens.
_mkrun() {
  local f="$1"; shift
  : > "$f"
  local tok
  for tok in "$@"; do
    printf '%s\t%s\n' "${tok%%:*}" "${tok#*:}" >> "$f"
  done
}

# --- parse -----------------------------------------------------------------

@test "parse: self-closed testcase is a PASS" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  printf '<testsuite><testcase name="alpha" time="1.0"/></testsuite>\n' > "$tmp"
  out=$("$SCRIPT" parse "$tmp")
  [ "$out" = "PASS${TAB}alpha" ]
}

@test "parse: <failure> and <error> children are FAILs, plain close is a PASS" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  cat > "$tmp" <<'XML'
<testsuite>
  <testcase name="beta">
    <failure message="boom">stack</failure>
  </testcase>
  <testcase name="gamma">
    <error>panic</error>
  </testcase>
  <testcase name="delta">
  </testcase>
</testsuite>
XML
  out=$("$SCRIPT" parse "$tmp")
  printf '%s\n' "$out" | grep -qx "FAIL${TAB}beta"
  printf '%s\n' "$out" | grep -qx "FAIL${TAB}gamma"
  printf '%s\n' "$out" | grep -qx "PASS${TAB}delta"
}

@test "parse: a classname attribute before name is not mistaken for the name" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  printf '<testsuite><testcase classname="suite" name="real" time="0.1"/></testsuite>\n' > "$tmp"
  out=$("$SCRIPT" parse "$tmp")
  [ "$out" = "PASS${TAB}real" ]
}

# --- classify: regression vs flake -----------------------------------------

@test "classify: a case failing the last 3 consecutive runs is a REGRESSION" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # oldest -> newest; x fails in all three, y always passes
  _mkrun "$d/r1" FAIL:x PASS:y
  _mkrun "$d/r2" FAIL:x PASS:y
  _mkrun "$d/r3" FAIL:x PASS:y
  out=$(TRIAGE_REGRESSION_STREAK=3 "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3")
  printf '%s\n' "$out" | grep -qx "REGRESSION${TAB}x${TAB}3"
  if printf '%s\n' "$out" | grep -q "REGRESSION${TAB}y"; then echo "FAIL: y is not a regression"; false; fi
}

@test "classify: a case that passed on the newest run is not a regression (flake at most)" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # newest (r3) passes x -> streak 0 -> FLAKE, never REGRESSION. Padding passes
  # (y,z,w) keep each run's failure ratio under the infra-outage threshold.
  _mkrun "$d/r1" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r2" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r3" PASS:x PASS:y PASS:z PASS:w
  out=$(TRIAGE_REGRESSION_STREAK=3 "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3")
  if printf '%s\n' "$out" | grep -q "REGRESSION"; then echo "FAIL: newest-green case must not be a regression"; false; fi
  printf '%s\n' "$out" | grep -qx "FLAKE${TAB}x${TAB}2/3"
}

@test "classify: a broken streak (fail,fail after an intervening pass) stays a FLAKE" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # oldest->newest: fail, pass, fail, fail  => newest streak = 2 (< 3). Padding
  # passes keep each run below the infra-outage threshold.
  _mkrun "$d/r1" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r2" PASS:x PASS:y PASS:z PASS:w
  _mkrun "$d/r3" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r4" FAIL:x PASS:y PASS:z PASS:w
  out=$(TRIAGE_REGRESSION_STREAK=3 "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3" "$d/r4")
  if printf '%s\n' "$out" | grep -q "REGRESSION"; then echo "FAIL: broken streak must not be a regression"; false; fi
  printf '%s\n' "$out" | grep -qx "FLAKE${TAB}x${TAB}3/4"
}

# --- classify: infra-outage guard ------------------------------------------

@test "classify: an infra-outage run is reported INFRA and excluded from the streak" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # r2 is a whole-cluster outage: >50% of its cases failed. x fails in the three
  # NON-infra runs (r1,r3,r4); with r2 excluded, x's streak over non-infra runs
  # is 3 -> REGRESSION, and r2 is flagged INFRA rather than resetting the streak.
  _mkrun "$d/r1" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r2" FAIL:x FAIL:y FAIL:z FAIL:w   # 4/4 failed => infra
  _mkrun "$d/r3" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r4" FAIL:x PASS:y PASS:z PASS:w
  out=$(TRIAGE_REGRESSION_STREAK=3 TRIAGE_INFRA_FAIL_RATIO=50 \
    "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3" "$d/r4")
  printf '%s\n' "$out" | grep -q "INFRA${TAB}r2"
  printf '%s\n' "$out" | grep -qx "REGRESSION${TAB}x${TAB}3"
}
