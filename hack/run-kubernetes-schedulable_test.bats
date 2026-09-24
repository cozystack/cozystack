#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for cozy_node_accepts_pods and cozy_has_schedulable_node in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# Together they are the pure exit-condition of the tenant scheduling gate
# (cozy_wait_schedulable_node), which runs before the backend workload is
# created so that scheduling delay is charged to its own budget instead of the
# workload's readiness budget. The input is the capture of
#   kubectl get nodes --no-headers -o custom-columns=NAME,READY,UNSCHEDULABLE,TAINTS
# where TAINTS is `.spec.taints[*].effect`, so custom-columns renders an absent
# field as the literal "<none>" and joins several effects with a comma. The
# rows below are shaped exactly as kubectl emits them.
#
# The predicate is the scheduler's own rule for a Pod with no tolerations:
# Ready, not SchedulingDisabled, and no NoSchedule/NoExecute taint.
# PreferNoSchedule only lowers the node's score and must not block.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Assertions are expressed as
# direct shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-schedulable_test.bats
# -----------------------------------------------------------------------------

@test "a Ready node with no taints accepts pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if ! cozy_node_accepts_pods True '<none>' '<none>'; then
        echo "expected a Ready, untainted, schedulable node to accept pods" >&2
        exit 1
    fi
}

@test "a NotReady node does not accept pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_node_accepts_pods False '<none>' '<none>'; then
        echo "expected a node whose Ready condition is False to be rejected" >&2
        exit 1
    fi
}

@test "an Unknown Ready condition does not accept pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_node_accepts_pods Unknown '<none>' '<none>'; then
        echo "expected a node whose Ready condition is Unknown to be rejected" >&2
        exit 1
    fi
}

@test "a cordoned node does not accept pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_node_accepts_pods True true '<none>' ; then
        echo "expected a node with .spec.unschedulable=true to be rejected" >&2
        exit 1
    fi
}

@test "a NoSchedule taint does not accept pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_node_accepts_pods True '<none>' NoSchedule; then
        echo "expected a NoSchedule-tainted node to be rejected" >&2
        exit 1
    fi
}

@test "a NoExecute taint does not accept pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_node_accepts_pods True '<none>' NoExecute; then
        echo "expected a NoExecute-tainted node to be rejected" >&2
        exit 1
    fi
}

@test "a blocking effect anywhere in a comma-joined list is found" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_node_accepts_pods True '<none>' PreferNoSchedule,NoExecute; then
        echo "expected a trailing NoExecute in a joined effect list to be found" >&2
        exit 1
    fi
    if cozy_node_accepts_pods True '<none>' NoSchedule,PreferNoSchedule; then
        echo "expected a leading NoSchedule in a joined effect list to be found" >&2
        exit 1
    fi
}

@test "PreferNoSchedule alone still accepts pods" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if ! cozy_node_accepts_pods True '<none>' PreferNoSchedule; then
        echo "expected PreferNoSchedule to lower the score, not block scheduling" >&2
        exit 1
    fi
    if ! cozy_node_accepts_pods True '<none>' PreferNoSchedule,PreferNoSchedule; then
        echo "expected several PreferNoSchedule taints not to block scheduling" >&2
        exit 1
    fi
}

@test "a Ready untainted node reports the tenant schedulable" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    nodes=$(printf '%s\n' \
        'kubernetes-test-latest-version-md0-abcde   True   <none>   <none>')
    if ! cozy_has_schedulable_node "$nodes"; then
        echo "expected a Ready, untainted node to release the gate" >&2
        exit 1
    fi
}

@test "the observed cilium/cordon node pair reports not-schedulable" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    nodes=$(printf '%s\n%s\n' \
        'kubernetes-test-latest-version-md0-abcde   True   <none>   NoSchedule' \
        'kubernetes-test-latest-version-md0-fghij   True   true     NoSchedule')
    if cozy_has_schedulable_node "$nodes"; then
        echo "expected agent-not-ready + SchedulingDisabled nodes to hold the gate" >&2
        exit 1
    fi
}

@test "one free node among blocked ones reports schedulable" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    nodes=$(printf '%s\n%s\n' \
        'kubernetes-test-latest-version-md0-abcde   True   <none>   NoSchedule' \
        'kubernetes-test-latest-version-md0-fghij   True   <none>   <none>')
    if ! cozy_has_schedulable_node "$nodes"; then
        echo "expected the second, untainted node to release the gate" >&2
        exit 1
    fi
}

@test "a node still becoming Ready reports not-schedulable" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    nodes=$(printf '%s\n' \
        'kubernetes-test-latest-version-md0-abcde   False   <none>   NoSchedule,NoExecute')
    if cozy_has_schedulable_node "$nodes"; then
        echo "expected a NotReady node to hold the gate" >&2
        exit 1
    fi
}

@test "an empty capture is never misread as schedulable" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    if cozy_has_schedulable_node ""; then
        echo "expected a failed or empty node probe to hold the gate" >&2
        exit 1
    fi
}

@test "a whitespace-only capture is never misread as schedulable" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    blank=$(printf '\n   \n')
    if cozy_has_schedulable_node "$blank"; then
        echo "expected a blank node probe to hold the gate" >&2
        exit 1
    fi
}

