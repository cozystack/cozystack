#!/bin/sh
# e2e-capture-dataplane.sh - capture host->pod data-plane state for NotReady pods.
#
# DIAGNOSTIC ONLY. This script collects extra evidence on an already-failed
# test; it never mutates the cluster and never changes the test's pass/fail
# outcome. cozytest.sh invokes it from its on-failure hook, AFTER the
# crust-gather snapshot and only when a test has already failed, writing into
# the same snapshot dir so the output lands in the uploaded cozyreport artifact.
#
# Why this exists: a recurrent install failure is a CNI host->local-pod
# data-plane transient -- kubelet on a node reaches a *local* pod's
# readiness/startup probe with "connection refused" for several minutes while
# overlay (pod->pod) traffic works, then it self-heals. This is rooted in the
# cozystack cilium+kube-ovn chaining config (forced enable-host-legacy-routing,
# CNI InstallEndpointRoute:false -> host->local-pod routing is delegated to
# kube-ovn/ovn0). The standard crust-gather snapshot captures Kubernetes object
# state but NOT the L3 forwarding state on the node, so the mechanism cannot be
# root-caused after the fact. This collects exactly that state from the pod's
# node so the next recurrence is dispositive.
#
# What it captures, per affected pod (NotReady, scheduled, has a podIP):
#   - cilium-agent on the node:  cilium-dbg endpoint list; bpf ct entries for
#     the podIP; a short bounded `cilium-dbg monitor --type drop`; hubble
#     dropped-verdict observations (if the hubble CLI is present in the agent).
#     NOTE: under enable-host-legacy-routing the host->local-pod path traverses
#     the KERNEL netfilter stack, not cilium BPF, so the cilium CT/monitor view
#     is complementary -- the authoritative conntrack table for this path is the
#     kernel one captured below. Both are kept: cilium's view still covers
#     pod->pod and confirms the path is NOT in BPF, which is itself evidence.
#   - host netns on the node (via the kube-ovn cni-server, which is
#     hostNetwork+NET_ADMIN): `ip route get <podIP>`, `ip neigh`, `ip rule`,
#     `ip addr show ovn0`, and the KERNEL conntrack entries for the podIP
#     (`conntrack -L`, falling back to /proc/net/nf_conntrack). These reveal a
#     kernel-path misforward / missing route / wrong rule / unresolved neighbor
#     -- the actual mechanism behind host->local-pod "connection refused" on the
#     legacy-routing path (the crux of the transient).
#   - OVS/OVN on the node: `ovs-ofctl dump-flows br-int`, and the OVN
#     Port_Binding / Logical_Switch_Port for the pod (LSP name = <pod>.<ns> in
#     kube-ovn) plus a bounded `ovn-sbctl lflow-list`.
#   - OVN flow-programming timing on the node (ovs-ovn pod, app=ovs -- its
#     openvswitch container also runs ovn-controller): the tail of
#     /var/log/ovn/ovn-controller.log plus a grep for the decisive lines
#     (physical_flow_output, if_status_mgr, "took <N>ms", recompute,
#     "Unreasonably long ... poll interval"); the per-interface ovn-installed
#     flag + ovn-installed-ts on the pod's OVS interface; and the ovs-ovn
#     container's cgroup cpu.stat (nr_throttled / throttled_usec). Why: the
#     host->local-pod transient is consistent with OVN incremental-processing
#     lag -- ovn-installed=true (the kubelet/CNI "port ready" barrier) is set by
#     if_status_mgr BEFORE physical_flow_output installs the LSP's local-delivery
#     OpenFlow under burst, aggravated when ovn-controller is CPU-throttled (it
#     shares the ovs-ovn CPU limit with vswitchd). A `physical_flow_output ...
#     took <N>ms` line spanning the failure window, an ovn-installed-ts that
#     predates that flow, and non-zero cpu.stat throttling together make a
#     recurrence dispositive.
#
# What it ALSO captures (added section) -- an UNREACHABLE LoadBalancer datapath:
#   The NotReady-pod trigger above misses a second, equally-recurrent flake. A
#   tenant `Service type=LoadBalancer` IP goes unreachable while its backend
#   pod/VMI stays Ready+Running -- so there is NO NotReady pod, the capture above
#   no-ops, and the host->cross-node->bridged-worker-VMI->tenant-nodePort
#   datapath (the suspected failing path) is never characterised. This section
#   closes that gap. It is gated by a LIVE reachability probe so it only does the
#   heavy capture for an LB that is actually broken; reachable LBs are enumerated
#   and explicitly recorded as "reachable, skipped".
#     - Enumerate every Service type=LoadBalancer that has a status ingress IP.
#       For each, derive: the EndpointSlice backend (endpoint IP + node +
#       targetPort), the Service nodePort + externalTrafficPolicy, and the
#       ANNOUNCER node from the MetalLB speaker logs (the hostNetwork speaker is
#       the L2 owner; the node of its most recent serviceAnnounced for the IP is
#       the announcer).
#     - Probe the LB IP:port a few times from a host netns (the kube-ovn
#       cni-server, so the probe traverses the same host-sourced datapath the
#       flake breaks). Only an LB whose every probe FAILS gets the heavy capture
#       below, and that same probe is the traffic the tcpdumps observe.
#     - For a FAILING LB, on BOTH the announcer node and the endpoint node
#       (reusing pod_on_node), labelled by node + role (ANNOUNCER vs ENDPOINT):
#         * `cilium-dbg bpf lb list` grepped for the LB IP + nodePort -- did the
#           HOST cilium program the LB->backend translation? This is the first
#           fork: host-cilium-not-programming vs kube-ovn-delivery-failure.
#         * kernel `conntrack -L -d <LB IP>` + `ip neigh` (LB IP, endpoint IP)
#           via the cni-server host netns.
#         * `ovs-ofctl dump-flows br-int` filtered to the nodePort / endpoint IP.
#         * a bounded `tcpdump` on the announcer node's geneve tunnel iface and
#           the endpoint node's backend OVS/tap iface, run WHILE the probe is
#           replayed, to show whether the packet crosses announcer->endpoint and
#           reaches the backend's host-side interface.
#         * the ovn-controller.log decisive lines for the window.
#     In-guest capture INSIDE the worker VMI (tenant `cilium-dbg bpf lb list` /
#     `tcpdump eth0`) is the one hop this cannot reach from the host; it is left
#     as a documented stretch at the call site (it needs a tenant
#     kubeconfig/virtctl this EXIT-trap diagnostic does not have). The host-side
#     ANNOUNCER/ENDPOINT split is the deliverable and already localises the
#     failing hop to host-cilium vs kube-ovn delivery.
#
# Robustness contract (matches docs/agents/e2e-testing.md): pure diagnostics,
# no retries, no behavior change, no traps. Every live capture is time-boxed
# and every command is `|| true`, so a missing tool, an absent pod, or a hung
# exec can never fail or stall the job. A wall-clock backstop wraps the whole
# run at the call site. It no-ops cleanly when there are no affected pods.
set -u

