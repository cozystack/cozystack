#!/usr/bin/env bats
# EXIT-TRAP DEBT: 1 -- see hack/bats-no-exit-trap.bats; lower it as the traps go, delete it at zero.
# -----------------------------------------------------------------------------
# Unit tests for the pure decision/parsing helpers in
# hack/e2e-capture-dataplane.sh -- specifically the LoadBalancer-datapath
# section, which fires for a Service type=LoadBalancer whose external IP is
# unreachable while its backend stays Ready (the NotReady-pod path never sees
# this case). Only the pure logic is unit-testable here:
#
#   - lb_filter_services      -- keep only whole LoadBalancer rows that carry an
#                               ingress IP;
#   - lb_first_ready_endpoint -- pick the first addressed, non-NotReady endpoint;
#   - lb_announcer_node       -- the speaker node of the most recent announce;
#   - lb_capture_decision     -- capture when probes failed, skip when one
#                               succeeded, unknown when none ran or none could.
#   - pod_filter_affected     -- retain scheduled NotReady pods even without IPs,
#                               and only from whole rows.
#   - pod_first_ready         -- pick the healthy-pod baseline for one node out
#                               of the cluster-wide snapshot, whole rows only.
#
# The tests come in two shapes, and neither needs a cluster.
#
# The first sources the script with E2E_CAPTURE_DATAPLANE_LIB set, which the
# sourcing guard honours by defining the helpers and returning before it touches
# $1 or runs any capture. Those tests call the pure helpers directly and assert
# with `[ ... ]`, matching this repo's plain-shell bats convention (no `run`
# helper). Mock IPs use the RFC 5737 / RFC 3849 documentation ranges.
#
# The second runs the script as a subprocess against a stub kubectl on PATH --
# one that hangs, refuses, or answers in part -- and reads what it wrote. That
# reaches the capture body itself, which no amount of sourcing can: the bounds,
# the notes and the difference between "nothing is there" and "the read never
# said" are all properties of the running script, and several of them are only
# observable in the artifact it leaves behind.
#
# A stub has to reach the branch a test is about. Several of these tests walk
# the LB heavy-capture path, which only runs when the probe reports the address
# unreachable, so their stubs answer `nc -z` with a failure on purpose. A stub
# that stops short leaves the test green against every implementation.
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
    'tenant|no-ip||node-a|False|Pending||eol' \
    'tenant|with-ip|192.0.2.80|node-b|False|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_filter_affected)"

  [ "$(printf '%s\n' "$out" | awk 'END { print NR }')" -eq 2 ]
  printf '%s\n' "$out" | awk '$0 == "tenant|no-ip||node-a|False|Pending||eol" { found = 1 } END { exit !found }'
}

@test "pod_filter_affected drops Ready unscheduled and terminal pods" {
  rows="$(printf '%s\n' \
    'tenant|ready|192.0.2.81|node-a|True|Running||eol' \
    'tenant|unscheduled|||False|Pending||eol' \
    'tenant|succeeded|192.0.2.82|node-b|False|Succeeded||eol' \
    'tenant|failed|192.0.2.83|node-b|False|Failed||eol')"

  [ -z "$(printf '%s\n' "$rows" | pod_filter_affected)" ]
}

@test "pod_first_ready picks the first Ready Running pod on the named node" {
  # Rows are the cluster-wide snapshot, so the filter scopes to a node itself:
  # ns|name|podIP|nodeName|ready|phase|hostNetwork.
  rows="$(printf '%s\n' \
    'tenant|not-ready|192.0.2.90|node-a|False|Running||eol' \
    'tenant|pending|192.0.2.91|node-a|True|Pending||eol' \
    'tenant|other-node|192.0.2.94|node-b|True|Running||eol' \
    'tenant|healthy-1|192.0.2.92|node-a|True|Running||eol' \
    'tenant|healthy-2|192.0.2.93|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|healthy-1|192.0.2.92" ]
}

@test "pod_first_ready ignores an eligible pod on a different node" {
  # The scoping is the whole reason this reads the cluster-wide list safely: a
  # baseline from another machine would compare the wedged pod's route against
  # a route that never shared a kernel with it.
  rows="$(printf '%s\n' 'tenant|healthy|192.0.2.95|node-b|True|Running||eol')"

  [ -z "$(printf '%s\n' "$rows" | pod_first_ready node-a)" ]
}

@test "pod_first_ready emits nothing when no pod on the node is Ready and Running" {
  rows="$(printf '%s\n' \
    'tenant|not-ready|192.0.2.94|node-a|False|Running||eol' \
    'tenant|pending|192.0.2.95|node-a|True|Pending||eol')"

  [ -z "$(printf '%s\n' "$rows" | pod_first_ready node-a)" ]
}

@test "pod_first_ready emits nothing on empty input" {
  [ -z "$(printf '' | pod_first_ready node-a)" ]
}

@test "pod_first_ready rejects a hostNetwork row cut at the separator" {
  # The half a value check cannot reach on its own. A stream cut RIGHT AFTER the
  # sixth separator leaves the hostNetwork column empty, and empty is
  # byte-identical to the omitempty-false the filter must accept -- so `$7 ==
  # ""` alone seats a hostNetwork pod whose `true` was cut away entirely. The
  # trailing constant is what separates them, and the two halves of that guard
  # catch different cuts, so both are exercised here:
  #   row 1 loses its tail before the last separator  -> NF is 7, not 8
  #   row 2 keeps the count but the sentinel is cut   -> NF is 8, $8 is not eol
  # Neither may be picked; the whole row behind them must be.
  rows="$(printf '%s\n' \
    'cozy-cilium|cilium-abcde|192.0.2.10|node-a|True|Running|' \
    'cozy-cilium|cilium-fghij|192.0.2.13|node-a|True|Running||eo' \
    'tenant|workload|192.0.2.12|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|workload|192.0.2.12" ]
}

@test "pod_first_ready rejects a hostNetwork row cut inside its last value" {
  # The one filter here where a truncated last value must NOT be kept.
  # `tru` carries all eight fields and passes every other gate, so a deny-list
  # (`$7 != "true"`) accepts it -- and because this filter stops at the row it
  # takes, the baseline then IS a hostNetwork pod, whose podIP is the node's own
  # address. That is the fingerprint the exclusion exists to keep out of the
  # comparison, so the control would be the thing it excludes. The allow-list
  # form rejects any value that is not an exact known state.
  rows="$(printf '%s\n' \
    'cozy-cilium|cilium-abcde|192.0.2.10|node-a|True|Running|tru|eol' \
    'tenant|workload|192.0.2.11|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|workload|192.0.2.11" ]
}

@test "pod_first_ready skips a Ready pod that has no podIP yet" {
  # The filter stops at the row it takes, so an addressless pod does not just
  # produce a thin baseline -- it consumes the only pick and the pod behind it
  # is never considered.
  rows="$(printf '%s\n' \
    'tenant|no-ip-yet||node-a|True|Running||eol' \
    'tenant|healthy|192.0.2.99|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|healthy|192.0.2.99" ]
}

@test "pod_first_ready drops a pod row cut off mid-record" {
  # The cut has to land where the fragment still satisfies every value test, or
  # the row is rejected for an unrelated reason and the count is never
  # consulted: here the stream ends right after `Running`, one separator short
  # of the hostNetwork column.
  rows="$(printf '%s\n' \
    'tenant|cut-off|192.0.2.9|node-a|True|Running' \
    'tenant|healthy|192.0.2.98|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|healthy|192.0.2.98" ]
}

