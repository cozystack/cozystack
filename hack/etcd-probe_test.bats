#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the probe-output resolver in
# hack/e2e-chainsaw/_lib/etcd-probe.sh.
#
# The etcd suite proves the member Pods serve Prometheus metrics over plaintext
# http by running a throwaway curl Pod and reading back a `code=<status>` marker.
# `kubectl run --attach` is how that marker comes back, and it loses the stream
# of a container that exits before the attach connection is established -- curl
# against a Pod IP finishes in milliseconds, so the loss is routine rather than
# exotic. When it happens the
# caller sees only kubectl's own chatter ("If you don't see a command prompt,
# try pressing enter."), reads no marker, and reports the metrics endpoint as
# broken -- a false negative on a healthy cluster.
#
# etcd_probe_resolve is the recovery: the container's stdout is still on disk in
# the Pod's log, so a lost attach stream is re-read rather than believed. These
# tests pin that recovery, the pass-through when attach did carry the marker,
# and the honest surfacing of a genuine non-200 (which must NOT be masked by the
# fallback -- a real failure has to stay a failure).
#
# The resolver is separated from the kubectl orchestration precisely so it can
# be tested without a cluster; the `kubectl run`/`kubectl logs` calls around it
# need a live cluster and are covered by the e2e run, matching how the other
# hack/ helpers are tested.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`, and setup()/teardown() are not
# honored. Each test runs under `set -eu -x`; assertions are direct shell tests
# that exit non-zero on failure. Titles are delimited by ASCII double quotes and
# only [A-Za-z0-9] survives into the generated function name, so keep them
# distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/etcd-probe_test.bats
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
# shellcheck source=/dev/null
. "$HACK_DIR/e2e-chainsaw/_lib/etcd-probe.sh"

@test "resolver returns the attach output unchanged when it carries the marker" {
    out="$(etcd_probe_resolve 'code=200' printf 'code=500')"
    # The fallback must not run at all when attach already delivered a marker:
    # re-reading the log would be a second source of truth for the same probe.
    [ "$out" = "code=200" ]
}

@test "resolver rereads the pod log when the attach stream carried no marker" {
    lost="If you don't see a command prompt, try pressing enter."
    out="$(etcd_probe_resolve "$lost" printf 'code=200')"
    # This is the regression under test: before the fallback existed the lost
    # stream was reported verbatim as the probe result and failed the suite on a
    # cluster whose metrics endpoint was serving 200 the whole time.
    [ "$out" = "code=200" ]
}

@test "resolver rereads the pod log when the attach stream was entirely empty" {
    out="$(etcd_probe_resolve '' printf 'code=200')"
    [ "$out" = "code=200" ]
}

@test "resolver surfaces a genuine non200 from the attach stream" {
    # A real failure must survive the resolver untouched. If this ever returns
    # the fallback's value instead, the fallback has become a way to launder a
    # broken metrics endpoint into a pass.
    out="$(etcd_probe_resolve 'code=503' printf 'code=200')"
    [ "$out" = "code=503" ]
}

@test "resolver surfaces a genuine non200 recovered from the pod log" {
    lost="If you don't see a command prompt, try pressing enter."
    out="$(etcd_probe_resolve "$lost" printf 'code=503')"
    [ "$out" = "code=503" ]
}

@test "resolver keeps the lost attach output when the log reread also yields nothing" {
    # Both sources empty means the probe genuinely produced no marker. Returning
    # the attach text keeps whatever kubectl said about why in the failure
    # message, instead of reporting an empty string the reader cannot act on.
    lost="Error from server: pods etcd-metrics-probe not found"
    out="$(etcd_probe_resolve "$lost" printf '')"
    [ "$out" = "$lost" ]
}

@test "resolver tolerates a failing log reread without aborting the caller" {
    # `kubectl logs` against an evicted or already-collected Pod exits non-zero.
    # Under the suite's `set -e` that must not kill the step before it can
    # report the probe result.
    lost="If you don't see a command prompt, try pressing enter."
    out="$(etcd_probe_resolve "$lost" false)"
    [ "$out" = "$lost" ]
}
