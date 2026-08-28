#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the EXPECTED_NOT_READY_FILE half of hack/e2e-wait-hr-ready.sh.
#
# That half can turn a red gate green, so what it accepts and what it refuses is
# pinned here rather than left to the one lane that sets the variable. Each test
# drives the real script through a `kubectl` stub on PATH, so the parsing, the
# set arithmetic and the exit status are exercised end to end; there is no
# cluster and no HelmRelease anywhere.
#
# NOT named hack/e2e-*.bats on purpose: the Makefile's bats-unit-tests target is
# `$(filter-out hack/e2e-%.bats,...)`, so a file named for the script it tests
# would be silently excluded from the unit suite and never run.
#
# Run under cozytest.sh (hack/cozytest.sh hack/wait-hr-ready_test.bats), NOT
# real bats: there is no `run`/`$status`/setup(); each @test is a shell function
# under `set -eu -x`, so assertions are direct shell tests that exit non-zero on
# failure. The script is invoked inside an `if`, because `set -e` exempts a
# condition but would abort the test before a following `rc=$?` could read a
# non-zero — read after the fact, such an assertion could only ever see 0.
#
# Each test removes its scratch directory as the last statement of its body
# rather than from an EXIT handler: a handler installed inside an @test replaces
# the one the bats binary keeps for its own bookkeeping, and a test that then
# fails prints no TAP line at all. hack/bats-no-exit-trap.bats enforces this.
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
GATE="$HACK_DIR/e2e-wait-hr-ready.sh"

# A kubectl stub that answers the two calls the ledger path makes: `wait` (whose
# exit status the caller only uses to decide whether to look further) and `get hr
# -A --no-headers`, whose rows are the state under test. The rows are passed in
# as the fixture; anything else the script might call answers success with no
# output, so an unexpected call cannot masquerade as a listing.
#
# WAIT_RC is deliberately settable: the gate must reach the same verdict whether
# the wait timed out or everything went Ready while it was running, and the
# came-back-Ready case can only be reached with a wait that succeeded.
_stub() {
  _dir=$1
  _rows=$2
  _wait_rc=${3:-1}
  mkdir -p "$_dir"
  {
    printf '%s\n' '#!/bin/sh'
    printf '%s\n' 'case "$1 $2" in'
    printf '  "wait hr") exit %s ;;\n' "$_wait_rc"
    printf '%s\n' 'esac'
    printf '%s\n' 'if [ "$1" = "get" ] && [ "$2" = "hr" ] && [ "$3" = "-A" ]; then'
    printf '  cat <<'"'"'ROWS'"'"'\n%s\nROWS\n' "$_rows"
    printf '%s\n' '  exit 0'
    printf '%s\n' 'fi'
    printf '%s\n' 'exit 0'
  } > "$_dir/kubectl"
  chmod +x "$_dir/kubectl"
  return 0
}

# NAMESPACE NAME AGE READY STATUS — column 4 is the one the gate reads.
_rows_one_bad() {
  printf '%s\n' \
    'cozy-system    cozystack        40m   True    Helm upgrade succeeded' \
    'tenant-test    vm-instance-test 30m   False   timeout waiting for VirtualMachine'
}

_rows_all_good() {
  printf '%s\n' \
    'cozy-system    cozystack        40m   True    Helm upgrade succeeded' \
    'tenant-test    vm-instance-test 30m   True    Helm upgrade succeeded'
}

@test "a not-Ready release named in the file is accepted, and the file is echoed" {
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_one_bad)"
  printf '%s\n' '# a comment line is not an entry' \
    'tenant-test/vm-instance-test  https://example.invalid/issues/1  VM does not return across upgrade' \
    > "$tmp/expected"

  if out=$(PATH="$tmp/bin:$PATH" EXPECTED_NOT_READY_FILE="$tmp/expected" sh "$GATE" 1s 2>&1); then
    :
  else
    echo "FAIL: a recorded not-Ready release should not fail the gate: $out" >&2
    false
  fi
  # The reason has to reach the log, or the next reader sees an accepted red with
  # no way to tell what was accepted.
  echo "$out" | grep -q 'VM does not return across upgrade'
  echo "$out" | grep -q 'NOT a healthy platform'
  rm -rf "$tmp"
}