@test "pod_first_ready skips a hostNetwork pod even when it is Ready Running and first" {
  # cilium-agent / kube-ovn-cni / ovs-ovn run hostNetwork on every node, and a
  # hostNetwork pod's podIP IS the node's own address -- picking one as the
  # baseline would resolve to a local route for an entirely ordinary reason,
  # which is the same fingerprint the address-attribution capture exists to
  # recognise, so the baseline would defeat itself.
  rows="$(printf '%s\n' \
    'cozy-cilium|cilium-agent-abcde|192.0.2.10|node-a|True|Running|true|eol' \
    'tenant|workload|192.0.2.96|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|workload|192.0.2.96" ]
}

@test "pod_first_ready treats a blank hostNetwork column as NOT hostNetwork" {
  # The API omits spec.hostNetwork (omitempty) when it is false/unset, so
  # kubectl's jsonpath emits an empty string, not the literal "false".
  rows="$(printf '%s\n' 'tenant|workload|192.0.2.97|node-a|True|Running||eol')"

  out="$(printf '%s\n' "$rows" | pod_first_ready node-a)"

  [ "$out" = "tenant|workload|192.0.2.97" ]
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

@test "lb_filter_services drops a Service row cut off mid-record" {
  # A bounded or interrupted list stops mid-record, and the fragment it leaves
  # behind still satisfies a filter that only looks at values: what remains of
  # `192.0.2.53` here is `192.0.2.5`, an address the cluster never had, and the
  # row carrying it would be probed and written up as a Service. The jsonpath
  # emits a fixed seven fields per Service, so a shorter row is a fragment by
  # construction rather than by guess.
  rows="$(printf '%s\n' \
    'tenant|app|LoadBalancer|192.0.2.50|80|31000|Cluster' \
    'tenant|db|LoadBalancer|192.0.2.5')"

  out="$(printf '%s\n' "$rows" | lb_filter_services)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  # `! cmd` is vacuous under cozytest's `set -e`, hence the if/false form used
  # by the sibling filter test above.
  if printf '%s\n' "$out" | grep -q 'tenant|db'; then
    echo "FAIL: lb_filter_services must drop the row cut off mid-record"
    false
  fi
}

@test "pod_filter_affected drops a pod row cut off mid-record" {
  # The same cut over the eight-field pod jsonpath. The fragment ends inside the
  # nodeName column, so the half-parsed `nod` reads as a scheduled pod on a node
  # of that name, and the capture opens a node-nod.txt for it. Note the cut has
  # to land in a column this filter reads: hostNetwork is last now, and a cut
  # inside THAT one leaves every column this filter decides on intact.
  rows="$(printf '%s\n' \
    'tenant|wedged|192.0.2.80|node-b|False|Running||eol' \
    'tenant|cut||nod')"

  out="$(printf '%s\n' "$rows" | pod_filter_affected)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  if printf '%s\n' "$out" | grep -q 'tenant|cut'; then
    echo "FAIL: pod_filter_affected must drop the row cut off mid-record"
    false
  fi
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

@test "lb_first_ready_endpoint drops an endpoint row cut off mid-record" {
  # The third record filter over a bounded list, and the same cut: the fragment
  # has to come first, because this one stops at the first row it accepts. A
  # half-parsed node name would otherwise be the node every endpoint-side
  # capture in the LB section is aimed at.
  eps="$(printf '%s\n' \
    '192.0.2.60|worker-' \
    '192.0.2.61|worker-2|tenant|app-1|true')"

  out="$(printf '%s\n' "$eps" | lb_first_ready_endpoint)"

  [ "$out" = "192.0.2.61|worker-2|tenant|app-1" ]
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

@test "lb_capture_decision returns unknown when no probe ran at all" {
  # Zero outcomes means nothing was ever attempted -- no probe client in the
  # host netns, or an exec that could not run. Folding that into skip put a
  # reachability verdict about the address into the artifact with not one probe
  # behind it, which on a minimal image is every LB in the cluster.
  [ "$(printf '' | lb_capture_decision)" = "unknown" ]
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

# -----------------------------------------------------------------------------
# Behavioural: the reads are bounded, and a bound that fires says so.
#
# These run the script rather than sourcing it, so they need the guard NOT set;
# the runner is invoked as a subprocess, which leaves this file's sourced copy
# untouched. A stub kubectl that never returns stands in for the case the bounds
# exist for -- an apiserver that hangs rather than refuses. The bounds are read
# from the environment so the test does not have to wait out the real ones.
# -----------------------------------------------------------------------------

# Scratch dir with a kubectl that hangs forever, mimicking an apiserver that
# accepts the connection and never answers.
dp_hanging_kubectl_dir() {
  _d=$(mktemp -d)
  mkdir -p "$_d/bin"
  printf '#!/bin/sh\nsleep 300\n' >"$_d/bin/kubectl"
  chmod +x "$_d/bin/kubectl"
  printf '%s' "$_d"
}

@test "a hung cluster-wide pod list is cut off rather than eating the envelope" {
  d=$(dp_hanging_kubectl_dir)
  # Without a per-read bound the first list runs until the CALLER's backstop and
  # the script writes nothing at all, so the outer timeout here stands in for
  # that backstop: reaching it means the read was never bounded.
  PATH="$d/bin:$PATH" COZY_DATAPLANE_LIST_TIMEOUT=2 COZY_DATAPLANE_READ_TIMEOUT=2 \
    timeout 20 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  grep -q 'cut off' "$d/log"
  rm -rf "$d"
}

@test "the script survives a hung apiserver and still reaches its own end" {
  # This stub ANSWERS the cluster-wide pod list and hangs on everything after
  # it, so the walk over affected pods actually runs. A stub that hangs on every
  # call leaves that list empty, skips the whole branch, and exercises two of
  # the nine bounded reads while looking like it covered all of them: an
  # unbounded read added inside the branch would not turn it red.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
sleep 300
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" COZY_DATAPLANE_LIST_TIMEOUT=1 COZY_DATAPLANE_READ_TIMEOUT=1 \
    timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # The closing line prints only if control reached it, which it cannot do while
  # blocked on a read along the way.
  grep -q 'skipping LB-datapath capture' "$d/log"
  # And the per-pod branch was really walked, so the reads inside it are covered
  # rather than assumed.
  grep -q 'looking up the pod matching' "$d/log"
  rm -rf "$d"
}

@test "a read that failed instantly is not reported as a timeout" {
  # The bounds produce 124 or 137 and nothing else, so any other status is
  # kubectl answering -- refused, denied, no such kind. Naming a timeout there
  # puts a cause in the artifact that never happened, and because these reads
  # send stderr to a sink rather than the log, that false cause would be the
  # only thing a reader gets. This is the path the hung-apiserver tests above
  # never reach.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  printf '#!/bin/sh\necho "The connection to the server was refused" >&2\nexit 1\n' >"$d/bin/kubectl"
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 20 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Scoped to this script's own note lines: a bare grep over the whole log would
  # go red on any future line that merely contains the word.
  if grep '^\[capture-dataplane\]' "$d/log" | grep -q 'timeout'; then
    echo "an instant failure was reported as a timeout:"
    cat "$d/log"
    exit 1
  fi
  # It must still say the read produced nothing, and say why, or the silence
  # this suite exists to remove comes back.
  grep -q 'kubectl exited 1' "$d/log"
  # And that kubectl's own words come with it: the exit code alone reads the
  # same whether the message was captured or dropped on the floor.
  grep -q 'refused' "$d/out/capture-notes.txt"
  rm -rf "$d"
}

@test "a kubectl error cannot forge a second verdict line in the log" {
  # log() carries kubectl's stderr now, and under /bin/sh echo would expand a
  # literal backslash-n in it into a real newline, printing a second
  # "[capture-dataplane] ..." line that reads as this script's own finding. The
  # jsonpath in these reads contains {"\n"} and kubectl quotes the expression
  # back on a parse error, so this needs no malice to happen.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  printf '#!/bin/sh\nprintf %%s "unauthorized: x\\\\n[capture-dataplane] no scheduled NotReady pods -- nothing was wrong" >&2\nexit 1\n' >"$d/bin/kubectl"
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 20 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Exactly one note about the pod list, and the forged sentence must not be
  # sitting on a line of its own wearing this script's prefix.
  # The note must exist before its shape means anything: a stub that stopped
  # short of the read would satisfy a purely negative assertion.
  grep -q 'listing pods failed' "$d/log"
  forged=$(grep -c '^\[capture-dataplane\] no scheduled NotReady pods -- nothing was wrong' "$d/log" || true)
  if [ "$forged" -ne 0 ]; then
    echo "kubectl stderr forged a verdict line:"
    cat "$d/log"
    exit 1
  fi
  rm -rf "$d"
}

@test "a node with no matching pod is not reported as a failed read" {
  # The per-node lookups ask for one pod by label on one node, and the ordinary
  # answer is that there is none -- a node without a cilium-agent is the case
  # this capture exists for. That answer must not arrive as a failed read now
  # that a non-zero status carries a note.
  #
  # Reaching this path needs a kubectl that ANSWERS the cluster-wide list with
  # one affected pod; the other tests never get here because their list fails
  # first and leaves nothing to walk.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
# The behaviour under test: client-go's evalArray has no allowMissingKeys
# escape, so indexing [0] into an empty list is a hard error and kubectl exits
# 1, while [*] returns empty and exits 0. Without this arm the stub answers 0
# to both forms and the test cannot see the difference it exists to pin.
case "$*" in
  *'items[0]'*) echo 'error: array index out of bounds: index 0, length 0' >&2; exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 30 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  if grep -q 'looking up the pod matching' "$d/log"; then
    echo "an absent pod was reported as a failed read:"
    grep 'looking up the pod matching' "$d/log"
    exit 1
  fi
  if grep -q 'looking up an ovn-central replica' "$d/log"; then
    echo "an absent ovn-central was reported as a failed read:"
    grep 'looking up an ovn-central replica' "$d/log"
    exit 1
  fi
  # Positive anchors, because everything above is a negative: a stub that
  # stopped short of the walk would satisfy all of it while asserting nothing.
  # These are the lines the path is supposed to produce when it does run.
  grep -q 'no cilium-agent pod found on node node-a' "$d/out/node-node-a.txt"
  grep -q 'no ovn-central pod in' "$d/log"
  rm -rf "$d"
}

@test "the same per-node pod lookup is asked once" {
  # Each repeat costs its own bound against an apiserver that hangs, so a lookup
  # asked three times per pod spends the caller's envelope on re-asking instead
  # of on more pods. The stub records every per-node lookup it is given.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
case "$*" in
  *--field-selector*) echo "$*" >>"$STUB_CALLS" ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  STUB_CALLS="$d/calls" PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >/dev/null 2>&1 || true
  [ -s "$d/calls" ]
  dups=$(sort "$d/calls" | uniq -d | wc -l | tr -d ' ')
  if [ "$dups" -ne 0 ]; then
    echo "the same lookup was issued more than once:"
    sort "$d/calls" | uniq -c | sort -rn | head -5
    exit 1
  fi
  rm -rf "$d"
}

@test "an unanswered pod list is not reported as a cluster with nothing wrong" {
  # The empty-list branch used to print the same line whether the list said
  # "nothing is affected" or never answered, directly under the note saying the
  # read had failed.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  printf '#!/bin/sh\nexit 1\n' >"$d/bin/kubectl"
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 20 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  if grep -q 'no scheduled NotReady pods' "$d/log"; then
    echo "a failed list was reported as nothing being affected:"
    cat "$d/log"
    exit 1
  fi
  grep -q 'whether any pod is affected is unknown' "$d/log"
  rm -rf "$d"
}

@test "an unanswered ovn-central lookup is not reported as a cluster without it" {
  # Mirror of the pod-list case: the note saying the lookup failed used to sit
  # directly above a line asserting there is no ovn-central, which the script
  # never observed.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
case "$*" in
  *'app=ovn-central'*) echo 'Error from server (Forbidden): pods is forbidden' >&2; exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  if grep -q 'no ovn-central pod in' "$d/log"; then
    echo "a failed lookup was reported as the cluster not running ovn-central:"
    cat "$d/log"
    exit 1
  fi
  # The distinctive form: 'is unknown' alone also matches the Service-list note.
  grep -q 'runs ovn-central is unknown' "$d/log"
  rm -rf "$d"
}

@test "an unanswered service list is not reported as a cluster without LoadBalancers" {
  # Third instance of the same rule, and the read this PR's stated purpose names
  # directly: an unanswered list must not be reported as an empty one.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *'get svc'*) echo 'Error from server (Forbidden): services is forbidden' >&2; exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 30 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  if grep -q 'no Service type=LoadBalancer' "$d/log"; then
    echo "a failed service list was reported as a cluster without LoadBalancers:"
    cat "$d/log"
    exit 1
  fi
  grep -q 'whether any LoadBalancer needs capturing is unknown' "$d/log"
  rm -rf "$d"
}

@test "a partially answered service list is not captured as a complete one" {
  # A list can fail after emitting rows: a bound firing mid-stream leaves output
  # on stdout and 124 in $?. Gating the note on an empty result would let that
  # partial inventory be captured with nothing saying it was partial.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *'get svc'*)
    echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'
    exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 30 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # It must both proceed with what it got and say the list did not finish.
  grep -q 'probing 1 LoadBalancer service' "$d/log"
  if ! grep -q 'listing services' "$d/log"; then
    echo "a partial service list was captured with no note that it failed:"
    cat "$d/log"
    exit 1
  fi
  rm -rf "$d"
}

@test "a partially answered endpointslice read is not called an unread one" {
  # The same shape as the service list above, at the two EndpointSlice reads: a
  # bound firing mid-stream leaves rows on stdout and 124 in $?. The backend is
  # then parsed from what did arrive and printed by name, so a note asserting
  # what the artifact will say -- rather than what is missing from it -- lands
  # two lines from a block that contradicts it. The sibling notes in this file
  # avoid that by naming the omission, and pod_on_node names no consequence at
  # all because it serves callers that do different things with the answer.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    # Complete rows AND a bound's status: the read answered in part.
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 124 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # The rows that did arrive are used, so the artifact names a backend ...
  grep -q 'ip=10.0.0.1' "$d/out/lb-tenant-web.txt"
  # ... and the cut is on the record beside it.
  grep -q 'was cut off' "$d/out/capture-notes.txt"
  # Neither note may claim the value went in as unknown while the block above
  # names it. Scoped to each note's own line: 'unknown' appears legitimately
  # elsewhere in this file, including on the announcer line of this very block.
  if grep 'endpointslices of tenant/web' "$d/out/capture-notes.txt" | grep -q 'unknown'; then
    echo "the note calls the backend unknown while the capture names it:"
    cat "$d/out/lb-tenant-web.txt"
    grep 'endpointslices of tenant/web' "$d/out/capture-notes.txt"
    exit 1
  fi
  if grep 'target port of tenant/web' "$d/out/capture-notes.txt" | grep -q 'unknown'; then
    echo "the note calls the target port unknown while the capture names one:"
    cat "$d/out/lb-tenant-web.txt"
    grep 'target port of tenant/web' "$d/out/capture-notes.txt"
    exit 1
  fi
  rm -rf "$d"
}

@test "a note does not claim a step was skipped when the step runs" {
  # A lookup can fail after naming what it was asked for. Both of these notes
  # used to state the consequence unconditionally, so the log said the OVN dump
  # was skipped and the announcer unresolved while both were being produced from
  # the partial answer.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
case "$*" in
  *'app=ovn-central'*) echo 'ovn-central-0'; echo 'boom' >&2; exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # It named a replica, so the dump ran; the note must not say it was skipped.
  [ -f "$d/out/ovn-lflows.txt" ]
  # Greps the wording the skip branches actually use, not a phrase that only
  # existed while this was being written: a dead string can never appear, so the
  # assertion could never fire.
  if grep -q 'skipping OVN logical-flow dump' "$d/log"; then
    echo "the log says the dump was skipped while the dump ran:"
    cat "$d/log"
    exit 1
  fi
  rm -rf "$d"
}