# --------------------------------------------------------------------------- #
# Pure, side-effect-free helpers for the LoadBalancer-datapath section below.  #
# Each takes text on stdin / in args and emits text -- no kubectl, no globals  #
# -- so hack/capture-dataplane.bats can source this file (with                 #
# E2E_CAPTURE_DATAPLANE_LIB set, see the guard below) and unit-test the        #
# enumeration parsing, announcer-node detection, and capture-or-skip decision  #
# against mock input without a cluster. Keep them above the guard and free of  #
# any runtime state.                                                           #
# --------------------------------------------------------------------------- #

# lb_filter_services: stdin = `ns|name|type|lbip|port|nodePort|extPolicy` rows
# (one Service per line, as emitted by the kubectl jsonpath in main). Emits only
# the rows that are type=LoadBalancer AND carry a status ingress IP -- i.e. the
# Services that actually have an external datapath to characterise.
lb_filter_services() {
  awk -F'|' '$3 == "LoadBalancer" && $4 != "" { print }'
}

# lb_first_ready_endpoint: stdin = `ip|node|targetNs|targetName|ready` rows (one
# EndpointSlice endpoint per line). Emits `ip|node|targetNs|targetName` for the
# first endpoint that has an address and is not explicitly NotReady, then stops.
# A blank `ready` (slice without conditions) counts as ready; only "false" is
# excluded. This is the backend the LB IP is supposed to reach.
lb_first_ready_endpoint() {
  awk -F'|' '$1 != "" && $5 != "false" { print $1 "|" $2 "|" $3 "|" $4; exit }'
}

# lb_announcer_node <lbip>: stdin = MetalLB speaker logs, each line prefixed with
# the emitting speaker pod's node and a TAB (`<node>\t<logline>`). The speaker is
# hostNetwork and only the elected L2 owner currently announces the IP. Reports a
# node ONLY IF that node's LAST own IP-event for this exact IP is an announce: per
# node we track its most recent announce/withdraw line for the IP, so a node that
# announced then withdrew (last event = withdraw) is excluded and a node that is
# still announcing (last event = announce) qualifies. This is robust to the
# per-pod (not globally time-sorted) concat order of the speaker logs and to L2
# failover. If more than one node still qualifies (should not happen for an L2
# announce), the one whose last announce appears latest in the input wins.
# The IP is matched as a whole token (not a substring), so a query for
# `192.0.2.5` does NOT match a `192.0.2.50` line. Emits the announcer node, or
# nothing when no node currently announces the IP.
lb_announcer_node() {
  awk -F'\t' -v ip="$1" '
    BEGIN {
      # Match the IP as a maximal IP-literal token: not flanked by another
      # IP char (digit / dot / hex / colon), so `192.0.2.5` != `192.0.2.50`.
      # `[.]` escapes each dot portably (no gsub-replacement backslash games).
      ipre = ip
      gsub(/\./, "[.]", ipre)
      re = "(^|[^0-9A-Fa-f:.])" ipre "([^0-9A-Fa-f:.]|$)"
    }
    $0 ~ re {
      if      ($0 ~ /[Ww]ithdraw/) ev = "withdraw"
      else if ($0 ~ /[Aa]nnounc/)  ev = "announce"
      else                         next
      last_ev[$1]   = ev
      last_line[$1] = NR
    }
    END {
      best = ""; bestline = -1
      for (n in last_ev) {
        if (last_ev[n] == "announce" && last_line[n] > bestline) {
          bestline = last_line[n]
          best = n
        }
      }
      if (best != "") print best
    }'
}

# lb_capture_decision: stdin = one probe outcome token per line ("ok" / "fail").
# Emits the gate decision for the heavy per-node capture:
#   - "capture" only when at least one probe ran AND every probe failed (the LB
#     IP is unreachable -- the symptom we want characterised);
#   - "skip" when any probe succeeded (LB reachable) OR no probe ran at all (no
#     HTTP/TCP client in the host netns -> cannot conclude unreachable, so only
#     the cheap metadata is kept, never the heavy capture).
lb_capture_decision() {
  awk '
    { if ($0 == "") next; n++; if ($0 == "ok") ok++ }
    END {
      if (n == 0) { print "skip"; exit }
      if (ok > 0) { print "skip"; exit }
      print "capture"
    }'
}

