# shellcheck shell=sh
# Sourced by the chainsaw app Tests after `cd` to the repo root. Provides:
#   lookup_report           — tell a diagnostic lookup's three outcomes apart,
#                             with the wording left to the caller; no cluster
#   pod_label_census_report — lookup_report with the census wording
#   pod_label_census        — print app.kubernetes.io/instance for every pod in
#                             a namespace, keeping the outcomes apart
# Sourcing has no side effects: it defines three functions and nothing else.
# None uses `local`, which is not POSIX and so unavailable to the `sh` that
# Chainsaw runs these steps under; the `_plc_` prefix is what keeps the
# variables from colliding with a caller's names.
#
# Why this exists. A `podLogs` collector selects on a label, and it cannot
# report that it matched nothing — an empty capture and a selector that has
# gone stale look identical in the artifact. This prints the values that
# actually exist, so a reader can see whether the selector above it can match.
# Deliberately no selector is repeated here: a copy would only prove itself
# right, and would keep matching after the collector's own selector drifted.
#
# The three outcomes are kept apart because each means something different and
# two of them are easy to misread as the third:
#
#   - the lookup did not run          — NOT the same as "no pods"; saying so
#                                       would report an unreachable apiserver
#                                       as a healthy empty namespace
#   - it ran and matched nothing      — a real, informative answer
#   - it ran and returned rows        — the census proper
#
# stderr is kept apart from the captured value rather than folded in with
# `2>&1`, and that is the load-bearing part. kubectl exits 0 while logging a
# klog line about an API group it could not reach, and an aggregated APIService
# in that state is what a failed run usually looks like (see
# docs/agents/e2e-testing.md). Folded into the value, that line makes the
# "no pods" branch unreachable and prints under the census heading as though it
# were a pod. Separated, it becomes what it is: a caveat that the census may be
# partial, printed alongside the answer rather than instead of it.
#
# `--request-timeout` bounds each attempt because on a degraded cluster kubectl
# otherwise retries discovery until something kills the script. Measured
# against an unreachable apiserver, a bounded call costs about five times the
# flag, since kubectl retries discovery five times before it gives up; the
# caller's op timeout is sized above that.
#
# The report half is separated from the kubectl call precisely so it can be
# tested without a cluster — see hack/pod-label-census_test.bats, matching how
# hack/e2e-chainsaw/_lib/etcd-probe.sh is covered.

# lookup_report RC STDOUT STDERR FAILED_MSG EMPTY_MSG ROWS_HEADING
#
# The outcome logic, with the wording left to the caller. Every diagnostic
# lookup in a `catch` has the same three answers to keep apart and the same two
# ways of confusing them, so they share one implementation and one set of tests
# rather than a copy per call site.
#
# Always returns 0: this runs inside a `catch`, where a non-zero exit would mask
# the test's own error.
lookup_report() {
    _plc_rc=$1
    _plc_out=$2
    _plc_err=$3
    _plc_failed=$4
    _plc_empty=$5
    _plc_heading=$6

    if [ "$_plc_rc" -ne 0 ]; then
        echo "$_plc_failed"
        [ -n "$_plc_err" ] && echo "$_plc_err"
        return 0
    fi

    if [ -z "$_plc_out" ]; then
        echo "$_plc_empty"
    else
        echo "$_plc_heading"
        echo "$_plc_out"
    fi

    if [ -n "$_plc_err" ]; then
        echo "the lookup also logged this, so the result above may be partial:"
        echo "$_plc_err"
    fi
    return 0
}

# pod_label_census_report RC STDOUT STDERR NAMESPACE
pod_label_census_report() {
    lookup_report "$1" "$2" "$3" \
        "pod label census FAILED (the lookup did not run; this is not 'no pods'):" \
        "pod label census: no pods in $4" \
        "pod label census, app.kubernetes.io/instance per pod:"
}

# pod_label_census NAMESPACE
pod_label_census() {
    _plc_ns=$1
    _plc_errfile=$(mktemp)

    if _plc_stdout=$(kubectl -n "$_plc_ns" --request-timeout=15s get pods \
            -o 'custom-columns=POD:.metadata.name,INSTANCE:.metadata.labels.app\.kubernetes\.io/instance' \
            --no-headers 2>"$_plc_errfile"); then
        _plc_status=0
    else
        _plc_status=1
    fi

    pod_label_census_report "$_plc_status" "$_plc_stdout" "$(cat "$_plc_errfile")" "$_plc_ns"
    rm -f "$_plc_errfile"
    return 0
}
