#!/usr/bin/env bats
# Behavioral coverage for Bats-compatible skip handling in hack/cozytest.sh.

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
RUNNER="$HACK_DIR/cozytest.sh"

@test "skip stops the current test and keeps the suite green" {
  dir=$(mktemp -d)
  fixture="$dir/fixture.bats"
  printf '%s\n' \
    '@test "skipped fixture" {' \
    '  skip "not applicable"' \
    '  echo SHOULD_NOT_RUN' \
    '  false' \
    '}' \
    '@test "following fixture" {' \
    '  true' \
    '}' >"$fixture"

  if ! "$RUNNER" "$fixture" >"$dir/out" 2>&1; then
    echo "runner failed a suite containing a skipped test" >&2
    cat "$dir/out" >&2
    exit 1
  fi

  grep -qF 'Test skipped: skipped fixture (not applicable)' "$dir/out"
  if grep -qF 'SHOULD_NOT_RUN' "$dir/out"; then
    echo "runner continued executing the skipped test body" >&2
    cat "$dir/out" >&2
    exit 1
  fi
  grep -qF 'Test OK: following fixture' "$dir/out"
  rm -rf "$dir"
}