# lb_budget_ok <captured-so-far> <max>: gate for the heavy per-LB capture budget.
# Emits "yes" while fewer than <max> LBs have been CAPTURED, "no" once the cap is
# reached. The cap bounds LBs actually captured -- the caller increments only on
# the capture branch, so reachable/skipped LBs never consume it and a broken LB
# enumerated after many reachable ones is still characterised. The cap bounds
# WORK, not wall-clock: at >=2 unreachable LBs (each heavy capture takes tens of
# seconds) the real wall-clock bound is the outer `timeout -k 30 600` backstop at
# the call site, not this cap.
lb_budget_ok() {
  if [ "$1" -lt "$2" ]; then echo yes; else echo no; fi
}

# Sourcing guard: hack/capture-dataplane.bats sets E2E_CAPTURE_DATAPLANE_LIB and
# sources this file purely to reach the helpers above; return before touching $1
# or running any capture so the unit test never needs a cluster. The script's
# only executing caller (the cozytest.sh EXIT trap) never sets this, so the
# guard is a no-op there.
if [ -n "${E2E_CAPTURE_DATAPLANE_LIB:-}" ]; then
  return 0 2>/dev/null
fi

OUT="${1:?Usage: e2e-capture-dataplane.sh <output-dir>}"

CILIUM_NS="${COZY_CILIUM_NS:-cozy-cilium}"
KUBEOVN_NS="${COZY_KUBEOVN_NS:-cozy-kubeovn}"
# Cap how many pods we inspect so a fully-wedged cluster cannot explode the
# runtime; the per-command timeouts and the call-site wall-clock wrapper are the
# other two bounds. Node-global captures are deduped per node, so the effective
# work is closer to (#affected-nodes) than (#affected-pods).
MAX_PODS="${COZY_DATAPLANE_MAX_PODS:-12}"

# LoadBalancer-datapath section tunables (see the header block and the
# capture_lb_datapath function near the end of this file). The speaker selector
# and geneve iface match the cozystack metallb/cilium+kube-ovn defaults but stay
# overridable so a renamed component cannot silently blank the capture.
METALLB_NS="${COZY_METALLB_NS:-cozy-metallb}"
SPEAKER_SELECTOR="${COZY_METALLB_SPEAKER_SELECTOR:-app.kubernetes.io/component=speaker}"
GENEVE_IFACE="${COZY_GENEVE_IFACE:-genev_sys_6081}"
# Cap how many UNREACHABLE LBs get the heavy datapath capture; reachable/skipped
# LBs never count toward it (see the captured-budget gate in capture_lb_datapath).
# This bounds WORK, not wall-clock: at >=2 unreachable LBs the real wall-clock
# bound is the outer `timeout -k 30 600` backstop at the call site (truncate +
# hard-kill), since each heavy capture itself takes tens of seconds.
MAX_LBS="${COZY_DATAPLANE_MAX_LBS:-6}"

command -v kubectl >/dev/null 2>&1 || exit 0
mkdir -p "$OUT" 2>/dev/null || exit 0

log() { echo "[capture-dataplane] $*"; }

