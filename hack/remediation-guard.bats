#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-apps/remediation-guard.sh
#
# helmrelease_has_remediation_cycle takes a newline-delimited list of
# HelmRelease history snapshot status values (deployed/superseded/failed/
# uninstalled/...) and returns 0 when any entry is "failed" or "uninstalled"
# (meaning flux helm-controller performed install/upgrade remediation).
#
# This is used by the e2e script after the HelmRelease reaches Ready. The
# failure/upgrade counters (.status.installFailures / .status.upgradeFailures)
# are useless there because flux's ClearFailures zeroes them on successful
# reconciliation; .status.history retains the snapshot trail.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on
# its own line; there is no bats `run` or `$status`. Assertions are
# expressed as direct shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/remediation-guard.bats
# -----------------------------------------------------------------------------

@test "empty history returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    if helmrelease_has_remediation_cycle ""; then
        echo "expected not-detected for empty history" >&2
        exit 1
    fi
}

@test "single deployed snapshot returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    if helmrelease_has_remediation_cycle "deployed"; then
        echo "expected not-detected for deployed-only history" >&2
        exit 1
    fi
}

@test "deployed then superseded returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    statuses=$(printf 'deployed\nsuperseded\n')
    if helmrelease_has_remediation_cycle "${statuses}"; then
        echo "expected not-detected for deployed+superseded history" >&2
        exit 1
    fi
}

@test "single failed snapshot returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    if ! helmrelease_has_remediation_cycle "failed"; then
        echo "expected detected when history contains failed snapshot" >&2
        exit 1
    fi
}

@test "single uninstalled snapshot returns detected" {
    # The exact signature of the install-remediation race: the first install
    # exceeded flux's wait budget, remediation uninstalled, the next retry
    # eventually succeeded. History still carries the uninstalled snapshot.
    . hack/e2e-apps/remediation-guard.sh
    if ! helmrelease_has_remediation_cycle "uninstalled"; then
        echo "expected detected when history contains uninstalled snapshot" >&2
        exit 1
    fi
}

@test "uninstalled then deployed still returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    statuses=$(printf 'uninstalled\ndeployed\n')
    if ! helmrelease_has_remediation_cycle "${statuses}"; then
        echo "expected detected despite later successful deploy" >&2
        exit 1
    fi
}

@test "deployed then failed still returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    statuses=$(printf 'deployed\nfailed\n')
    if ! helmrelease_has_remediation_cycle "${statuses}"; then
        echo "expected detected when any entry is failed" >&2
        exit 1
    fi
}

@test "installFailures extraction pins HR v2 status.history shape" {
    # Pins the Flux HelmRelease v2 .status.history[].status shape that
    # run-kubernetes.sh relies on. If a future flux release renames the
    # field, the jsonpath returns nothing, the guard reports no cycle,
    # and real remediation loops slip past the e2e assertion. This test
    # uses yq to read the exact path used in the e2e script; the upstream
    # Snapshot type lives at
    # github.com/fluxcd/helm-controller/api/v2.Snapshot (via go.mod).
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    cat > "$tmp/hr.yaml" <<'YAML'
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: kubernetes-test
  namespace: tenant-test
status:
  history:
    - name: kubernetes-test
      namespace: tenant-test
      version: 1
      status: uninstalled
    - name: kubernetes-test
      namespace: tenant-test
      version: 2
      status: deployed
YAML

    # Default yq output is yaml scalar format, which for string values emits
    # bare unquoted tokens - matching what kubectl -o jsonpath produces in
    # e2e. Do not switch to JSON output here; that would quote the values
    # and break the loop in helmrelease_has_remediation_cycle.
    statuses=$(yq '.status.history[].status' "$tmp/hr.yaml")

    [ -n "$statuses" ]
    echo "$statuses" | grep --quiet '^uninstalled$'

    . hack/e2e-apps/remediation-guard.sh
    if ! helmrelease_has_remediation_cycle "$statuses"; then
        echo "expected detected for pinned HR snippet with uninstalled + deployed history" >&2
        exit 1
    fi
}
