#!/usr/bin/env bats
# `make bats-unit-tests` must run every bats file even when an early one fails,
# and still exit non-zero. Each file is its own make target, and without
# --keep-going make stops scheduling targets at the first failure, so the files
# sorting after it never run and the output reads like a complete suite.
#
# The tests drive the real root Makefile against a miniature tree: two bats
# files and a stub hack/cozytest.sh that reports which file it was given.
#
# Requires: make.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# kg_run <stub body line> [makefiles]: builds the fixture, runs the target with
# MAKEFILES set to the second argument, and leaves the output in $fixture/out
# and the exit status in $rc.
kg_run() {
  fixture="$(mktemp -d)"
  mkdir "$fixture/hack"
  : >"$fixture/hack/common-envs.mk"
  : >"$fixture/hack/a.bats"
  : >"$fixture/hack/b.bats"
  printf '%s\n' '#!/bin/sh' 'echo "stub ran $1"' "$1" >"$fixture/hack/cozytest.sh"
  chmod +x "$fixture/hack/cozytest.sh"

  # Under `make unit-tests -k` this suite inherits MAKEFLAGS carrying the
  # parent's --keep-going, which would let the old behaviour pass here.
  rc=0
  env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL MAKEFILES="${2:-}" \
    make --file="$REPO_ROOT/Makefile" --directory="$fixture" bats-unit-tests >"$fixture/out" 2>&1 || rc=$?
  cat "$fixture/out"
}

@test "bats-unit-tests runs every file after a failure and still fails" {
  # Fail whichever file runs first, whatever order make picks, so the exit
  # status cannot come from the last file alone.
  kg_run 'mkdir "$(dirname "$0")/.failed" 2>/dev/null && exit 1; exit 0'
  ran_a=$(grep -c '^stub ran hack/a.bats$' "$fixture/out" || true)
  ran_b=$(grep -c '^stub ran hack/b.bats$' "$fixture/out" || true)
  rm -rf "$fixture"

  [ "$ran_a" = 1 ]
  [ "$ran_b" = 1 ]
  [ "$rc" -ne 0 ]
}

@test "bats-unit-tests still finds the root Makefile when MAKEFILES reads another first" {
  extra="$(mktemp)"
  printf '%s\n' 'unrelated:' '	@true' >"$extra"
  kg_run 'exit 0' "$extra"
  ran_b=$(grep -c '^stub ran hack/b.bats$' "$fixture/out" || true)
  rm -rf "$fixture" "$extra"
  [ "$ran_b" = 1 ]
  [ "$rc" -eq 0 ]
}

@test "bats-unit-tests exits zero when every file passes" {
  kg_run 'exit 0'
  rm -rf "$fixture"
  [ "$rc" -eq 0 ]
}
