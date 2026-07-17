#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/select-install.sh
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Each test runs as a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. setup()/teardown() are not honored; per-test `trap ...
# EXIT` cleanup IS honored. To assert a NON-zero exit, invert with `if` so
# `set -e` doesn't abort the test.
#
# Tests that only READ the production sources call the script directly (it
# defaults sources-dir to packages/core/platform/sources); only the tests that
# build a synthetic graph or suite tree use a scratch dir.
#
# Run with: hack/cozytest.sh hack/select-install_test.bats
# -----------------------------------------------------------------------------

@test "single app selects its forward dependency closure" {
    output=$(hack/select-install.sh "postgres")
    echo "$output" | grep -wq cozystack.postgres-application
    echo "$output" | grep -wq cozystack.postgres-operator
    echo "$output" | grep -wq cozystack.networking
    # engine edge is KEPT on the install walk (unlike select-e2e.sh)
    echo "$output" | grep -wq cozystack.cozystack-engine
}

@test "closure is transitive (deps of deps are pulled in)" {
    output=$(hack/select-install.sh "postgres")
    # cert-manager is a 2-hop dep (via postgres-operator and via the engine)
    echo "$output" | grep -wq cozystack.cert-manager
    # gateway-api-crds is a 3-hop dep (postgres-application -> networking -> gateway-api-crds)
    echo "$output" | grep -wq cozystack.gateway-api-crds
}

@test "app pulls its direct operator dependencies" {
    output=$(hack/select-install.sh "harbor")
    echo "$output" | grep -wq cozystack.harbor-application
    echo "$output" | grep -wq cozystack.postgres-operator
    echo "$output" | grep -wq cozystack.redis-operator
    echo "$output" | grep -wq cozystack.objectstorage-controller
}

@test "kubernetes suites map back to the kubernetes application source" {
    output=$(hack/select-install.sh "kubernetes-latest")
    echo "$output" | grep -wq cozystack.kubernetes-application
}

@test "multiple suites union their closures" {
    output=$(hack/select-install.sh "postgres kafka")
    echo "$output" | grep -wq cozystack.postgres-application
    echo "$output" | grep -wq cozystack.kafka-application
}

@test "suite list can be read from stdin with -" {
    output=$(printf '%s\n' "postgres kafka" | hack/select-install.sh -)
    echo "$output" | grep -wq cozystack.postgres-application
    echo "$output" | grep -wq cozystack.kafka-application
    echo "$output" | grep -wq cozystack.cozystack-engine
}

@test "empty suites select nothing" {
    output=$(hack/select-install.sh "")
    [ -z "$output" ]
}

@test "unmapped suites are a hard error, all reported, no partial output" {
    # Fail closed: a suite that maps to nothing must abort, not silently emit an
    # empty set (which would install nothing and let the run pass vacuously).
    # Also lock the contract that EVERY bad suite is reported in one run (not
    # just the first) and that no partial closure leaks to stdout.
    out=$(mktemp); err=$(mktemp)
    trap 'rm -f "$out" "$err"' EXIT
    if hack/select-install.sh "bad-one postgres bad-two" >"$out" 2>"$err"; then
        echo "expected a non-zero exit for unmapped suites" >&2
        exit 1
    fi
    grep -q "bad-one" "$err"
    grep -q "bad-two" "$err"
    [ ! -s "$out" ]
}

@test "suite whose source omits the -application suffix resolves via fallback" {
    # kuberture's PackageSource is cozystack.kuberture (no -application suffix)
    output=$(hack/select-install.sh "kuberture")
    echo "$output" | grep -wq cozystack.kuberture
}

@test "securitygroup closure includes the engine that serves sdn.cozystack.io" {
    # Regression for the under-selection bug: the securitygroup suite applies
    # sdn.cozystack.io/v1alpha1, served by the aggregated apiserver
    # (a cozystack-engine component). The forward walk from the controller can't
    # reach the engine, so it must be seeded. Assert the controller AND the
    # engine AND the engine's stable prerequisites — asserting only the
    # controller (a single member) is what let the bug through.
    output=$(hack/select-install.sh "securitygroup")
    echo "$output" | grep -wq cozystack.securitygroup-controller
    echo "$output" | grep -wq cozystack.cozystack-engine
    echo "$output" | grep -wq cozystack.cert-manager
    echo "$output" | grep -wq cozystack.networking
    echo "$output" | grep -wq cozystack.gateway-api-crds
}

@test "validate passes on the real source graph and suite mapping" {
    hack/select-install.sh --validate
}

@test "validate detects a dangling dependency" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/sources" "$tmp/suites"
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
    # empty suites-dir isolates this to the graph check
    if hack/select-install.sh --validate "$tmp/sources" "$tmp/suites" 2>/dev/null; then
        echo "expected validation to fail on a dangling dependency" >&2
        exit 1
    fi
}

@test "validate detects a dependency cycle" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/sources" "$tmp/suites"
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
    # empty suites-dir isolates this to the graph check
    if hack/select-install.sh --validate "$tmp/sources" "$tmp/suites" 2>/dev/null; then
        echo "expected validation to fail on a dependency cycle" >&2
        exit 1
    fi
}

@test "validate detects a suite directory with no source mapping" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/suites/totally-unmapped-suite"
    : > "$tmp/suites/totally-unmapped-suite/chainsaw-test.yaml"
    # real graph (so graph checks pass); the only failure is the unmapped suite
    if hack/select-install.sh --validate packages/core/platform/sources "$tmp/suites" 2>/dev/null; then
        echo "expected validation to fail on an unmapped suite dir" >&2
        exit 1
    fi
}

@test "validate fails when the suites dir does not exist" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    # real graph passes the graph checks; a missing suites dir must still FAIL
    # (find would yield nothing and silently pass the mapping check otherwise).
    if hack/select-install.sh --validate packages/core/platform/sources "$tmp/does-not-exist" 2>/dev/null; then
        echo "expected validate to fail on a missing suites dir" >&2
        exit 1
    fi
}

@test "closure fails closed when the engine PackageSource is absent" {
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/sources"
    # a lone app source, with no cozystack.cozystack-engine in the graph
    cat > "$tmp/sources/foo-application.yaml" <<'YAML'
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: cozystack.foo-application
spec:
  variants:
    - name: default
YAML
    if hack/select-install.sh "foo" "$tmp/sources" 2>/dev/null; then
        echo "expected a non-zero exit when the engine source is missing" >&2
        exit 1
    fi
}
