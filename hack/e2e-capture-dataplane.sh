#!/bin/sh
# e2e-capture-dataplane.sh - capture host->pod data-plane state for NotReady pods.
#
# DIAGNOSTIC ONLY. This script collects extra evidence on an already-failed
# test; it never mutates the cluster and never changes the test's pass/fail
# outcome. Two callers invoke it, each from its own on-failure path and each
# AFTER the crust-gather snapshot: the cozytest.sh EXIT trap and the Chainsaw
# global catch in hack/e2e-chainsaw/.chainsaw.yaml. Both write into the same
# snapshot dir so the output lands in the uploaded cozyreport artifact, and both
# wrap this script in a wall-clock backstop -- but not the same one (600s and
# 300s respectively), so a change sized against one caller can still overrun the
# other. The MAX_LBS and MAX_PODS notes below carry that arithmetic, one per
# fan-out cap.
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
# What it captures, per affected pod (NotReady and scheduled; podIP may still
# be empty while CNI endpoint allocation is the thing that is stuck):
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
#   - Address attribution for a podIP that turns out to be a LOCAL address of
#     the host netns instead of reachable via ovn0 (a route to nowhere-in-
#     particular is not enough to name the owner): the same host netns above
#     also gets `ip -o addr show` (every interface) and `ip route show table
#     local`, and one Ready, Running, non-hostNetwork pod on the same node that
#     has an address gets the route and conntrack half of the per-pod capture
#     below as an in-run healthy-pod control, since this collector otherwise
#     inspects NotReady pods only and had no same-window baseline. hostNetwork
#     is excluded because its podIP IS the node's own address, so it would show
#     the same local-route fingerprint as the fault for an unrelated reason.
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
#   and explicitly recorded as "reachable, skipped", and an LB no probe could be
#   run against is recorded as unprobed rather than as either of those.
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
#     kubeconfig/virtctl that neither caller of this diagnostic has). The
#     host-side ANNOUNCER/ENDPOINT split is the deliverable and already
#     localises the failing hop to host-cilium vs kube-ovn delivery.
#
# Robustness contract (matches docs/agents/e2e-testing.md): pure diagnostics,
# no retries, no behavior change, no traps. Every live capture is time-boxed,
# and no command can fail or stall the job: most are `|| true`, and the few
# whose status is read take it into a variable and use it only to say why a
# read produced nothing. A wall-clock backstop wraps the whole run at each call
# site. It no-ops cleanly when there are no affected pods.
#
# Why a read produced nothing is written to capture-notes.txt beside the
# capture, not only to the job log: the reader who has the uploaded report and
# not the run is the one who cannot otherwise tell a capture that found nothing
# from one that never ran.
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
#
# NF == 7 first, because a truncated list is exactly what a bounded or
# interrupted read leaves behind and a fragment passes a value-only test: an IP
# cut mid-octet still looks like an IP, and the row it sits in would be probed
# and written up as a Service that has no such address. The jsonpath emits a
# fixed seven fields per record, so anything shorter is a cut rather than a
# Service. Dropping it loses nothing a reader needs -- the read's own note
# already says the list did not finish.
#
# What a field count cannot catch on its own: a cut inside the LAST field
# still delivers every separator, so `...|30080|Clus` counts seven and passes,
# and nothing in-band separates a truncated final value from a short one. Here
# the last column is display-only, so it costs nothing.
# `lb_first_ready_endpoint` decides on its own last column (`ready`) as a
# deny-list against the single value `false`, so a truncated value matches
# nothing it excludes and the row is kept -- over-capturing rather than
# dropping evidence, the safe direction for a diagnostic.
#
# The two pod filters do not rely on that reasoning at all. Their jsonpath
# ends in a constant `eol` column, and both require it present and intact, so
# a cut anywhere in a row either drops the field count or corrupts the
# sentinel. That matters for `pod_first_ready` specifically: it decides on
# `hostNetwork`, and a cut landing exactly on the preceding separator leaves
# that column EMPTY, which is byte-identical to the omitempty-false the filter
# must accept. No test of the value itself can tell those apart -- only a
# marker after it can. Either way the read's own cut-off note is what tells
# the reader the list ended early.
lb_filter_services() {
  awk -F'|' 'NF == 7 && $3 == "LoadBalancer" && $4 != "" { print }'
}

