#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/select-e2e.sh
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Each test runs as a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. setup()/teardown() are not honored — each test creates
# and cleans its own scratch dir.
#
# Run with: hack/cozytest.sh hack/select-e2e_test.bats
# -----------------------------------------------------------------------------

@test "single app diff selects only that bats" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/apps/postgres/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "postgres" ]
}

@test "operator diff selects all dependent app bats" {
    # postgres-operator is depended on by postgres-application, harbor-application
    # (Harbor uses postgres as its backing DB), and monitoring-application (Grafana
    # DB). monitoring isn't in hack/e2e-apps/ so it's filtered out by the selector.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/system/postgres-operator/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    echo "$output" | grep -wq postgres
    echo "$output" | grep -wq harbor
    if echo "$output" | grep -wq kafka; then
        echo "operator diff must not trigger full suite; got: $output" >&2
        exit 1
    fi
}

@test "networking change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/system/cilium/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    # Full suite means more than 5 bats files
    [ "$(echo "$output" | wc -w)" -gt 5 ]
}

@test "library change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/library/cozy-lib/templates/_helpers.tpl" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$(echo "$output" | wc -w)" -gt 5 ]
}

@test "docs-only diff selects nothing" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "docs/README.md" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ -z "$output" ]
}

@test "kubernetes-application maps to two bats files" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "packages/apps/kubernetes/values.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    echo "$output" | grep -q "kubernetes-latest"
    echo "$output" | grep -q "kubernetes-previous"
}

@test "dashboards-only diff selects nothing (path is plural)" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "dashboards/gpu/gpu-fleet.json" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ -z "$output" ]
}

@test "shared E2E helper script triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-apps/run-kubernetes.sh" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$(echo "$output" | wc -w)" -gt 5 ]
}

@test "install bats triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-install-cozystack.bats" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$(echo "$output" | wc -w)" -gt 5 ]
}

@test "per-app bats edit selects only that app, never escalates" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo "hack/e2e-apps/redis.bats" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$output" = "redis" ]
}

@test "release-e2e workflow change triggers full suite" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    echo ".github/workflows/release-e2e.yaml" > "$tmp/diff"
    output=$(hack/select-e2e.sh "$tmp/diff" "$tmp/sources")
    [ "$(echo "$output" | wc -w)" -gt 5 ]
}
