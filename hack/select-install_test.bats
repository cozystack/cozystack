#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/select-install.sh
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Each test runs as a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. setup()/teardown() are not honored, and cleanup goes on
# the last line of the body rather than in an EXIT trap -- a trap there replaces
# the bats binary's own, and a failing test then prints no TAP line at all. To
# assert a NON-zero exit, invert with `if` so `set -e` doesn't abort the test.
#
# Tests that only READ the production sources call the script directly (it
# defaults sources-dir to packages/core/platform/sources); only the tests that
# build a synthetic graph or suite tree use a scratch dir.
#
# Run with: hack/cozytest.sh hack/select-install_test.bats
# -----------------------------------------------------------------------------

assert_contains_package() {
    printf '%s\n' "$1" | tr ' ' '\n' | grep -Fxq "$2"
}

@test "single app selects its forward dependency closure" {
    output=$(hack/select-install.sh "postgres")
    assert_contains_package "$output" cozystack.postgres-application
    assert_contains_package "$output" cozystack.postgres-operator
    assert_contains_package "$output" cozystack.networking
    # engine edge is KEPT on the install walk (unlike select-e2e.sh)
    assert_contains_package "$output" cozystack.cozystack-engine
}

@test "closure is transitive (deps of deps are pulled in)" {
    output=$(hack/select-install.sh "postgres")
    # cert-manager is a 2-hop dep (via postgres-operator and via the engine)
    assert_contains_package "$output" cozystack.cert-manager
    # gateway-api-crds is a 3-hop dep (postgres-application -> networking -> gateway-api-crds)
    assert_contains_package "$output" cozystack.gateway-api-crds
}

@test "app pulls its direct operator dependencies" {
    output=$(hack/select-install.sh "harbor")
    assert_contains_package "$output" cozystack.harbor-application
    assert_contains_package "$output" cozystack.postgres-operator
    assert_contains_package "$output" cozystack.redis-operator
    assert_contains_package "$output" cozystack.objectstorage-controller
}

@test "kubernetes suites map back to the kubernetes application source" {
    output=$(hack/select-install.sh "kubernetes-latest")
    assert_contains_package "$output" cozystack.kubernetes-application
}

@test "vminstance keeps both application owners" {
    output=$(hack/select-install.sh "vminstance")
    printf '%s\n' "$output" | tr ' ' '\n' | grep -Fxq cozystack.vm-instance-application
    printf '%s\n' "$output" | tr ' ' '\n' | grep -Fxq cozystack.vm-disk-application
    printf '%s\n' "$output" | tr ' ' '\n' | grep -Fxq cozystack.kubevirt-cdi
}

@test "multiple suites union their closures" {
    output=$(hack/select-install.sh "postgres kafka")
    assert_contains_package "$output" cozystack.postgres-application
    assert_contains_package "$output" cozystack.kafka-application
}

@test "suite list can be read from stdin with -" {
    output=$(printf '%s\n' "postgres kafka" | hack/select-install.sh -)
    assert_contains_package "$output" cozystack.postgres-application
    assert_contains_package "$output" cozystack.kafka-application
    assert_contains_package "$output" cozystack.cozystack-engine
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
    if hack/select-install.sh "bad-one postgres bad-two" >"$out" 2>"$err"; then
        echo "expected a non-zero exit for unmapped suites" >&2
        exit 1
    fi
    grep -q "bad-one" "$err"
    grep -q "bad-two" "$err"
    [ ! -s "$out" ]
    rm -f "$out" "$err"
}

@test "suite whose source omits the -application suffix resolves via fallback" {
    # kuberture's PackageSource is cozystack.kuberture (no -application suffix)
    output=$(hack/select-install.sh "kuberture")
    assert_contains_package "$output" cozystack.kuberture
}

@test "securitygroup closure includes the engine that serves sdn.cozystack.io" {
    # Regression for the under-selection bug: the securitygroup suite applies
    # sdn.cozystack.io/v1alpha1, served by the aggregated apiserver
    # (a cozystack-engine component). The forward walk from the controller can't
    # reach the engine, so it must be seeded. Assert the controller AND the
    # engine AND the engine's stable prerequisites — asserting only the
    # controller (a single member) is what let the bug through.
    output=$(hack/select-install.sh "securitygroup")
    assert_contains_package "$output" cozystack.securitygroup-controller
    assert_contains_package "$output" cozystack.cozystack-engine
    assert_contains_package "$output" cozystack.cert-manager
    assert_contains_package "$output" cozystack.networking
    assert_contains_package "$output" cozystack.gateway-api-crds
}