@test "a partially answered pod list is not called unanswered" {
  # The list can fail after emitting rows. If everything it named is Ready the
  # affected set is empty and the status is non-zero, which is the same pair of
  # conditions a total failure produces -- so this branch must not assert what
  # the read did, only what is left unknown.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|healthy|10.0.0.9|node-a|True|Running||eol'; exit 1 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 30 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Both assertions are positive because the wording they pin is the whole
  # point: a branch that described the read instead would not emit either line.
  grep -q 'whatever it did not name is missing' "$d/log"
  grep -q 'whether any pod is affected is unknown' "$d/log"
  rm -rf "$d"
}

@test "a cut-off node lookup is not written into the artifact as an absent pod" {
  # The artifact is what a reader opens out of the cozyreport bundle, and it is
  # where "(no cilium-agent pod found)" used to be written for a lookup that
  # never answered. Bounding that read is what made this reachable: unbounded,
  # the script hung until the caller's backstop killed it and no file was
  # written at all, so the bound turned "no artifact" into "an artifact with a
  # claim in it".
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
case "$*" in
  *--field-selector*spec.nodeName*) sleep 300 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" COZY_DATAPLANE_LIST_TIMEOUT=1 COZY_DATAPLANE_READ_TIMEOUT=1 \
    timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/node-node-a.txt" ]
  if grep -q 'no cilium-agent pod found' "$d/out/node-node-a.txt"; then
    echo "a cut-off lookup was recorded as an absent pod:"
    cat "$d/out/node-node-a.txt"
    exit 1
  fi
  grep -q 'could not determine whether a cilium-agent runs' "$d/out/node-node-a.txt"
  rm -rf "$d"
}

