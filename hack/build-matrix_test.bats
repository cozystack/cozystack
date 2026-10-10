#!/usr/bin/env bats
# Unit tests for hack/build-matrix.sh — the CI build-matrix selector.
#
# Run from the repo root through `make bats-unit-tests`; the relative
# `hack/build-matrix.sh` calls below resolve against that cwd. The file also
# remains compatible with the legacy `hack/cozytest.sh` translator, which does
# not invoke setup(), so neither runner depends on a setup() cd.
#
# Test-level EXIT traps replace Bats' own handler and hide failing TAP results.
# Cleanup follows aborting assertions; see docs/agents/e2e-testing.md.

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
  # `! cmd` would be vacuous: cozytest.sh runs each @test under `set -e`,
  # which is suppressed for any command whose status is inverted with `!`, so
  # a regression that wrongly included these paths would not fail the test.
  # Assert via `if cmd; then ...`.
  if echo "$out" | grep -q '"packages/core/talos"'; then echo "FAIL: packages/core/talos must be excluded from the parallel matrix"; false; fi
  if echo "$out" | grep -q '"packages/core/installer"'; then echo "FAIL: packages/core/installer must be excluded from the parallel matrix"; false; fi
}

@test "the arm64 matrix drops the amd64-only e2e sandbox and nothing else" {
  full=$(hack/build-matrix.sh)
  arm=$(MATRIX_ARCH=arm64 hack/build-matrix.sh)
  echo "$full" | grep -q '"packages/core/testing"'
  if echo "$arm" | grep -q '"packages/core/testing"'; then echo "FAIL: the arm64 matrix builds the amd64-only sandbox"; false; fi
  [ "$(echo "$full" | sed 's/,*"packages\/core\/testing"//')" = "$arm" ]
  # A diff that touches only the sandbox gives the arm64 leg nothing to build.
  tmp=$(mktemp)
  echo "packages/core/testing/Makefile" > "$tmp"
  [ "$(MATRIX_ARCH=arm64 hack/build-matrix.sh "$tmp")" = "[]" ]
  rm -f "$tmp"
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

@test "kubeovn source patch selects the kubeovn image build" {
  tmp=$(mktemp)
  echo "packages/system/kubeovn/images/kubeovn/patches/fix-vmim-scheduling-retry.diff" > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '["packages/system/kubeovn"]' ]
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
  [ "$out" = '["packages/system/seaweedfs","packages/system/objectstorage-controller"]' ]
  rm -f "$tmp"
}

@test "seaweedfs change does not duplicate an already-selected objectstorage-controller" {
  tmp=$(mktemp)
  printf 'packages/system/seaweedfs/values.yaml\npackages/system/objectstorage-controller/values.yaml\n' > "$tmp"
  out=$(hack/build-matrix.sh "$tmp")
  [ "$out" = '["packages/system/seaweedfs","packages/system/objectstorage-controller"]' ]
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

@test "every package that builds an image is in the build list" {
  # The build list is maintained by hand, and a package missing from it is
  # rebuilt by no CI path: its Dockerfile and patches keep changing while the
  # pinned image stays whatever was last pushed by hand. Derive the set from
  # the package Makefiles so a new image target cannot be forgotten.
  listed=$(sed -n '/^build:/,/^[^[:space:]]/p' Makefile \
    | grep -oE 'make -C packages/[A-Za-z0-9._/-]+ image' \
    | sed -E 's/^make -C (packages[^ ]+) image$/\1/')
  missing=""
  checked=0
  for m in packages/*/*/Makefile; do
    grep -q 'docker buildx build' "$m" || continue
    checked=$((checked + 1))
    d=${m%/Makefile}
    printf '%s\n' "$listed" | grep -qx "$d" || missing="$missing $d"
  done
  [ "$checked" -gt 0 ]
  if [ -n "$missing" ]; then
    echo "image-building packages missing from the root Makefile build: list:$missing" >&2
    false
  fi
}