# lb_first_ready_endpoint: stdin = `ip|node|targetNs|targetName|ready` rows (one
# EndpointSlice endpoint per line). Emits `ip|node|targetNs|targetName` for the
# first endpoint that has an address and is not explicitly NotReady, then stops.
# A blank `ready` (slice without conditions) counts as ready; only "false" is
# excluded. This is the backend the LB IP is supposed to reach.
#
# NF == 5 for the reason the two row filters carry their own counts, and it
# matters more here: this one stops at the first row it accepts, so a fragment
# at the head of the list is not one bad row among many -- it is the backend,
# and its half-parsed node is the node every endpoint-side capture below aims
# at. Five fields per endpoint is what the jsonpath emits.
lb_first_ready_endpoint() {
  awk -F'|' 'NF == 5 && $1 != "" && $5 != "false" { print $1 "|" $2 "|" $3 "|" $4; exit }'
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

# lb_capture_decision: stdin = one probe outcome token per line ("ok" / "fail" /
# "unknown", the last meaning the probe could not be run at all).
# Emits the gate decision for the heavy per-node capture:
#   - "capture" when at least one probe ran, none succeeded, and at least one
#     genuinely failed (the LB IP is unreachable -- the symptom we want
#     characterised). A failure alongside an unrun probe still counts: the
#     failures are evidence, and the unrun one adds no reason to doubt them;
#   - "skip" when any probe succeeded, i.e. the LB is reachable and only the
#     cheap metadata is worth keeping;
#   - "unknown" when nothing was ever attempted -- no outcome at all (no
#     HTTP/TCP client in the host netns, or an exec that could not run), or
#     every outcome unknown because the lookups behind the probe did not
#     answer. Collapsing either into skip would put a verdict about the address
#     into the artifact without a single probe behind it.
lb_capture_decision() {
  awk '
    { if ($0 == "") next; n++; if ($0 == "ok") ok++; if ($0 == "unknown") unk++ }
    END {
      if (n == 0) { print "unknown"; exit }
      # Only a wholly unknown set is unknown. One unrun probe beside real
      # failures must not erase them: the failures are the evidence this
      # capture exists to characterise, and merge-base behaviour for that mix
      # was to capture.
      if (unk > 0 && unk == n) { print "unknown"; exit }
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
# seconds) the real wall-clock bound is the outer `timeout -k 30 <n>` backstop at
# the call site, not this cap. That budget differs per caller -- the cozytest.sh
# EXIT trap runs with no containing operation and allows 600s, while the Chainsaw
# global catch shares an op envelope with the crust-gather snapshot and allows
# 300s -- so do not hard-code either number here.
lb_budget_ok() {
  if [ "$1" -lt "$2" ]; then echo yes; else echo no; fi
}

# pod_filter_affected: stdin =
# `namespace|name|podIP|nodeName|ready|phase|hostNetwork` rows. Keep scheduled,
# non-terminal pods whose Ready condition is not True. podIP is deliberately
# NOT a gate: the cilium endpointManager leak this collector diagnoses can
# strand a pod before an IP is assigned, and the node-global Cilium/OVN state
# plus pod events remain useful in that state. IP-specific commands are gated
# later at their call sites.
#
# NF == 8 for the reason lb_filter_services carries its own count, though the
# counts differ because the jsonpaths do. A list cut mid-record leaves a
# fragment whose remaining columns still read as values, and a cut landing
# inside the nodeName column produces a half-parsed node this capture would then
# open a node-<name>.txt for. Eight fields per record is what the jsonpath
# emits: six this filter reads, then hostNetwork for the baseline filter below,
# then a constant `eol` whose only job is to be the thing a truncation destroys.
# Both are checked here even though neither is read, because that is what makes
# a cut anywhere in the row detectable.
pod_filter_affected() {
  awk -F'|' 'NF == 8 && $8 == "eol" && $4 != "" && $5 != "True" && $6 != "Succeeded" && $6 != "Failed"'
}

# pod_first_ready <node>: stdin = the cluster-wide
# `namespace|name|podIP|nodeName|ready|phase|hostNetwork` rows, unfiltered.
# Emits `namespace|name|podIP` for the first pod ON <node> that is Ready=True,
# Running, addressed and NOT hostNetwork -- the healthy-pod baseline for that
# node -- then stops, or nothing when the node has no eligible pod.
#
# It scopes itself rather than being handed a per-node list, and that is the
# design rather than a convenience. The rows are the same snapshot the affected
# set was computed from, so an affected pod cannot be selected here:
# pod_filter_affected keeps rows whose $5 is not True, this one requires $5 to
# be True, and a row cannot be both. Reading the node separately would need an
# exclusion list instead, because a pod that recovered between the two reads
# reports Ready in the later one and could be offered as the baseline for its
# own failure.
#
# hostNetwork is excluded because such a pod's podIP IS the node's own address,
# and cilium-agent / kube-ovn-cni / ovs-ovn run hostNetwork on every node. A
# baseline picked from those would resolve `ip route get <ip>` to `local <ip>
# dev lo table local` for an entirely ordinary reason -- the exact fingerprint
# the address-attribution capture above exists to recognise -- so the
# comparison would defeat itself. A blank hostNetwork column counts as NOT
# hostNetwork, since the API omits the field when it is false.
#
# The test is written as an allow-list -- empty or the literal "false" -- rather
# than as `!= "true"`, which rejects a value cut mid-token: `tru` carries every
# field and passes every other gate, and because this filter stops at the row it
# takes, a deny-list would seat a hostNetwork pod as the baseline. That alone
# is not enough, though: a cut landing on the separator BEFORE hostNetwork
# leaves the column empty and indistinguishable from a genuine false. That case
# is caught by the constant `eol` column instead -- the row loses the sentinel
# with its tail. The two guards cover different cuts and neither replaces the
# other.
#
# An addressless pod is skipped for the same `exit`-at-first-match reason: a
# Ready pod that has no podIP yet would become the baseline and then carry none
# of the route or conntrack evidence the comparison exists for.
#
# NF == 8 and an intact `eol` for the reason the filters above carry their own
# counts: a list cut mid-record leaves a fragment whose remaining columns still
# read as values, and a fragment here would name a pod that does not exist as
# the baseline every reading of the affected pod gets compared against.
pod_first_ready() {
  awk -F'|' -v node="$1" 'NF == 8 && $8 == "eol" && $4 == node && $3 != "" && $5 == "True" && $6 == "Running" && ($7 == "" || $7 == "false") { print $1 "|" $2 "|" $3; exit }'
}

# How a cutoff should be described, mirroring prevlog_cutoff_desc in the sibling
# script: 137 cannot tell its own kill grace apart from something else killing
# the read, and an empty bound means the read was never bounded here at all.
dp_cutoff_desc() {
  if [ -z "$3" ]; then
    printf '%s' "a signal from outside this script, which ran this read unbounded"
  elif [ "${1:-}" = "137" ]; then
    printf '%s' "a SIGKILL -- the kill grace of its own ${2}s timeout, or something else killing the read, which 137 does not tell apart"
  else
    printf '%s' "its own ${2}s timeout"
  fi
}


# dp_read_outcome <rc> <seconds> <bound> [errfile] -> phrase describing why a
# read produced nothing.
#
# 124 and 137 are the only statuses a bound produces, so they are the only ones
# allowed to name the timeout. Everything else is kubectl answering -- a refused
# connection, an RBAC denial, a kind the cluster does not serve -- and calling
# that a cutoff would put a cause in the artifact that was never observed. The
# sibling capture gates every one of its notes the same way, and this script's
# notes claim to follow it.
dp_read_outcome() {
  if [ "${1:-}" = "124" ] || [ "${1:-}" = "137" ]; then
    printf '%s' "was cut off by $(dp_cutoff_desc "$1" "$2" "$3")"
  elif [ -n "${4:-}" ] && [ -s "${4:-}" ]; then
    printf 'failed: kubectl exited %s: %s' "$1" "$(tr '\n\r' '  ' <"$4" | cut -c1-300 | sed 's/[[:space:]]*$//')"
  else
    printf 'failed: kubectl exited %s' "$1"
  fi
}

# Kept above the sourcing guard with the other pure helpers, per the note at
# the top of this section: their branches carry the difference between a
# deadline, a kill and a read that had no ceiling, and the unit suite asserts
# them directly rather than inferring them from a capture.

# Sourcing guard: hack/capture-dataplane.bats sets E2E_CAPTURE_DATAPLANE_LIB and
# sources this file purely to reach the helpers above; return before touching $1
# or running any capture so the unit test never needs a cluster. Neither
# executing caller sets it -- not the cozytest.sh EXIT trap, not the Chainsaw
# global catch -- so the guard is a no-op for both.
if [ -n "${E2E_CAPTURE_DATAPLANE_LIB:-}" ]; then
  return 0 2>/dev/null
fi

OUT="${1:?Usage: e2e-capture-dataplane.sh <output-dir>}"

CILIUM_NS="${COZY_CILIUM_NS:-cozy-cilium}"
KUBEOVN_NS="${COZY_KUBEOVN_NS:-cozy-kubeovn}"
# Cap how many pods we inspect so a fully-wedged cluster cannot explode the
# runtime; the per-command timeouts and the call-site wall-clock wrapper are the
# other two bounds. Node-global captures are deduped per node, and so is the
# healthy-pod baseline, so the total is (#affected-pods) per-pod captures plus
# (#affected-nodes) baselines and node-global captures -- not two per pod.
#
# The header asks for this arithmetic against the tighter caller rather than
# only the roomier one, so here it is. Every term counts the OUTER bound of
# each call and adds the bounded lookups that call makes; a nested inner
# timeout is not a second wait. The terms are the whole path before
# capture_lb_datapath, not only the per-pod ones: the preamble is 82s of the
# total, and a sum that omits it reports a margin roomy enough for another
# collector where the real one has no room at all.
#  - preamble, once per run: 82s -- the cluster pod list at 28s+2s, the
#    ovn-central lookup at 20s+2s, and the 30s ovn-sbctl lflow-list;
#  - a node capture bounds at 288s: 222s of execs (the `cilium-dbg monitor`
#    leg counts its outer 12s, not the inner 8s it wraps) plus three 22s
#    pod_on_node lookups, memo misses the first time a node is seen;
#  - an affected pod's capture bounds at 179s: 135s of execs plus two 20s
#    reads with their 2s kill graces;
#  - the baseline adds 30s per affected node: it runs at route-only scope, so
#    the route and the conntrack and nothing else, and it issues no read of
#    its own.
#
# One affected pod on one node therefore bounds at 579s, inside the 600s the
# cozytest.sh trap allows and still past the 300s the Chainsaw catch shares
# with its snapshot leg. That margin is 21 seconds, so the next collector
# added to this path does not fit and the number has to be recomputed rather
# than assumed. Read the 21s as what this path leaves the rest of the run, not
# as headroom the run has: capture_lb_datapath executes unconditionally after
# it and inside the same backstop, and the MAX_LBS note above says that section
# bounds work rather than wall clock, with each heavy capture costing tens of
# seconds.
#
# The 579s holds only WHILE the lookup memo exists. When mktemp has failed and
# _POD_MEMO is empty every pod_on_node is live again, each capture pays for
# its own, and the same case bounds at 667s -- outside both backstops. The
# guarantee is conditional and this is the condition.
#
# Route-only scope is what buys it. At full scope the baseline costs 179s
# instead of 30s, the single-pod case bounds at 728s, and it would lose a
# backstop it clears at the merge base (519s there). The three blocks that
# scope drops describe how a pod was programmed, and a pod chosen for being
# healthy is programmed correctly by definition -- they compare nothing.
#
# What the numbers do say is that this cap counts pods while the ceiling is
# measured in seconds, and past one affected pod the two stop meeting: raising
# MAX_PODS buys nothing the envelope can pay for.
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
# bound is the outer `timeout -k 30 <n>` backstop at the call site (truncate +
# hard-kill), since each heavy capture itself takes tens of seconds. That budget
# is per-caller (600s from the cozytest.sh EXIT trap, 300s from the Chainsaw
# global catch, which shares an op envelope with the snapshot leg).
MAX_LBS="${COZY_DATAPLANE_MAX_LBS:-6}"

# Per-read wall-clock bounds for the plain `kubectl get` reads below. Named once
# so a message reporting a cutoff cannot quote a number the read never used, and
# overridable so a test does not have to wait out the real ones. The list bound
# is larger because that call asks about every namespace at once, where the
# others ask about one object.
#
# These bound a single READ, not the script: the sum of every bound here is far
# past any caller's envelope, and deliberately so, because the caps above bound
# work while the outer `timeout -k` bounds wall clock (see the note above). What
# a per-read bound buys is forward progress -- one hung apiserver call can no
# longer consume the whole envelope before anything is written.
DP_READ_TIMEOUT="${COZY_DATAPLANE_READ_TIMEOUT:-20}"
DP_LIST_TIMEOUT="${COZY_DATAPLANE_LIST_TIMEOUT:-28}"
DP_READ_GRACE=2

# Resolved once. Empty when `timeout` is absent: the reads then run unbounded
# rather than every call exiting 127 with its output swallowed, which would
# report that kubectl failed when kubectl never ran. Every read that carries a
# note says which of the two happened, the two EndpointSlice reads inside
# capture_lb_datapath included.
if command -v timeout >/dev/null 2>&1; then
  DP_BOUND="timeout -k $DP_READ_GRACE $DP_READ_TIMEOUT"
  DP_LIST_BOUND="timeout -k $DP_READ_GRACE $DP_LIST_TIMEOUT"
else
  DP_BOUND=""
  DP_LIST_BOUND=""
fi

command -v kubectl >/dev/null 2>&1 || exit 0
mkdir -p "$OUT" 2>/dev/null || exit 0
# Every note below explains why this capture is smaller than the cluster, so it
# ships beside the capture rather than only in the job log: a reader who has the
# uploaded report and not the run cannot otherwise tell a capture that found
# nothing from one that never ran. The sibling collector and the report writer
# both hold this contract, and docs/agents/e2e-testing.md states it for them.
#
# Appended, not truncated, with a separator: a caller may aim two runs at one
# directory, and the earlier run's reasons are as load-bearing as the later
# one's.
NOTES="$OUT/capture-notes.txt"

# Created here rather than beside the helpers above, and the position is the
# point: everything before the sourcing guard runs in every unit test that
# sources this file, so a temp file made up there is one leaked per test, and
# the two early exits would leave one behind on every host without kubectl.
# Below the guard and below those exits, the cleanup at the end is genuinely
# the only path that can be reached once this exists.
#
# Empty if mktemp fails; the notes then omit kubectl's message rather than
# inventing one.
DP_ERR=$(mktemp "${TMPDIR:-/tmp}/dataplane-read.XXXXXX" 2>/dev/null) || DP_ERR=""
if [ -s "$NOTES" ]; then
  printf -- '--- new capture run ---\n' >> "$NOTES" 2>/dev/null || true
fi

# printf, not echo: under /bin/sh (dash on the CI image) echo expands backslash
# escapes, and this logger now carries kubectl's own stderr. A message holding a
# literal \n could then break the note in two and print a second
# "[capture-dataplane] ..." line that reads as this script's own verdict. No
# malice needed -- the jsonpath in these reads contains {"\n"}, and kubectl
# quotes the expression back when it fails to parse it. The tr in
# dp_read_outcome flattens the message on the way in; echo would undo that here,
# on the way out.
log() {
  printf '%s\n' "[capture-dataplane] $*"
  printf '%s\n' "[capture-dataplane] $*" >> "$NOTES" 2>/dev/null || true
}


# Affected = scheduled (has nodeName), Ready!=True, and not already terminal.
# A podIP is intentionally optional: a CNI endpoint leak can strand the pod
# before allocation. Running pods with an IP cover the host->pod probe failure;
# Pending pods without one cover the endpoint-allocation failure. Succeeded/
# Failed pods (completed Jobs, hook pods) are Ready!=True too but are not either
# symptom, so they are excluded to keep the MAX_PODS budget on actual wedges.
# jsonpath keeps this dependency-free (no jq / go-template reassignment).
#
# hostNetwork rides along on this one read even though only the healthy-pod
# baseline consults it, and only for nodes that turn out to have an affected
# pod. Asking per node instead would be a second read of the same objects, and
# the two answers could then disagree: a pod that recovered in between reports
# Ready in the later one and could be offered as the baseline for its own
# failure. One snapshot makes that impossible rather than guarded -- the
# affected set and the baseline candidates are the same rows -- and the extra
# column costs one boolean per pod on a list this script already takes.
# shellcheck disable=SC2086  # empty DP_LIST_BOUND must vanish, not become ""
_pods_raw=$($DP_LIST_BOUND kubectl get pods -A \
  -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{.status.podIP}{"|"}{.spec.nodeName}{"|"}{.status.conditions[?(@.type=="Ready")].status}{"|"}{.status.phase}{"|"}{.spec.hostNetwork}{"|eol"}{"\n"}{end}' \
  2>"${DP_ERR:-/dev/null}")
_pods_rc=$?
if [ "$_pods_rc" -ne 0 ]; then
  log "listing pods $(dp_read_outcome "$_pods_rc" "$DP_LIST_TIMEOUT" "$DP_LIST_BOUND" "$DP_ERR"); whatever it did not name is missing from the capture below, which is not the same as nothing being affected"
fi
affected=$(printf '%s' "$_pods_raw" | pod_filter_affected)

# Set even when the pod path is empty; the per-pod capture references it, while
# the independent LoadBalancer path below does not.
central=""

# pod_on_node <ns> <label> <node> -> first matching pod name (empty if none).
pod_on_node() {
  _pon_key="$1/$2/$3"
  if _pon_hit=$(pod_memo_get "$_pon_key"); then
    printf '%s' "${_pon_hit%%	*}"
    return "${_pon_hit#*	}"
  fi
  # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
  # items[*], not items[0]: client-go's evalArray has no allowMissingKeys escape
  # (evalField does), so indexing [0] into an empty list is a hard error and
  # kubectl exits 1. "No such pod on this node" is the ordinary answer here --
  # a node without a cilium-agent is exactly what this capture is called for --
  # and with a note attached to a non-zero status that answer would be reported
  # as a failed read. [*] yields nothing and exits 0, so a non-zero status again
  # means something actually went wrong.
  _pon=$($DP_BOUND kubectl get pod -n "$1" -l "$2" --field-selector "spec.nodeName=$3" \
    -o jsonpath='{.items[*].metadata.name}' 2>"${DP_ERR:-/dev/null}")
  _pon_rc=$?
  # The note goes to stderr because this function's stdout is captured by its
  # callers, so a log line on stdout would be read as part of the pod name.
  if [ "$_pon_rc" -ne 0 ]; then
    # No consequence named: this helper serves five call sites with three
    # different ones -- a node-global capture skipped, an LB probe not run, an
    # announcer left to the fallback -- so any clause here is wrong for someone.
    # The status goes back with the value instead, and the two callers that
    # write an absence into the artifact use it to say "unknown" rather than
    # "none"; the ones that only skip have nothing to record.
    log "looking up the pod matching $2 on node $3 $(dp_read_outcome "$_pon_rc" "$DP_READ_TIMEOUT" "$DP_BOUND" "$DP_ERR")" >&2
  fi
  # [*] can name several pods; the callers want one. The status goes back to the
  # caller as well: an empty answer means "no such pod" only when the read
  # actually answered, and the callers write that difference into the artifact.
  case "$_pon_rc" in
    0 | 124 | 137) pod_memo_put "$_pon_key" "${_pon%% *}" "$([ "$_pon_rc" -ne 0 ] && echo 1 || echo 0)" ;;
  esac
  printf '%s' "${_pon%% *}"
  [ "$_pon_rc" -eq 0 ]
}

# Per-node captures are node-global (every pod on a node shares one cilium-agent
# / ovs / cni-server), so run them once per node and reuse across that node's
# pods. POSIX-sh membership test over a space-delimited string.
_SEEN_NODES=" "
node_seen() { case "$_SEEN_NODES" in *" $1 "*) return 0 ;; esac; return 1; }
mark_node() { _SEEN_NODES="$_SEEN_NODES$1 "; }

# Memo for pod_on_node. The same (namespace, label, node) is asked up to three
# times per affected pod across the sections below, and each repeat now costs
# its own timeout against an apiserver that hangs: the bounds turned a free
# repetition into one that spends the caller's envelope on re-asking rather than
# on more pods. An empty answer is memoised too, since "there is no
# cilium-agent on this node" is exactly the lookup worth not repeating.
#
# A read that failed is stored only when a bound cut it off. An instant failure
# -- refused, denied -- costs nothing to ask again, and caching it would make
# one transient permanent for the run: the LB section would take the hit, report
# the component unknown, and skip the capture this file's header calls the first
# fork of an LB diagnosis. A cutoff is the opposite, since asking again spends
# another full bound, which is the budget this memo exists to protect.
#
# Unlike the node memo above, which is a space-delimited string matched with a
# case glob, this one is a tab-separated file compared field by field, so the
# key needs no quoting and the '=' inside a label selector is ordinary payload.
# The invariant it does rest on is that none of namespace, label selector or
# node name may contain a tab, which Kubernetes object names and label
# selectors cannot.
# Backed by a file rather than a variable on purpose: the walk over affected
# pods runs on the right-hand side of a pipeline, which is a subshell, so a
# variable memo would be discarded at the end of it and the same lookups would
# be paid for again in the sections that follow. Empty when mktemp fails, which
# only costs the memo.
_POD_MEMO=$(mktemp "${TMPDIR:-/tmp}/dataplane-podmemo.XXXXXX" 2>/dev/null) || _POD_MEMO=""
# Cache for the per-node healthy-pod baseline, one file per node, for the same
# reason and with the same subshell constraint as the memo above. Empty when
# mktemp fails, which costs the saving and not the evidence: the baseline is
# then captured per pod, the way it was before the cache existed.
_REF_CACHE=$(mktemp -d "${TMPDIR:-/tmp}/dataplane-refcache.XXXXXX" 2>/dev/null) || _REF_CACHE=""
# The stored row carries the read's status beside its value. A hit that dropped
# it would hand a later caller an empty answer with no way to tell "there is no
# such pod" from "the read never said", which is the distinction the callers
# below are built on.
#
# The status leaves through stdout, packed with the value, because every caller
# reads this through a command substitution and a variable set in there dies
# with the subshell -- the same property that makes the memo itself a file.
pod_memo_get() {
  [ -n "$_POD_MEMO" ] || return 1
  awk -F'\t' -v k="$1" '$1 == k { printf "%s\t%s", $2, $3; found = 1; exit } END { exit !found }' \
    "$_POD_MEMO" 2>/dev/null
}
pod_memo_put() {
  [ -n "$_POD_MEMO" ] || return 0
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$_POD_MEMO" 2>/dev/null || true
}

capture_node() {
  node=$1
  node_seen "$node" && return 0
  mark_node "$node"
  nf="$OUT/node-$node.txt"

  agent=$(pod_on_node "$CILIUM_NS" k8s-app=cilium "$node"); _agent_ok=$?
  ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$node"); _ovs_ok=$?
  {
    echo "################################################################"
    _agent_shown=${agent:-$([ "$_agent_ok" -eq 0 ] && echo '<none>' || echo '<unknown>')}
    _ovs_shown=${ovs:-$([ "$_ovs_ok" -eq 0 ] && echo '<none>' || echo '<unknown>')}
    echo "# NODE $node  (cilium-agent=$_agent_shown ovs=$_ovs_shown)"
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
      if [ "$_agent_ok" -eq 0 ]; then
        echo "(no cilium-agent pod found on node $node)"
      else
        echo "(could not determine whether a cilium-agent runs on node $node -- the lookup did not answer)"
      fi
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
      if [ "$_ovs_ok" -eq 0 ]; then
        echo "(no ovs pod found on node $node)"
      else
        echo "(could not determine whether an ovs pod runs on node $node -- the lookup did not answer)"
      fi
    fi

    cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$node"); _cni_ok=$?
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

      echo
      echo "=== host netns: ip -o addr show, all interfaces (address attribution) ==="
      # ovn0 alone cannot name the owner when a podIP turns out to be a LOCAL
      # address instead: the interface actually holding it might be anything on
      # the host (see the header block). This is the same info scoped to every
      # interface, not just ovn0.
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
        ip -o addr show 2>&1 || true

      echo
      echo "=== host netns: ip route show table local (address attribution) ==="
      # A podIP resolving to `local <ip> dev lo table local` here is the
      # fingerprint from the header block: it wins over the ovn0 route in
      # `main` because table local is consulted first (see `ip rule` above).
      timeout 15 kubectl exec -n "$KUBEOVN_NS" "$cni" -c cni-server -- \
        ip route show table local 2>&1 || true
    else
      echo
      if [ "$_cni_ok" -eq 0 ]; then
        echo "(no kube-ovn-cni pod found on node $node -- host netns capture skipped)"
      else
        echo "(could not determine whether a kube-ovn-cni pod runs on node $node -- the lookup did not answer; host netns capture skipped)"
      fi
    fi
  } >> "$nf" 2>&1 || true
}

# capture_pod_dataplane <ns> <pod> <podip> <node> <label> [scope] -- the
# pod-specific captures for ONE pod on <node>.
#
# scope is `full` (default) or `route-only`. Full takes everything: Ready
# conditions and events, cilium CT, the host route, kernel conntrack, the OVN
# port binding and the OVS ovn-installed flag. route-only takes the route and
# the conntrack and nothing else, and the healthy-pod baseline asks for it.
# Three of the four blocks it drops -- cilium CT, the OVN port binding, the OVS
# ovn-installed flag -- describe how a pod was PROGRAMMED, and a pod chosen for
# being healthy is correctly programmed by definition; they compare nothing and
# cost 105s of the 135s of exec bounds, on the leg most likely to be cut. The
# fourth is the Ready-conditions and events pair, which a pod selected for
# reporting Ready=True and Running has already answered, and dropping it is what
# leaves the baseline with no read of its own -- the 30s the MAX_PODS note above
# spends on it, against 179s at full scope. What a baseline is read for is the
# route and the conntrack beside the wedged pod's own. <label> is
# stamped in the section header and says which role the pod plays: the affected
# pod under investigation, or the Ready pod captured beside it as a baseline.
# Writes to stdout; the caller redirects into the pod's output file. $central
# (the ovn-central pod, or empty) is read from the caller's scope rather than
# threaded as a parameter, matching how the rest of this script shares
# node-global lookups.
capture_pod_dataplane() {
  _cpd_ns=$1; _cpd_pod=$2; _cpd_ip=$3; _cpd_node=$4; _cpd_label=$5; _cpd_scope=${6:-full}

  # Banner first, then the lookups: pod_on_node writes its failure note to
  # stderr, and this whole function is redirected into the pod's file, so a
  # lookup resolved above the banner puts that note on line 1 and pushes the
  # header down. The note still reaches capture-notes.txt either way.
  echo "################################################################"
  echo "# POD $_cpd_ns/$_cpd_pod  podIP=${_cpd_ip:-<none>}  node=$_cpd_node  ($_cpd_label)"
  echo "# node-global captures are in node-$_cpd_node.txt"
  echo "################################################################"

  # Only the cni-server is needed at route-only scope; resolving the other two
  # there would cost a live lookup each whenever the memo is unavailable, for
  # blocks that scope does not take.
  _cpd_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_cpd_node")
  _cpd_agent=""
  _cpd_ovs=""
  if [ "$_cpd_scope" = full ]; then
    _cpd_agent=$(pod_on_node "$CILIUM_NS" k8s-app=cilium "$_cpd_node")
    _cpd_ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$_cpd_node")
  else
    echo
    echo "=== scope: route and conntrack only ==="
    echo "This is a reference pod, so the cilium CT, OVN Port_Binding and OVS"
    echo "ovn-installed blocks are deliberately NOT taken: they describe how a"
    echo "pod was programmed, and this one was chosen for being healthy. Its"
    echo "Ready conditions and events go for the same reason: reporting Ready"
    echo "and Running is what selected it."
    echo "Their absence below is a decision, not a lookup that came back empty."
  fi

  if [ "$_cpd_scope" = full ]; then
    echo
    echo "=== pod Ready conditions + recent probe events ==="
    # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
    $DP_BOUND kubectl get pod -n "$_cpd_ns" "$_cpd_pod" \
      -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason}: {.message}{"\n"}{end}' 2>&1 \
      || echo "(reading this pod's Ready conditions $(dp_read_outcome "$?" "$DP_READ_TIMEOUT" "$DP_BOUND"))"
    # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
    $DP_BOUND kubectl get events -n "$_cpd_ns" --field-selector "involvedObject.name=$_cpd_pod" \
      -o jsonpath='{range .items[*]}{.lastTimestamp}{" "}{.reason}{": "}{.message}{"\n"}{end}' 2>&1 \
      || echo "(reading this pod's events $(dp_read_outcome "$?" "$DP_READ_TIMEOUT" "$DP_BOUND"))"
  fi

  if [ -z "$_cpd_ip" ]; then
    echo
    echo "=== pod has no podIP; IP-specific route/conntrack capture skipped ==="
    echo "Node-global Cilium/OVN state and pod allocation events remain captured."
  fi

  if [ "$_cpd_scope" = full ] && [ -n "$_cpd_ip" ] && [ -n "$_cpd_agent" ]; then
    echo
    echo "=== cilium-dbg bpf ct list global | grep $_cpd_ip (node=$_cpd_node agent=$_cpd_agent) ==="
    timeout 25 kubectl exec -n "$CILIUM_NS" "$_cpd_agent" -c cilium-agent -- \
      sh -c "cilium-dbg bpf ct list global 2>/dev/null | grep -F '$_cpd_ip' || echo 'no CT entries for $_cpd_ip'" 2>&1 || true
  fi

  if [ -n "$_cpd_ip" ] && [ -n "$_cpd_cni" ]; then
    echo
    echo "=== host netns: ip route get $_cpd_ip (via kube-ovn cni-server) ==="
    timeout 15 kubectl exec -n "$KUBEOVN_NS" "$_cpd_cni" -c cni-server -- \
      ip route get "$_cpd_ip" 2>&1 || true

    echo
    echo "=== host netns: kernel conntrack for $_cpd_ip ==="
    # Under enable-host-legacy-routing the host->local-pod path is in the
    # kernel netfilter conntrack table, NOT cilium BPF -- this is the
    # authoritative table for the transient. Prefer the conntrack CLI; fall
    # back to /proc/net/nf_conntrack when it is absent from the image.
    timeout 15 kubectl exec -n "$KUBEOVN_NS" "$_cpd_cni" -c cni-server -- \
      sh -c "if command -v conntrack >/dev/null 2>&1; then conntrack -L 2>/dev/null | grep -F '$_cpd_ip' || echo 'no conntrack entries for $_cpd_ip'; else grep -F '$_cpd_ip' /proc/net/nf_conntrack 2>/dev/null || echo 'no conntrack CLI; no /proc/net/nf_conntrack match for $_cpd_ip'; fi" 2>&1 || true
  fi

  if [ "$_cpd_scope" = full ] && [ -n "$central" ]; then
    echo
    echo "=== OVN Port_Binding / Logical_Switch_Port for $_cpd_pod.$_cpd_ns ==="
    # kube-ovn names the OVN logical port <pod>.<namespace>.
    timeout 20 kubectl exec -n "$KUBEOVN_NS" "$central" -c ovn-central -- \
      ovn-sbctl --no-leader-only find port_binding "logical_port=$_cpd_pod.$_cpd_ns" 2>&1 || true
    timeout 20 kubectl exec -n "$KUBEOVN_NS" "$central" -c ovn-central -- \
      ovn-nbctl --no-leader-only find logical_switch_port "name=$_cpd_pod.$_cpd_ns" 2>&1 || true
  fi

  if [ "$_cpd_scope" = full ] && [ -n "$_cpd_ovs" ]; then
    echo
    echo "=== OVS interface ovn-installed flag + ts for iface-id=$_cpd_pod.$_cpd_ns (ovs pod $_cpd_ovs) ==="
    # kube-ovn stamps external_ids:ovn-installed (+ -ts) on the pod's OVS
    # interface once ovn-controller reports the port programmed -- this is
    # the barrier the CNI waits on. The OVS interface name is opaque, so
    # look it up by iface-id (<pod>.<ns>), then read the flag + timestamp.
    # An ovn-installed-ts set early (before physical_flow_output installed
    # the local-delivery flow, see node-$node.txt) is the I-P-lag signature.
    _cpd_ovsif=$(timeout 20 kubectl exec -n "$KUBEOVN_NS" "$_cpd_ovs" -c openvswitch -- \
      ovs-vsctl --no-heading --columns=name find interface "external_ids:iface-id=$_cpd_pod.$_cpd_ns" 2>/dev/null \
      | head -n 1 | tr -d '" ')
    if [ -n "$_cpd_ovsif" ]; then
      echo "ovs interface = $_cpd_ovsif"
      timeout 20 kubectl exec -n "$KUBEOVN_NS" "$_cpd_ovs" -c openvswitch -- \
        ovs-vsctl get interface "$_cpd_ovsif" external_ids:ovn-installed external_ids:ovn-installed-ts 2>&1 || true
    else
      echo "no OVS interface with external_ids:iface-id=$_cpd_pod.$_cpd_ns"
    fi
  fi
}