@test "the host netns capture names every address, not only ovn0" {
  # The reason this commit exists: when a podIP turns out to be a local address
  # of the host netns, `ip addr show ovn0` cannot say which interface holds it.
  # Nothing else in this suite reaches these two captures -- deleting both left
  # every other test green -- so without this the collector could quietly lose
  # the evidence the change was written to collect.
  #
  # Asserted on the kubectl argv, and the section headers are the secondary
  # check rather than the primary one. A header is an `echo` the script emits
  # before the exec, so dropping the exec and keeping the label leaves an empty
  # section behind and every header grep green -- either capture alone, or both
  # together, and the whole suite stays green. The argv also pins WHERE the
  # capture runs -- `-c cni-server`, the container that shares the host netns --
  # which is half the property and one a header cannot carry at all.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *'ip -o addr show'*|*'ip route show table local'*) echo "$*" >>"$STUB_CALLS" ;;
esac
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  STUB_CALLS="$d/calls" PATH="$d/bin:$PATH" timeout 120 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/node-node-a.txt" ]
  # Positive control: the host-netns section ran, so a missing address capture
  # below is that capture's absence and not the whole block being skipped.
  grep -q 'host netns: ip rule' "$d/out/node-node-a.txt"
  # The commands the collector actually issued into the host netns ...
  grep -q -- '-c cni-server -- ip -o addr show' "$d/calls"
  grep -q -- '-c cni-server -- ip route show table local' "$d/calls"
  # ... and the labels a reader of the artifact finds them under.
  grep -q 'ip -o addr show, all interfaces' "$d/out/node-node-a.txt"
  grep -q 'ip route show table local' "$d/out/node-node-a.txt"
  rm -rf "$d"
}

@test "a healthy pod on the node is captured beside the affected one" {
  # The baseline is the point of the reference leg, and it only pays off in the
  # SAME file as the affected pod: a reader comparing a suspicious route against
  # a normal one should not have to go and find the normal one.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'
      echo 'cozy-cilium|cilium-xyz|10.0.0.5|node-a|True|Running|true|eol'
      echo 'tenant|healthy|10.0.0.9|node-a|True|Running||eol'
      exit 0 ;;
  esac
done
case "$*" in
  # A cni-server on the node, so the IP-specific legs actually run. Without it
  # they are skipped and the baseline would be a header with no comparison in
  # it -- which is the whole thing this leg is for.
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/pod-tenant-test-wedged.txt" ]
  grep -q 'POD tenant-test/wedged' "$d/out/pod-tenant-test-wedged.txt"
  grep -q 'REFERENCE (Ready)' "$d/out/pod-tenant-test-wedged.txt"
  grep -q 'POD tenant/healthy' "$d/out/pod-tenant-test-wedged.txt"
  # And the baseline carries the evidence it exists to provide: the route and
  # conntrack for the HEALTHY pod's address, not just a section header. A
  # baseline without these compares nothing.
  grep -q 'ip route get 10.0.0.9' "$d/out/pod-tenant-test-wedged.txt"
  grep -q 'kernel conntrack for 10.0.0.9' "$d/out/pod-tenant-test-wedged.txt"
  # The hostNetwork pod sorts first in the snapshot and must not have been
  # taken: its podIP is the node's own address, the fingerprint the baseline
  # exists to rule out.
  if grep -q 'POD cozy-cilium/cilium-xyz' "$d/out/pod-tenant-test-wedged.txt"; then
    echo "a hostNetwork pod was taken as the baseline:"
    cat "$d/out/pod-tenant-test-wedged.txt"
    exit 1
  fi
  rm -rf "$d"
}

@test "the baseline is captured at route-only scope, not the full one" {
  # The baseline exists to show a normal route and conntrack beside a suspicious
  # one. Cilium CT, the OVN port binding and the OVS ovn-installed flag describe
  # how a pod was PROGRAMMED, and a pod chosen for being healthy is correctly
  # programmed by definition -- they compare nothing and cost 105s of exec bound
  # on the leg most likely to be cut. Dropping them is what keeps one affected
  # pod on one node inside the 600s backstop, so the scope is load-bearing and
  # not a preference.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'
      echo 'tenant|healthy|10.0.0.9|node-a|True|Running||eol'
      exit 0 ;;
  esac
done
case "$*" in
  # Everything the full scope needs is present, so its absence from the
  # baseline is a decision and not a missing dependency.
  *'k8s-app=cilium'*) echo 'cilium-xyz'; exit 0 ;;
  *'app=ovs'*) echo 'ovs-xyz'; exit 0 ;;
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
  *'app=ovn-central'*) echo 'ovn-central-0'; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 120 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  f="$d/out/pod-tenant-test-wedged.txt"
  [ -f "$f" ]
  # Positive control: the affected pod gets the full scope, so the blocks exist
  # and their absence below is scope and not a stub that answered nothing.
  awk '/REFERENCE \(Ready\)/ { exit } { print }' "$f" | grep -q 'cilium-dbg bpf ct list global'
  awk '/REFERENCE \(Ready\)/ { exit } { print }' "$f" | grep -q 'OVN Port_Binding'
  # The baseline keeps what it is read for ...
  awk '/REFERENCE \(Ready\)/,0' "$f" | grep -q 'ip route get 10.0.0.9'
  awk '/REFERENCE \(Ready\)/,0' "$f" | grep -q 'kernel conntrack for 10.0.0.9'
  # ... says in the artifact that the rest was a decision ...
  awk '/REFERENCE \(Ready\)/,0' "$f" | grep -q 'scope: route and conntrack only'
  awk '/REFERENCE \(Ready\)/,0' "$f" | grep -q 'absence below is a decision, not a lookup'
  # ... and drops what compares nothing. Matched against SECTION HEADERS only:
  # the marker line above names the very blocks it skips, so a bare substring
  # search finds the explanation and calls it the block.
  for section in 'cilium-dbg bpf ct list global' 'OVN Port_Binding' 'ovn-installed' 'Ready conditions'; do
    if awk '/REFERENCE \(Ready\)/,0' "$f" | grep '^=== ' | grep -q "$section"; then
      echo "the baseline was captured at full scope: section '$section' present after REFERENCE"
      awk '/REFERENCE \(Ready\)/,0' "$f" | grep '^=== '
      exit 1
    fi
  done
  rm -rf "$d"
}

