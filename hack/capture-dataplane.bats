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
#   - pod_filter_affected     -- retain scheduled NotReady pods even without IPs.
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

@test "pod_filter_affected keeps a scheduled NotReady pod without a podIP" {
  rows="$(printf '%s\n' \
    'tenant|no-ip||node-a|False|Pending' \
    'tenant|with-ip|192.0.2.80|node-b|False|Running')"

  out="$(printf '%s\n' "$rows" | pod_filter_affected)"

  [ "$(printf '%s\n' "$out" | awk 'END { print NR }')" -eq 2 ]
  printf '%s\n' "$out" | awk '$0 == "tenant|no-ip||node-a|False|Pending" { found = 1 } END { exit !found }'
}

@test "pod_filter_affected drops Ready unscheduled and terminal pods" {
  rows="$(printf '%s\n' \
    'tenant|ready|192.0.2.81|node-a|True|Running' \
    'tenant|unscheduled|||False|Pending' \
    'tenant|succeeded|192.0.2.82|node-b|False|Succeeded' \
    'tenant|failed|192.0.2.83|node-b|False|Failed')"

  [ -z "$(printf '%s\n' "$rows" | pod_filter_affected)" ]
}

@test "runtime checks LoadBalancers when there are no affected pods" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  calls="$tmp/kubectl.calls"
  mkdir -p "$tmp/bin"
  cat > "$tmp/bin/kubectl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$MOCK_KUBECTL_CALLS"
exit 0
EOF
  chmod +x "$tmp/bin/kubectl"

  MOCK_KUBECTL_CALLS="$calls" PATH="$tmp/bin:$PATH" \
    "$SCRIPT" "$tmp/out" > "$tmp/stdout" 2>&1

  awk '$0 ~ /^get svc -A / { found = 1 } END { exit !found }' "$calls"
  awk 'index($0, "checking LoadBalancers independently") { found = 1 } END { exit !found }' "$tmp/stdout"
  awk 'index($0, "no Service type=LoadBalancer") { found = 1 } END { exit !found }' "$tmp/stdout"
}

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
  # `! cmd` is vacuous under cozytest's `set -e` (errexit is suppressed for a
  # `!`-negated pipeline), so a filter regression that let these rows through
  # would not fail the test. Assert the absence via `if cmd; then ...; false`.
  if printf '%s\n' "$out" | grep -q 'kube-dns'; then echo "FAIL: lb_filter_services must drop the kube-dns row"; false; fi
  if printf '%s\n' "$out" | grep -q 'pending'; then echo "FAIL: lb_filter_services must drop the pending (no external IP) row"; false; fi
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

@test "lb_announcer_node excludes a node whose last own event is a withdraw" {
  # Case A: node-a announces the IP then withdraws it and never re-announces.
  # Its last own IP-event is the withdraw, so it is NOT the current announcer and
  # nothing must be reported. The pre-polish logic kept node-a (a withdraw did
  # not retract a prior announce), so this pins the failover fix.
  logs="$(printf '%s\t%s\n' \
    'node-a' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-a"}' \
    'node-a' '{"event":"serviceWithdrawn","ips":["192.0.2.50"],"node":"node-a"}')"

  out="$(printf '%s\n' "$logs" | lb_announcer_node '192.0.2.50')"

  [ -z "$out" ]
}

@test "lb_announcer_node ignores a stale owner that appears later in concat order" {
  # Case B: node-c is the true current owner (announce, never withdrawn). Later
  # in the input -- speaker logs are concatenated per-pod, NOT globally time-
  # sorted -- a stale node-a announce+withdraw block appears. node-a's last own
  # event is a withdraw, so the announcer is node-c, NOT the later-in-input
  # node-a. The pre-polish logic returned node-a (most recent announce by concat
  # order), so this pins robustness to concat order.
  logs="$(printf '%s\t%s\n' \
    'node-c' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-c"}' \
    'node-a' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-a"}' \
    'node-a' '{"event":"serviceWithdrawn","ips":["192.0.2.50"],"node":"node-a"}')"

  out="$(printf '%s\n' "$logs" | lb_announcer_node '192.0.2.50')"

  [ "$out" = "node-c" ]
}

@test "lb_announcer_node matches the IP as a whole token not a substring" {
  # Case C: querying 192.0.2.5 must NOT match a 192.0.2.50 announce (the
  # pre-polish index()/substring match returned node-d here). The exact 192.0.2.5
  # announce on node-e is matched and wins.
  logs="$(printf '%s\t%s\n' \
    'node-d' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-d"}' \
    'node-e' '{"event":"serviceAnnounced","ips":["192.0.2.5"],"node":"node-e"}')"

  no_substr="$(printf '%s\n' "$logs" | lb_announcer_node '192.0.2.5')"
  no_match="$(printf '%s\t%s\n' \
    'node-d' '{"event":"serviceAnnounced","ips":["192.0.2.50"],"node":"node-d"}' \
    | lb_announcer_node '192.0.2.5')"

  [ "$no_substr" = "node-e" ]
  [ -z "$no_match" ]
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

@test "lb_budget_ok yes below the cap and no once it is reached" {
  [ "$(lb_budget_ok 0 6)" = "yes" ]
  [ "$(lb_budget_ok 5 6)" = "yes" ]
  [ "$(lb_budget_ok 6 6)" = "no" ]
  [ "$(lb_budget_ok 7 6)" = "no" ]
}

@test "capture budget counts captured LBs so a late broken LB is still captured" {
  # The MAX_LBS cap must bound LBs actually CAPTURED, not LBs enumerated: reachable
  # (skipped) LBs must not consume the budget, so a broken LB enumerated after many
  # reachable ones is still characterised. This mirrors the per-LB loop in
  # capture_lb_datapath, which only consults lb_budget_ok / increments the counter
  # on the capture branch -- here driven by a fixed decision sequence so no cluster
  # is needed. With the pre-polish "increment per enumerated LB" logic and max=2,
  # the loop would have broken at the 3rd LB and never reached the broken one.
  max=2
  captured=0
  last=""
  for decision in skip skip skip skip skip capture; do
    if [ "$decision" != "capture" ]; then
      last="skip"
      continue
    fi
    if [ "$(lb_budget_ok "$captured" "$max")" != "yes" ]; then
      last="dropped"
      continue
    fi
    captured=$((captured + 1))
    last="captured"
  done

  [ "$captured" -eq 1 ]
  [ "$last" = "captured" ]
}

@test "capture budget stops after the cap worth of broken LBs" {
  # Once MAX_LBS broken LBs are captured, further unreachable LBs are not
  # characterised (left to the outer wall-clock backstop). max=2, three broken.
  max=2
  captured=0
  dropped=0
  for decision in capture capture capture; do
    if [ "$(lb_budget_ok "$captured" "$max")" != "yes" ]; then
      dropped=$((dropped + 1))
      continue
    fi
    captured=$((captured + 1))
  done

  [ "$captured" -eq 2 ]
  [ "$dropped" -eq 1 ]
}