# capture_pod_reference <node> <outfile> -- the healthy-pod baseline for a
# NODE, appended to <outfile>. This collector otherwise inspects NotReady pods
# only, so a run has nothing to compare a suspicious route or conntrack state
# against from the same node in the same window. Picks one Ready, Running,
# non-hostNetwork pod on <node> that this run is not already investigating, and
# runs the route-only half of the per-pod capture against it -- the route and
# the conntrack, which is what a baseline is read for.
#
# The baseline belongs to the node, not to any one affected pod, and nothing
# about it depends on which pod asked: the candidates are the rows of the one
# snapshot, and pod_first_ready keeps only the Ready=True ones, which every
# affected pod fails by definition. That is what keeps the answer a function of
# the node alone, and that is the precondition for the cache below. Had
# eligibility been decided by excluding the asking pod instead, one node would
# yield a different baseline for each caller, and a shared cache would then hand
# one pod's answer to another -- including to the pod the answer names. Hence
# the signature: a node, and a file to append to.
#
# The absence is recorded rather than left silent, and so is the difference
# between the two ways of having none: a node with no eligible pod is not a
# read that never answered, and the artifact says which one happened.
capture_pod_reference() {
  _cpr_node=$1; _cpr_of=$2

  # Captured once per NODE, then folded into every affected pod's file from a
  # cache. The pod is a function of the node and nothing else, so several
  # affected pods on one node -- the ordinary shape of a wedged host->local-pod
  # datapath rather than an edge -- would otherwise each pay for a second full
  # per-pod capture of the same healthy pod. The cache is about that capture,
  # not about any read: the candidates come from the cluster-wide pod list this
  # script has already taken.
  _cpr_cache="${_REF_CACHE:+$_REF_CACHE/$_cpr_node}"
  if [ -n "$_cpr_cache" ] && [ -f "$_cpr_cache" ]; then
    cat "$_cpr_cache" >> "$_cpr_of" 2>/dev/null || true
    return 0
  fi
  # With no cache directory the capture still happens, once per pod as before.
  _cpr_sink="${_cpr_cache:-$_cpr_of}"

  # Picked out of the SAME snapshot the affected set came from, which is what
  # makes an affected pod ineligible by construction rather than by exclusion:
  # pod_filter_affected keeps rows whose Ready is not True, pod_first_ready
  # requires Ready True, and one row cannot satisfy both. A second read would
  # reopen that -- a pod recovering between the two reports Ready in the later
  # one, and could be offered as the baseline for its own failure.
  _cpr_ref=$(printf '%s\n' "$_pods_raw" | pod_first_ready "$_cpr_node")

  if [ -z "$_cpr_ref" ]; then
    {
      echo
      echo "################################################################"
      if [ "$_pods_rc" -eq 0 ]; then
        echo "# REFERENCE (Ready) pod on node=$_cpr_node: none found -- no baseline available"
      else
        echo "# REFERENCE (Ready) pod on node=$_cpr_node: unknown -- the pod list did not finish, so whether a baseline exists was never established"
      fi
      echo "################################################################"
    } >> "$_cpr_sink" 2>&1 || true
    if [ -n "$_cpr_cache" ]; then
      cat "$_cpr_cache" >> "$_cpr_of" 2>/dev/null || true
    fi
    return 0
  fi

  _cpr_refns=$(printf '%s' "$_cpr_ref" | cut -d'|' -f1)
  _cpr_refpod=$(printf '%s' "$_cpr_ref" | cut -d'|' -f2)
  _cpr_refip=$(printf '%s' "$_cpr_ref" | cut -d'|' -f3)
  {
    echo
    capture_pod_dataplane "$_cpr_refns" "$_cpr_refpod" "$_cpr_refip" "$_cpr_node" "REFERENCE (Ready) on node=$_cpr_node" route-only
  } >> "$_cpr_sink" 2>&1 || true
  if [ -n "$_cpr_cache" ]; then
    cat "$_cpr_cache" >> "$_cpr_of" 2>/dev/null || true
  fi
}

