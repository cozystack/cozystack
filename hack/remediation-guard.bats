#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-chainsaw/_lib/remediation-guard.sh
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
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    if helmrelease_has_remediation_cycle ""; then
        echo "expected not-detected for empty history" >&2
        exit 1
    fi
}

@test "single deployed snapshot returns not-detected" {
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    if helmrelease_has_remediation_cycle "deployed"; then
        echo "expected not-detected for deployed-only history" >&2
        exit 1
    fi
}

@test "deployed then superseded returns not-detected" {
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    statuses=$(printf 'deployed\nsuperseded\n')
    if helmrelease_has_remediation_cycle "${statuses}"; then
        echo "expected not-detected for deployed+superseded history" >&2
        exit 1
    fi
}

@test "single failed snapshot returns detected" {
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    if ! helmrelease_has_remediation_cycle "failed"; then
        echo "expected detected when history contains failed snapshot" >&2
        exit 1
    fi
}

@test "single uninstalled snapshot returns detected" {
    # The exact signature of the install-remediation race: the first install
    # exceeded flux's wait budget, remediation uninstalled, the next retry
    # eventually succeeded. History still carries the uninstalled snapshot.
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    if ! helmrelease_has_remediation_cycle "uninstalled"; then
        echo "expected detected when history contains uninstalled snapshot" >&2
        exit 1
    fi
}

@test "uninstalled then deployed still returns detected" {
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    statuses=$(printf 'uninstalled\ndeployed\n')
    if ! helmrelease_has_remediation_cycle "${statuses}"; then
        echo "expected detected despite later successful deploy" >&2
        exit 1
    fi
}

@test "deployed then failed still returns detected" {
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    statuses=$(printf 'deployed\nfailed\n')
    if ! helmrelease_has_remediation_cycle "${statuses}"; then
        echo "expected detected when any entry is failed" >&2
        exit 1
    fi
}