@test "the baseline column is read from the cluster-wide snapshot" {
  # The seventh column is what lets the baseline be chosen without a second
  # read. Drop it from the jsonpath and every row is a six-field fragment that
  # the NF guards reject -- no affected pods, no baseline, nothing captured. The
  # stub answers only a request that asks for hostNetwork, so a jsonpath that
  # stopped asking produces an empty list and this test goes red.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *'spec.hostNetwork'*)
    echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'
    echo 'tenant|healthy|10.0.0.9|node-a|True|Running||eol'
    exit 0 ;;
esac
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/pod-tenant-test-wedged.txt" ]
  grep -q 'POD tenant/healthy' "$d/out/pod-tenant-test-wedged.txt"
  rm -rf "$d"
}

@test "two affected pods on one node share a single baseline capture" {
  # The baseline depends on the node alone, and several affected pods on one
  # node is what a wedged datapath looks like rather than an edge case. What
  # must not repeat is the CAPTURE -- a second full per-pod capture of the same
  # healthy pod, against a backstop the pod leg already strains. Counted by the
  # baseline pod's own conditions read, which happens once per capture.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  # The baseline runs at route-only scope, so its conditions read is gone --
  # count the route exec, which is the read that scope does make.
  *'ip route get 10.0.0.9'*) echo ref-capture >>"$STUB_CALLS" ;;
esac
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged-a|10.0.0.1|node-a|False|Running||eol'
      echo 'tenant-test|wedged-b|10.0.0.2|node-a|False|Running||eol'
      echo 'tenant|healthy|10.0.0.9|node-a|True|Running||eol'
      exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  STUB_CALLS="$d/calls" PATH="$d/bin:$PATH" timeout 120 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Both pods carry the baseline: the saving must not cost one of them its copy.
  grep -q 'POD tenant/healthy' "$d/out/pod-tenant-test-wedged-a.txt"
  grep -q 'POD tenant/healthy' "$d/out/pod-tenant-test-wedged-b.txt"
  n=$(grep -c ref-capture "$d/calls")
  if [ "$n" -ne 1 ]; then
    echo "the baseline was captured $n times for one node instead of once"
    exit 1
  fi
  rm -rf "$d"
}

@test "the pod list jsonpath emits its columns in the order its readers index" {
  # Every runtime test below answers the pod list from a stub that writes rows
  # by hand, so none of them can observe the jsonpath's column ORDER: swap two
  # fields in it and the suite stays green while the filters go on reading
  # position 6 as phase and 7 as hostNetwork. Measured -- swapping
  # `.status.phase` with `.spec.hostNetwork` passed 72 of 72.
  #
  # Against a cluster that swap makes pod_filter_affected compare a boolean
  # against Succeeded/Failed and pod_first_ready compare a phase against
  # "false", so the collector selects the wrong pods and says nothing. The
  # positions are a contract between one jsonpath and three readers -- both awk
  # filters and the `read` in the capture loop -- and no stub can hold it.
  #
  # Asserted as ORDER rather than as an exact string, so reformatting the
  # jsonpath is free and reordering it is not.
  #
  # Each field carries its closing brace, and that is load-bearing rather than
  # tidy: `.metadata.name` is a prefix of `.metadata.namespace`, so searching
  # for it bare finds the namespace field and reports the two columns at one
  # offset. The brace is what makes each search hit its own column.
  line=$(grep -F '{range .items[*]}' "$SCRIPT" | grep -F 'spec.hostNetwork')
  [ -n "$line" ]
  prev=0
  for f in '.metadata.namespace}' '.metadata.name}' '.status.podIP}' '.spec.nodeName}' \
           'type=="Ready")].status}' '.status.phase}' '.spec.hostNetwork}' '|eol"}'; do
    cur=$(printf '%s\n' "$line" | awk -v s="$f" '{ print index($0, s) }')
    # index() returns 0 for absent, so this one comparison catches a field that
    # moved and a field that vanished.
    if [ "$cur" -le "$prev" ]; then
      echo "the pod list jsonpath no longer emits its columns in the order its readers assume"
      echo "field '$f' is at offset $cur, expected after $prev"
      echo "line: $line"
      exit 1
    fi
    prev=$cur
  done
}

@test "an affected pod gets the baseline from its own node" {
  # The complement of the test above, and the half a shared cache can break
  # without breaking that one: the cache is keyed by node, and a key that
  # stopped telling nodes apart would serve every node the first node's
  # answer. The test above counts captures and a single shared answer is
  # exactly one capture, so it would stay green.
  #
  # What the reader would then get is a route and a conntrack table read on
  # one kernel, filed under a heading naming a different node, with nothing
  # in the artifact saying so -- a capture reporting what it never observed,
  # which is the failure this collector exists to rule out. So the assertion
  # is on WHICH healthy pod each file carries, per file, not on how many
  # captures ran.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged-a|10.0.0.1|node-a|False|Running||eol'
      echo 'tenant-test|wedged-b|10.0.0.2|node-b|False|Running||eol'
      echo 'tenant|healthy-a|10.0.0.9|node-a|True|Running||eol'
      echo 'tenant|healthy-b|10.0.0.8|node-b|True|Running||eol'
      exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 120 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  fa="$d/out/pod-tenant-test-wedged-a.txt"
  fb="$d/out/pod-tenant-test-wedged-b.txt"
  [ -f "$fa" ]
  [ -f "$fb" ]
  # Each file names its own node's healthy pod ...
  grep -q 'POD tenant/healthy-a' "$fa"
  grep -q 'POD tenant/healthy-b' "$fb"
  # ... and carries that pod's address in the evidence, not just in a heading.
  grep -q 'ip route get 10.0.0.9' "$fa"
  grep -q 'ip route get 10.0.0.8' "$fb"
  # ... and neither carries the other node's, which is what a node-agnostic
  # cache key produces: both files served whichever node was reached first.
  if grep -q 'POD tenant/healthy-b' "$fa" || grep -q 'POD tenant/healthy-a' "$fb"; then
    echo "a baseline crossed nodes:"
    grep -H 'POD tenant/healthy' "$fa" "$fb"
    exit 1
  fi
  rm -rf "$d"
}

@test "a partially answered pod list is not called unanswered by the baseline" {
  # The absence branch fires on "read failed AND no eligible pod", which is what
  # a partial answer produces: rows arrive, none of them can serve -- the
  # affected pod is not Ready, the hostNetwork pod is excluded -- and the list
  # then dies mid-stream. Saying it never answered would assert what the run did
  # not observe. It did not FINISH, which is true either way.
  #
  # With one snapshot this is the only way to reach that branch at all: a list
  # that answered nothing leaves no affected pods, so no pod file is written.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'
      echo 'cozy-cilium|cilium-xyz|10.0.0.5|node-a|True|Running|true|eol'
      exit 1 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/pod-tenant-test-wedged.txt" ]
  grep -q 'REFERENCE (Ready) pod on node=node-a: unknown' "$d/out/pod-tenant-test-wedged.txt"
  grep -q 'the pod list did not finish' "$d/out/pod-tenant-test-wedged.txt"
  rm -rf "$d"
}

