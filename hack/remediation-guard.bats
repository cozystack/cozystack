#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/e2e-chainsaw/_lib/remediation-guard.sh
#
# Two readings of a newline-delimited list of HelmRelease history snapshot
# status values (deployed/superseded/failed/uninstalled/...).
# helmrelease_has_teardown reports whether the release was REMOVED, which only
# an "uninstalled" Snapshot proves. helmrelease_has_remediation_cycle is the
# broader one, "failed" or "uninstalled", which also catches the upgrade
# remediation that rolls back rather than uninstalls - but is not proof that
# anything was removed, since a retried install under RetryOnFailure leaves
# "failed" too. Both are covered below.
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

@test "teardown predicate fires on uninstalled under any strategy" {
    # helmrelease_has_teardown answers the narrow question - was the release
    # removed - and takes no strategy, because no strategy uninstalls to recover
    # except the default one's install remediation. Callers that must not fail a
    # run on a guess use this rather than the reading above.
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    statuses=$(printf 'failed\nuninstalled\ndeployed\n')
    if ! helmrelease_has_teardown "${statuses}"; then
        echo "expected detected when history contains an uninstalled snapshot" >&2
        exit 1
    fi
}

@test "teardown predicate ignores a failed snapshot" {
    # The rollback path: an upgrade remediated by replacing the release leaves
    # "failed" and no "uninstalled". That is a remediation cycle but not a
    # teardown, and this predicate exists to tell those two apart.
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    statuses=$(printf 'failed\ndeployed\n')
    if helmrelease_has_teardown "${statuses}"; then
        echo "expected not-detected for a failed snapshot with no teardown" >&2
        exit 1
    fi
}

@test "teardown predicate treats empty history as not-detected" {
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    if helmrelease_has_teardown ""; then
        echo "expected not-detected for empty history" >&2
        exit 1
    fi
}

@test "neither predicate overwrites a caller variable named statuses" {
    # This library is sourced rather than executed, and both helpers assign
    # "statuses" internally, so without a local the argument lands in the
    # caller's scope. Nothing in this tree keeps release history under that
    # name, which makes the scoping hardening rather than a repair - and is
    # also why every other case here stays green with the local removed, so
    # the scoping needs a case of its own or it has no carrier at all.
    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    statuses=sentinel
    helmrelease_has_teardown "uninstalled"
    if [ "${statuses}" != sentinel ]; then
        echo "helmrelease_has_teardown replaced the caller's statuses with: ${statuses}" >&2
        exit 1
    fi
    helmrelease_has_remediation_cycle "failed"
    if [ "${statuses}" != sentinel ]; then
        echo "helmrelease_has_remediation_cycle replaced the caller's statuses with: ${statuses}" >&2
        exit 1
    fi
}

@test "status.history extraction pins HR v2 status.history shape" {
    # Pins the Flux HelmRelease v2 .status.history[].status shape that
    # run-kubernetes.sh relies on. If a future flux release renames the
    # field, the jsonpath returns nothing, the guard reports no cycle,
    # and real remediation loops slip past the e2e assertion. This test
    # uses yq to read the exact path used in the e2e script; the upstream
    # Snapshot type lives at
    # github.com/fluxcd/helm-controller/api/v2.Snapshot (via go.mod).
    tmp=$(mktemp -d)

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

    . hack/e2e-chainsaw/_lib/remediation-guard.sh
    if ! helmrelease_has_remediation_cycle "$statuses"; then
        echo "expected detected for pinned HR snippet with uninstalled + deployed history" >&2
        exit 1
    fi
    rm -rf "$tmp"
}