@test "a not-Ready release absent from the file still fails" {
  # The whole point: the ledger accepts a named set, never "whatever is broken".
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_one_bad)"
  printf '%s\n' '# nothing recorded' > "$tmp/expected"

  if out=$(PATH="$tmp/bin:$PATH" EXPECTED_NOT_READY_FILE="$tmp/expected" sh "$GATE" 1s 2>&1); then
    echo "FAIL: an unrecorded not-Ready release passed the gate: $out" >&2
    false
  fi
  echo "$out" | grep -q 'not Ready and not recorded'
  echo "$out" | grep -q 'tenant-test/vm-instance-test'
  rm -rf "$tmp"
}

@test "an entry whose release is Ready again fails until the line is deleted" {
  # This is what stops the file outliving the defect it describes. Reached with a
  # wait that SUCCEEDED, which is the real shape of the fix landing.
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_all_good)" 0
  printf '%s\n' 'tenant-test/vm-instance-test  https://example.invalid/issues/1  stale' > "$tmp/expected"

  if out=$(PATH="$tmp/bin:$PATH" EXPECTED_NOT_READY_FILE="$tmp/expected" sh "$GATE" 1s 2>&1); then
    echo "FAIL: a stale entry was accepted: $out" >&2
    false
  fi
  echo "$out" | grep -q 'Ready now'
  echo "$out" | grep -q 'tenant-test/vm-instance-test'
  rm -rf "$tmp"
}

@test "an entry naming no release in the cluster fails too" {
  # A renamed or removed release leaves a line that can never expire on the
  # rule above, so it has its own. Otherwise the file accumulates entries that
  # are neither true nor falsifiable.
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_all_good)" 0
  printf '%s\n' 'tenant-test/no-such-release  https://example.invalid/issues/2  typo' > "$tmp/expected"

  if out=$(PATH="$tmp/bin:$PATH" EXPECTED_NOT_READY_FILE="$tmp/expected" sh "$GATE" 1s 2>&1); then
    echo "FAIL: an entry naming nothing was accepted: $out" >&2
    false
  fi
  echo "$out" | grep -q 'no such HelmRelease'
  rm -rf "$tmp"
}

@test "a file that cannot be read fails instead of expecting nothing" {
  # Treating an unreadable path as an empty expectation would turn a typo in the
  # variable into a gate that accepts more than it claims — and it would do it
  # silently, since every release being Ready looks identical to that.
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_one_bad)"

  if out=$(PATH="$tmp/bin:$PATH" EXPECTED_NOT_READY_FILE="$tmp/does-not-exist" sh "$GATE" 1s 2>&1); then
    echo "FAIL: a missing ledger file was treated as no expectations: $out" >&2
    false
  fi
  echo "$out" | grep -q 'is not a readable file'
  rm -rf "$tmp"
}

@test "with no file set the gate is the install gate: every release must be Ready" {
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_one_bad)"

  if out=$(PATH="$tmp/bin:$PATH" sh "$GATE" 1s 2>&1); then
    echo "FAIL: the unconfigured gate accepted a not-Ready release: $out" >&2
    false
  fi
  echo "$out" | grep -q 'Some HelmReleases failed to reconcile'
  # And nothing about a ledger, which the install path must not even mention.
  [ "$(echo "$out" | grep -c 'recorded')" -eq 0 ]
  rm -rf "$tmp"
}

@test "with no file set an all-Ready cluster passes" {
  tmp=$(mktemp -d)
  _stub "$tmp/bin" "$(_rows_all_good)" 0

  if out=$(PATH="$tmp/bin:$PATH" sh "$GATE" 1s 2>&1); then
    :
  else
    echo "FAIL: the unconfigured gate failed a healthy cluster: $out" >&2
    false
  fi
  rm -rf "$tmp"
}
