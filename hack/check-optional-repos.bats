#!/usr/bin/env bats
# docs/agents/changelog.md sends the changelog generator to
# hack/check-optional-repos.sh to see what the companion repositories shipped in
# a release window. The prompt names the script by path and the script anchors
# its _repos clones on a repository root derived from its own location. When
# either is wrong the generator cannot run the script or reads clones that were
# made somewhere else, and a window with no companion commits looks the same as
# one the check never ran for.
#
# Run from the repo root with `make bats-unit-tests`, or directly with
# `bats hack/check-optional-repos.bats`.

load test_helper

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
ROOT="$HACK_DIR/.."
SCRIPT="$HACK_DIR/check-optional-repos.sh"
PROMPT="$ROOT/docs/agents/changelog.md"

@test "every hack script the changelog prompt runs exists" {
  n=0
  # A link such as https://github.com/<org>/<repo>/blob/main/hack/x.sh matches
  # from the "//" after the scheme; it is a URL, not a path to run.
  for p in $(grep -oE '(\./)?[A-Za-z0-9._/-]*hack/[A-Za-z0-9._-]+\.sh' "$PROMPT" | grep -v '^//' | sort -u); do
    n=$((n + 1))
    if [ ! -f "$ROOT/${p#./}" ]; then
      echo "docs/agents/changelog.md names $p, which is not a file in this repository" >&2
      false
    fi
  done
  [ "$n" -gt 0 ]
}

@test "check-optional-repos.sh clones the companion repos under the repository root" {
  mkdir -p "$BATS_TEST_TMPDIR/repo/hack" "$BATS_TEST_TMPDIR/bin"
  cp "$SCRIPT" "$BATS_TEST_TMPDIR/repo/hack/"
  # git and gh are stubs: the script's own path arithmetic is under test, not
  # the network. A clone only has to leave a directory behind.
  printf '%s\n' '#!/bin/sh' 'if [ "$1" = clone ]; then mkdir -p "$(basename "$2" .git)"; fi' 'exit 0' > "$BATS_TEST_TMPDIR/bin/git"
  printf '%s\n' '#!/bin/sh' 'exit 0' > "$BATS_TEST_TMPDIR/bin/gh"
  chmod +x "$BATS_TEST_TMPDIR/bin/git" "$BATS_TEST_TMPDIR/bin/gh"
  (cd "$BATS_TEST_TMPDIR" && PATH="$BATS_TEST_TMPDIR/bin:$PATH" repo/hack/check-optional-repos.sh '2025-10-10 00:00:00 +0000' '2025-10-13 00:00:00 +0000' > out 2>&1) \
    || { cat "$BATS_TEST_TMPDIR/out" >&2; false; }
  for r in talm boot-to-talos cozyhr cozy-proxy; do
    if [ ! -d "$BATS_TEST_TMPDIR/repo/_repos/$r" ]; then
      echo "no clone of $r under the repository root" >&2
      false
    fi
  done
  if [ -e "$BATS_TEST_TMPDIR/_repos" ]; then
    echo "clones were made above the repository root, in $BATS_TEST_TMPDIR/_repos" >&2
    false
  fi
}
