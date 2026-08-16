#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for cozy_guard_addon_helmreleases in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# cozy_guard_helmrelease judges one release; cozy_guard_addon_helmreleases runs
# it over the addon releases a parent installs. One rule decides the verdict:
# the run fails when a release was REMOVED although it is configured never to
# remove itself to recover - an "uninstalled" Snapshot on a RetryOnFailure
# release, which is the tenant CNI and CSI and every Application HelmRelease.
# Everything else is reported and left green, including an "uninstalled"
# Snapshot on a release still using the default strategy, whose install
# remediation is an uninstall and which pairs it with retries: -1.
#
# kubectl is stubbed from fixture files, so these tests exercise the wiring the
# guard depends on - the name prefix that selects the addons, the readiness that
# interprets an empty history, and the failure paths of the reads themselves -
# without a cluster. The readings are unit-tested in hack/remediation-guard.bats.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Assertions are expressed as
# direct shell tests that exit non-zero on failure. Scratch directories are
# removed at the end of the test body rather than from a trap (both runners set
# -e, so a failed test leaves its fixtures behind for inspection).
#
# Run with: hack/cozytest.sh hack/run-kubernetes-remediation_test.bats
# -----------------------------------------------------------------------------

# Writes a fixture-driven kubectl stub into $1/bin and returns with $1/fixtures
# created. The stub answers the shapes the guard issues: the namespace listing
# (from ALL_HR), one release's install strategy, Ready condition and install
# retry budget together (<name>.strategy, <name>.ready and <name>.retries,
# joined the way the guard's jsonpath joins them), and its history statuses
# (<name>.history). A missing fixture file stands for a field the object does
# not carry, which is what kubectl prints for an absent jsonpath - nothing.
#
# An absent <name>.retries is not a neutral default here: retries is declared
# `omitempty` on an int, so a release configured with retries: 0 serialises to
# no field at all, and the two are the same bytes on the wire. Both mean a
# release that takes no remediation attempt, which is what the guard reads them
# as.
#
# A FAILS marker file makes the matching read exit non-zero with a message on
# stderr, which is how the API answers a timeout or a denied request. That
# looks identical to an absent field on stdout, so it is the case the guard has
# to tell apart by exit status rather than by output. Strategy, readiness and
# the retry budget travel in one read, so a marker on any of them fails that
# read.
#
# A WARNS marker makes the read write to stderr and still exit 0, which is what
# kubectl does for a deprecation notice or for the discovery noise a degraded
# aggregated APIService produces. No value may pick that line up: inside the
# history capture it turns an empty history into a populated one, which skips
# the readiness branch and reports an uninspected release as clean, and inside
# the strategy capture it stops the value matching RetryOnFailure, which
# downgrades a real teardown to a note.
#
# Every invocation appends its arguments to a CALLS file, so a case can assert
# on the shape of the reads rather than only on what they returned.
#
# A TURNS_READY marker beside a history fixture makes serving that read write
# "True" into the release's Ready fixture, which is the interleaving the guard's
# read order has to survive: a release that completes its install between the
# two reads.

# Fixed, advancing clock. The failure-path report bounds its walk on wall time,
# so the cases about that decision own the clock rather than racing it. Empty
# date_now leaves the real one in place for every other case; date_step is how
# far the clock moves per reading, which is what lets a walk cross its deadline
# between two releases without anything sleeping.
#
# The tick count lives in a file rather than in a variable, because every
# reading the report takes is inside a command substitution and a subshell's
# increment does not survive it - a clock that never moved would leave this
# green against a walk with no bound at all.
date_now=
date_step=0
date_ticks=
date() {
    if [ -n "${date_now}" ] && [ "${1:-}" = +%s ]; then
        ticks=0
        if [ -n "${date_ticks}" ] && [ -f "${date_ticks}" ]; then
            ticks=$(cat "${date_ticks}")
            printf '%s' "$((ticks + 1))" > "${date_ticks}"
        fi
        printf '%s\n' "$((date_now + ticks * date_step))"
        return 0
    fi
    command date "$@"
}

cozy_make_kubectl_stub() {
    mkdir -p "$1/bin" "$1/fixtures"
    cat > "$1/bin/kubectl" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURES/CALLS"
mode=list
name=
for a in "$@"; do
  case "$a" in
    describe) mode=describe ;;
    *'status.history'*) mode=history ;;
    *'install.strategy.name'*) mode=probe ;;
    kubernetes-*) name=$a ;;
  esac
done
case "$mode" in
  list) markers=LIST ;;
  history) markers="$name.history" ;;
  probe) markers="$name.strategy $name.ready $name.retries" ;;
  *) markers= ;;
esac
for m in $markers; do
  if [ -f "$FIXTURES/$m.CUTOFF" ]; then
    # What the wall clock produces: the process is signalled before it writes,
    # so the read fails with no output on either stream. 124 is what `timeout`
    # reports for it.
    exit 124
  fi
done
for m in $markers; do
  if [ -f "$FIXTURES/$m.FAILS" ]; then
    echo "Error from server: the server was unable to return a response in the time allotted" >&2
    exit 1
  fi
done
for m in $markers; do
  if [ -f "$FIXTURES/$m.WARNS" ]; then
    echo "E0806 12:00:00.000000   1 memcache.go:265] couldn't get current server API group list: the server is currently unable to handle the request" >&2
  fi
done
case "$mode" in
  list) cat "$FIXTURES/ALL_HR" ;;
  history)
    cat "$FIXTURES/$name.history" 2>/dev/null || true
    if [ -f "$FIXTURES/$name.history.TURNS_READY" ]; then
      printf 'True' > "$FIXTURES/$name.ready"
    fi
    ;;
  probe)
    printf '%s@%s@%s' \
      "$(cat "$FIXTURES/$name.strategy" 2>/dev/null || true)" \
      "$(cat "$FIXTURES/$name.ready" 2>/dev/null || true)" \
      "$(cat "$FIXTURES/$name.retries" 2>/dev/null || true)"
    ;;
  describe) echo "stub describe of $name" ;;