@test "a node whose snapshot holds no eligible pod says none found" {
  # The other side of the same branch, and the reason it needs two wordings: a
  # complete list with nothing eligible on the node is an answer, not a gap.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'
      echo 'cozy-cilium|cilium-xyz|10.0.0.5|node-a|True|Running|true|eol'
      exit 0 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  grep -q 'none found -- no baseline available' "$d/out/pod-tenant-test-wedged.txt"
  if grep -q 'did not finish' "$d/out/pod-tenant-test-wedged.txt"; then
    echo "a complete list was reported as unfinished:"
    cat "$d/out/pod-tenant-test-wedged.txt"
    exit 1
  fi
  rm -rf "$d"
}

@test "an affected pod is never offered as a healthy baseline" {
  # Now true by construction rather than by exclusion: candidates come from the
  # same rows the affected set came from, and one row cannot be both Ready and
  # not-Ready. The test stays because the property is what matters, not the
  # mechanism -- a future change that reads the node separately would reopen it.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods)
      echo 'tenant-test|wedged-a|10.0.0.1|node-a|False|Running||eol'
      echo 'tenant-test|wedged-b|10.0.0.2|node-a|False|Running||eol'
      exit 0 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 120 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/pod-tenant-test-wedged-a.txt" ]
  [ -f "$d/out/pod-tenant-test-wedged-b.txt" ]
  for f in "$d"/out/pod-tenant-test-wedged-a.txt "$d"/out/pod-tenant-test-wedged-b.txt; do
    if grep 'REFERENCE' "$f" | grep -q 'tenant-test/wedged'; then
      echo "a pod under investigation was used as the healthy baseline in $(basename "$f"):"
      grep '^# POD' "$f"
      exit 1
    fi
  done
  grep -q 'none found -- no baseline available' "$d/out/pod-tenant-test-wedged-b.txt"
  rm -rf "$d"
}

@test "a memo hit reports the same answer as the miss that filled it" {
  # The status has to survive the memo. Every caller reads pod_on_node through a
  # command substitution, so a status kept in a variable dies with that subshell
  # and every hit then looks like a lookup that never answered -- the same node
  # getting <none> from the miss and <unknown> from the hit, in one bundle.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  # Answers, and answers "there is no such pod" -- status 0, empty output.
  *'app=ovs'*) exit 0 ;;
  *'k8s-app=cilium'*) echo 'cilium-xyz'; exit 0 ;;
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
  # The LB must probe UNREACHABLE, or the heavy capture is skipped and
  # capture_lb_node -- the only consumer that reads this lookup from a memo
  # hit -- never runs. Without this the test cannot observe the difference it
  # exists to pin, whatever the code does.
  *'nc -z'*) echo fail; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # A lookup that answered "none" must read as none wherever it is consumed.
  # capture_lb_node is the only consumer that reads this lookup from a memo
  # hit, and it runs only on the heavy-capture path. Pin that it ran, or stub
  # drift makes both assertions below vacuous without a word.
  grep -q 'ENDPOINT node=node-a' "$d/out/lb-tenant-web.txt"
  if grep -rq 'ovs=<unknown>' "$d/out" 2>/dev/null; then
    echo "a lookup that answered was reported as unanswered after a memo hit:"
    grep -r 'ovs=' "$d/out" 2>/dev/null
    exit 1
  fi
  # And the run must not leak shell diagnostics into the log.
  if grep -q 'unbound variable\|parameter not set' "$d/log"; then
    echo "the run leaked shell diagnostics:"
    grep -n 'unbound variable\|parameter not set' "$d/log" | head -3
    exit 1
  fi
  rm -rf "$d"
}

@test "an instant lookup failure is re-asked rather than cached" {
  # Caching a refusal makes one transient permanent for the run. Only a cutoff
  # is worth not repeating, because repeating that one spends another full
  # bound. The stub refuses the first ovs lookup and answers the rest, so a
  # cached failure would show as a second lookup never being issued.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) echo 'tenant-test|wedged|10.0.0.1|node-a|False|Running||eol'; exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  *'app=ovs'*)
    echo "$*" >>"$STUB_CALLS"
    echo 'Error from server (Forbidden): pods is forbidden' >&2
    exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  STUB_CALLS="$d/calls" PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # More than one ovs lookup means the refusal was not cached.
  n=$(wc -l <"$d/calls" | tr -d ' ')
  if [ "$n" -lt 2 ]; then
    echo "an instant failure was cached instead of re-asked (ovs lookups: $n)"
    cat "$d/calls"
    exit 1
  fi
  rm -rf "$d"
}

@test "the reasons a capture is incomplete ship inside the capture" {
  # A hung apiserver leaves dataplane/ with nothing collected. The lines saying
  # why must be there too, or a reader holding only the uploaded report cannot
  # tell a capture that found nothing from one that never ran -- the contract
  # docs/agents/e2e-testing.md states for the sibling collectors, which this
  # script's notes are modelled on.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  printf '#!/bin/sh\nsleep 300\n' >"$d/bin/kubectl"
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" COZY_DATAPLANE_LIST_TIMEOUT=1 COZY_DATAPLANE_READ_TIMEOUT=1 \
    timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/capture-notes.txt" ]
  grep -q 'cut off' "$d/out/capture-notes.txt"
  grep -q 'whether any pod is affected is unknown' "$d/out/capture-notes.txt"
  rm -rf "$d"
}

@test "dp_cutoff_desc tells a SIGKILL from a deadline and both from no bound" {
  # Pure helper, reached through the sourcing guard like the lb_* ones above.
  # Its three branches carry the difference between "the bound fired", "something
  # killed the read and 137 cannot say which", and "this read had no ceiling at
  # all" -- the last being what an absent `timeout` produces.
  deadline=$(dp_cutoff_desc 124 20 "timeout -k 2 20")
  case "$deadline" in
    *"its own 20s timeout"*) : ;;
    *) echo "124 did not name the deadline: $deadline"; exit 1 ;;
  esac

  killed=$(dp_cutoff_desc 137 20 "timeout -k 2 20")
  case "$killed" in
    *SIGKILL*) : ;;
    *) echo "137 did not name a SIGKILL: $killed"; exit 1 ;;
  esac

  unbounded=$(dp_cutoff_desc 137 20 "")
  case "$unbounded" in
    *unbounded*) : ;;
    *) echo "an empty bound was not described as unbounded: $unbounded"; exit 1 ;;
  esac
}

