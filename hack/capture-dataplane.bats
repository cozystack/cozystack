#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the pure decision/parsing helpers in
# hack/e2e-capture-dataplane.sh -- specifically the LoadBalancer-datapath
# section, which fires for a Service type=LoadBalancer whose external IP is
# unreachable while its backend stays Ready (the NotReady-pod path never sees
# this case). Only the pure logic is unit-testable here:
#
#   - lb_filter_services      -- keep only LoadBalancer rows with an ingress IP;
#   - lb_first_ready_endpoint -- pick the first addressed, non-NotReady endpoint;
#   - lb_announcer_node       -- the speaker node of the most recent announce;
#   - lb_capture_decision     -- capture only when every probe failed.
#
# The kubectl exec / tcpdump capture itself is not unit-testable (it needs a
# live cluster); these tests pin the derivations that decide WHICH node to
# capture on and WHETHER to capture at all, fed mock kubectl/log output.
#
# Strategy: the script is sourced once with E2E_CAPTURE_DATAPLANE_LIB set, which
# the script's sourcing guard honours by defining the helpers and returning
# before it touches $1 or runs any capture -- so no cluster is required and the
# capture body never executes. Each @test then calls the helpers directly and
# asserts with `[ ... ]`, matching this repo's plain-shell bats convention (no
# `run` helper). Mock IPs use the RFC 5737 / RFC 3849 documentation ranges.
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/capture-dataplane.bats
#           (or `bats hack/capture-dataplane.bats` if the bats binary is
#           installed; cozytest.sh is the CI path.)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/e2e-capture-dataplane.sh"

# Load the pure helpers. The guard returns before the capture body, so this is
# side-effect-free and needs no cluster.
E2E_CAPTURE_DATAPLANE_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

@test "lb_filter_services keeps only LoadBalancer rows that have an ingress IP" {
  rows="$(printf '%s\n' \
    'tenant|app|LoadBalancer|192.0.2.50|80|31000|Cluster' \
    'kube-system|kube-dns|ClusterIP||53||Cluster' \
    'tenant|pending|LoadBalancer||443|31443|Local' \
    'tenant|db|LoadBalancer|192.0.2.51|5432|31543|Local')"

  out="$(printf '%s\n' "$rows" | lb_filter_services)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
  printf '%s\n' "$out" | grep -q '^tenant|app|LoadBalancer|192.0.2.50|'
  printf '%s\n' "$out" | grep -q '^tenant|db|LoadBalancer|192.0.2.51|'
  ! printf '%s\n' "$out" | grep -q 'kube-dns'
  ! printf '%s\n' "$out" | grep -q 'pending'
}

@test "lb_first_ready_endpoint picks the first addressed endpoint that is not NotReady" {
  eps="$(printf '%s\n' \
    '||tenant|virt-launcher-x|true' \
    '192.0.2.60|worker-1|tenant|virt-launcher-x|false' \
    '192.0.2.61|worker-2|tenant|virt-launcher-y|true' \
    '192.0.2.62|worker-3|tenant|virt-launcher-z|true')"

  out="$(printf '%s\n' "$eps" | lb_first_ready_endpoint)"

  [ "$out" = "192.0.2.61|worker-2|tenant|virt-launcher-y" ]
}

@test "lb_first_ready_endpoint treats a blank ready column as ready" {
  eps="$(printf '%s\n' '192.0.2.70|worker-9|tenant|app-0|')"

  out="$(printf '%s\n' "$eps" | lb_first_ready_endpoint)"

  [ "$out" = "192.0.2.70|worker-9|tenant|app-0" ]
}

@test "lb_announcer_node returns the speaker node of the most recent announce" {
  logs="$(printf '%s\t%s\n' \
    'node-a' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-a"}' \
    'node-b' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-b"}')"

  out="$(printf '%s\n' "$logs" | lb_announcer_node '192.0.2.50')"

  [ "$out" = "node-b" ]
}

@test "lb_announcer_node ignores withdraw lines and announces for other IPs" {
  logs="$(printf '%s\t%s\n' \
    'node-x' '{"event":"serviceAnnounced","ips":["198.51.100.9"],"node":"node-x"}' \
    'node-a' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-a"}' \
    'node-b' '{"event":"serviceWithdrawn","ips":["192.0.2.50"],"node":"node-b"}' \
    'node-c' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-c"}')"

  out="$(printf '%s\n' "$logs" | lb_announcer_node '192.0.2.50')"

  [ "$out" = "node-c" ]
}

@test "lb_announcer_node emits nothing when the IP was never announced" {
  logs="$(printf '%s\t%s\n' \
    'node-a' '{"event":"serviceAnnounced","ips":["198.51.100.9"],"node":"node-a"}')"

  out="$(printf '%s\n' "$logs" | lb_announcer_node '192.0.2.50')"

  [ -z "$out" ]
}

@test "lb_capture_decision returns capture when every probe failed" {
  [ "$(printf 'fail\nfail\nfail\n' | lb_capture_decision)" = "capture" ]
  [ "$(printf 'fail\n' | lb_capture_decision)" = "capture" ]
}

@test "lb_capture_decision returns skip when any probe succeeded" {
  [ "$(printf 'fail\nok\nfail\n' | lb_capture_decision)" = "skip" ]
  [ "$(printf 'ok\n' | lb_capture_decision)" = "skip" ]
}

@test "lb_capture_decision returns skip when no probe ran at all" {
  [ "$(printf '' | lb_capture_decision)" = "skip" ]
}