@test "etcd closure includes the operator that serves its CRD" {
    # The other half of the same missing edge. extra/etcd renders kind:
    # EtcdCluster from etcd-operator.cozystack.io/v1alpha2, so an etcd suite
    # installed without cozystack.etcd-operator has no CRD to apply against and
    # fails on `no matches for kind "EtcdCluster"`. The forward walk reaches the
    # operator only through etcd-application's dependsOn, so this asserts the
    # operator AND the deps it drags in -- asserting only the app source is what
    # left the gap invisible.
    output=$(hack/select-install.sh "etcd")
    assert_contains_package "$output" cozystack.etcd-application
    assert_contains_package "$output" cozystack.etcd-operator
    assert_contains_package "$output" cozystack.cert-manager
    assert_contains_package "$output" cozystack.vertical-pod-autoscaler
    assert_contains_package "$output" cozystack.cozystack-engine
}

@test "validate passes on the real source graph and suite mapping" {
    hack/select-install.sh --validate
}

@test "validate detects a dangling dependency" {
    tmp=$(mktemp -d)
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
    rm -rf "$tmp"
}

@test "validate detects a dependency cycle" {
    tmp=$(mktemp -d)
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
    rm -rf "$tmp"
}

@test "validate detects a suite directory with no source mapping" {
    tmp=$(mktemp -d)
    mkdir -p "$tmp/suites/totally-unmapped-suite"
    : > "$tmp/suites/totally-unmapped-suite/chainsaw-test.yaml"
    # real graph (so graph checks pass); the only failure is the unmapped suite
    if hack/select-install.sh --validate packages/core/platform/sources "$tmp/suites" 2>/dev/null; then
        echo "expected validation to fail on an unmapped suite dir" >&2
        exit 1
    fi
    rm -rf "$tmp"
}

@test "validate detects a suite mapped to a source absent from the graph" {
    # suite_to_source() hardcodes securitygroup -> cozystack.securitygroup-controller;
    # a sources dir without that PackageSource must fail validation (fail closed),
    # pinning that the elif validates every mapping, including hardcoded ones,
    # against the graph.
    tmp=$(mktemp -d)
    mkdir -p "$tmp/sources" "$tmp/suites/securitygroup"
    : > "$tmp/suites/securitygroup/chainsaw-test.yaml"
    cat > "$tmp/sources/standalone.yaml" <<'YAML'
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: cozystack.standalone
spec:
  variants:
    - name: default
YAML
    err=$(hack/select-install.sh --validate "$tmp/sources" "$tmp/suites" 2>&1 1>/dev/null) && {
        echo "expected validation to fail on a mapping to a missing source" >&2
        exit 1
    }
    echo "$err" | grep -q "maps to 'cozystack.securitygroup-controller', not a PackageSource"
    rm -rf "$tmp"
}

@test "validate fails when the suites dir does not exist" {
    tmp=$(mktemp -d)
    # real graph passes the graph checks; a missing suites dir must still FAIL
    # (find would yield nothing and silently pass the mapping check otherwise).
    if hack/select-install.sh --validate packages/core/platform/sources "$tmp/does-not-exist" 2>/dev/null; then
        echo "expected validate to fail on a missing suites dir" >&2
        exit 1
    fi
    rm -rf "$tmp"
}

@test "validate fails when suite discovery fails" {
    tmp=$(mktemp -d)
    mkdir -p "$tmp/bin" "$tmp/suites/one"
    : > "$tmp/suites/one/chainsaw-test.yaml"
    cat > "$tmp/bin/find" <<'SH'
#!/bin/sh
exit 42
SH
    chmod +x "$tmp/bin/find"
    out=$(mktemp); err=$(mktemp)
    if PATH="$tmp/bin:$PATH" hack/select-install.sh --validate \
        packages/core/platform/sources "$tmp/suites" >"$out" 2>"$err"; then
        echo "expected validate to fail when find fails" >&2
        exit 1
    fi
    [ ! -s "$out" ]
    grep -Fq "find failed listing Chainsaw suites" "$err"
    rm -rf "$tmp"; rm -f "$out" "$err"
}

@test "yq producer failures cannot become a successful partial graph" {
    tmp=$(mktemp -d)
    mkdir -p "$tmp/bin"
    real_yq=$(command -v yq)
    cat > "$tmp/bin/yq" <<'SH'
#!/bin/sh
case "${FAIL_YQ:-}" in
  names)
    case "$*" in
      *metadata.name*) echo cozystack.partial; exit 42 ;;
    esac
    ;;
  dependencies)
    case "$*" in
      *spec.variants*) printf 'cozystack.partial\tcozystack.missing\n'; exit 42 ;;
    esac
    ;;
