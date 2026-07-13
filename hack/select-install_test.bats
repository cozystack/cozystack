#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/select-install.sh
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Each test runs as a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. setup()/teardown() are not honored — each test creates
# and cleans its own scratch dir. To assert a NON-zero exit, invert with `if`
# so `set -e` doesn't abort the test.
#
# Run with: hack/cozytest.sh hack/select-install_test.bats
# -----------------------------------------------------------------------------

@test "single app selects its forward dependency closure" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "postgres" "$tmp/sources")
    echo "$output" | grep -wq cozystack.postgres-application
    echo "$output" | grep -wq cozystack.postgres-operator
    echo "$output" | grep -wq cozystack.networking
    # engine edge is KEPT on the install walk (unlike select-e2e.sh)
    echo "$output" | grep -wq cozystack.cozystack-engine
}

@test "closure is transitive (deps of deps are pulled in)" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "postgres" "$tmp/sources")
    # cert-manager is a 2-hop dep (via postgres-operator and via the engine)
    echo "$output" | grep -wq cozystack.cert-manager
    # gateway-api-crds is a 3-hop dep (postgres-application -> networking -> gateway-api-crds)
    echo "$output" | grep -wq cozystack.gateway-api-crds
}

@test "app pulls its direct operator dependencies" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "harbor" "$tmp/sources")
    echo "$output" | grep -wq cozystack.harbor-application
    echo "$output" | grep -wq cozystack.postgres-operator
    echo "$output" | grep -wq cozystack.redis-operator
    echo "$output" | grep -wq cozystack.objectstorage-controller
}

@test "kubernetes suites map back to the kubernetes application source" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "kubernetes-latest" "$tmp/sources")
    echo "$output" | grep -wq cozystack.kubernetes-application
}

@test "multiple suites union their closures" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "postgres kafka" "$tmp/sources")
    echo "$output" | grep -wq cozystack.postgres-application
    echo "$output" | grep -wq cozystack.kafka-application
}

@test "empty suites select nothing" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "" "$tmp/sources")
    [ -z "$output" ]
}

@test "unknown suite is skipped, not fatal" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "definitely-not-an-app" "$tmp/sources" 2>/dev/null)
    [ -z "$output" ]
}

@test "suite whose source omits the -application suffix resolves via fallback" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    # kuberture's PackageSource is cozystack.kuberture (no -application suffix)
    output=$(hack/select-install.sh "kuberture" "$tmp/sources")
    echo "$output" | grep -wq cozystack.kuberture
}

@test "suite with an irregular source name maps via explicit override" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    output=$(hack/select-install.sh "securitygroup" "$tmp/sources")
    echo "$output" | grep -wq cozystack.securitygroup-controller
}

@test "core-platform suite contributes no package and emits no warning" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    # serviceexposure is a core-platform CRD; nothing to enable, no warning.
    err=$(hack/select-install.sh "serviceexposure" "$tmp/sources" 2>&1 1>/dev/null)
    [ -z "$err" ]
    output=$(hack/select-install.sh "serviceexposure" "$tmp/sources" 2>/dev/null)
    [ -z "$output" ]
}

@test "validate passes on the real source graph" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cp -r packages/core/platform/sources "$tmp/sources"
    hack/select-install.sh --validate "$tmp/sources"
}

@test "validate detects a dangling dependency" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/sources"
    cat > "$tmp/sources/foo-application.yaml" <<'YAML'
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: cozystack.foo-application
spec:
  variants:
    - name: default
      dependsOn:
        - cozystack.does-not-exist
YAML
    if hack/select-install.sh --validate "$tmp/sources" 2>/dev/null; then
        echo "expected validation to fail on a dangling dependency" >&2
        exit 1
    fi
}

@test "validate detects a dependency cycle" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/sources"
    cat > "$tmp/sources/a.yaml" <<'YAML'
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: cozystack.a
spec:
  variants:
    - name: default
      dependsOn:
        - cozystack.b
YAML
    cat > "$tmp/sources/b.yaml" <<'YAML'
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: cozystack.b
spec:
  variants:
    - name: default
      dependsOn:
        - cozystack.a
YAML
    if hack/select-install.sh --validate "$tmp/sources" 2>/dev/null; then
        echo "expected validation to fail on a dependency cycle" >&2
        exit 1
    fi
}
