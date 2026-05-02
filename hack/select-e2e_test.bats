#!/usr/bin/env bats

setup() {
  TMPDIR=$(mktemp -d)
  cp -r packages/core/platform/sources $TMPDIR/sources
}

@test "single app diff selects only that bats" {
  echo "packages/apps/postgres/values.yaml" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$status" -eq 0 ]
  [ "$output" = "postgres" ]
}

@test "operator diff selects all dependent app bats" {
  # postgres-operator is depended on by postgres-application, harbor-application
  # (Harbor uses postgres as its backing DB), and monitoring-application (Grafana
  # DB). monitoring isn't in hack/e2e-apps/ so it's filtered out by the selector.
  echo "packages/system/postgres-operator/values.yaml" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$status" -eq 0 ]
  echo "$output" | grep -wq postgres
  echo "$output" | grep -wq harbor
  # Must NOT trigger full suite — confirm an unrelated bats is absent
  ! echo "$output" | grep -wq kafka
}

@test "networking change triggers full suite" {
  echo "packages/system/cilium/values.yaml" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$status" -eq 0 ]
  # Full suite means more than 5 bats files
  [ "$(echo $output | wc -w)" -gt 5 ]
}

@test "library change triggers full suite" {
  echo "packages/library/cozy-lib/templates/_helpers.tpl" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$(echo $output | wc -w)" -gt 5 ]
}

@test "docs-only diff selects nothing" {
  echo "docs/README.md" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "kubernetes-application maps to two bats files" {
  echo "packages/apps/kubernetes/values.yaml" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  echo $output | grep -q "kubernetes-latest"
  echo $output | grep -q "kubernetes-previous"
}

@test "dashboards-only diff selects nothing (path is plural)" {
  echo "dashboards/gpu/gpu-fleet.json" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "shared E2E helper script triggers full suite" {
  echo "hack/e2e-apps/run-kubernetes.sh" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$(echo $output | wc -w)" -gt 5 ]
}

@test "install bats triggers full suite" {
  echo "hack/e2e-install-cozystack.bats" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$(echo $output | wc -w)" -gt 5 ]
}

@test "per-app bats edit selects only that app, never escalates" {
  echo "hack/e2e-apps/redis.bats" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$status" -eq 0 ]
  [ "$output" = "redis" ]
}

@test "release-e2e workflow change triggers full suite" {
  echo ".github/workflows/release-e2e.yaml" > $TMPDIR/diff
  run hack/select-e2e.sh $TMPDIR/diff $TMPDIR/sources
  [ "$(echo $output | wc -w)" -gt 5 ]
}

teardown() {
  rm -rf $TMPDIR
}
