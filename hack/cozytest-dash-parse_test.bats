#!/usr/bin/env bats
# hack/cozytest.sh runs every hack/e2e-*.bats under /bin/sh, which is dash in
# the sandbox, after turning each @test into a shell function. A bash-only
# construct in one of those files fails the whole file at parse time, and the
# unit lane never runs them, so each is parsed here the same way under dash.

load test_helper

parses_under_dash() {
  awk '/^@test /{print "cozytest_case() {"; next} {print}' "$1" > "$BATS_TEST_TMPDIR/case.sh"
  dash -n "$BATS_TEST_TMPDIR/case.sh"
}

@test "every e2e bats file parses under dash once its tests are functions" {
  command -v dash >/dev/null || { echo "dash is required to check the e2e files" >&2; false; }
  found=0
  for f in hack/e2e-*.bats; do
    found=$((found + 1))
    parses_under_dash "$f" || { echo "$f does not parse under dash" >&2; false; }
  done
  [ "$found" -gt 0 ]
}

@test "the dash parse check refuses a bash array in a test body" {
  printf '%s\n' '@test "uses an array" {' '  local values=()' '}' > "$BATS_TEST_TMPDIR/e2e-sample.bats"
  rc=0; parses_under_dash "$BATS_TEST_TMPDIR/e2e-sample.bats" 2>/dev/null || rc=$?
  [ "$rc" -ne 0 ]
}
