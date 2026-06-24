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
# Robustness contract (matches docs/agents/e2e-testing.md): pure diagnostics,
# no retries, no behavior change, no traps. Every live capture is time-boxed
# and every command is `|| true`, so a missing tool, an absent pod, or a hung
# exec can never fail or stall the job. A wall-clock backstop wraps the whole
# run at the call site. It no-ops cleanly when there are no affected pods.
set -u

OUT="${1:?Usage: e2e-capture-dataplane.sh <output-dir>}"

CILIUM_NS="${COZY_CILIUM_NS:-cozy-cilium}"
KUBEOVN_NS="${COZY_KUBEOVN_NS:-cozy-kubeovn}"
# Cap how many pods we inspect so a fully-wedged cluster cannot explode the
# runtime; the per-command timeouts and the call-site wall-clock wrapper are the
# other two bounds. Node-global captures are deduped per node, so the effective
# work is closer to (#affected-nodes) than (#affected-pods).
MAX_PODS="${COZY_DATAPLANE_MAX_PODS:-12}"

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

exit 0