# Affected = scheduled (has nodeName), has a podIP (so an endpoint exists to
# inspect), Ready!=True, and not already terminal. That is the superset of the
# "readiness/startup probe failing with connection refused" symptom -- a pod
# that is Running with an IP but whose Ready condition is False is exactly the
# transient's signature. Succeeded/Failed pods (completed Jobs, hook pods) are
# Ready!=True too but are not the symptom, so they are excluded to keep the
# MAX_PODS budget on Running/Pending pods that are actually wedged.
# jsonpath keeps this dependency-free (no jq / go-template reassignment).
affected=$(kubectl get pods -A \
  -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{.status.podIP}{"|"}{.spec.nodeName}{"|"}{.status.conditions[?(@.type=="Ready")].status}{"|"}{.status.phase}{"\n"}{end}' \
  2>/dev/null | awk -F'|' '$3!="" && $4!="" && $5!="True" && $6!="Succeeded" && $6!="Failed"')

if [ -z "$affected" ]; then
  log "no NotReady pods with a podIP+node -- nothing to capture"
  exit 0
fi

ncount=$(printf '%s\n' "$affected" | wc -l | tr -d ' ')
log "capturing host->pod data-plane for up to $MAX_PODS of $ncount affected pod(s) -> $OUT"

# OVN southbound logical-flow dump is cluster-global, so capture it once rather
# than per pod. ovn-central is a Deployment (not per-node); any replica answers.
# --no-leader-only lets a read land on a raft follower instead of erroring.
central=$(kubectl get pod -n "$KUBEOVN_NS" -l app=ovn-central \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -n "$central" ]; then
  {
    echo "=== ovn-sbctl lflow-list (cluster-global, pod=$central) ==="
    timeout 30 kubectl exec -n "$KUBEOVN_NS" "$central" -c ovn-central -- \
      ovn-sbctl --no-leader-only lflow-list 2>&1 || true
  } > "$OUT/ovn-lflows.txt" 2>&1 || true
else
  log "no ovn-central pod in $KUBEOVN_NS -- skipping OVN logical-flow dump"
fi

# pod_on_node <ns> <label> <node> -> first matching pod name (empty if none).
pod_on_node() {
  kubectl get pod -n "$1" -l "$2" --field-selector "spec.nodeName=$3" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

# Per-node captures are node-global (every pod on a node shares one cilium-agent
# / ovs / cni-server), so run them once per node and reuse across that node's
# pods. POSIX-sh membership test over a space-delimited string.
_SEEN_NODES=" "
node_seen() { case "$_SEEN_NODES" in *" $1 "*) return 0 ;; esac; return 1; }
mark_node() { _SEEN_NODES="$_SEEN_NODES$1 "; }

capture_node() {
  node=$1
  node_seen "$node" && return 0
  mark_node "$node"
  nf="$OUT/node-$node.txt"

  agent=$(pod_on_node "$CILIUM_NS" k8s-app=cilium "$node")
  ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$node")
  {
    echo "################################################################"
    echo "# NODE $node  (cilium-agent=${agent:-<none>} ovs=${ovs:-<none>})"
    echo "################################################################"

    if [ -n "$agent" ]; then
      echo
      echo "=== cilium-dbg endpoint list ==="
      timeout 25 kubectl exec -n "$CILIUM_NS" "$agent" -c cilium-agent -- \
        cilium-dbg endpoint list 2>&1 || true

      echo
      echo "=== cilium-dbg monitor --type drop (bounded ~8s) ==="
      # Two nested bounds: an inner `timeout 8` so the capture self-terminates
      # if the agent ships coreutils, and an outer `timeout 12` on the exec as
      # the hard backstop if it does not. Either way it cannot hang.
      timeout 12 kubectl exec -n "$CILIUM_NS" "$agent" -c cilium-agent -- \
        sh -c 'timeout 8 cilium-dbg monitor --type drop 2>&1 || true' 2>&1 || true

      echo
      echo "=== hubble observe --verdict DROPPED --last 200 (if hubble present) ==="
      timeout 25 kubectl exec -n "$CILIUM_NS" "$agent" -c cilium-agent -- \
        sh -c 'command -v hubble >/dev/null 2>&1 && hubble observe --verdict DROPPED --last 200 2>&1 || echo "hubble CLI not present in agent"' 2>&1 || true
    else
      echo
      echo "(no cilium-agent pod found on node $node)"
    fi

    if [ -n "$ovs" ]; then
      echo
      echo "=== ovs-ofctl dump-flows br-int ==="
      timeout 25 kubectl exec -n "$KUBEOVN_NS" "$ovs" -c openvswitch -- \
        ovs-ofctl dump-flows br-int 2>&1 || true

      echo
      echo "=== ovn-controller.log decisive lines (flow-programming / I-P timing) ==="
      # ovn-controller runs inside this ovs-ovn (app=ovs) pod's openvswitch
      # container and logs to /var/log/ovn/ovn-controller.log. Grep the lines
      # that pin OVN incremental-processing lag: physical_flow_output (installs
      # the LSP local-delivery OpenFlow), if_status_mgr (sets ovn-installed --
      # the CNI "port ready" barrier), and "took <N>ms" / recompute /
      # "Unreasonably long ... poll interval" (ovn-controller stalls). A
      # physical_flow_output "took <N>ms" spanning the failure window is proof.
      timeout 25 kubectl exec -n "$KUBEOVN_NS" "$ovs" -c openvswitch -- \
        sh -c 'grep -E "physical_flow_output|if_status_mgr|took [0-9]+ ?ms|recompute|Unreasonably long" /var/log/ovn/ovn-controller.log 2>/dev/null | tail -n 400 || echo "no matching lines in /var/log/ovn/ovn-controller.log"' 2>&1 || true

      echo
      echo "=== ovn-controller.log tail (bounded) ==="
      timeout 20 kubectl exec -n "$KUBEOVN_NS" "$ovs" -c openvswitch -- \
        sh -c 'tail -n 2000 /var/log/ovn/ovn-controller.log 2>/dev/null || echo "no /var/log/ovn/ovn-controller.log"' 2>&1 || true

      echo
      echo "=== ovs-ovn cgroup cpu.stat (ovn-controller/vswitchd CPU throttling) ==="
      # ovs-ovn caps CPU (shared between ovn-controller and vswitchd); non-zero
      # nr_throttled / throttled_usec means ovn-controller was CPU-starved, which
      # aggravates the flow-programming lag above. cgroup v2 path first, v1
      # fallback.
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$ovs" -c openvswitch -- \
        sh -c 'cat /sys/fs/cgroup/cpu.stat 2>/dev/null || cat /sys/fs/cgroup/cpu/cpu.stat 2>/dev/null || echo "no cpu.stat at /sys/fs/cgroup/cpu.stat (v2) or /sys/fs/cgroup/cpu/cpu.stat (v1)"' 2>&1 || true
    else
      echo
      echo "(no ovs pod found on node $node)"
    fi

    cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$node")
    if [ -n "$cni" ]; then
      echo
      echo "=== host netns: ip neigh (via kube-ovn cni-server, hostNetwork) ==="
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
        ip neigh 2>&1 || true

      echo
      echo "=== host netns: ip rule ==="
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
        ip rule 2>&1 || true

      echo
      echo "=== host netns: ip addr show ovn0 ==="
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
        ip addr show ovn0 2>&1 || true
    else
      echo
      echo "(no kube-ovn-cni pod found on node $node -- host netns capture skipped)"
    fi
  } >> "$nf" 2>&1 || true
}

i=0
printf '%s\n' "$affected" | {
  while IFS='|' read -r ns pod podip node _ready _phase; do
    [ -n "$ns" ] && [ -n "$pod" ] && [ -n "$podip" ] && [ -n "$node" ] || continue
    i=$((i + 1))
    if [ "$i" -gt "$MAX_PODS" ]; then
      log "reached MAX_PODS=$MAX_PODS cap; $((ncount - MAX_PODS)) more affected pod(s) NOT captured"
      break
    fi

    # Node-global state (cilium endpoints, monitor, hubble, ovs flows, ip neigh,
    # ip rule, ovn0 addr) -- captured once per node.
    capture_node "$node"

    # Pod-specific state.
    pf="$OUT/pod-$ns-$pod.txt"
    agent=$(pod_on_node "$CILIUM_NS" k8s-app=cilium "$node")
    ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$node")
    {
      echo "################################################################"
      echo "# POD $ns/$pod  podIP=$podip  node=$node  (Ready=$_ready)"
      echo "# node-global captures are in node-$node.txt"
      echo "################################################################"

      echo
      echo "=== pod Ready conditions + recent probe events ==="
      kubectl get pod -n "$ns" "$pod" \
        -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason}: {.message}{"\n"}{end}' 2>&1 || true
      kubectl get events -n "$ns" --field-selector "involvedObject.name=$pod" \
        -o jsonpath='{range .items[*]}{.lastTimestamp}{" "}{.reason}{": "}{.message}{"\n"}{end}' 2>&1 || true

      if [ -n "$agent" ]; then
        echo
        echo "=== cilium-dbg bpf ct list global | grep $podip (node=$node agent=$agent) ==="
        timeout 25 kubectl exec -n "$CILIUM_NS" "$agent" -c cilium-agent -- \
          sh -c "cilium-dbg bpf ct list global 2>/dev/null | grep -F '$podip' || echo 'no CT entries for $podip'" 2>&1 || true
      fi

      cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$node")
      if [ -n "$cni" ]; then
        echo
        echo "=== host netns: ip route get $podip (via kube-ovn cni-server) ==="
        timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
          ip route get "$podip" 2>&1 || true

        echo
        echo "=== host netns: kernel conntrack for $podip ==="
        # Under enable-host-legacy-routing the host->local-pod path is in the
        # kernel netfilter conntrack table, NOT cilium BPF -- this is the
        # authoritative table for the transient. Prefer the conntrack CLI; fall
        # back to /proc/net/nf_conntrack when it is absent from the image.
        timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
          sh -c "if command -v conntrack >/dev/null 2>&1; then conntrack -L 2>/dev/null | grep -F '$podip' || echo 'no conntrack entries for $podip'; else grep -F '$podip' /proc/net/nf_conntrack 2>/dev/null || echo 'no conntrack CLI; no /proc/net/nf_conntrack match for $podip'; fi" 2>&1 || true
      fi

      if [ -n "$central" ]; then
        echo
        echo "=== OVN Port_Binding / Logical_Switch_Port for $pod.$ns ==="
        # kube-ovn names the OVN logical port <pod>.<namespace>.
        timeout 20 kubectl exec -n "$KUBEOVN_NS" "$central" -c ovn-central -- \
          ovn-sbctl --no-leader-only find port_binding "logical_port=$pod.$ns" 2>&1 || true
        timeout 20 kubectl exec -n "$KUBEOVN_NS" "$central" -c ovn-central -- \
          ovn-nbctl --no-leader-only find logical_switch_port "name=$pod.$ns" 2>&1 || true
      fi

      if [ -n "$ovs" ]; then
        echo
        echo "=== OVS interface ovn-installed flag + ts for iface-id=$pod.$ns (ovs pod $ovs) ==="
        # kube-ovn stamps external_ids:ovn-installed (+ -ts) on the pod's OVS
        # interface once ovn-controller reports the port programmed -- this is
        # the barrier the CNI waits on. The OVS interface name is opaque, so
        # look it up by iface-id (<pod>.<ns>), then read the flag + timestamp.
        # An ovn-installed-ts set early (before physical_flow_output installed
        # the local-delivery flow, see node-$node.txt) is the I-P-lag signature.
        ovsif=$(timeout 20 kubectl exec -n "$KUBEOVN_NS" "$ovs" -c openvswitch -- \
          ovs-vsctl --no-heading --columns=name find interface "external_ids:iface-id=$pod.$ns" 2>/dev/null \
          | head -n 1 | tr -d '" ')
        if [ -n "$ovsif" ]; then
          echo "ovs interface = $ovsif"
          timeout 20 kubectl exec -n "$KUBEOVN_NS" "$ovs" -c openvswitch -- \
            ovs-vsctl get interface "$ovsif" external_ids:ovn-installed external_ids:ovn-installed-ts 2>&1 || true
        else
          echo "no OVS interface with external_ids:iface-id=$pod.$ns"
        fi
      fi
    } > "$pf" 2>&1 || true
  done
  log "host->pod data-plane capture complete"
}