esac
exec "$REAL_YQ" "$@"
SH
    chmod +x "$tmp/bin/yq"
    for stage in names dependencies; do
        out="$tmp/$stage.out"; err="$tmp/$stage.err"
        if REAL_YQ="$real_yq" FAIL_YQ="$stage" PATH="$tmp/bin:$PATH" \
            hack/select-install.sh postgres >"$out" 2>"$err"; then
            echo "expected the $stage yq failure to propagate" >&2
            exit 1
        fi
        [ ! -s "$out" ]
        grep -Fq "yq failed while reading PackageSource" "$err"
    done
    rm -rf "$tmp"
}

@test "closure fails closed when the engine PackageSource is absent" {
    tmp=$(mktemp -d)
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
    rm -rf "$tmp"
}

# --disabled emits the complement of the closure: what bundles.disabledPackages
# must hold so an ordinary platform variant installs only what the suites need.
@test "disabled mode: closure and complement partition the source list" {
    # Count names only after yq itself has succeeded; dash has no pipefail.
    names=$(yq -rN '.metadata.name | select(. != null and . != "")' \
              packages/core/platform/sources/*.yaml)
    total=$(printf '%s\n' "$names" | sort -u | wc -l | tr -d ' ')
    keep_output=$(hack/select-install.sh "postgres")
    drop_output=$(hack/select-install.sh --disabled "postgres")
    keep=$(printf '%s\n' "$keep_output" | wc -w | tr -d ' ')
    drop=$(printf '%s\n' "$drop_output" | wc -w | tr -d ' ')
    sum=$((keep + drop))
    if [ "$sum" -ne "$total" ]; then
        echo "closure ($keep) + complement ($drop) = $sum, but there are $total PackageSources" >&2
        exit 1
    fi
    [ "$total" -eq 101 ]
    [ "$keep" -eq 33 ]
    [ "$drop" -eq 68 ]
}

@test "disabled mode: never lists a package the closure keeps" {
    keep=$(hack/select-install.sh "postgres kafka")
    drop=$(hack/select-install.sh --disabled "postgres kafka")
    for k in $keep; do
        case " $drop " in
            *" $k "*) echo "package '$k' is both kept and disabled" >&2; exit 1 ;;
        esac
    done
}

# A declared dependency of something kept can never land in the complement.
# Assert the property over every suite instead of citing an unreachable pair.
@test "disabled mode: no kept package has a dependency in the complement" {
    deps=$(mktemp)
    yq -rN '.metadata.name as $n | .spec.variants[]?.dependsOn[]? | select(. != null and . != "") | $n + " " + .' \
      packages/core/platform/sources/*.yaml > "$deps"

    for suite in $(find hack/e2e-chainsaw -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml \
                    | sed -e 's,^hack/e2e-chainsaw/,,' -e 's,/chainsaw-test\.yaml$,,'); do
        keep=$(hack/select-install.sh "$suite")
        drop=$(hack/select-install.sh --disabled "$suite")
        for k in $keep; do
            for d in $(awk -v owner="$k" '$1 == owner {print $2}' "$deps"); do
                case " $drop " in
                    *" $d "*)
                        echo "suite '$suite': kept '$k' depends on '$d', which is in the disable list" >&2
                        rm -f "$deps"; exit 1 ;;
                esac
            done
        done
    done
    rm -f "$deps"
}

# Direct seeds correspond to unconditional install steps, even when another
# baseline source also reaches them through dependsOn.
@test "disabled mode: never disables the direct runtime baseline" {
    drop=$(hack/select-install.sh --disabled "postgres")
    for b in cozystack.cozystack-engine cozystack.cozystack-basics \
             cozystack.tenant-application cozystack.etcd-application \
             cozystack.ingress-application cozystack.monitoring-application \
             cozystack.seaweedfs-application cozystack.linstor \
             cozystack.kubevirt-cdi cozystack.metallb cozystack.capi-operator \
             cozystack.capi-provider-bootstrap-kubeadm \
             cozystack.capi-provider-core cozystack.capi-provider-cp-kamaji \
             cozystack.capi-provider-infra-kubevirt cozystack.keycloak \
             cozystack.keycloak-operator; do
        case " $drop " in
            *" $b "*) echo "baseline package '$b' is in the disable list" >&2; exit 1 ;;
        esac
    done
}

@test "runtime baseline reaches every derived controller and CRD prerequisite" {
    keep=$(hack/select-install.sh "postgres")
    for required in cozystack.cert-manager cozystack.networking \
        cozystack.gateway-api-crds cozystack.etcd-operator \
        cozystack.grafana-operator cozystack.postgres-operator \
        cozystack.victoria-metrics-operator cozystack.prometheus-operator-crds \
        cozystack.vertical-pod-autoscaler cozystack.reloader \
        cozystack.snapshot-controller cozystack.gateway-application \
        cozystack.info-application cozystack.kamaji cozystack.kubevirt; do
        if ! printf '%s\n' "$keep" | tr ' ' '\n' | grep -Fxq "$required"; then
            echo "runtime baseline is missing derived prerequisite '$required'" >&2
            exit 1
        fi
    done
}

@test "runtime baseline fails closed when a provider prerequisite edge is lost" {
    tmp=$(mktemp -d)
    cp -r packages/core/platform/sources "$tmp/sources"
    yq -i '(.spec.variants[].dependsOn) -= ["cozystack.kamaji"]' \
      "$tmp/sources/capi-provider-cp-kamaji.yaml"
    out="$tmp/out"; err="$tmp/err"
    if hack/select-install.sh postgres "$tmp/sources" >"$out" 2>"$err"; then
        echo "expected a missing Kamaji prerequisite to fail closed" >&2
        exit 1
    fi
    [ ! -s "$out" ]
    grep -Fq "runtime baseline dependency closure is missing: cozystack.kamaji" "$err"

    cp packages/core/platform/sources/capi-provider-cp-kamaji.yaml \
      "$tmp/sources/capi-provider-cp-kamaji.yaml"
    yq -i '(.spec.variants[].dependsOn) -= ["cozystack.kubevirt"]' \
      "$tmp/sources/capi-provider-infra-kubevirt.yaml"
    if hack/select-install.sh postgres "$tmp/sources" >"$out" 2>"$err"; then
        echo "expected a missing KubeVirt prerequisite to fail closed" >&2
        exit 1
    fi
    [ ! -s "$out" ]
    grep -Fq "runtime baseline dependency closure is missing: cozystack.kubevirt" "$err"
    rm -rf "$tmp"
}

@test "runtime baseline fails closed when storage prerequisite edges are lost" {
    tmp=$(mktemp -d)
    cp -r packages/core/platform/sources "$tmp/sources"
    yq -i '(.spec.variants[].dependsOn) -= ["cozystack.reloader", "cozystack.snapshot-controller"]' \
      "$tmp/sources/linstor.yaml"
    out="$tmp/out"; err="$tmp/err"
    if hack/select-install.sh postgres "$tmp/sources" >"$out" 2>"$err"; then
        echo "expected missing LINSTOR prerequisites to fail closed" >&2
        exit 1
    fi
    [ ! -s "$out" ]
    grep -Fq "cozystack.reloader" "$err"
    grep -Fq "cozystack.snapshot-controller" "$err"
    rm -rf "$tmp"
}

@test "generated Helm values fixture matches the current disable list" {
    generated_raw=$(hack/select-install.sh --disabled postgres)
    generated=$(printf '%s\n' "$generated_raw" | tr ' ' '\n' | sort)
    fixture_raw=$(yq -r '.bundles.disabledPackages[]' \
      packages/core/platform/tests/fixtures/selective-install-postgres-values.yaml)
    fixture=$(printf '%s\n' "$fixture_raw" | sort)
    if [ "$generated" != "$fixture" ]; then
        echo "selective-install-postgres-values.yaml is stale; regenerate it from select-install.sh" >&2
        exit 1
    fi

    tmp=$(mktemp -d)
    cp -r packages/core/platform/sources "$tmp/sources"
    cat > "$tmp/sources/fixture-drift.yaml" <<'YAML'
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: cozystack.fixture-drift
spec:
  variants:
    - name: default
YAML
    drifted_raw=$(hack/select-install.sh --disabled postgres "$tmp/sources")
    drifted=$(printf '%s\n' "$drifted_raw" | tr ' ' '\n' | sort)
    if [ "$drifted" = "$fixture" ]; then
        echo "a new PackageSource did not invalidate the generated fixture" >&2
        exit 1
    fi
    printf '%s\n' "$drifted" | grep -Fxq cozystack.fixture-drift
    rm -rf "$tmp"
}

# An empty closure would make the complement the entire platform. As an install
# instruction that is a platform with no packages, so it must refuse rather than
# emit it.
@test "disabled mode: refuses an empty selection" {
    out=$(mktemp); err=$(mktemp)
    if hack/select-install.sh --disabled "" >"$out" 2>"$err"; then
        echo "expected a non-zero exit for an empty selection, got: $(cat "$out")" >&2
        rm -f "$out" "$err"; exit 1
    fi
    grep -q "refusing to emit a disable list" "$err" \
      || { echo "expected the refusal message, got: $(cat "$err")" >&2; rm -f "$out" "$err"; exit 1; }
    rm -f "$out" "$err"
}
