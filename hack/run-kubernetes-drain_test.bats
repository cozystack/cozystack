#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for cozy_tenant_drained in hack/e2e-apps/run-kubernetes.sh
#
# cozy_tenant_drained is the pure exit-condition of the inter-test drain loop
# (cozy_wait_tenant_drained). Each argument is one resource-probe capture: the
# stdout of a `kubectl get -o name` (empty once the resource is gone) or the
# literal "err" the loop substitutes when a probe itself fails, so an API blip
# is never misread as "the tenant has drained". The function returns 0 (drained)
# only when every capture is empty/whitespace, non-zero otherwise.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Assertions are expressed as
# direct shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-drain_test.bats
# -----------------------------------------------------------------------------

@test "all-empty captures report drained" {
    . hack/e2e-apps/run-kubernetes.sh
    if ! cozy_tenant_drained "" "" ""; then
        echo "expected drained when every capture is empty" >&2
        exit 1
    fi
}

@test "no arguments reports drained" {
    . hack/e2e-apps/run-kubernetes.sh
    if ! cozy_tenant_drained; then
        echo "expected drained when there are no captures" >&2
        exit 1
    fi
}

@test "a remaining VirtualMachine reports not-drained" {
    . hack/e2e-apps/run-kubernetes.sh
    if cozy_tenant_drained "virtualmachine.kubevirt.io/kubernetes-test-latest-version-md0-abcde" "" ""; then
        echo "expected not-drained while a VirtualMachine is still present" >&2
        exit 1
    fi
}

@test "a remaining PVC in the last capture reports not-drained" {
    . hack/e2e-apps/run-kubernetes.sh
    if cozy_tenant_drained "" "" "persistentvolumeclaim/disk-system-kubernetes-test-latest-version-md0-abcde"; then
        echo "expected not-drained while a worker-disk PVC is still present" >&2
        exit 1
    fi
}

@test "a multi-line resource list reports not-drained" {
    . hack/e2e-apps/run-kubernetes.sh
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
    . hack/e2e-apps/run-kubernetes.sh
    if cozy_tenant_drained "" "err" ""; then
        echo "expected not-drained when a probe returned the err sentinel" >&2
        exit 1
    fi
}

@test "whitespace-only captures are treated as drained" {
    # `kubectl get -o name` against an empty result set yields no resource
    # names; a stray newline must not be mistaken for a live resource.
    . hack/e2e-apps/run-kubernetes.sh
    blank=$(printf '\n')
    if ! cozy_tenant_drained "$blank" "" "  "; then
        echo "expected drained when captures hold only whitespace" >&2
        exit 1
    fi
}