# ============================================================================ #
# LoadBalancer-datapath capture (see the header block for the full rationale).  #
# Independent of the NotReady-pod path above: it fires for a Service            #
# type=LoadBalancer whose external IP is unreachable even though its backend is #
# Ready, which the pod path cannot see. Everything below is bounded + best-     #
# effort and never changes the job outcome.                                     #
# ============================================================================ #

# host_http_probe <node> <lbip> <port> -- one bounded reachability probe of the
# LB IP from <node>'s host netns (via the hostNetwork kube-ovn cni-server, so the
# probe traverses the same host->cross-node->backend datapath the flake breaks).
# Echoes "ok" / "fail" per the result, or nothing when the cni-server image ships
# no probe client (the decision helper treats no-attempt as skip). Prefers a pure
# TCP connect (nc -z) so a non-HTTP backend is not misread as unreachable; the
# curl/wget fallbacks send HTTP and so fail-toward-capture on a non-HTTP port,
# the safe direction (capture is cheap; a missed broken LB is not). Doubles as
# the traffic generator for the tcpdumps.
host_http_probe() {
  _hp_node=$1; _hp_ip=$2; _hp_port=$3
  _hp_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_hp_node")
  [ -n "$_hp_cni" ] || return 0
  # The LB IP/port are embedded as inner single-quoted literals (same idiom as
  # the pod-path captures above) so the inner shell never re-splits them.
  timeout 12 kubectl exec -n "$KUBEOVN_NS" "$_hp_cni" -c cni-server -- \
    sh -c "
      if command -v nc >/dev/null 2>&1; then
        nc -z -w 5 '$_hp_ip' '$_hp_port' >/dev/null 2>&1 && echo ok || echo fail
      elif command -v curl >/dev/null 2>&1; then
        curl -sS -o /dev/null --max-time 6 --connect-timeout 5 'http://$_hp_ip:$_hp_port/' >/dev/null 2>&1 && echo ok || echo fail
      elif command -v wget >/dev/null 2>&1; then
        wget -q -T 6 -O /dev/null 'http://$_hp_ip:$_hp_port/' >/dev/null 2>&1 && echo ok || echo fail
      fi
    " 2>/dev/null || true
}

