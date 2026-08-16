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
# and so on. A remediation cycle leaves a footprint there, and the two
# helpers below read it at two different strengths.
#
# "Bounded" is literal, and it bounds what a caller may conclude. The
# controller truncates history on every in-sync reconcile: on a release
# whose strategy is a retry it keeps the newest five Snapshots, and on any
# other it keeps those down to the previous deployed or superseded one,
# falling back to that same five when history holds no such Snapshot to
# cut at. A footprint therefore outlives the reconcile that follows it,
# not an arbitrary number of them. Only the truncation itself is checkable
# from here: it lives in helm-controller/api, which this repository pins in
# go.mod. Which of the two variants a release gets is decided in the
# controller, and that module is not a dependency at all, so that half
# rests on upstream source this tree does not carry. Reading history
# immediately after the action that wrote it, which is what an e2e run
# does, sees it; reading it after the release has moved on may not.
#
# helmrelease_has_teardown is the narrow one: it reports whether the
# release was REMOVED, which an "uninstalled" Snapshot proves. Two
# configurations can write that status: the default RemediateOnFailure
# strategy's install remediation, which uninstalls between the retry
# attempts its budget allows, and an upgrade remediation explicitly set to
# strategy: uninstall (the default there is rollback). Which of them a
# release has is what tells a caller whether the removal was expected -
# under RetryOnFailure, with no uninstalling upgrade remediation
# configured, nothing in the release uninstalls to recover at all: a failed
# attempt keeps its manifests applied and is retried as an upgrade. The
# same holds on the default strategy once .spec.install.remediation.retries
# is zero, since no attempt and therefore no uninstall is made then either.
#
# helmrelease_has_remediation_cycle is the broad one: any "failed" or
# "uninstalled" Snapshot. It catches one case the narrow reading gives up,
# the upgrade remediation that rolls back rather than uninstalls and so
# leaves "failed" alone behind - but it is not proof that anything was
# removed, because "failed" is also what a retried install leaves under
# RetryOnFailure, where the manifests stayed in place throughout. A caller
# that fails a run on this reading fails it on a release that recovered by
# design; the e2e guard therefore reports this one and fails on the other.
#
# Both take a newline-delimited list of snapshot statuses (whatever the
# caller extracted via kubectl -o jsonpath or equivalent) and return 0 when
# the footprint is present, 1 otherwise. Empty input is treated as "no
# history yet, nothing observed" by both.
#
# Neither reading takes the release's configuration, because configuration
# answers a different question than the history does: the history says what
# happened, the configuration says what the release was supposed to be able
# to do about it. Weighing the two against each other is the caller's
# decision and differs per caller - the e2e guard reads both
# .spec.install.strategy.name and .spec.install.remediation.retries, and
# treats a teardown as a failure on a release with neither a strategy that
# retries in place nor a budget of attempts to uninstall between, since
# there the removal has no explanation inside the release. Keeping that out
# of here leaves each helper answering one question about one list of
# statuses.

# Both helpers scope "statuses", so sourcing this library and calling one
# cannot overwrite a variable of that name in the caller.
helmrelease_has_teardown() {
    local statuses
    statuses="$1"
    if [ -z "${statuses}" ]; then
        return 1
    fi
    if printf '%s\n' "${statuses}" | grep --extended-regexp --quiet '^uninstalled$'; then
        return 0
    fi
    return 1
}

helmrelease_has_remediation_cycle() {
    local statuses
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
