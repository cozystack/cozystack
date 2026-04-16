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

helmrelease_has_remediation_cycle() {
    statuses="$1"
    if [ -z "${statuses}" ]; then
        return 1
    fi
    while IFS= read -r status; do
        case "${status}" in
            failed|uninstalled)
                return 0
                ;;
        esac
    done <<EOF
${statuses}
EOF
    return 1
}
