#!/usr/bin/env bats
# Unit tests for hack/flake-triage.sh — the pure `parse` and `classify` halves
# (the `triage` subcommand is GitHub I/O and is not unit-tested here).
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); the
# relative `hack/flake-triage.sh` calls resolve against that cwd. Each @test
# runs under `set -e`, so negative assertions use `if cmd; then …; false; fi`
# rather than a `!`-negated pipeline (which set -e ignores).

SCRIPT=hack/flake-triage.sh
TAB=$'\t'

# --- parse -----------------------------------------------------------------

@test "parse: self-closed testcase is a PASS" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  printf '<testsuite><testcase name="alpha" time="1.0"/></testsuite>\n' > "$tmp"
  run "$SCRIPT" parse "$tmp"
  [ "$status" -eq 0 ]
  [ "$output" = "PASS${TAB}alpha" ]
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
  run "$SCRIPT" parse "$tmp"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qx "FAIL${TAB}beta"
  echo "$output" | grep -qx "FAIL${TAB}gamma"
  echo "$output" | grep -qx "PASS${TAB}delta"
}

# --- classify: regression vs flake -----------------------------------------

# Helper: write a run file (parse-format PASS/FAIL lines) from "FAIL:a PASS:b …".
_mkrun() {
  local f="$1"; shift
  : > "$f"
  local tok
  for tok in "$@"; do
    printf '%s\t%s\n' "${tok%%:*}" "${tok#*:}" >> "$f"
  done
}

@test "classify: a case failing the last 3 consecutive runs is a REGRESSION" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # oldest -> newest; x fails in all three, y always passes
  _mkrun "$d/r1" FAIL:x PASS:y
  _mkrun "$d/r2" FAIL:x PASS:y
  _mkrun "$d/r3" FAIL:x PASS:y
  TRIAGE_REGRESSION_STREAK=3 run "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qx "REGRESSION${TAB}x${TAB}3"
  if echo "$output" | grep -q "REGRESSION${TAB}y"; then echo "FAIL: y is not a regression"; false; fi
}

@test "classify: a case that passed on the newest run is not a regression (flake at most)" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # newest (r3) passes x -> streak 0 -> FLAKE, never REGRESSION. Padding passes
  # (y,z,w) keep each run's failure ratio under the infra-outage threshold.
  _mkrun "$d/r1" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r2" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r3" PASS:x PASS:y PASS:z PASS:w
  TRIAGE_REGRESSION_STREAK=3 run "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3"
  [ "$status" -eq 0 ]
  if echo "$output" | grep -q "REGRESSION"; then echo "FAIL: newest-green case must not be a regression"; false; fi
  echo "$output" | grep -qx "FLAKE${TAB}x${TAB}2/3"
}

@test "classify: a broken streak (fail,fail after an intervening pass) stays a FLAKE" {
  d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
  # oldest->newest: fail, pass, fail, fail  => newest streak = 2 (< 3). Padding
  # passes keep each run below the infra-outage threshold.
  _mkrun "$d/r1" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r2" PASS:x PASS:y PASS:z PASS:w
  _mkrun "$d/r3" FAIL:x PASS:y PASS:z PASS:w
  _mkrun "$d/r4" FAIL:x PASS:y PASS:z PASS:w
  TRIAGE_REGRESSION_STREAK=3 run "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3" "$d/r4"
  [ "$status" -eq 0 ]
  if echo "$output" | grep -q "REGRESSION"; then echo "FAIL: broken streak must not be a regression"; false; fi
  echo "$output" | grep -qx "FLAKE${TAB}x${TAB}3/4"
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
  TRIAGE_REGRESSION_STREAK=3 TRIAGE_INFRA_FAIL_RATIO=50 \
    run "$SCRIPT" classify "$d/r1" "$d/r2" "$d/r3" "$d/r4"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "INFRA${TAB}r2"
  echo "$output" | grep -qx "REGRESSION${TAB}x${TAB}3"
}
