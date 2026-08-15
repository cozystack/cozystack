#!/usr/bin/env bats
# Unit tests for hack/build-matrix.sh — the CI build-matrix selector.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); the
# relative `hack/build-matrix.sh` calls below resolve against that cwd. A bats
# setup() hook would be dead here — cozytest never invokes it — so the
# repo-root cwd is supplied by the runner rather than a setup() cd.
#
# Each fixture test removes its own scratch file at the last reachable cleanup
# point of its body rather than from an EXIT handler. A handler installed inside
# an @test body replaces the one the bats binary keeps its bookkeeping in, and a
# test that then FAILS prints no TAP line at all — the run just reports having
# executed fewer tests than it planned, which reads as a green suite. Every
# converted body reaches cleanup only after an assertion whose failure aborts
# the body, so the scratch file survives for inspection on failure. See
# hack/bats-no-exit-trap.bats and docs/agents/e2e-testing.md.

load test_helper

@test "no argument emits the full matrix" {
  out=$(hack/build-matrix.sh)
  # The parallel units; assert known members are present.
  echo "$out" | grep -q '"packages/core/platform"'
  echo "$out" | grep -q '"packages/apps/mariadb"'
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
}

@test "talos and installer are excluded from the parallel matrix" {
  out=$(hack/build-matrix.sh)
  # `! cmd` would be vacuous: cozytest.sh runs each @test under `set -e`, which
  # is suppressed for a `!`-negated pipeline, so a regression that wrongly
  # included these paths would not fail the test. Assert via `if cmd; then ...`.
  if echo "$out" | grep -q '"packages/core/talos"'; then echo "FAIL: packages/core/talos must be excluded from the parallel matrix"; false; fi
  if echo "$out" | grep -q '"packages/core/installer"'; then echo "FAIL: packages/core/installer must be excluded from the parallel matrix"; false; fi
}

@test "talos-only diff selects nothing (handled by the dedicated leg)" {
  tmp=$(mktemp)
  echo "packages/core/talos/images/matchbox/Dockerfile" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
  rm -f "$tmp"
}

@test "installer-only diff selects nothing (handled by finalize)" {
  tmp=$(mktemp)
  echo "packages/core/installer/images/cozystack-operator/Dockerfile" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
  rm -f "$tmp"
}

@test "FULL sentinel emits the full matrix" {
  out=$(hack/build-matrix.sh FULL)
  echo "$out" | grep -q '"packages/system/dashboard"'
}

@test "single-package diff selects only that unit" {
  tmp=$(mktemp)
  echo "packages/apps/mariadb/values.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '["packages/apps/mariadb"]' ]
  rm -f "$tmp"
}

@test "two-package diff selects both units" {
  tmp=$(mktemp)
  printf 'packages/apps/mariadb/values.yaml\npackages/system/dashboard/values.yaml\n' > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  echo "$out" | grep -q '"packages/apps/mariadb"'
  echo "$out" | grep -q '"packages/system/dashboard"'
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -eq 2 ]
  rm -f "$tmp"
}

@test "docs-only diff selects nothing" {
  tmp=$(mktemp)
  echo "docs/agents/overview.md" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
  rm -f "$tmp"
}

@test "a package with no image target selects nothing" {
  tmp=$(mktemp)
  # postgres ships no in-repo image build, so it is not a build unit.
  echo "packages/apps/postgres/values.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '[]' ]
  rm -f "$tmp"
}

@test "seaweedfs change fans out to objectstorage-controller" {
  tmp=$(mktemp)
  echo "packages/system/seaweedfs/values.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '["packages/system/objectstorage-controller"]' ]
  rm -f "$tmp"
}

@test "seaweedfs change does not duplicate an already-selected objectstorage-controller" {
  tmp=$(mktemp)
  printf 'packages/system/seaweedfs/values.yaml\npackages/system/objectstorage-controller/values.yaml\n' > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '["packages/system/objectstorage-controller"]' ]
  rm -f "$tmp"
}

@test "cozy-lib change forces the full matrix" {
  tmp=$(mktemp)
  echo "packages/library/cozy-lib/templates/_helpers.tpl" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  echo "$out" | grep -q '"packages/core/platform"'
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
  rm -f "$tmp"
}

@test "common-envs.mk change forces the full matrix" {
  tmp=$(mktemp)
  echo "hack/common-envs.mk" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
  rm -f "$tmp"
}

@test "go.mod change forces the full matrix" {
  tmp=$(mktemp)
  echo "go.mod" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
  rm -f "$tmp"
}

@test "build workflow change forces the full matrix" {
  tmp=$(mktemp)
  echo ".github/workflows/pull-requests.yaml" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
  rm -f "$tmp"
}

@test "root Go source change (api/) forces the full matrix" {
  tmp=$(mktemp)
  echo "api/apps/v1alpha1/kubernetes/types.go" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
  rm -f "$tmp"
}

@test "root Go source change (pkg/) forces the full matrix" {
  tmp=$(mktemp)
  echo "pkg/cluster/reconciler.go" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$(echo "$out" | tr ',' '\n' | wc -l)" -gt 20 ]
  rm -f "$tmp"
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