if [ -z "$affected" ] && [ "$_pods_rc" -ne 0 ]; then
  # Says what is unknown rather than what the read did. The list can answer in
  # part and name only healthy pods, which lands here too, so a line asserting
  # that it never answered would be wrong in exactly the case the note above
  # already describes. The other two branches of this kind are worded the same
  # way for the same reason.
  log "whether any pod is affected is unknown -- checking LoadBalancers independently"
elif [ -z "$affected" ]; then
  log "no scheduled NotReady pods -- skipping host->pod capture; checking LoadBalancers independently"
else
  ncount=$(printf '%s\n' "$affected" | wc -l | tr -d ' ')
  log "capturing host->pod data-plane for up to $MAX_PODS of $ncount affected pod(s) -> $OUT"

  # OVN southbound logical-flow dump is cluster-global, so capture it once
  # rather than per pod. ovn-central is a Deployment (not per-node); any replica
  # answers. --no-leader-only lets a read land on a raft follower instead of
  # erroring.
  # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
  # items[*] for the same reason as pod_on_node: an absent ovn-central is a
  # cluster without kube-ovn, not a read that failed.
  central=$($DP_BOUND kubectl get pod -n "$KUBEOVN_NS" -l app=ovn-central \
    -o jsonpath='{.items[*].metadata.name}' 2>"${DP_ERR:-/dev/null}")
  _central_rc=$?
  central=${central%% *}
  if [ "$_central_rc" -ne 0 ]; then
    # No consequence named here: the lookup can fail after naming a replica, in
    # which case the dump below runs on what it named. The branch that actually
    # skips says so itself.
    log "looking up an ovn-central replica $(dp_read_outcome "$_central_rc" "$DP_READ_TIMEOUT" "$DP_BOUND" "$DP_ERR")"
  fi
  if [ -n "$central" ]; then
    {
      echo "=== ovn-sbctl lflow-list (cluster-global, pod=$central) ==="
      timeout 30 kubectl exec -n "$KUBEOVN_NS" "$central" -c ovn-central -- \
        ovn-sbctl --no-leader-only lflow-list 2>&1 || true
    } > "$OUT/ovn-lflows.txt" 2>&1 || true
  elif [ "$_central_rc" -ne 0 ]; then
    # Same distinction the pod list makes above: a lookup that never answered is
    # not a cluster without ovn-central. Saying otherwise here would contradict
    # the note printed a few lines up.
    log "whether $KUBEOVN_NS runs ovn-central is unknown -- skipping OVN logical-flow dump"
  else
    log "no ovn-central pod in $KUBEOVN_NS -- skipping OVN logical-flow dump"
  fi

  i=0
  printf '%s\n' "$affected" | {
  while IFS='|' read -r ns pod podip node _ready _phase _hostnet _eol; do
    [ -n "$ns" ] && [ -n "$pod" ] && [ -n "$node" ] || continue
    i=$((i + 1))
    if [ "$i" -gt "$MAX_PODS" ]; then
      log "reached MAX_PODS=$MAX_PODS cap; $((ncount - MAX_PODS)) more affected pod(s) NOT captured"
      break
    fi

    # Node-global state (cilium endpoints, monitor, hubble, ovs flows, ip neigh,
    # ip rule, ovn0 addr) -- captured once per node.
    capture_node "$node"

    # Pod-specific state, plus a healthy pod on the same node as a baseline.
    pf="$OUT/pod-$ns-$pod.txt"
    capture_pod_dataplane "$ns" "$pod" "$podip" "$node" "NotReady, Ready=$_ready" > "$pf" 2>&1 || true
    capture_pod_reference "$node" "$pf"
  done
  log "host->pod data-plane capture complete"
  }