@test "sourcing the script for its helpers leaves no temp files behind" {
  # Everything above the sourcing guard runs in every unit test that sources
  # this file, so a scratch file created up there is one leaked per test, and
  # the two early exits would strand one on any host without kubectl.
  d=$(mktemp -d)
  ( TMPDIR="$d"; export TMPDIR; E2E_CAPTURE_DATAPLANE_LIB=1 . "$SCRIPT" ) >/dev/null 2>&1
  left=$(ls -1 "$d" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$left" -ne 0 ]; then
    echo "sourcing left $left file(s) behind:"
    ls -1 "$d"
    exit 1
  fi
  rm -rf "$d"
}

@test "an LB whose probe never ran is not recorded as reachable" {
  # Fifth site of the same class, and the last one the bounds made reachable.
  # host_http_probe resolves a cni-server first; a cut-off lookup used to emit
  # no probe outcome at all, which the decision helper read as "nothing failed"
  # and the artifact stamped as reachable -- a verdict about an address the
  # script never touched. At merge base the script never got this far: the
  # unbounded lookup hung until the caller's backstop killed it.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) sleep 300 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" COZY_DATAPLANE_LIST_TIMEOUT=1 COZY_DATAPLANE_READ_TIMEOUT=1 \
    timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/lb-tenant-web.txt" ]
  # The specific claim, not the bare word: the honest line says "whether the LB
  # is reachable ... is unknown" and contains it too.
  if grep -q -- '-- reachable, skipped' "$d/out/lb-tenant-web.txt"; then
    echo "an LB that was never probed was recorded as reachable:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  grep -q 'is unknown' "$d/out/lb-tenant-web.txt"
  # And this is the branch that MAY name the lookup, because here it genuinely
  # did not answer. Pinning it positively is what keeps the two negative pins
  # in the empty-outcome tests below from going vacuous: the phrase has to stay
  # reachable somewhere for its absence elsewhere to mean anything.
  grep -q 'the cni-server lookup did not answer' "$d/out/lb-tenant-web.txt"
  rm -rf "$d"
}

@test "an unread endpointslice is written as unknown rather than as none" {
  # The two EndpointSlice reads discarded stderr and were interpreted by
  # emptiness alone, so a refused read produced the same `<none>` a Service with
  # no endpoints does. A reader working from the artifact gets the assertion and
  # never sees that nothing was read.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo 'Error from server (Forbidden): endpointslices is forbidden' >&2; exit 1 ;;
  esac
done
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/lb-tenant-web.txt" ]
  grep -q 'ip=<unknown>' "$d/out/lb-tenant-web.txt"
  grep -q 'targetPort=<unknown>' "$d/out/lb-tenant-web.txt"
  if grep -q 'ip=<none>' "$d/out/lb-tenant-web.txt"; then
    echo "a read that never answered was recorded as an absent endpoint:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  # And the reason ships beside the capture, not only in the job log.
  grep -q 'endpointslices' "$d/out/capture-notes.txt"
  rm -rf "$d"
}

@test "a Service with no endpointslices at all is not called an unread one" {
  # The other direction, and a landmine the status threading arms: client-go's
  # evalArray has no allowMissingKeys escape, so a jsonpath indexing [0] into an
  # empty list is a hard error and kubectl exits 1, while [*] yields nothing and
  # exits 0. With the status now reported, an ordinary Service without endpoints
  # would start reporting as a failed read unless the read asks with [*].
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
  esac
done
# The endpointslice reads fall through to here: an empty list, answered.
case "$*" in
  *'items[0]'*) echo 'error: array index out of bounds: index 0, length 0' >&2; exit 1 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  [ -f "$d/out/lb-tenant-web.txt" ]
  grep -q 'targetPort=<none>' "$d/out/lb-tenant-web.txt"
  if grep -q 'targetPort=<unknown>' "$d/out/lb-tenant-web.txt"; then
    echo "an empty endpointslice list was recorded as a read that never answered:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  if grep -q 'endpointslices' "$d/out/capture-notes.txt"; then
    echo "an empty endpointslice list was noted as a failed read:"
    cat "$d/out/capture-notes.txt"
    exit 1
  fi
  rm -rf "$d"
}

@test "an unknown announcer does not point the tcpdump at a pending pod" {
  # pod_on_node builds `--field-selector spec.nodeName=$3`, so an empty node
  # asks for pods whose nodeName is EMPTY -- the unscheduled ones. The announcer
  # is unknown exactly when MetalLB is misbehaving, which is when this leg
  # matters, and an unguarded lookup then hands the first pending kube-ovn-cni
  # pod to a tcpdump labelled ANNOUNCER. The sibling uses of the announcer node
  # in this same block are all guarded; this one was not.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  *--field-selector*) echo "$*" >>"$STUB_CALLS" ;;
esac
case "$*" in
  # Answers every per-node cni lookup, INCLUDING one made with an empty node --
  # which is what an apiserver holding a pending pod returns for that selector.
  # A stub that answered nothing there would hide the bug behind an empty result.
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
  # The LB must probe UNREACHABLE or the heavy capture that carries the tcpdumps
  # never runs and the test asserts nothing.
  *'nc -z'*) echo fail; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  STUB_CALLS="$d/calls" PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Positive controls: the heavy capture ran, and it ran with no announcer --
  # no speaker logs were produced, so lb_announcer_node reported nothing.
  grep -q 'UNREACHABLE' "$d/log"
  grep -q 'announcer node: <unknown>' "$d/out/lb-tenant-web.txt"
  # The lookup itself must not go out with an empty node. Trailing space, so this
  # does not also match the endpoint node's own `spec.nodeName=node-a` lookup.
  if grep -q -- '--field-selector spec.nodeName= ' "$d/calls"; then
    echo "the announcer lookup was issued with an empty node selector:"
    grep -- 'spec.nodeName=' "$d/calls"
    exit 1
  fi
  # And no ANNOUNCER tcpdump artifact may exist for a node nobody identified.
  if [ -f "$d/out/lb-tenant-web.tcpdump-announcer.txt" ]; then
    echo "an announcer tcpdump ran with no announcer node:"
    cat "$d/out/lb-tenant-web.tcpdump-announcer.txt"
    exit 1
  fi
  # The skip is stated rather than silent: absence of a block is not a reason.
  grep -q 'announcer node unknown' "$d/out/lb-tenant-web.txt"
  rm -rf "$d"
}

@test "an unknown endpoint node does not point the tcpdump at a pending pod" {
  # The endpoint half of the same defect, on the same block: an LB whose
  # announcer IS known but whose Service has no ready endpoint reaches the heavy
  # capture with an empty endpoint node, and the two lookups on that side --
  # the cni-server pod and the backend's OVS interface -- both go out with
  # `spec.nodeName=`, which matches unscheduled pods. No ready endpoint is
  # exactly the shape of an LB outage, so this is not a rare corner of the leg.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    # No ready endpoint: the Service is enumerated, its backend is not.
    endpointslices) exit 0 ;;
    # The announcer, so the probe has somewhere to run from and the capture is
    # reached with only the ENDPOINT side unknown.
    logs) echo '{"event":"serviceAnnounced","ips":["192.0.2.10"],"node":"node-a"}'; exit 0 ;;
  esac
done
case "$*" in
  *'component=speaker'*) echo 'speaker-0|node-a'; exit 0 ;;
esac
case "$*" in
  *--field-selector*) echo "$*" >>"$STUB_CALLS" ;;
esac
case "$*" in
  # Answers every cni lookup, the one with an empty node included -- what an
  # apiserver holding a pending pod returns for that selector.
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
  *'nc -z'*) echo fail; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  STUB_CALLS="$d/calls" PATH="$d/bin:$PATH" timeout 90 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Positive controls: the heavy capture ran, the announcer side was identified
  # and did run, so what follows is about the endpoint side alone.
  grep -q 'UNREACHABLE' "$d/log"
  [ -f "$d/out/lb-tenant-web.tcpdump-announcer.txt" ]
  if grep -q -- '--field-selector spec.nodeName= ' "$d/calls"; then
    echo "an endpoint-side lookup was issued with an empty node selector:"
    grep -- 'spec.nodeName=' "$d/calls"
    exit 1
  fi
  if [ -f "$d/out/lb-tenant-web.tcpdump-endpoint.txt" ]; then
    echo "an endpoint tcpdump ran with no endpoint node:"
    cat "$d/out/lb-tenant-web.tcpdump-endpoint.txt"
    exit 1
  fi
  grep -q 'endpoint node unknown' "$d/out/lb-tenant-web.txt"
  rm -rf "$d"
}

@test "an LB whose probe answers is still recorded as reachable" {
  # Positive control for the three unprobed cases below and for the cut-off one
  # further up: each of those asserts the ABSENCE of the reachable line, and a
  # change that routed every LB to unknown would satisfy all four while
  # destroying the distinction they exist to protect. A probe that answers must
  # still produce the reachable line and must still skip the heavy capture.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
  *'nc -z'*) echo ok; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  grep -q -- '-- reachable, skipped' "$d/out/lb-tenant-web.txt"
  if [ -f "$d/out/lb-tenant-web.tcpdump-endpoint.txt" ]; then
    echo "a reachable LB got the heavy capture anyway:"
    ls -1 "$d/out"
    exit 1
  fi
  rm -rf "$d"
}

