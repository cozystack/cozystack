# shellcheck shell=bash
# Helpers for asserting that a Flux HelmRelease did not fall into an
# install/upgrade remediation cycle during an e2e run.
#
# Background: Flux helm-controller's ClearFailures() zeroes
# .status.installFailures / .status.upgradeFailures on every successful
# reconciliation (see the upstream ClearFailures method on
# HelmReleaseStatus). That makes those counters useless for a guard that
# runs after the HelmRelease has reached Ready - the values are always 0.
#
# What survives a successful reconciliation is .status.history, a bounded
# list of release Snapshots. Each Snapshot carries a status field that
# tracks the Helm release state: deployed, superseded, failed, uninstalled,
# and so on. A remediation cycle leaves the footprint behind: a snapshot
# with status "uninstalled" (from install/upgrade remediation) or "failed"
# (Helm release failure that remediation then uninstalled). Those stay in
# history even after a subsequent successful reinstall.
#
# helmrelease_has_remediation_cycle takes a newline-delimited list of
# snapshot statuses (whatever the caller extracted via kubectl -o jsonpath
# or equivalent) and returns 0 (detected) when any entry is "failed" or
# "uninstalled", 1 otherwise. Empty input is treated as "no history yet,
# no cycle observed".

# The jsonpath a caller hands to kubectl to read those statuses, one per
# line. It lives here rather than inline at the call site so that it is a
# named assignment in a library that sources cleanly on its own, which is
# what lets internal/fluxcontract source this file and run the resulting
# value against the upstream Flux type: renaming a field upstream fails that
# test instead of turning the read into a silent empty string. How this
# assignment may be written is pinned there rather than restated here.
# Locally that test runs under `make test-controllers` rather than
# `make unit-tests`; in CI both are steps of one job.
# shellcheck disable=SC2034  # used by callers that source this file
HELMRELEASE_HISTORY_JSONPATH='{range .status.history[*]}{.status}{"\n"}{end}'

helmrelease_has_remediation_cycle() {
    statuses="$1"
    if [ -z "${statuses}" ]; then
        return 1
    fi
    # printf + grep over the pipe, rather than a heredoc plus while read.
    # printf %s treats the status string as a literal payload, so any stray
    # $ in a future caller's input does not trigger shell expansion. grep
    # returns 0 iff at least one line matches the allowlist, which is
    # exactly the contract the caller wants, so we can return its exit
    # status directly.
    if printf '%s\n' "${statuses}" | grep --extended-regexp --quiet '^(failed|uninstalled)$'; then
        return 0
    fi
    return 1
}
