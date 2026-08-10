#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the pod-label census in
# hack/e2e-chainsaw/_lib/pod-label-census.sh.
#
# The census exists because a `podLogs` collector cannot report that its
# selector matched nothing: an empty capture and a stale selector are the same
# artifact. It prints the instance labels that actually exist, so a reader can
# tell the two apart.
#
# What these tests pin is the discrimination between its three outcomes, which
# is where the value is and where it is easy to lose. In particular the fourth
# case below: kubectl exits 0 while logging a klog line about an API group it
# could not reach, which is the normal shape of a degraded cluster and so the
# normal shape of a run where this catch fires at all. An earlier form captured
# with `2>&1`, and that klog line then made the value non-empty -- the "no pods"
# branch became unreachable and the error printed under the census heading as
# though it were a pod. Separating the streams is the fix; these tests are what
# keeps it separated.
#
# The report half is a pure function of (rc, stdout, stderr, namespace), so it
# needs no cluster. The last test drives the kubectl-calling half through a stub
# on PATH, to prove the streams stay apart end to end rather than only in the
# half that never touches kubectl.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`, and setup()/teardown() are not
# honored. Each test runs under `set -eu -x`; assertions are direct shell tests
# that exit non-zero on failure. Titles are delimited by ASCII double quotes and
# only [A-Za-z0-9] survives into the generated function name, so keep them
# distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/pod-label-census_test.bats
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
# shellcheck source=/dev/null
. "$HACK_DIR/e2e-chainsaw/_lib/pod-label-census.sh"

@test "census prints the rows it was given under the census heading" {
    out="$(pod_label_census_report 0 "pod-a  harbor-test-system" "" tenant-test)"
    echo "$out" | grep -q 'app.kubernetes.io/instance per pod'
    echo "$out" | grep -q 'pod-a  harbor-test-system'
}

@test "census names the namespace when the lookup ran and matched nothing" {
    out="$(pod_label_census_report 0 "" "" tenant-test)"
    echo "$out" | grep -q 'no pods in tenant-test'
    # An empty result is an answer, not a failure: saying FAILED here would
    # report a healthy empty namespace as an unreachable apiserver.
    #
    # Counted rather than written `! ... | grep -q`: POSIX exempts a command
    # whose status is inverted with `!` from `set -e`, so that form cannot fail
    # a test here -- it would pass whatever the output said.
    [ "$(echo "$out" | grep -c 'FAILED')" -eq 0 ]
}

@test "census says the lookup did not run and does not call it no pods" {
    out="$(pod_label_census_report 1 "" "Unable to connect to the server" tenant-test)"
    echo "$out" | grep -q 'FAILED'
    echo "$out" | grep -q 'Unable to connect to the server'
    # The distinction this whole helper exists for: a lookup that never
    # returned must not be reported as an empty namespace. Counted, not
    # `! ... | grep -q`, for the reason given in the empty-result test above.
    #
    # Matched on the empty-result sentence rather than on "no pods": the FAILED
    # line quotes that phrase itself, to say what it is not.
    [ "$(echo "$out" | grep -c 'no pods in')" -eq 0 ]
}

@test "census keeps a zero exit with a klog line out of the rows" {
    klog='E0805 memcache.go:265 Unhandled Error: couldnt get API group list'
    out="$(pod_label_census_report 0 "" "$klog" tenant-test)"
    # The regression: with the streams folded together this printed the klog
    # line under the census heading, as if the API error were a pod.
    echo "$out" | grep -q 'no pods in tenant-test'
    echo "$out" | grep -q 'may be partial'
    echo "$out" | grep -q 'memcache.go'
}

@test "census marks rows as possibly partial when kubectl also logged an error" {
    klog='E0805 memcache.go:265 Unhandled Error: couldnt get API group list'
    out="$(pod_label_census_report 0 "pod-a  harbor-test-system" "$klog" tenant-test)"
    echo "$out" | grep -q 'pod-a  harbor-test-system'
    echo "$out" | grep -q 'may be partial'
}

@test "shared report keeps the three outcomes apart for the cleanup wording too" {
    # The qdrant cleanup catch feeds the same helper with its own messages, so
    # the discrimination is tested once and used twice rather than living
    # inline in YAML where nothing can pin it.
    failed="cleanup lookup FAILED"
    empty="matched no objects"
    heading="cleanup objects still present:"

    out="$(lookup_report 1 "" "Unable to connect to the server" "$failed" "$empty" "$heading")"
    echo "$out" | grep -q 'cleanup lookup FAILED'
    # A lookup that never ran must not read as "nothing left to clean up".
    [ "$(echo "$out" | grep -c 'matched no objects')" -eq 0 ]

    out="$(lookup_report 0 "" "" "$failed" "$empty" "$heading")"
    echo "$out" | grep -q 'matched no objects'

    out="$(lookup_report 0 "job.batch/qdrant-test-qdrant-cleanup" "" "$failed" "$empty" "$heading")"
    echo "$out" | grep -q 'cleanup objects still present'
    echo "$out" | grep -q 'job.batch/qdrant-test-qdrant-cleanup'
}

@test "shared report flags a zero exit with stderr as possibly partial for any wording" {
    klog='E0805 memcache.go:265 Unhandled Error'
    out="$(lookup_report 0 "" "$klog" "FAILED-MSG" "EMPTY-MSG" "ROWS-MSG")"
    # Same regression as the census case: folded into the value this klog line
    # would suppress the empty branch and print as though it were a result.
    echo "$out" | grep -q 'EMPTY-MSG'
    echo "$out" | grep -q 'may be partial'
    [ "$(echo "$out" | grep -c 'ROWS-MSG')" -eq 0 ]
}

@test "census driven through a kubectl stub keeps stderr out of the value" {
    stubdir="$(mktemp -d)"
    printf '#!/bin/sh\necho "E0805 memcache.go:265 Unhandled Error" >&2\nexit 0\n' \
        > "$stubdir/kubectl"
    chmod +x "$stubdir/kubectl"
    out="$(PATH="$stubdir:$PATH" pod_label_census tenant-test)"
    rm -rf "$stubdir"
    # End to end: kubectl exited 0 and wrote only to stderr, so the namespace
    # really is empty and the klog line is a caveat, not a row.
    echo "$out" | grep -q 'no pods in tenant-test'
    echo "$out" | grep -q 'may be partial'
}

@test "census returns zero even when the lookup failed so a catch cannot mask the real error" {
    stubdir="$(mktemp -d)"
    printf '#!/bin/sh\necho "Unable to connect to the server" >&2\nexit 1\n' \
        > "$stubdir/kubectl"
    chmod +x "$stubdir/kubectl"
    # The call goes in the `if` condition, not on a line of its own: these
    # tests run under `set -e`, which exempts a condition but would abort the
    # test before a following `rc=$?` could ever read a non-zero. Read after
    # the fact, that assertion could only ever see 0 and would pass whatever
    # the function returned.
    if PATH="$stubdir:$PATH" pod_label_census tenant-test > /dev/null; then
        rc=0
    else
        rc=1
    fi
    rm -rf "$stubdir"
    [ "$rc" -eq 0 ]
}