@test "an LB with no probe client in the host netns is not recorded as reachable" {
  # The headline unprobed input: the exec runs, finds no nc/curl/wget, prints
  # nothing and exits 0. Zero outcomes used to read as "nothing failed", so a
  # minimal e2e image stamped every LB reachable and skipped the whole heavy
  # capture -- which reads as a healthy datapath.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
  # The probe exec itself: no client in the image, so it emits nothing and
  # succeeds. A stub that answered ok or fail here would never reach the branch.
  *'nc -z'*) exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  # Positive control: the LB was enumerated and the probe leg was reached.
  grep -q 'probing 1 LoadBalancer service' "$d/log"
  [ -f "$d/out/lb-tenant-web.txt" ]
  if grep -q -- '-- reachable, skipped' "$d/out/lb-tenant-web.txt"; then
    echo "an LB no probe could be run against was recorded as reachable:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  # The reason has to survive contact with what actually happened: the lookup
  # answered and named a pod, and the exec ran -- it found nothing to run. A
  # line blaming the lookup would be a cause this script never observed, the
  # same rule dp_read_outcome states for the reads.
  grep -q 'no probe outcome from node-a' "$d/out/lb-tenant-web.txt"
  if grep -q 'the cni-server lookup did not answer' "$d/out/lb-tenant-web.txt"; then
    echo "the artifact blames a lookup that answered:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  rm -rf "$d"
}

@test "an LB whose probe node runs no cni-server is not blamed on the lookup" {
  # The fifth zero-probe input, and the one missing from the enumeration until
  # now: the lookup ANSWERS, and its answer is that the node has no
  # kube-ovn-cni pod. host_http_probe returns without emitting an outcome, so
  # this lands in the same empty-outcome branch as a missing probe client --
  # but "the lookup did not answer" is false here in the most direct way.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  # Answers, and the answer is "no such pod": status 0, empty output.
  *'app=kube-ovn-cni'*) exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  grep -q 'probing 1 LoadBalancer service' "$d/log"
  [ -f "$d/out/lb-tenant-web.txt" ]
  if grep -q -- '-- reachable, skipped' "$d/out/lb-tenant-web.txt"; then
    echo "an LB with no cni-server to probe from was recorded as reachable:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  grep -q 'no probe outcome from node-a' "$d/out/lb-tenant-web.txt"
  if grep -q 'the cni-server lookup did not answer' "$d/out/lb-tenant-web.txt"; then
    echo "the artifact blames a lookup that answered 'no such pod':"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  rm -rf "$d"
}

@test "an LB with no node to probe from is not recorded as reachable" {
  # Second unprobed input: neither the announcer nor the endpoint node resolved,
  # so the probe block never ran. The decision defaulted to skip, and the
  # artifact asserted reachability of an address the script never touched.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10|80|30080|Cluster'; exit 0 ;;
  esac
done
# Everything else answers empty: no endpointslices, so no endpoint node, and no
# metallb speakers, so no announcer.
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  grep -q 'probing 1 LoadBalancer service' "$d/log"
  [ -f "$d/out/lb-tenant-web.txt" ]
  if grep -q -- '-- reachable, skipped' "$d/out/lb-tenant-web.txt"; then
    echo "an LB with nowhere to probe from was recorded as reachable:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  grep -q 'nowhere to probe from' "$d/out/lb-tenant-web.txt"
  rm -rf "$d"
}

@test "an LB whose Service names no port is not recorded as reachable" {
  # Third unprobed input: the port column is empty, the probe block is gated on
  # a non-zero port, and the same default carried a reachability verdict out.
  d=$(mktemp -d)
  mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    pods) exit 0 ;;
    svc) echo 'tenant|web|LoadBalancer|192.0.2.10||30080|Cluster'; exit 0 ;;
    endpointslices) echo '10.0.0.1|node-a|tenant-test|wedged|true'; exit 0 ;;
  esac
done
case "$*" in
  *'app=kube-ovn-cni'*) echo 'cni-abc'; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  PATH="$d/bin:$PATH" timeout 60 "$SCRIPT" "$d/out" >"$d/log" 2>&1 || true
  grep -q 'probing 1 LoadBalancer service' "$d/log"
  [ -f "$d/out/lb-tenant-web.txt" ]
  if grep -q -- '-- reachable, skipped' "$d/out/lb-tenant-web.txt"; then
    echo "an LB with no port to probe was recorded as reachable:"
    cat "$d/out/lb-tenant-web.txt"
    exit 1
  fi
  grep -q 'no port to probe' "$d/out/lb-tenant-web.txt"
  rm -rf "$d"
}

@test "lb_capture_decision keeps failures visible when one probe could not run" {
  # An unrun probe adds no reason to doubt the ones that failed, so the mix must
  # not read as reachable. Collapsing it toward skip is what stamps the artifact
  # with a reachability verdict for an address whose probes failed.
  [ "$(printf 'unknown\nfail\nfail\n' | lb_capture_decision)" = "capture" ]
}

@test "lb_capture_decision returns unknown only when nothing could be attempted" {
  [ "$(printf 'unknown\nunknown\nunknown\n' | lb_capture_decision)" = "unknown" ]
  # A success still wins: the address answered, whatever else could not be tried.
  [ "$(printf 'unknown\nok\n' | lb_capture_decision)" = "skip" ]
}

@test "the Chainsaw caller also reports a capture its backstop cut short" {
  # This collector has two callers, and the honesty contract has to hold at both
  # or the reader learns to distrust the one that keeps it. hack/cozytest.sh's
  # side is covered behaviourally in cozytest-capture-gate.bats; this runs the
  # Chainsaw catch itself, extracted from the config it lives in, so neither
  # side can quietly stop reporting. The Chainsaw backstop is the shorter of the
  # two -- 300s against 600s, because it shares an op envelope with the snapshot
  # leg -- so it is the likelier of the pair to fire.
  #
  # The backstop is simulated with a `timeout` on PATH that exits 124, for the
  # same reason the cozytest.sh-side test does it: the call site sees a non-zero
  # status and whatever landed, whichever way the kill arrived.
  # Named here rather than left to fail downstream: without it a missing yq
  # surfaces as the extraction guard below going red, which reads as the catch
  # op having been renamed. Same form the other yq-using suites use.
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required to read the Chainsaw catch script" >&2; exit 1; }
  d=$(mktemp -d)
  mkdir -p "$d/bin" "$d/hack/e2e-chainsaw/suite"
  printf '#!/bin/sh\nexit 124\n' >"$d/bin/timeout"
  printf '#!/bin/sh\nexit 0\n' >"$d/bin/crust-gather"
  # The catch resolves this collector as ../../e2e-capture-dataplane.sh from the
  # failing suite's own directory, and gates the leg on it being executable.
  printf '#!/bin/sh\nexit 0\n' >"$d/hack/e2e-capture-dataplane.sh"
  chmod +x "$d/bin/timeout" "$d/bin/crust-gather" "$d/hack/e2e-capture-dataplane.sh"
  yq -r '.spec.error.catch[] | select(.description == "crust-gather host snapshot on failure") | .script.content' \
    "$HACK_DIR/e2e-chainsaw/.chainsaw.yaml" >"$d/catch.sh"
  # The extraction found the right op: a renamed description would otherwise
  # leave an empty script that satisfies nothing and reports nothing.
  grep -q 'e2e-capture-dataplane.sh' "$d/catch.sh"
  ( cd "$d/hack/e2e-chainsaw/suite" \
    && PATH="$d/bin:$PATH" COZY_REPORT_DIR="$d/report" TEST_NAME=fixture sh "$d/catch.sh" ) \
    >"$d/out" 2>&1 || true
  # Positive control: the leg was reached, so the line below is about what it
  # said and not about the catch having stopped earlier.
  grep -q 'capturing host->pod data-plane' "$d/out"
  grep -q 'data-plane capture INCOMPLETE (exit 124)' "$d/out"
  rm -rf "$d"
}