# ovs_iface_for <node> <iface-id> -- the OVS interface name on <node> whose
# external_ids:iface-id matches (kube-ovn stamps <pod>.<ns>); empty if none. Used
# to point the endpoint-node tcpdump at the backend's host-side tap.
ovs_iface_for() {
  _oi_ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$1")
  [ -n "$_oi_ovs" ] || return 0
  timeout 20 kubectl exec -n "$KUBEOVN_NS" "$_oi_ovs" -c openvswitch -- \
    ovs-vsctl --no-heading --columns=name find interface "external_ids:iface-id=$2" 2>/dev/null \
    | head -n 1 | tr -d '" '
}

# capture_lb_node <node> <role> <lbip> <nodeport> <endpointip> <outfile>
# -- the static (non-tcpdump) host-side captures on one node for a failing LB.
# role is ANNOUNCER or ENDPOINT and is stamped on every block so the two halves
# of the path are unambiguous in the artifact.
capture_lb_node() {
  _n=$1; _role=$2; _lbip=$3; _np=$4; _epip=$5; _of=$6
  _agent=$(pod_on_node "$CILIUM_NS" k8s-app=cilium "$_n")
  _ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$_n")
  _cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_n")
  {
    echo
    echo "---------------- $_role node=$_n (cilium=${_agent:-<none>} ovs=${_ovs:-<none>} cni=${_cni:-<none>}) ----------------"

    if [ -n "$_agent" ]; then
      echo
      echo "=== [$_role $_n] cilium-dbg bpf lb list | grep LB IP / nodePort -- host cilium LB->backend programming ==="
      # Answers the first fork: if the host cilium has NO LB map entry for the
      # LB IP or the nodePort, the fault is host-cilium-not-programming; if it
      # does, suspicion shifts to kube-ovn delivery (the captures below).
      timeout 25 kubectl exec -n "$CILIUM_NS" "$_agent" -c cilium-agent -- \
        sh -c "cilium-dbg bpf lb list 2>/dev/null | grep -E '$_lbip|:$_np' || echo 'no bpf lb entry for $_lbip or nodePort $_np'" 2>&1 || true
    fi

    if [ -n "$_cni" ]; then
      echo
      echo "=== [$_role $_n] host netns: kernel conntrack -d $_lbip (via cni-server) ==="
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$_cni" -c cni-server -- \
        sh -c "if command -v conntrack >/dev/null 2>&1; then conntrack -L -d '$_lbip' 2>/dev/null || echo 'no conntrack entries for $_lbip'; else grep -F '$_lbip' /proc/net/nf_conntrack 2>/dev/null || echo 'no conntrack CLI; no /proc/net/nf_conntrack match for $_lbip'; fi" 2>&1 || true

      echo
      echo "=== [$_role $_n] host netns: ip neigh (LB IP $_lbip, endpoint IP $_epip) ==="
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$_cni" -c cni-server -- \
        sh -c "ip neigh 2>/dev/null | grep -E '$_lbip|$_epip' || echo 'no neigh entry for $_lbip or $_epip'" 2>&1 || true
    fi

    if [ -n "$_ovs" ]; then
      echo
      echo "=== [$_role $_n] ovs-ofctl dump-flows br-int | grep nodePort $_np / endpoint $_epip ==="
      timeout 25 kubectl exec -n "$KUBEOVN_NS" "$_ovs" -c openvswitch -- \
        sh -c "ovs-ofctl dump-flows br-int 2>/dev/null | grep -E '$_np|$_epip' || echo 'no br-int flow matching nodePort $_np or endpoint $_epip'" 2>&1 || true

      echo
      echo "=== [$_role $_n] ovn-controller.log decisive lines ==="
      timeout 20 kubectl exec -n "$KUBEOVN_NS" "$_ovs" -c openvswitch -- \
        sh -c 'grep -E "physical_flow_output|if_status_mgr|took [0-9]+ ?ms|recompute|Unreasonably long" /var/log/ovn/ovn-controller.log 2>/dev/null | tail -n 200 || echo "no matching lines in /var/log/ovn/ovn-controller.log"' 2>&1 || true
    fi
  } >> "$_of" 2>&1 || true
}

