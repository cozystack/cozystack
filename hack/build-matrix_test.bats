#!/usr/bin/env bats
# Unit tests for hack/build-matrix.sh — the CI build-matrix selector.
#
# Run from the repo root:  bats hack/build-matrix_test.bats

setup() {
  REPO_ROOT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  cd "$REPO_ROOT"
}

@test "no argument emits the full matrix" {
  out=$(hack/build-matrix.sh)
  # The parallel units; assert known members are present.
  echo "$out" | grep -q '"packages/core/platform"'
  echo "$out" | grep -q '"packages/apps/mariadb"'
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
}

@test "talos and installer are excluded from the parallel matrix" {
  out=$(hack/build-matrix.sh)
  ! echo "$out" | grep -q '"packages/core/talos"'
  ! echo "$out" | grep -q '"packages/core/installer"'
}

@test "talos-only diff selects nothing (handled by the dedicated leg)" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "packages/core/talos/images/matchbox/Dockerfile" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
}

@test "installer-only diff selects nothing (handled by finalize)" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "packages/core/installer/images/cozystack-operator/Dockerfile" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
}

@test "FULL sentinel emits the full matrix" {
  out=$(hack/build-matrix.sh FULL)
  echo "$out" | grep -q '"packages/system/dashboard"'
}

@test "single-package diff selects only that unit" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "packages/apps/mariadb/values.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '["packages/apps/mariadb"]' ]
}

@test "two-package diff selects both units" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  printf 'packages/apps/mariadb/values.yaml\npackages/system/dashboard/values.yaml\n' > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  echo "$out" | grep -q '"packages/apps/mariadb"'
  echo "$out" | grep -q '"packages/system/dashboard"'
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -eq 2 ]
}

@test "docs-only diff selects nothing" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "docs/agents/overview.md" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
}

@test "a package with no image target selects nothing" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  # postgres ships no in-repo image build, so it is not a build unit.
  echo "packages/apps/postgres/values.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
}

@test "cozy-lib change forces the full matrix" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "packages/library/cozy-lib/templates/_helpers.tpl" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  echo "$out" | grep -q '"packages/core/platform"'
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
}

@test "common-envs.mk change forces the full matrix" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "hack/common-envs.mk" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
}

@test "go.mod change forces the full matrix" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo "go.mod" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
}

@test "build workflow change forces the full matrix" {
  tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
  echo ".github/workflows/pull-requests.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
}

@test "emitted JSON is parseable and matches make build's unit list" {
  out=$(hack/build-matrix.sh)
  # Valid JSON array.
  echo "$out" | jq -e 'type == "array"' >/dev/null
  # Every emitted dir exists and has a Makefile.
  for d in $(echo "$out" | jq -r '.[]'); do
    [ -f "$d/Makefile" ]
  done
  # Count matches the `make -C packages/... image` lines in Makefile, minus the
  # two units handled outside the parallel matrix (talos, installer).
  expected=$(sed -n '/^build:/,/^[^[:space:]]/p' Makefile \
    | grep -oE 'make -C packages/[A-Za-z0-9._/-]+ image' \
    | sed -E 's/^make -C (packages[^ ]+) image$/\1/' \
    | grep -vxcE 'packages/core/(talos|installer)')
  actual=$(echo "$out" | jq 'length')
  [ "$expected" -eq "$actual" ]
}
