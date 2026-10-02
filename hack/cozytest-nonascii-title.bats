#!/usr/bin/env bats
# Guards that hack/cozytest.sh parses a @test title that is not ASCII.
#
# The runner reconstructs each test's function name by walking its title one
# character at a time with awk's substr(). BSD awk aborts that walk on the first
# multibyte byte ("towc: multibyte conversion failure") rather than returning it,
# so a file carrying a non-ASCII @test title is never parsed and not one of its
# tests runs -- silently, because the suite only reports that the runner errored.
# Four hack/*.bats files carry such a title today; the byte scan is correct only
# when the runner pins a single-byte (C) ctype for the parse.
#
# This is BSD-specific: GNU awk returns the byte and never aborts, so on the
# Linux CI runner this guard passes whether or not the fix is present. It earns
# its keep on a developer's macOS box, where the default UTF-8 locale is exactly
# what triggers the abort -- the environment `make unit-tests` is meant to be
# green in. The fixture is therefore run under an explicit UTF-8 ctype so the
# byte scan is exercised the way that box exercises it; under a C ctype the scan
# cannot abort and the guard would pass without proving anything.
#
# Harness note (see hack/promote-rewrite-tags_test.bats): the CI path is
# hack/cozytest.sh, not real bats -- no `run`, `$status`, `skip` or setup(). Each
# test runs as a shell function under `set -eu -x`; paths are repo-root-relative.
#
# Run with: hack/cozytest.sh hack/cozytest-nonascii-title.bats

@test "cozytest.sh runs a test whose title contains a multibyte character" {
  tmp=$(mktemp -d)

  # Build the multibyte bytes at runtime so this file stays pure ASCII on disk:
  # the runner parsing THIS file must not itself meet a byte the bug chokes on.
  # U+2014 EM DASH (e2 80 94) plus U+00E9 (c3 a9), the same shapes the four
  # real offenders carry.
  emdash=$(printf '\342\200\224')
  eacute=$(printf '\303\251')
  title="promote body carries a caf${eacute} status line ${emdash} multibyte"

  fixture="$tmp/nonascii.bats"
  {
    printf '@test "%s" {\n' "$title"
    printf '  :\n'
    printf '}\n'
  } > "$fixture"

  # A UTF-8 ctype makes BSD awk's per-character scan abort on the title unless the
  # runner pins C for the parse. en_US.UTF-8 is present on macOS and typical CI;
  # where no UTF-8 locale exists libc falls back to C, and there the bug cannot
  # occur, so a pass is the correct answer rather than a false negative.
  out=$(LC_ALL=en_US.UTF-8 COZYTEST_TRACE=0 hack/cozytest.sh "$fixture" 2>&1) || true

  case "$out" in
    *"Test OK: $title"*) ;;
    *)
      echo "cozytest.sh did not run the multibyte-titled test; its output was:" >&2
      printf '%s\n' "$out" >&2
      rm -rf "$tmp"
      return 1
      ;;
  esac
  rm -rf "$tmp"
}