esac
exit 0
STUB
    chmod +x "$1/bin/kubectl"
}

@test "the guard is wired into the run, before the snapshot trap is disarmed" {
    lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # Everything else here tests the guards in isolation, which says nothing
    # about whether the run still calls them: delete both invocations and every
    # case in this file stays green while the suite asserts nothing. Ordering is
    # part of the wiring - the tenant crust-gather snapshot is taken from an EXIT
    # trap, so a guard that fired after `trap - EXIT` would report the teardown
    # with the cluster state already gone. Same shape as the ordering pin in
    # hack/run-kubernetes-schedulable_test.bats.
    # The arguments are pinned, not only the call. The addon prefix is derived
    # from the parent name inside the guard, so naming a different release here
    # would select a different set of addons and still read as wired.
    #
    # The line has to END at the call, not merely contain it, because the
    # verdict reaches the run through errexit rather than through an exit of its
    # own: appending `|| true` disarms the guard completely and leaves a
    # substring match, and every other case here, green. Indentation is left
    # free, so wrapping the call in a block stays a refactor rather than
    # becoming a red test that reports the guard as missing.
    call=$(grep -n -E '^ *cozy_guard_all_helmreleases tenant-test "kubernetes-\$\{test_name\}"$' "$lib" | head -n 1 | cut -d: -f1)
    # Keyword and signal are assembled from separate arguments, the way the
    # fixtures in hack/bats-no-exit-trap.bats do it: the scan there is lexical
    # and counts the pair wherever it appears on a line, so spelling the pattern
    # out here would read as this file installing a handler it does not install.
    disarm_re=$(printf '^  %s - %s' 'trap' 'EXIT')
    disarm=$(grep -n "$disarm_re" "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$call" ]; then
        echo "expected run_kubernetes_test to guard the parent HelmRelease and its addons" >&2
        exit 1
    fi
    if [ -z "$disarm" ]; then
        echo "expected to find the tenant-snapshot trap being disarmed in $lib" >&2
        exit 1
    fi
    if [ "$call" -ge "$disarm" ]; then
        echo "expected the guard (line $call) before the trap is disarmed (line $disarm)" >&2
        exit 1
    fi
}

@test "a failing parent still leaves every addon inspected" {
    # Two bare calls under errexit would end the script on the parent's verdict
    # and read no addon at all. The addon output is the context that explains a
    # parent failure, which is the same reason the addon loop keeps going after
    # one of its own fails. Parent here is torn down under RetryOnFailure, which
    # is fatal; the addon carries a failed Snapshot, which is a note. Both have
    # to appear, and the verdict has to stay non-zero.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'failed\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_all_helmreleases tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected the run to fail on the torn-down parent, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'was uninstalled and reinstalled, though its install strategy is RetryOnFailure'; then
        echo "expected the parent teardown to be reported, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'NOTE: kubernetes-test-latest-version-coredns carries a failed Snapshot'; then
        echo "expected the addon to still be inspected after the parent failed, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "an addon teardown alone still fails the run" {
    # The composite carries two verdicts and the addons' has to survive on its
    # own. With the parent clean, dropping the addon guard's contribution -
    # trading its collection for a bare || true - leaves every other case in
    # this file green, so the second verdict needs its own pin.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_all_helmreleases tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected the torn-down addon alone to fail the run, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "single-release guard passes a release that failed and recovered" {
    # The entry point the parent kubernetes-<test> release goes through, and the
    # case the guard was changed for: the apiserver stamps RetryOnFailure on
    # every Application HelmRelease, so a failed Snapshot there is an attempt
    # that kept its manifests and was retried. Failing on it - which the guard
    # did before this change - reds a run on a release that recovered by design.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'failed\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"

    PATH="$tmp/bin:$PATH" cozy_guard_helmrelease tenant-test kubernetes-test-latest-version

    rm -rf "$tmp"
}

@test "single-release guard reports a teardown" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_helmrelease tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected failure for a history carrying an uninstalled snapshot" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'HelmRelease kubernetes-test-latest-version was uninstalled and reinstalled'; then
        echo "expected the failure to name the teardown, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard passes when every addon release history is clean" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version\nkubernetes-test-latest-version-cilium\nkubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"
    printf 'deployed\nsuperseded\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version-

    rm -rf "$tmp"
}

@test "addon guard reports a torn-down addon" {
    # The case the guard exists for: the tenant CNI was uninstalled and
    # reinstalled. RetryOnFailure never uninstalls to recover, so this teardown
    # came from somewhere else and must fail the run.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    if PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version-; then
        echo "expected failure when an addon history carries an uninstalled snapshot" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard tolerates a failure that left the manifests in place" {
    # A failed attempt on cilium or csi keeps its manifests applied and is
    # retried as an upgrade. Failing on it would red exactly the runs the retry
    # strategy was adopted to let through, so this must stay green.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-csi\n' > "$FIXTURES/ALL_HR"
    printf 'failed\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-csi.history"

    PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version-

    rm -rf "$tmp"
}