# capture_lb_datapath: enumerate LBs, gate on a live probe, and characterise the
# announcer/endpoint datapath for any LB whose every probe fails. Wraps the whole
# per-LB body in `|| true`-guarded, time-boxed captures so it is always best-
# effort and self-limiting (MAX_LBS cap + per-command timeouts).
capture_lb_datapath() {
  _raw=$(timeout 20 kubectl get svc -A \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{.spec.type}{"|"}{.status.loadBalancer.ingress[0].ip}{"|"}{.spec.ports[0].port}{"|"}{.spec.ports[0].nodePort}{"|"}{.spec.externalTrafficPolicy}{"\n"}{end}' \
    2>/dev/null)
  _lbs=$(printf '%s\n' "$_raw" | lb_filter_services)
  if [ -z "$_lbs" ]; then
    log "no Service type=LoadBalancer with an ingress IP -- skipping LB-datapath capture"
    return 0
  fi

  _lbcount=$(printf '%s\n' "$_lbs" | wc -l | tr -d ' ')
  log "probing $_lbcount LoadBalancer service(s); heavy-capturing up to $MAX_LBS unreachable one(s) -> $OUT/lb-*.txt"

  # MetalLB speaker logs once, prefixed with each speaker's node (hostNetwork, so
  # .spec.nodeName IS the announcing node). Kept as an artifact file that
  # lb_announcer_node greps per LB. A blank file is fine -- the announcer just
  # reports <unknown> and the probe falls back to the endpoint node.
  _speakerlog="$OUT/lb-speaker-logs.txt"
  : > "$_speakerlog" 2>/dev/null || _speakerlog=""
  _speakers=$(kubectl get pod -n "$METALLB_NS" -l "$SPEAKER_SELECTOR" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.spec.nodeName}{"\n"}{end}' 2>/dev/null)
  if [ -n "$_speakers" ] && [ -n "$_speakerlog" ]; then
    printf '%s\n' "$_speakers" | while IFS='|' read -r _sp _spnode; do
      [ -n "$_sp" ] && [ -n "$_spnode" ] || continue
      timeout 15 kubectl logs -n "$METALLB_NS" "$_sp" -c speaker --tail=2000 2>/dev/null \
        | awk -v n="$_spnode" '{ print n "\t" $0 }' >> "$_speakerlog" 2>/dev/null || true
    done
  fi

  # Counts LBs actually CAPTURED (heavy capture performed), not enumerated, so the
  # MAX_LBS cap only bounds failing LBs -- reachable/skipped ones below never
  # increment it and a broken LB enumerated after many reachable ones still gets
  # captured. The gate lives on the capture branch (lb_budget_ok), not here.
  _captured=0
  printf '%s\n' "$_lbs" | {
    while IFS='|' read -r _ns _name _type _lbip _port _np _etp; do
      [ -n "$_lbip" ] || continue
      _lbport="${_port:-0}"

      # EndpointSlice backend for this Service (first ready, addressed endpoint).
      _eps=$(timeout 20 kubectl get endpointslices -n "$_ns" \
        -l "kubernetes.io/service-name=$_name" \
        -o jsonpath='{range .items[*]}{range .endpoints[*]}{.addresses[0]}{"|"}{.nodeName}{"|"}{.targetRef.namespace}{"|"}{.targetRef.name}{"|"}{.conditions.ready}{"\n"}{end}{end}' \
        2>/dev/null)
      _backend=$(printf '%s\n' "$_eps" | lb_first_ready_endpoint)
      _epip=$(printf '%s' "$_backend" | cut -d'|' -f1)
      _epnode=$(printf '%s' "$_backend" | cut -d'|' -f2)
      _eptns=$(printf '%s' "$_backend" | cut -d'|' -f3)
      _eptname=$(printf '%s' "$_backend" | cut -d'|' -f4)
      _eptport=$(timeout 20 kubectl get endpointslices -n "$_ns" \
        -l "kubernetes.io/service-name=$_name" \
        -o jsonpath='{.items[0].ports[0].port}' 2>/dev/null)

      # Announcer node = node of the most recent serviceAnnounced for the IP.
      _annode=$(lb_announcer_node "$_lbip" < "${_speakerlog:-/dev/null}")

      _of="$OUT/lb-$_ns-$_name.txt"
      {
        echo "################################################################"
        echo "# LB $_ns/$_name  ip=$_lbip port=$_lbport nodePort=${_np:-<none>} etp=${_etp:-<default>}"
        echo "# backend: ip=${_epip:-<none>} node=${_epnode:-<none>} pod=${_eptns:-?}/${_eptname:-?} targetPort=${_eptport:-<none>}"
        echo "# announcer node: ${_annode:-<unknown>}"
        echo "################################################################"
      } > "$_of" 2>&1 || true

      # Gate: probe the LB IP from the announcer node's host netns (fall back to
      # the endpoint node if the announcer is unknown). Reachable -> record and
      # skip the heavy capture; unreachable -> characterise both hops.
      _probenode="${_annode:-$_epnode}"
      _decision=skip
      if [ -n "$_probenode" ] && [ "$_lbport" != "0" ]; then
        _decision=$( { host_http_probe "$_probenode" "$_lbip" "$_lbport"
                       host_http_probe "$_probenode" "$_lbip" "$_lbport"
                       host_http_probe "$_probenode" "$_lbip" "$_lbport"; } | lb_capture_decision )
      fi

      if [ "$_decision" != "capture" ]; then
        echo "probe: LB reachable or not probeable from ${_probenode:-<none>} -- reachable, skipped (no heavy capture)" >> "$_of" 2>&1 || true
        log "LB $_ns/$_name ($_lbip) reachable or not probeable -- skipped"
        continue
      fi

      # Heavy-capture budget: only failing LBs consume MAX_LBS (reachable ones
      # skipped above without counting). Stop once the cap's worth of broken LBs
      # have been captured; remaining ones are left to the outer wall-clock bound.
      if [ "$(lb_budget_ok "$_captured" "$MAX_LBS")" != "yes" ]; then
        echo "probe: LB IP unreachable from node $_probenode but MAX_LBS=$MAX_LBS captured cap reached -- NOT characterised" >> "$_of" 2>&1 || true
        log "reached MAX_LBS=$MAX_LBS captured cap; further unreachable LB(s) NOT characterised"
        break
      fi
      _captured=$((_captured + 1))

      log "LB $_ns/$_name ($_lbip) UNREACHABLE -- capturing announcer/endpoint datapath"
      {
        echo
        echo "probe: LB IP unreachable from node $_probenode (every probe failed) -- heavy capture follows"
      } >> "$_of" 2>&1 || true

      # Static host-side captures on each hop.
      if [ -n "$_annode" ]; then
        capture_lb_node "$_annode" ANNOUNCER "$_lbip" "${_np:-0}" "${_epip:-0.0.0.0}" "$_of"
      fi
      if [ -n "$_epnode" ] && [ "$_epnode" != "$_annode" ]; then
        capture_lb_node "$_epnode" ENDPOINT "$_lbip" "${_np:-0}" "${_epip:-0.0.0.0}" "$_of"
      elif [ -n "$_epnode" ] && [ "$_epnode" = "$_annode" ]; then
        echo "(endpoint node == announcer node $_annode -- single-node path, no cross-node hop)" >> "$_of" 2>&1 || true
      fi

      # Live tcpdump on both hops WHILE the probe is replayed: announcer geneve
      # tunnel egress + endpoint backend tap ingress. Shows whether the packet
      # crosses announcer->endpoint and reaches the backend's host-side iface.
      _an_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "${_annode:-}")
      _en_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "${_epnode:-}")
      _en_iface="$GENEVE_IFACE"
      if [ -n "$_eptname" ] && [ -n "$_eptns" ]; then
        _cand=$(ovs_iface_for "${_epnode:-}" "$_eptname.$_eptns")
        [ -n "$_cand" ] && _en_iface="$_cand"
      fi

      _an_pcap="$OUT/lb-$_ns-$_name.tcpdump-announcer.txt"
      _en_pcap="$OUT/lb-$_ns-$_name.tcpdump-endpoint.txt"
      _an_td=""
      _en_td=""
      if [ -n "$_an_cni" ]; then
        {
          echo "# ANNOUNCER tcpdump node=${_annode:-<none>} iface=$GENEVE_IFACE filter='host $_lbip or host ${_epip:-0.0.0.0}'"
          timeout 15 kubectl exec -n "$KUBEOVN_NS" "$_an_cni" -c cni-server -- \
            sh -c "timeout 10 tcpdump -n -i '$GENEVE_IFACE' -c 60 'host $_lbip or host ${_epip:-0.0.0.0}' 2>&1 || true"
        } > "$_an_pcap" 2>&1 &
        _an_td=$!
      fi
      if [ -n "$_en_cni" ]; then
        {
          echo "# ENDPOINT tcpdump node=${_epnode:-<none>} iface=$_en_iface filter='host $_lbip or host ${_epip:-0.0.0.0} or port ${_np:-0}'"
          timeout 15 kubectl exec -n "$KUBEOVN_NS" "$_en_cni" -c cni-server -- \
            sh -c "timeout 10 tcpdump -n -i '$_en_iface' -c 60 'host $_lbip or host ${_epip:-0.0.0.0} or port ${_np:-0}' 2>&1 || true"
        } > "$_en_pcap" 2>&1 &
        _en_td=$!
      fi

      # Give the captures a moment to attach, then drive the probe traffic and
      # collect. Bounded by the inner tcpdump timeout/-c and the exec timeout.
      if [ -n "$_an_td" ] || [ -n "$_en_td" ]; then
        sleep 2
        host_http_probe "$_probenode" "$_lbip" "$_lbport" >/dev/null 2>&1 || true
        host_http_probe "$_probenode" "$_lbip" "$_lbport" >/dev/null 2>&1 || true
        [ -n "$_an_td" ] && { wait "$_an_td" 2>/dev/null || true; }
        [ -n "$_en_td" ] && { wait "$_en_td" 2>/dev/null || true; }
      fi

      # STRETCH / TODO (deferred -- needs tenant-cluster access, not host-side):
      #   The one hop this cannot reach is INSIDE the worker VMI. To finish the
      #   chain, exec/console into the tenant cluster (virtctl console / SSH to
      #   the worker VMI) and capture, in-guest:
      #     - `cilium-dbg bpf lb list | grep <tenant-nodePort>` -- did the TENANT
      #       cilium program the nodePort->pod translation? (separates a
      #       tenant-side miss from a host-side delivery miss);
      #     - `tcpdump -ni eth0 port <tenant-nodePort>` -- did the packet even
      #       arrive on the VMI's NIC?
      #   Deferred because it needs a tenant kubeconfig/virtctl that this EXIT-
      #   trap diagnostic does not have; the host-side ANNOUNCER/ENDPOINT split
      #   already localises the failing hop to host-cilium vs kube-ovn delivery.
    done
    log "LoadBalancer-datapath capture complete"
  }
}

capture_lb_datapath

exit 0