fi

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
# Echoes "ok" / "fail" per the result, "unknown" when the cni-server lookup did
# not answer so no probe could be attempted, or nothing when the lookup answered
# that the node has no cni-server pod, when the image ships no probe client, or
# when the exec itself could not run (the decision helper reads an empty set as
# unknown too, and the caller tells the two shapes apart). Prefers a pure
# TCP connect (nc -z) so a non-HTTP backend is not misread as unreachable; the
# curl/wget fallbacks send HTTP and so fail-toward-capture on a non-HTTP port,
# the safe direction (capture is cheap; a missed broken LB is not). Doubles as
# the traffic generator for the tcpdumps.
host_http_probe() {
  _hp_node=$1; _hp_ip=$2; _hp_port=$3
  _hp_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_hp_node"); _hp_rc=$?
  # A lookup that never answered is not a node without a cni-server, and the
  # two have to leave different traces. This emits an explicit unknown; the
  # "no such pod" answer below emits nothing, as do a missing probe client and
  # a failed exec. The caller reads exactly that difference: an outcome set
  # that is present but wholly unknown names the lookup, an empty one names the
  # three ways a probe can fail after the lookup answered.
  #
  # The decision helper reaches the same verdict from either shape -- an
  # all-unknown set and an empty set both come back unknown -- so for
  # capture-or-skip this token changes nothing. It exists for the caller's
  # reason line, which is the only place the distinction reaches a reader.
  if [ "$_hp_rc" -ne 0 ]; then
    echo unknown
    return 0
  fi
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
  _agent=$(pod_on_node "$CILIUM_NS" k8s-app=cilium "$_n"); _lb_agent_ok=$?
  _ovs=$(pod_on_node "$KUBEOVN_NS" app=ovs "$_n"); _lb_ovs_ok=$?
  _cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_n"); _lb_cni_ok=$?
  _lb_a=${_agent:-$([ "$_lb_agent_ok" -eq 0 ] && echo '<none>' || echo '<unknown>')}
  _lb_o=${_ovs:-$([ "$_lb_ovs_ok" -eq 0 ] && echo '<none>' || echo '<unknown>')}
  _lb_c=${_cni:-$([ "$_lb_cni_ok" -eq 0 ] && echo '<none>' || echo '<unknown>')}
  {
    echo
    echo "---------------- $_role node=$_n (cilium=$_lb_a ovs=$_lb_o cni=$_lb_c) ----------------"

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
  # shellcheck disable=SC2086  # empty DP_LIST_BOUND must vanish, not become ""
  _raw=$($DP_LIST_BOUND kubectl get svc -A \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{.spec.type}{"|"}{.status.loadBalancer.ingress[0].ip}{"|"}{.spec.ports[0].port}{"|"}{.spec.ports[0].nodePort}{"|"}{.spec.externalTrafficPolicy}{"\n"}{end}' \
    2>"${DP_ERR:-/dev/null}")
  _raw_rc=$?
  # Unconditional, like every other read's note: a list can fail AFTER emitting
  # rows -- a bound firing mid-stream leaves output on stdout and 124 in $? --
  # and gating this on emptiness would let a partial inventory be captured as a
  # complete one. The pod list splits the same way, an unconditional note here
  # and the empty-versus-unanswered distinction below.
  if [ "$_raw_rc" -ne 0 ]; then
    log "listing services $(dp_read_outcome "$_raw_rc" "$DP_LIST_TIMEOUT" "$DP_LIST_BOUND" "$DP_ERR"); any LoadBalancer it did not name is missing from the capture below"
  fi
  _lbs=$(printf '%s\n' "$_raw" | lb_filter_services)
  if [ -z "$_lbs" ] && [ "$_raw_rc" -ne 0 ]; then
    # A Service list that never answered is not a cluster without
    # LoadBalancers, the same distinction the pod list and the ovn-central
    # lookup make.
    log "whether any LoadBalancer needs capturing is unknown -- skipping LB-datapath capture"
    return 0
  fi
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
  # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
  _speakers=$($DP_BOUND kubectl get pod -n "$METALLB_NS" -l "$SPEAKER_SELECTOR" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.spec.nodeName}{"\n"}{end}' 2>"${DP_ERR:-/dev/null}")
  _speakers_rc=$?
  if [ "$_speakers_rc" -ne 0 ]; then
    # Same reason: a list that failed after naming speakers still resolves an
    # announcer from the ones it named, so the note says what is missing rather
    # than what was given up on.
    log "listing metallb speakers $(dp_read_outcome "$_speakers_rc" "$DP_READ_TIMEOUT" "$DP_BOUND" "$DP_ERR"); any speaker it did not name is missing from the announcer lookup"
  fi
  if [ -n "$_speakers" ] && [ -n "$_speakerlog" ]; then
    # No field-count check here, unlike the three record filters above, and the
    # absence is the answer rather than an oversight. These records carry two
    # fields. A cut in the first leaves no separator at all, so the emptiness
    # test below already rejects it. A cut in the second delivers every
    # separator, so a count sees a whole record: `speaker-1|nod` and
    # `speaker-1|nodeA` are the same shape, and nothing in-band tells a
    # truncated node name from a short one. The test below is therefore as far
    # as any check here can reach -- adding NF == 2 would reject no input it
    # accepts, while reading as a closed gap.
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
      # The status is kept beside the value, as the per-node pod lookups do:
      # these two reads are interpreted by emptiness alone, and without the
      # status a read that never answered writes the same `<none>` into the
      # artifact as a Service that genuinely has no endpoint. The reader who has
      # the uploaded report and not the run cannot then tell the two apart.
      #
      # Neither note asserts a consequence a partial read can falsify. A bound
      # firing mid-stream leaves output on stdout and 124 in $?, so a read that
      # failed can still have named the backend printed below, and a note
      # claiming the value went in as unknown would contradict the block two
      # lines above it.
      #
      # The two say it differently because the reads fail differently. The row
      # read names what is MISSING: a record it never finished is dropped by the
      # filter, so it is genuinely absent from the block. The scalar read cannot
      # borrow that wording -- a number cut mid-token still prints, and `80` cut
      # from `8080` is indistinguishable from a real 80 -- so its note names
      # where the printed value came from instead. The service and pod lists are
      # worded like the first; pod_on_node names no consequence at all.
      # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
      _eps=$($DP_BOUND kubectl get endpointslices -n "$_ns" \
        -l "kubernetes.io/service-name=$_name" \
        -o jsonpath='{range .items[*]}{range .endpoints[*]}{.addresses[0]}{"|"}{.nodeName}{"|"}{.targetRef.namespace}{"|"}{.targetRef.name}{"|"}{.conditions.ready}{"\n"}{end}{end}' \
        2>"${DP_ERR:-/dev/null}")
      _eps_rc=$?
      if [ "$_eps_rc" -ne 0 ]; then
        log "listing the endpointslices of $_ns/$_name $(dp_read_outcome "$_eps_rc" "$DP_READ_TIMEOUT" "$DP_BOUND" "$DP_ERR"); any endpoint it did not name is missing from the backend line below"
      fi
      _backend=$(printf '%s\n' "$_eps" | lb_first_ready_endpoint)
      _epip=$(printf '%s' "$_backend" | cut -d'|' -f1)
      _epnode=$(printf '%s' "$_backend" | cut -d'|' -f2)
      _eptns=$(printf '%s' "$_backend" | cut -d'|' -f3)
      _eptname=$(printf '%s' "$_backend" | cut -d'|' -f4)
      # items[*]/ports[*], not items[0]/ports[0], for the reason pod_on_node
      # spells out: client-go's evalArray has no allowMissingKeys escape, so
      # indexing [0] into an empty list is a hard error and kubectl exits 1. A
      # Service without endpointslices is an ordinary answer here, and now that
      # the status is reported it would otherwise arrive as a failed read. The
      # wildcard yields nothing and exits 0; several ports collapse to the first,
      # the same way the pod lookups take the first name.
      # shellcheck disable=SC2086  # empty DP_BOUND must vanish, not become ""
      _eptport=$($DP_BOUND kubectl get endpointslices -n "$_ns" \
        -l "kubernetes.io/service-name=$_name" \
        -o jsonpath='{.items[*].ports[*].port}' 2>"${DP_ERR:-/dev/null}")
      _eptport_rc=$?
      _eptport=${_eptport%% *}
      if [ "$_eptport_rc" -ne 0 ]; then
        log "reading the target port of $_ns/$_name $(dp_read_outcome "$_eptport_rc" "$DP_READ_TIMEOUT" "$DP_BOUND" "$DP_ERR"); the port below is what arrived before it stopped, which may be nothing at all or a value cut short"
      fi
      # Absent versus never-read, once per value, in the vocabulary the per-node
      # captures already use.
      _ep_absent=$([ "$_eps_rc" -eq 0 ] && echo '<none>' || echo '<unknown>')
      _eptport_absent=$([ "$_eptport_rc" -eq 0 ] && echo '<none>' || echo '<unknown>')

      # Announcer node = node of the most recent serviceAnnounced for the IP.
      _annode=$(lb_announcer_node "$_lbip" < "${_speakerlog:-/dev/null}")

      _of="$OUT/lb-$_ns-$_name.txt"
      {
        echo "################################################################"
        echo "# LB $_ns/$_name  ip=$_lbip port=$_lbport nodePort=${_np:-<none>} etp=${_etp:-<default>}"
        echo "# backend: ip=${_epip:-$_ep_absent} node=${_epnode:-$_ep_absent} pod=${_eptns:-$_ep_absent}/${_eptname:-$_ep_absent} targetPort=${_eptport:-$_eptport_absent}"
        echo "# announcer node: ${_annode:-<unknown>}"
        echo "################################################################"
      } > "$_of" 2>&1 || true

      # Gate: probe the LB IP from the announcer node's host netns (fall back to
      # the endpoint node if the announcer is unknown). Reachable -> record and
      # skip the heavy capture; unreachable -> characterise both hops; nothing
      # probed -> say that, and say which of the ways it happened.
      #
      # Every path that runs zero probes ends on the unknown outcome, carrying
      # the reason that path can actually support. Five of them used to arrive
      # at the "reachable, skipped" line: no node to probe from, a Service with
      # no port, a probe node the lookup says runs no kube-ovn-cni pod, a host
      # netns with no TCP/HTTP client, and an exec that could not run at all. A
      # sixth, the lookup itself not answering, already reported unknown and
      # still does. None of the five observed the address. Inferring reachability from an
      # unattempted probe is what this section must never write down -- an image
      # without nc/curl/wget alone would stamp every LB in the cluster reachable
      # and skip the entire heavy capture, which reads as a healthy datapath.
      _probenode="${_annode:-$_epnode}"
      _decision=unknown
      if [ -z "$_probenode" ]; then
        _why="nowhere to probe from -- neither an announcer nor an endpoint node is known"
      elif [ "$_lbport" = "0" ]; then
        _why="the Service names no port to probe"
      else
        # Held in a variable rather than piped straight in, because the SHAPE of
        # the outcome set is the only place the two routes to an unknown verdict
        # stay apart, and the reason line has to name a cause that was actually
        # observed. host_http_probe emits an explicit unknown only when the
        # cni-server lookup did not answer; everything else that stops a probe
        # emits nothing.
        _outcomes=$( { host_http_probe "$_probenode" "$_lbip" "$_lbport"
                       host_http_probe "$_probenode" "$_lbip" "$_lbport"
                       host_http_probe "$_probenode" "$_lbip" "$_lbport"; } )
        _decision=$(printf '%s\n' "$_outcomes" | lb_capture_decision)
        if [ -z "$_outcomes" ]; then
          # Three ways in, and this script genuinely cannot tell them apart: the
          # lookup answered that the node runs no kube-ovn-cni pod, its host
          # netns ships no nc/curl/wget, or the exec could not run at all (no
          # timeout on its PATH, exec denied, the pod gone mid-probe). The
          # exec's status is swallowed by design -- this collector never fails a
          # job -- so they are reported as the set they are rather than guessed
          # apart. What is NOT said here is that the lookup went unanswered: it
          # answered, and pod_on_node keeps that distinction for its callers.
          _why="no probe outcome from $_probenode -- no kube-ovn-cni pod there, or no TCP/HTTP client in its host netns, or the exec into it could not run"
        else
          # Outcomes exist and the verdict is still unknown, which the decision
          # helper only returns when every one of them is unknown -- and that
          # token has exactly one source.
          _why="no probe could be attempted from $_probenode -- the cni-server lookup did not answer"
        fi
      fi

      if [ "$_decision" = "unknown" ]; then
        echo "probe: whether the LB is reachable is unknown -- $_why (no heavy capture)" >> "$_of" 2>&1 || true
        log "LB $_ns/$_name ($_lbip) not probed -- $_why"
        continue
      fi

      if [ "$_decision" != "capture" ]; then
        echo "probe: at least one probe from $_probenode succeeded -- reachable, skipped (no heavy capture)" >> "$_of" 2>&1 || true
        log "LB $_ns/$_name ($_lbip) reachable -- skipped"
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
      # Guarded like the announcer capture above, and for a reason the empty
      # string hides: pod_on_node turns its third argument into
      # `--field-selector spec.nodeName=<node>`, so an empty node asks for pods
      # whose nodeName is EMPTY -- the unscheduled ones. An unknown announcer
      # would then hand the first pending kube-ovn-cni pod to a tcpdump stamped
      # ANNOUNCER, pointing the reader away from the actual problem at the one
      # moment this leg matters: the announcer is unknown exactly when MetalLB
      # is misbehaving.
      _an_cni=""
      if [ -n "$_annode" ]; then
        _an_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_annode")
      else
        echo "(announcer node unknown -- announcer-side tcpdump skipped)" >> "$_of" 2>&1 || true
      fi
      # Same guard on the endpoint side, and it is not the rarer case: a Service
      # with no ready endpoint is the ordinary shape of an LB outage, and both
      # lookups here -- the cni-server pod and the backend's OVS interface --
      # reach pod_on_node with the node in hand.
      _en_cni=""
      _en_iface="$GENEVE_IFACE"
      if [ -n "$_epnode" ]; then
        _en_cni=$(pod_on_node "$KUBEOVN_NS" app=kube-ovn-cni "$_epnode")
        if [ -n "$_eptname" ] && [ -n "$_eptns" ]; then
          _cand=$(ovs_iface_for "$_epnode" "$_eptname.$_eptns")
          [ -n "$_cand" ] && _en_iface="$_cand"
        fi
      else
        echo "(endpoint node unknown -- endpoint-side tcpdump skipped)" >> "$_of" 2>&1 || true
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
      #   Deferred because it needs a tenant kubeconfig/virtctl that neither
      #   caller of this diagnostic has; the host-side ANNOUNCER/ENDPOINT split
      #   already localises the failing hop to host-cilium vs kube-ovn delivery.
    done
    log "LoadBalancer-datapath capture complete"
  }
}

capture_lb_datapath

# The stderr sink is scratch, not evidence: everything worth keeping from it is
# already quoted into a note above. Removed here rather than from an EXIT trap,
# which this repo's suites ban. This is the only exit reachable once the sink
# exists -- the two earlier ones run before it is created -- so the file leaks
# only when the call-site backstop kills the script mid-run, into the runner's
# TMPDIR.
[ -n "$DP_ERR" ] && rm -f "$DP_ERR"
[ -n "$_POD_MEMO" ] && rm -f "$_POD_MEMO"
[ -n "$_REF_CACHE" ] && rm -rf "$_REF_CACHE"

exit 0
