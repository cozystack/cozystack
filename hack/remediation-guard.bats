#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-apps/remediation-guard.sh
#
# helmrelease_has_remediation_cycle is consumed from e2e tests to assert that
# the parent HelmRelease did not hit flux helm-controller's wait timeout and
# enter uninstall remediation. The function accepts two arguments (values of
# .status.installFailures and .status.upgradeFailures) and returns 0 when a
# remediation cycle is detected, 1 otherwise.
#
# Each argument can be empty (controller never populated the field), "0"
# (populated but never failed), or a positive integer. Shell's && and ||
# have equal precedence with left-to-right associativity, which used to
# break this check on the most common failure mode - install_failures=1
# and upgrade_failures="". These tests pin the correct behavior.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on
# its own line; there is no bats `run` or `$status`. Assertions are
# expressed as direct shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/remediation-guard.bats
# -----------------------------------------------------------------------------

@test "no counters set returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "" "" || rc=$?
    [ "$rc" -eq 1 ]
}

@test "both counters zero returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "0" "0" || rc=$?
    [ "$rc" -eq 1 ]
}

@test "install zero upgrade empty returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "0" "" || rc=$?
    [ "$rc" -eq 1 ]
}

@test "install empty upgrade zero returns not-detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "" "0" || rc=$?
    [ "$rc" -eq 1 ]
}

@test "install one upgrade empty returns detected" {
    # Canonical race: first install exceeded helm-wait, remediation fired,
    # no upgrade has happened yet.
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "1" "" || rc=$?
    [ "$rc" -eq 0 ]
}

@test "install empty upgrade one returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "" "1" || rc=$?
    [ "$rc" -eq 0 ]
}

@test "install two upgrade zero returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "2" "0" || rc=$?
    [ "$rc" -eq 0 ]
}

@test "install zero upgrade two returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "0" "2" || rc=$?
    [ "$rc" -eq 0 ]
}

@test "both counters positive returns detected" {
    . hack/e2e-apps/remediation-guard.sh
    rc=0
    helmrelease_has_remediation_cycle "3" "5" || rc=$?
    [ "$rc" -eq 0 ]
}
