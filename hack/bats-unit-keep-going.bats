#!/usr/bin/env bats
# `make bats-unit-tests` must run every bats file even when an early one fails,
# and still exit non-zero, so a red file never hides the ones after it behind
# output that reads like a complete suite. One Bats invocation owns the whole
# set, which gives that for free; this pins that the recipe keeps it that way.
#
# The tests drive the real root Makefile against a miniature tree of two bats
# files, with the real bats binary.
#
# Requires: make, bats.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# kg_run <body of a.bats's only test>: builds the fixture, runs the target in it
# and leaves the output in $fixture/out and the exit status in $rc. a.bats sorts
# first, so a failure there is the one that could hide b.bats.
kg_run() {
  fixture="$(mktemp -d)"
  mkdir "$fixture/hack"
  : >"$fixture/hack/common-envs.mk"
  printf '%s\n' '@test "first file" {' "  $1" '}' >"$fixture/hack/a.bats"
  printf '%s\n' '@test "second file" {' '  true' '}' >"$fixture/hack/b.bats"

  # Under `make unit-tests -k` this suite inherits MAKEFLAGS carrying the
  # parent's --keep-going, which would hide a recipe that stops on its own.
  #
  # Bats puts its own libexec first on PATH, and the `bats` found there is a
  # helper that needs bats_readlinkf, a bash function bin/bats exports. The
  # recipe runs under /bin/sh, and dash (the CI runner's) drops exported
  # functions, so the helper dies with "command not found" and no test runs.
  # Dropping libexec makes the recipe find the real bats on either shell.
  rc=0
  env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL PATH="${PATH#"$BATS_LIBEXEC":}" \
    make --file="$REPO_ROOT/Makefile" --directory="$fixture" --no-print-directory \
      BATS_JOBS=1 "BATS_REPORT_DIR=$fixture/reports" bats-unit-tests >"$fixture/out" 2>&1 || rc=$?
  cat "$fixture/out"
}

@test "bats-unit-tests runs every file after a failure and still fails" {
  kg_run 'false'
  ran_a=$(grep -c '^not ok 1 first file' "$fixture/out" || true)
  ran_b=$(grep -c '^ok 2 second file' "$fixture/out" || true)
  rm -rf "$fixture"

  [ "$ran_a" = 1 ]
  [ "$ran_b" = 1 ]
  [ "$rc" -ne 0 ]
}

@test "bats-unit-tests exits zero when every file passes" {
  kg_run 'true'
  ran=$(grep -c '^ok [12] ' "$fixture/out" || true)
  rm -rf "$fixture"

  [ "$ran" = 2 ]
  [ "$rc" -eq 0 ]
}