@test "the scheduling gate runs before the backend Deployment is applied" {
    lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # Ordering is the fix: a gate placed after the apply would charge the
    # scheduling delay to the workload's readiness budget again, which is the
    # arrangement that failed. Nothing else in the suite can catch that.
    gate=$(grep -n 'cozy_wait_schedulable_node "tenantkubeconfig-' "$lib" | head -n 1 | cut -d: -f1)
    deploy=$(grep -n 'name: "\${test_name}-backend"' "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$gate" ]; then
        echo "expected run_kubernetes_test to gate on a schedulable tenant node" >&2
        exit 1
    fi
    if [ -z "$deploy" ]; then
        echo "expected to find the backend Deployment in $lib" >&2
        exit 1
    fi
    if [ "$gate" -ge "$deploy" ]; then
        echo "expected the scheduling gate (line $gate) before the backend Deployment (line $deploy)" >&2
        exit 1
    fi
}

@test "the tenant backend workload image is pinned by digest" {
    lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # The tenant workers reach no registry mirror, so this image is fetched
    # from Docker Hub inside the readiness budget on every run. A floating tag
    # puts content of unknown size under a fixed deadline.
    ref=$(grep -E '^[[:space:]]*image: nginx' "$lib" | head -n 1)
    if [ -z "$ref" ]; then
        echo "expected the backend workload to declare an nginx image" >&2
        exit 1
    fi
    case "$ref" in
        *@sha256:*) ;;
        *)  echo "expected the backend workload image to be pinned by digest: $ref" >&2
            exit 1 ;;
    esac
}

@test "the probe asks for the columns in the order the predicate parses them" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # A shell function shadows the PATH lookup, so the gate's own kubectl call
    # lands here with no stub binary and nothing to put on PATH. The gate reads
    # the probe through a command substitution, which is a subshell, so what
    # the stub records has to travel through a file rather than a variable.
    argfile=$(mktemp)
    kubectl() {
        printf '%s\n' "$*" > "$argfile"
        printf 'kubernetes-test-latest-version-md0-abcde   True   <none>   <none>\n'
    }
    cozy_wait_schedulable_node tenantkubeconfig-test-latest-version 0 > /dev/null
    args=$(cat "$argfile")
    rm -f "$argfile"
    case "$args" in
        *"--kubeconfig tenantkubeconfig-test-latest-version"*) ;;
        *)  echo "expected the probe to carry the tenant kubeconfig: $args" >&2
            exit 1 ;;
    esac
    case "$args" in
        *--no-headers*) ;;
        *)  echo "expected --no-headers, or a header row is parsed as a node: $args" >&2
            exit 1 ;;
    esac
    # Column order is the predicate's argument order; reordering the query
    # without reordering the parse would compare a taint effect against Ready.
    case "$args" in
        *'NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,UNSCHEDULABLE:.spec.unschedulable,TAINTS:.spec.taints[*].effect'*) ;;
        *)  echo "expected NAME,READY,UNSCHEDULABLE,TAINTS in that order: $args" >&2
            exit 1 ;;
    esac
}

@test "the gate releases on a later poll once the taint clears" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    callfile=$(mktemp)
    printf '0\n' > "$callfile"
    kubectl() {
        _calls=$(( $(cat "$callfile") + 1 ))
        printf '%s\n' "$_calls" > "$callfile"
        if [ "$_calls" -eq 1 ]; then
            printf 'kubernetes-test-latest-version-md0-abcde   True   <none>   NoSchedule\n'
        else
            printf 'kubernetes-test-latest-version-md0-abcde   True   <none>   <none>\n'
        fi
    }
    rc=0
    cozy_wait_schedulable_node tenantkubeconfig-test-latest-version 60 > /dev/null || rc=$?
    calls=$(cat "$callfile")
    rm -f "$callfile"
    if [ "$rc" -ne 0 ]; then
        echo "expected the gate to release once the taint cleared" >&2
        exit 1
    fi
    if [ "$calls" -lt 2 ]; then
        echo "expected the gate to poll again rather than accept the first answer" >&2
        exit 1
    fi
}

@test "a tainted node holds the gate to its own deadline" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl() {
        printf 'kubernetes-test-latest-version-md0-abcde   True   <none>   NoSchedule\n'
    }
    if cozy_wait_schedulable_node tenantkubeconfig-test-latest-version 0 > /dev/null 2>&1; then
        echo "expected an elapsed deadline with a tainted node to fail the gate" >&2
        exit 1
    fi
}

@test "a failing probe holds the gate rather than releasing it" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl() {
        echo 'Unable to connect to the server' >&2
        return 1
    }
    if cozy_wait_schedulable_node tenantkubeconfig-test-latest-version 0 > /dev/null 2>&1; then
        echo "expected an unreachable tenant API to hold the gate, not release it" >&2
        exit 1
    fi
}

@test "the scan reports a hit without exiting its caller" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    nodes=$(printf '%s\n' \
        'kubernetes-test-latest-version-md0-abcde   True   <none>   <none>')
    # The scan signals a hit with `exit 0`, which is contained only because it
    # runs in the subshell on the right of a pipeline. Rewritten to read the
    # capture in the current shell (a heredoc, say) that same `exit` would end
    # the caller mid-test, silently skipping everything after the gate. The
    # command substitution below is itself a subshell, so a leaked exit kills
    # it before the marker is written.
    reached=$( ( cozy_has_schedulable_node "$nodes"; printf reached ) )
    if [ "$reached" != reached ]; then
        echo "expected the scan to return to its caller, not exit it" >&2
        exit 1
    fi
}