@test "addon guard notes a failed Snapshot without failing the run" {
    # A failed Snapshot with no uninstalled beside it is the rollback path: under
    # the default strategy an upgrade is remediated by replacing the release, not
    # by removing it. Most of this chart's addons pair that strategy with
    # retries: -1, which declares repeated remediation acceptable for them, and
    # nothing measures how often they use it - so this is reported rather than
    # made fatal on the gate every PR crosses. The line has to be there: without
    # it the run says nothing at all about the cycle, which is the blindness the
    # guard was added to remove.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'failed\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "expected a rollback footprint to be reported, not to fail the run, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'NOTE: kubernetes-test-latest-version-coredns carries a failed Snapshot'; then
        echo "expected the rollback to be named in the output, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the failed-Snapshot note does not claim a recovery that has not happened" {
    # The note is reached on any release carrying a failed Snapshot, Ready or
    # not. On one that is still not Ready the failure is live rather than
    # behind it, and a note saying it "recovered" sends a reader looking for a
    # second cause while the first one is still the answer. The release here is
    # the shape that makes the difference visible: a failed Snapshot in its
    # history and no Ready condition.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'False' > "$FIXTURES/kubernetes-test-latest-version-cilium.ready"
    printf 'failed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "expected a failed Snapshot on a not-Ready addon to be reported, not to fail the run, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'carries a failed Snapshot and is not Ready'; then
        echo "expected the note to say the release is not Ready, got: $out" >&2
        exit 1
    fi
    if printf '%s\n' "$out" | grep -q 'remediated in place and recovered'; then
        echo "expected no claim of recovery on a release that is not Ready, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard notes a teardown the default strategy performed itself" {
    # An uninstalled Snapshot on a release still using the default strategy is
    # that strategy's own install remediation: it uninstalls before retrying, and
    # these addons pair it with retries: -1, so the configuration declares this
    # their recovery path. The run has no measurement of how often they take it,
    # and reddening a 25-minute bringup on a release doing what it is configured
    # to do is how a guard gets switched off. That the configuration is itself
    # the defect is true and is filed separately - it is not this guard's to
    # assert. The line must still be there: a teardown nobody reports is the
    # blindness the guard was added to remove.
    #
    # The retry budget is a fixture here rather than left absent, because the
    # guard reads it: unlimited retries is what makes this teardown the
    # release's own recovery path, and the same history with no budget behind it
    # fails the run in the case below.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf -- '-1' > "$FIXTURES/kubernetes-test-latest-version-coredns.retries"
    printf 'failed\nuninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "expected a default-strategy teardown to be reported, not to fail the run, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'NOTE: kubernetes-test-latest-version-coredns was uninstalled and reinstalled by its own install remediation'; then
        echo "expected the teardown to be named in the output, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "no guard read is issued without a per-request bound" {
    # The guard runs around minute 25 of the bringup on the passing path, and
    # inside a diagnostics phase on the failing ones, where the tenant
    # crust-gather snapshot still has to be reachable afterwards. An unbounded
    # read against a wedged apiserver holds the Chainsaw op until the op is
    # killed, and the snapshot is then lost rather than truncated - the same
    # failure the node-join collectors carry their own bounds against.
    #
    # Asserted over every recorded call rather than over the one this case
    # happens to exercise, so a read added later without a bound is caught by
    # this case rather than by a run. The wall-clock half is pinned on the
    # failure path, in hack/run-kubernetes-node-join_test.bats, where the
    # harness can tell a bounded read from an unbounded one.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    PATH="$tmp/bin:$PATH" cozy_guard_all_helmreleases tenant-test kubernetes-test-latest-version >/dev/null 2>&1 || true

    if [ ! -s "$FIXTURES/CALLS" ]; then
        echo "expected the guard to have issued reads at all" >&2
        exit 1
    fi
    unbounded=$(grep -v -F -e '--request-timeout=' "$FIXTURES/CALLS" || true)
    if [ -n "$unbounded" ]; then
        echo "expected every guard read to carry --request-timeout, these did not:" >&2
        printf '%s\n' "$unbounded" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails a teardown on a release with no retry budget" {
    # The default strategy uninstalls between install attempts and the retry
    # budget decides how many it may take. At zero it takes none: Flux bails on
    # the first failure and, with remediateLastFailure unset, remediates
    # nothing, so an uninstalled Snapshot there is as unexplained as one on a
    # RetryOnFailure release. Reading only the strategy would put it in the soft
    # branch and name the release's own install remediation as the cause of a
    # removal that remediation never performed.
    #
    # Nothing is in this shape today - every addon of this chart sets
    # retries: -1 - which is the point: the addons are enumerated from the
    # cluster so a release added later is covered without editing the script,
    # and this is the shape where reading the strategy alone would not cover it.
    # The budget is absent from the object rather than set to 0 because that is
    # what 0 serialises to.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected a teardown with no retry budget behind it to fail the run, got: $out" >&2
        exit 1
    fi
    # The reason, not only the status: the release also has no strategy set, so
    # a guard that failed every teardown outright would pass this case while
    # naming the wrong cause, and the note a reader acts on would send them to a
    # strategy the release does not carry.
    if ! printf '%s\n' "$out" | grep -q 'no install retry budget'; then
        echo "expected the failure to name the retry budget, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the parent guard fails when its install strategy reads back empty" {
    # The strategy is what arms the fatal branch, and it is read with kubectl
    # -o jsonpath, which prints nothing and exits 0 for a path that no longer
    # resolves. Empty then takes the soft branch, so a rename of
    # spec.install.strategy in a future helm-controller would turn every
    # teardown - a real removal of the tenant CNI included - into a NOTE about
    # an install remediation, on a green run, with every other case in this file
    # still passing because they feed the strategy from a fixture rather than
    # through the jsonpath.
    #
    # The parent is where that is checkable: the aggregated apiserver stamps
    # RetryOnFailure on the HelmRelease of every Application it serves, on
    # install and upgrade alike, so an empty strategy there is never the object
    # and always the read. The addons cannot carry the same check - 18 of the
    # 20 addon releases the chart declares set no strategy at all - which is
    # why it is the parent that is asked for it.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'True' > "$FIXTURES/kubernetes-test-latest-version.ready"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_all_helmreleases tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected an empty install strategy on the parent to fail the run, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q -F 'spec.install.strategy.name of kubernetes-test-latest-version came back empty'; then
        echo "expected the failure to name the field that read back empty, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "an addon that sets no install strategy is not read as a broken jsonpath" {
    # The counterpart to the case above, and the reason that one is scoped to
    # the parent. Most addons of this chart set no strategy and take the default
    # one; an empty value there is the object rather than the read, and failing
    # on it would red every run on the 18 addon releases that are configured
    # exactly as intended.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'True' > "$FIXTURES/kubernetes-test-latest-version-coredns.ready"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version-

    rm -rf "$tmp"
}

@test "addon guard inspects every addon even after one fails" {
    # The notes the other addons print are the context that explains the
    # failure - which release rolled back, which one the default strategy tore
    # down - so stopping at the first failure hides exactly what a reader would
    # compare it against. Both releases here have something to say, and the run
    # must still end non-zero.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\nkubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"
    printf 'failed\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected the run to fail on the torn-down addon, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'NOTE: kubernetes-test-latest-version-coredns carries a failed Snapshot'; then
        echo "expected the addon after the failing one to still be inspected, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails when no addon HelmRelease matches the prefix" {
    # The parent is Ready, so its addon releases exist. An empty selection means
    # the naming this guard walks has changed, and a guard that silently
    # inspects nothing is worse than no guard at all.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version\n' > "$FIXTURES/ALL_HR"

    if PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version-; then
        echo "expected failure when the prefix selects no addon HelmRelease" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails a Ready addon that reports no release history" {
    # A Ready HelmRelease has a completed helm action behind it, and the
    # controller keeps a Snapshot of every one. Ready with no history is the
    # Flux status shape having moved under the guard, and passing over it would
    # leave that release unchecked while the run stayed green - the silent gap
    # this guard exists to close. The parent's own empty-history check does not
    # cover it: the parent's history says nothing about its children's.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-vsnap-crd\n' > "$FIXTURES/ALL_HR"
    printf 'True' > "$FIXTURES/kubernetes-test-latest-version-vsnap-crd.ready"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected failure for a Ready addon whose history came back empty" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'Unexpected empty .status.history on Ready HelmRelease kubernetes-test-latest-version-vsnap-crd'; then
        echo "expected the failure to name the empty history, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard skips an addon that never completed a helm action" {
    # The parent Application HelmRelease carries disableWait, so it goes Ready
    # without waiting for the releases it applied, and the suite waits on only
    # some of them by name - metrics-server and prometheus-operator-crds among
    # those it does not. A release still working through its dependsOn has no
    # history and no teardown to find, and failing on it would be a red run with
    # a Flux-API-change message for a release that is merely still installing.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-metrics-server\n' > "$FIXTURES/ALL_HR"
    printf 'False' > "$FIXTURES/kubernetes-test-latest-version-metrics-server.ready"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "expected a not-Ready addon with no history to be skipped, got rc=$rc: $out" >&2
        exit 1
    fi
    # Skipped, but never in silence, and the line has to say it was skipped: the
    # name alone is already printed by the per-addon header above, so asserting
    # on the name would pass with the skip line deleted and the guard would be
    # reporting coverage it did not have.
    if ! printf '%s\n' "$out" | grep -q 'kubernetes-test-latest-version-metrics-server is not Ready and has no release history'; then
        echo "expected the skipped addon to be reported as not inspected, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails when the Ready condition cannot be read" {
    # Readiness must not conflate "read failed" with "field absent", and this is
    # where the conflation is worst: an empty Ready condition sends the release
    # down the not-Ready branch, which reports it as not inspected and returns 0.
    # A denied or timed-out read would then pass an uninspected release off as
    # covered. It travels in the same read as the strategy, so the read fails
    # whichever of the two the API could not answer.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.ready.FAILS"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected failure when the Ready condition could not be read, got: $out" >&2
        exit 1
    fi
    # The message too, as with its sibling: without it the case passes when some
    # other read is the one that broke.
    if ! printf '%s\n' "$out" | grep -q 'Reading .spec.install.strategy.name, the Ready condition and .spec.install.remediation.retries of kubernetes-test-latest-version-cilium failed'; then
        echo "expected the failure to name the read that carries the Ready condition, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard reads readiness before history, not after" {
    # The controller writes the Snapshot and flips Ready in one status patch, so
    # a release is never Ready before its first Snapshot exists. Reading the
    # history first and readiness afterwards observes the two in the opposite
    # order: an addon still installing at the history read and Ready one
    # round-trip later presents as Ready with an empty history, which the guard
    # fails the run on as a Flux status shape it can no longer read. Nothing
    # waits on most of these releases, so that window is open on every run. The
    # other order is not open to the same misreading through truncation, because
    # neither variant empties a non-empty history: Truncate returns early below
    # two Snapshots, and TruncateIgnoringPreviousSnapshots cuts only above five.
    # ClearHistory() drops it outright and is not covered by that - see the
    # comment on the read order in the library, which says what the order does
    # and does not buy.
    #
    # The stub turns the addon Ready as a side effect of serving the history
    # read, which is that interleaving exactly: green while readiness is read
    # first, red the moment the two reads swap back.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.history.TURNS_READY"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "expected an addon that turns Ready mid-check to be reported as not inspected, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'kubernetes-test-latest-version-cilium is not Ready and has no release history'; then
        echo "expected the still-installing addon to be reported as not inspected, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails when an addon strategy cannot be read" {
    # The strategy decides whether a teardown is fatal, so a read that timed out
    # or was denied cannot be folded into "no strategy set": that reading is the
    # lenient one, and a real teardown of the tenant CNI would be reported as the
    # default strategy's own recovery. Fail on the read instead, and say so.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy.FAILS"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected failure when the install strategy could not be read" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'Reading .spec.install.strategy.name, the Ready condition and .spec.install.remediation.retries of kubernetes-test-latest-version-cilium failed'; then
        echo "expected the failure to name the strategy read, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "a kubectl warning does not downgrade a teardown to a note" {
    # The strategy value decides whether a teardown is fatal, and it is compared
    # for equality. kubectl writes warnings to stderr while exiting 0, so a
    # capture that took stderr would carry the warning in front of
    # RetryOnFailure, stop matching it, and report a real teardown of the tenant
    # CNI as the default strategy doing its job - the guard weakened silently,
    # through the success path, by a line that has nothing to do with the
    # release.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy"
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.strategy.WARNS"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected the teardown to stay fatal when kubectl warns on the strategy read, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "a kubectl warning does not turn an empty history into a clean one" {
    # kubectl writes warnings to stderr and still exits 0. If the history capture
    # takes stderr, that line becomes the history: a release with no Snapshots at
    # all reads as populated, the readiness branch never runs, and an
    # uninspected release is reported clean. The addon here is Ready with no
    # history, which must fail - and does only while the warning stays out of the
    # value.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.history.WARNS"
    printf 'True' > "$FIXTURES/kubernetes-test-latest-version-cilium.ready"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected a Ready addon with no history to fail even when kubectl warns, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'Unexpected empty .status.history on Ready HelmRelease'; then
        echo "expected the empty history to be recognised as empty, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails when the HelmRelease listing fails" {
    # A failed listing prints nothing, exactly like a namespace with no
    # HelmReleases, so only the exit status tells them apart. Reporting the
    # empty-prefix message for an API error would send the reader after a
    # renamed release that is not the problem.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    : > "$FIXTURES/LIST.FAILS"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected failure when listing the HelmReleases failed" >&2
        exit 1
    fi
    # The message, not just the status: a failed listing also empties the
    # selection, so the run fails either way and only the reason distinguishes
    # a broken API call from a release the prefix no longer matches.
    if ! printf '%s\n' "$out" | grep -q 'Listing HelmReleases in tenant-test failed'; then
        echo "expected the failure to name the listing, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard fails when an addon history cannot be read" {
    # The dangerous conflation: a timed-out or denied read yields no output,
    # which is also what a release with no history yields. Skipping on that
    # would let the guard pass over the very release it was pointed at.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.history.FAILS"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version- 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected failure when an addon history could not be read" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'Reading .status.history of kubernetes-test-latest-version-cilium failed'; then
        echo "expected the failure to name the history read, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon selection reads the prefix literally, not as a regex" {
    # A HelmRelease name is a DNS-1123 subdomain, so a dot in one is legal, and
    # an anchored regex would read that dot as any character and pull in
    # releases belonging to no addon of this parent. Every other case here uses
    # a prefix with no metacharacter in it and so stays green either way, which
    # leaves the literal match without a carrier unless it gets a case of its
    # own - the same reason the scoping in hack/remediation-guard.bats has one.
    #
    # The listing holds a name only a wildcard reading would select, so the
    # selection must come back empty, which the guard reports as a prefix that
    # matched nothing.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-aXb-cilium\n' > "$FIXTURES/ALL_HR"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test 'kubernetes-a.b-' 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected a dot in the prefix to select nothing, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q -F 'No addon HelmReleases matched kubernetes-a.b-'; then
        echo "expected the empty selection to be reported, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "addon guard ignores HelmReleases outside the prefix" {
    # tenant-test also holds releases of other suites and of the previous
    # version's cluster. A teardown there is not this test's finding.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-previous-version-cilium\nkubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-previous-version-cilium.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    PATH="$tmp/bin:$PATH" cozy_guard_addon_helmreleases tenant-test kubernetes-test-latest-version-

    rm -rf "$tmp"
}

@test "the failure-path report classifies the releases and fails nothing" {
    # Every deadline the guard's own comment names as the way a child
    # remediation surfaces - a node that never turns Ready, a HelmRelease wait
    # that expires, a PVC that never binds - ends the script under errexit
    # before the guard is reached, so on exactly the runs a teardown explains,
    # the guard said nothing. This is the same reading issued from those paths.
    #
    # It must not fail its caller: the callers are collectors on a path whose
    # exit status the tenant crust-gather snapshot hangs off, and a non-zero
    # return here would replace it. The parent below is torn down under
    # RetryOnFailure, which is the shape the guard fails a green run on.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf -- '-1' > "$FIXTURES/kubernetes-test-latest-version-coredns.retries"
    printf 'failed\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "expected the report to return zero so the caller keeps its exit, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'was uninstalled and reinstalled, though its install strategy is RetryOnFailure'; then
        echo "expected the parent teardown to be classified, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'NOTE: kubernetes-test-latest-version-coredns carries a failed Snapshot'; then
        echo "expected the addon to be classified too, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the failure-path report stops at its budget and names what it did not read" {
    # What runs after this report on the exit path is the tenant crust-gather
    # snapshot, which needs the rest of the op. A namespace whose apiserver
    # answers slowly therefore has to cost a stated number of seconds rather
    # than however many releases happen to be in it. The release the budget cut
    # off is named: a walk that stopped in silence leaves a bundle that reads
    # like one where those releases were clean.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\nkubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"
    # 30s per reading against a 90s budget: the report stamps its deadline at
    # 1000+90, the listing is checked at 1030, the first addon at 1060 and the
    # second at 1090, which is the first reading the deadline refuses.
    date_ticks="$tmp/ticks"
    printf '0' > "$date_ticks"
    date_now=1000
    date_step=30
    COZY_GUARD_REPORT_BUDGET=90

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    date_now=
    date_step=0
    date_ticks=
    unset COZY_GUARD_REPORT_BUDGET
    if [ "$rc" -ne 0 ]; then
        echo "expected the report to return zero even when its budget ran out, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'HelmRelease kubernetes-test-latest-version-cilium '; then
        echo "expected the first addon to have been read inside the budget, got: $out" >&2
        exit 1
    fi
    if printf '%s\n' "$out" | grep -q 'HelmRelease kubernetes-test-latest-version-coredns (install strategy'; then
        echo "expected the second addon to fall outside the budget, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q -F 'stopped after 90s; kubernetes-test-latest-version-coredns and every release after it were not read'; then
        echo "expected the cut-off release to be named, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the failure-path report classifies one run once" {
    # It is reachable from two call sites that both fire on a node-join failure:
    # the collector block, and the exit handler after it. The same verdicts
    # printed twice read as two observations of one release, which is a
    # different finding from the one the run made.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    # Both calls run in one command substitution, because the flag is set inside
    # the function and a subshell's assignment does not reach the next call
    # otherwise - which is also how the two real call sites see it, both being
    # in the same shell. Only stdout is compared: the other runner traces the
    # body to stderr, and a capture that took it would be comparing the trace.
    _COZY_GUARD_REPORT_DONE=0
    out=$( { PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version
             PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version
           } 2>/dev/null )
    headers=$(printf '%s\n' "$out" | grep -c 'tenant HelmRelease remediation footprint' || true)
    if [ "$headers" != 1 ]; then
        echo "expected exactly one classification, got $headers:
$out" >&2
        exit 1
    fi
    releases=$(printf '%s\n' "$out" | grep -c '^HelmRelease kubernetes-test-latest-version' || true)
    if [ "$releases" != 2 ]; then
        echo "expected the parent and its one addon classified once each, got $releases:
$out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the failure paths reach the classification the guard cannot" {
    lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # The behaviour above says nothing about whether the failing paths still
    # call it: delete both call sites and every case in this file stays green
    # while a torn-down tenant CNI goes unnamed exactly as it did before.
    #
    # Ordering is part of it on the exit handler. The classification is a
    # handful of bounded reads and it discriminates between failure modes; the
    # snapshot costs minutes. Behind the snapshot it would be the thing a slow
    # collect drops.
    #
    # Keyword and signal are assembled from separate arguments, the way the
    # ordering pin above does it: hack/bats-no-exit-trap.bats scans this file
    # lexically and would count a spelled-out pair as a handler this file
    # installs.
    arm_re=$(printf "^  %s '_tenant_failure_on_exit' %s\$" 'trap' 'EXIT')
    arm=$(grep -n "$arm_re" "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$arm" ]; then
        echo "expected run_kubernetes_test to arm the composite failure handler" >&2
        exit 1
    fi
    # Armed before the first management-cluster wait, not after them. Everything
    # from the Kubernetes CR apply to the tenant LB answering is a wait on the
    # parent HelmRelease or on what it installs, and a helm-wait budget expiring
    # in there is the remediation this classification exists to name. Armed
    # afterwards, each of those waits exits under errexit with the handler not
    # yet installed and the run says nothing about the releases.
    #
    # Anchored on the first wait of that window by name, not on whichever
    # kubectl_wait_retry comes first in the file: a file-global match happens to
    # land inside run_kubernetes_test today, and one added to any function
    # defined above it would turn this into a comparison of two unrelated lines
    # that passes for the wrong reason. If the control-plane wait is renamed the
    # lookup comes back empty and says so, which is the honest failure.
    cp_wait=$(grep -n 'kubectl_wait_retry --for=condition=TenantControlPlaneCreated' "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$cp_wait" ]; then
        echo "expected to find the tenant control-plane wait in $lib" >&2
        exit 1
    fi
    if [ "$arm" -ge "$cp_wait" ]; then
        echo "expected the handler armed (line $arm) before the control-plane wait (line $cp_wait)" >&2
        exit 1
    fi
    # The parent name is part of this one, because it is what tells the
    # node-join call apart from the handler's. What follows it is not: a
    # section label is presentation and may change without the wiring changing.
    node_join=$(grep -n '^ *cozy_report_helmrelease_remediation tenant-test "kubernetes-\${test_name}"' "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$node_join" ]; then
        echo "expected the node-join collector to classify the tenant HelmReleases" >&2
        exit 1
    fi
    # Anchored on the two calls and not on the text of their arguments. Those
    # arguments name variables of this library, which a refactor there is free
    # to rename without touching the ordering this asserts; spelling them out
    # turns such a rename into a red test that reports the collectors as
    # missing. The trailing space is what keeps each pattern off the function's
    # own definition line, where the name is followed by a parenthesis.
    snapshot=$(grep -n '^ *_tenant_snapshot_on_fail ' "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$snapshot" ]; then
        echo "expected the exit handler to take the tenant snapshot" >&2
        exit 1
    fi
    # The last classification call above the snapshot, so the node-join one
    # further down the file cannot stand in for the handler's.
    report=$(grep -n '^ *cozy_report_helmrelease_remediation ' "$lib" | cut -d: -f1 \
        | awk -v s="$snapshot" '$1 < s' | tail -n 1)
    if [ -z "$report" ]; then
        echo "expected the exit handler to classify the HelmReleases before the snapshot" >&2
        exit 1
    fi
    if [ "$report" -ge "$snapshot" ]; then
        echo "expected the classification (line $report) before the snapshot (line $snapshot)" >&2
        exit 1
    fi
}

@test "the wait the guard depends on names the release it timed out on" {
    lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # The parent readiness wait exists so the guard below it has a completed
    # helm action to read. On elapse, kubectl prints its own timeout line and
    # nothing about the release, and the guard never runs at all - so the run
    # reported a readiness timeout without naming what the release was stuck
    # on. The exit handler classifies the history from there, but a release
    # mid-install has a Ready message and events that the history does not
    # carry.
    #
    # Pinned on the failure branch rather than on the wait, because the wait
    # was always there and the branch is what makes its failure legible.
    wait_line=$(grep -n -F 'if ! kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready; then' "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$wait_line" ]; then
        echo "expected the parent readiness wait to carry a failure branch" >&2
        exit 1
    fi
    for read_label in 'parent HelmRelease detail' 'tenant-test events'; do
        if ! grep -q -F "cozy_diag_read '${read_label}'" "$lib"; then
            echo "expected the timeout branch to collect: ${read_label}" >&2
            exit 1
        fi
    done
}

@test "the failure-path report does not list the addons past its budget" {
    # The per-release check admits or declines a release that is already known.
    # The listing is what discovers them, and it is a read of its own: spent
    # past the deadline it buys a name for a walk that has no time left to make.
    # Checked only inside the loop, the parent's reads plus this one could carry
    # the walk well past the budget before anything looked at the clock, on the
    # paths where nothing else bounds it at all.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"
    # The deadline is stamped at 1000+90 and the listing is checked one reading
    # later, at 1100.
    date_ticks="$tmp/ticks"
    printf '0' > "$date_ticks"
    date_now=1000
    date_step=100
    COZY_GUARD_REPORT_BUDGET=90

    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version 2>&1) || rc=$?
    date_now=
    date_step=0
    date_ticks=
    unset COZY_GUARD_REPORT_BUDGET
    if [ "$rc" -ne 0 ]; then
        echo "expected the report to return zero, got rc=$rc: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'HelmRelease kubernetes-test-latest-version (install strategy'; then
        echo "expected the parent to have been classified before the budget ran out, got: $out" >&2
        exit 1
    fi
    if printf '%s\n' "$out" | grep -q 'kubernetes-test-latest-version-cilium'; then
        echo "expected no addon to be read once the budget was spent, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q -F 'the addon releases of kubernetes-test-latest-version were not listed'; then
        echo "expected the skipped listing to be named, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the guard's own verdict does not get classified a second time on the way out" {
    # The guard call sits before the snapshot trap is disarmed, deliberately, so
    # when it finds a teardown the script exits with the handler still armed and
    # the handler runs the report over the releases the guard just judged. Both
    # go through cozy_guard_helmrelease, so every line would be identical: two
    # observations of one release where there was one, and a second walk
    # spending its budget in front of the snapshot on the run where the snapshot
    # matters most.
    #
    # Both calls run in one command substitution because the flag is set inside
    # the functions, which is also how the real pair sees it: the guard runs in
    # run_kubernetes_test and the handler on that shell's exit. Only stdout is
    # compared, since the other runner traces the body to stderr.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'uninstalled\ndeployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-cilium.history"

    _COZY_GUARD_REPORT_DONE=0
    out=$( { PATH="$tmp/bin:$PATH" cozy_guard_all_helmreleases tenant-test kubernetes-test-latest-version || true
             PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version
           } 2>/dev/null )
    parent=$(printf '%s\n' "$out" | grep -c '^HelmRelease kubernetes-test-latest-version (install strategy' || true)
    if [ "$parent" != 1 ]; then
        echo "expected the parent classified once, got $parent:
$out" >&2
        exit 1
    fi
    addon=$(printf '%s\n' "$out" | grep -c '^HelmRelease kubernetes-test-latest-version-cilium (install strategy' || true)
    if [ "$addon" != 1 ]; then
        echo "expected the addon classified once, got $addon:
$out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "a report that printed nothing does not spend the run's one classification" {
    # The flag is set after the early returns, not before them. A call that
    # returns without printing has classified nothing, and letting it claim the
    # slot would leave the release unclassified while the run looked as though
    # it had been read once. The reachable shape is an empty parent name, which
    # is what the exit handler passes when it fires before the release exists.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    _COZY_GUARD_REPORT_DONE=0
    out=$( { PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test ""
             PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version
           } 2>/dev/null )
    if ! printf '%s\n' "$out" | grep -q '^HelmRelease kubernetes-test-latest-version (install strategy'; then
        echo "expected the second call to classify after the first printed nothing, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "a zero report budget is rejected because zero declines the walk instead of bounding it" {
    # The budget gates whether the next read may start, so zero does not tighten
    # the walk, it refuses the first reading: the deadline is stamped at the
    # current second and the listing that discovers the addons is already past
    # it. A run reaching this collector is a failing run, and a zero here turns
    # the only classification it gets into "stopped after 0s".
    #
    # Both halves have to reject it. The knob is resolved once at source time,
    # which is where a value from the environment arrives, and again where the
    # walk reads it, which is the only moment a caller inside this shell can
    # move it - the suite lowers it exactly that way in the budget case above.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)

    out=$(env COZY_GUARD_REPORT_BUDGET=0 bash -c '
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      printf "resolved=%s\n" "$COZY_GUARD_REPORT_BUDGET"
    ' 2>&1) || true
    if ! printf '%s\n' "$out" | grep -q "ignoring COZY_GUARD_REPORT_BUDGET='0'"; then
        echo "expected a zero from the environment to be rejected and named, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q "resolved=${COZY_GUARD_REPORT_BUDGET_DEFAULT}"; then
        echo "expected the rejected zero to fall back to the default, got: $out" >&2
        exit 1
    fi

    # And after sourcing, which the assignment-time check cannot see. The walk
    # has to reach the releases: a report that declined every one of them at a
    # zero deadline reads, in the bundle, exactly like a namespace whose
    # releases were all clean.
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-coredns\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version-coredns.history"

    _COZY_GUARD_REPORT_DONE=0
    COZY_GUARD_REPORT_BUDGET=0
    out=$(PATH="$tmp/bin:$PATH" cozy_report_helmrelease_remediation tenant-test kubernetes-test-latest-version 2>&1) || true
    unset COZY_GUARD_REPORT_BUDGET
    if ! printf '%s\n' "$out" | grep -q "ignoring COZY_GUARD_REPORT_BUDGET='0'"; then
        echo "expected a post-source zero to be rejected and named, got: $out" >&2
        exit 1
    fi
    if printf '%s\n' "$out" | grep -q 'stopped after 0s'; then
        echo "expected the walk to run rather than decline at a zero deadline, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'HelmRelease kubernetes-test-latest-version-coredns (install strategy'; then
        echo "expected the addon to be read after the zero was corrected, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "the exit handler carries the failing status past the collector that clears it" {
    # The snapshot half used to be the EXIT trap itself and read $? as its first
    # action, which made it right by construction. It is the second half of a
    # composite handler now, so the status has to be captured once at the top and
    # passed down, and what sits between the two is a collector ending in
    # `|| true`. Read $? inside the snapshot half again and it sees that `|| true`
    # rather than the run: zero, on every failing run, and the tenant
    # crust-gather capture returns at its own guard without collecting anything.
    #
    # The trap installation is pinned above; this is the other half, the value
    # that travels through it. Called directly after a command of known status
    # rather than from a real trap, because $? at function entry is the same
    # either way and a trap inside a test body is what hack/bats-no-exit-trap.bats
    # exists to keep out.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)

    # The collector between the two halves returns zero, which is exactly what
    # makes the bug invisible: it is the status $? would report.
    cozy_report_helmrelease_remediation() { return 0; }
    _tenant_snapshot_on_fail() { printf '%s\n' "${1-<none>}" > "$tmp/rc"; }

    # errexit off across the pair: the point is to enter the handler with a
    # non-zero $?, which under `set -e` ends the test at the command that sets it.
    set +e
    sh -c 'exit 7'
    _tenant_failure_on_exit
    set -e
    if [ ! -f "$tmp/rc" ]; then
        echo "expected the handler to reach the snapshot on a failing status" >&2
        exit 1
    fi
    if [ "$(cat "$tmp/rc")" != "7" ]; then
        echo "expected the snapshot to receive the run's status 7, got $(cat "$tmp/rc")" >&2
        exit 1
    fi

    # The other half of the contract: a zero status returns before either
    # collector, so a passing run pays for neither.
    rm -f "$tmp/rc"
    set +e
    true
    _tenant_failure_on_exit
    set -e
    if [ -f "$tmp/rc" ]; then
        echo "expected the handler to return on a zero status, got $(cat "$tmp/rc")" >&2
        exit 1
    fi

    # And the receiving half reads the argument rather than $?, which the two
    # checks above cannot see because they replace it. Called with the two
    # disagreeing on purpose: $? says the opposite of the argument each time, so
    # a function reading the wrong one lands on the wrong branch in both.
    # Re-sourced rather than unset: the stub above replaced the definition, and
    # bash keeps one function per name, so unsetting it takes the real one with it.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # crust-gather goes on PATH rather than into a shell function, the way
    # cozy_make_kubectl_stub does it above and the way the other suites stub this
    # same binary. A hyphen is legal in a command name and not in a function
    # name, so the function form is a parse error under the sh the second runner
    # uses -- accepted by the bash this file also runs under, which is what makes
    # it a stub that works in one runner and takes the whole file down in the
    # other. `command -v` finds a PATH entry exactly as readily.
    mkdir -p "$tmp/bin"
    printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/crust-gather"
    chmod +x "$tmp/bin/crust-gather"
    timeout() { :; }
    CURRENT_TENANT_KC="$tmp/kubeconfig"
    : > "$CURRENT_TENANT_KC"
    COZY_REPORT_DIR="$tmp/report"

    set +e
    true
    out=$(PATH="$tmp/bin:$PATH" _tenant_snapshot_on_fail 7 2>&1)
    set -e
    if ! printf '%s\n' "$out" | grep -q 'capturing tenant crust-gather snapshot'; then
        echo "expected a failing argument to reach the capture despite a zero \$?, got: $out" >&2
        exit 1
    fi

    set +e
    sh -c 'exit 7'
    out=$(PATH="$tmp/bin:$PATH" _tenant_snapshot_on_fail 0 2>&1)
    set -e
    # Absence of the marker rather than an empty capture: one of the two runners
    # executes test bodies under xtrace, which writes the trace to the same fd
    # this redirects, so "nothing was printed" is never true there. The marker is
    # traced only if the line it comes from is reached, so it discriminates under
    # both.
    if printf '%s\n' "$out" | grep -q 'capturing tenant crust-gather snapshot'; then
        echo "expected a zero argument to return before the capture despite a failing \$?, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}

@test "a read the wall clock cut off does not send the reader after a kubectl error" {
    # The bound exists to kill a read that will not finish, and a killed read
    # writes nothing on either stream. Ending the note with "kubectl's error is
    # above" points at an empty screen on exactly the failure the bound produces,
    # while a read that failed for a reason kubectl reported keeps that ending.
    # cozy_diag_read draws the same line for the same reason.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    tmp=$(mktemp -d)
    cozy_make_kubectl_stub "$tmp"
    export FIXTURES="$tmp/fixtures"
    printf 'kubernetes-test-latest-version-cilium\n' > "$FIXTURES/ALL_HR"
    printf 'RetryOnFailure' > "$FIXTURES/kubernetes-test-latest-version.strategy"
    printf 'deployed\n' > "$FIXTURES/kubernetes-test-latest-version.history"

    : > "$FIXTURES/kubernetes-test-latest-version-cilium.history.CUTOFF"
    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_helmrelease tenant-test kubernetes-test-latest-version-cilium 2>&1) || rc=$?
    rm -f "$FIXTURES/kubernetes-test-latest-version-cilium.history.CUTOFF"
    if [ "$rc" -eq 0 ]; then
        echo "expected a cut-off read to fail the guard, got rc=0: $out" >&2
        exit 1
    fi
    if printf '%s\n' "$out" | grep -q "kubectl's error is above"; then
        echo "expected no claim of a kubectl error on a read that printed nothing, got: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q 'was cut off'; then
        echo "expected the note to name the cut-off, got: $out" >&2
        exit 1
    fi

    # The other side: a read that failed with kubectl reporting why keeps the
    # ending that sends the reader to it.
    : > "$FIXTURES/kubernetes-test-latest-version-cilium.history.FAILS"
    rc=0
    out=$(PATH="$tmp/bin:$PATH" cozy_guard_helmrelease tenant-test kubernetes-test-latest-version-cilium 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "expected a failed read to fail the guard, got rc=0: $out" >&2
        exit 1
    fi
    if ! printf '%s\n' "$out" | grep -q "kubectl's error is above"; then
        echo "expected a reported failure to keep pointing at kubectl's output, got: $out" >&2
        exit 1
    fi

    rm -rf "$tmp"
}
