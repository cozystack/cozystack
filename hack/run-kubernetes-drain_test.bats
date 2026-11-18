#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the scoped tenant-drain helpers in run-kubernetes.sh.
#
# cozy_tenant_drained is the pure exit-condition of the inter-test drain loop
# (cozy_wait_tenant_drained). Each argument is one resource-probe capture: the
# stdout of a `kubectl get -o name` (empty once the resource is gone) or the
# literal "err" the loop substitutes when a probe itself fails, so an API blip
# is never misread as "the tenant has drained". The function returns 0 (drained)
# only when every capture is empty/whitespace, non-zero otherwise.
#
# CI runs this file under Bats through `make bats-unit-tests`. It also remains
# compatible with the legacy `hack/cozytest.sh` translator, whose awk parser
# recognizes only @test blocks and a bare `}` on its own line. The tests avoid
# `run` and `$status` for that compatibility path and use direct shell assertions.
#
# Run with: bats hack/run-kubernetes-drain_test.bats
# -----------------------------------------------------------------------------

load test_helper

@test "all-empty captures report drained" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if ! cozy_tenant_drained "" "" ""; then
        echo "expected drained when every capture is empty" >&2
        exit 1
    fi
}

@test "no arguments reports drained" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if ! cozy_tenant_drained; then
        echo "expected drained when there are no captures" >&2
        exit 1
    fi
}

@test "a remaining VirtualMachine reports not-drained" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_tenant_drained "virtualmachine.kubevirt.io/kubernetes-test-latest-version-md0-abcde" "" ""; then
        echo "expected not-drained while a VirtualMachine is still present" >&2
        exit 1
    fi
}

@test "a remaining PVC in the last capture reports not-drained" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_tenant_drained "" "" "persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md0-abcde"; then
        echo "expected not-drained while a worker-disk PVC is still present" >&2
        exit 1
    fi
}

@test "a multi-line resource list reports not-drained" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    vms=$(printf 'virtualmachine.kubevirt.io/a\nvirtualmachine.kubevirt.io/b\n')
    if cozy_tenant_drained "$vms" "" ""; then
        echo "expected not-drained for a multi-line VirtualMachine list" >&2
        exit 1
    fi
}

@test "the err sentinel is never misread as drained" {
    # A failed probe (transient API error) substitutes the literal "err". Even
    # when every other capture is empty, an err capture MUST count as
    # not-drained so the loop keeps polling instead of declaring victory on a
    # blip and letting the next tenant schedule onto an un-vacated sandbox.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_tenant_drained "" "err" ""; then
        echo "expected not-drained when a probe returned the err sentinel" >&2
        exit 1
    fi
}

@test "whitespace-only captures are treated as drained" {
    # `kubectl get -o name` against an empty result set yields no resource
    # names; a stray newline must not be mistaken for a live resource.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    blank=$(printf '\n')
    if ! cozy_tenant_drained "$blank" "" "  "; then
        echo "expected drained when captures hold only whitespace" >&2
        exit 1
    fi
}

@test "PVC filtering ignores leftovers from databases and other Kubernetes clusters" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    pvcs=$(printf '%s\n' \
        'persistentvolumeclaim/clickhouse-data-test' \
        'persistentvolumeclaim/disk-system-kubernetes-test-previous-version-md0-worker-a' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md0-worker-b')
    got=$(cozy_filter_cluster_pvcs test-latest-version "$pvcs")
    expected='persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md0-worker-b'
    [ "$got" = "$expected" ] || {
        echo "PVC filter crossed cluster ownership: [$got]" >&2
        exit 1
    }
}

# The Kairos pool of the kubernetes-latest suite runs on the same cluster as
# md0; its disk must hold the barrier like md0's, a pool of another name must
# not, including one whose name only starts with md0 or with the Kairos pool's.
@test "PVC filtering keeps the Kairos pool disk and drops other pools of the cluster" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    pvcs=$(printf '%s\n' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-kairos-abcde-xyz12' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-custom-abcde-xyz12' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md01-abcde-xyz12' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-kairos2-abcde-xyz12' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md0-abcde-xyz12')
    got=$(cozy_filter_cluster_pvcs test-latest-version "$pvcs")
    expected=$(printf '%s\n' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-kairos-abcde-xyz12' \
        'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md0-abcde-xyz12')
    [ "$got" = "$expected" ] || {
        echo "PVC filter did not keep exactly md0 and the Kairos pool: [$got]" >&2
        exit 1
    }
    if cozy_tenant_drained "" "" "$(cozy_filter_cluster_pvcs test-latest-version 'persistentvolumeclaim/disk-system-kubernetes-test-latest-version-kairos-abcde-xyz12')"; then
        echo "a remaining Kairos pool disk read as drained" >&2
        exit 1
    fi
    kairos_suffix=$(sed -n 's/^KAIROS_POOL_SUFFIX=//p' hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh)
    [ "$kairos_suffix" = kairos ] || {
        echo "the filter's default Kairos pool name drifted from run-kubernetes-kairos.sh ($kairos_suffix)" >&2
        exit 1
    }
}

@test "scoped drain queries the CAPI label and ignores unrelated PVCs" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    calls=$(mktemp)
    kubectl() {
        printf '%s\n' "$*" >> "$calls"
        case "$*" in
            *" get pvc "*) printf 'persistentvolumeclaim/clickhouse-data-test\n' ;;
            *) printf '' ;;
        esac
    }

    cozy_wait_tenant_drained test-latest-version 0 >/dev/null
    [ "$(grep -c 'cluster.x-k8s.io/cluster-name=kubernetes-test-latest-version' "$calls")" -eq 2 ] || {
        echo "VM and VMI probes were not scoped to the current CAPI cluster" >&2
        cat "$calls" >&2
        exit 1
    }
}
