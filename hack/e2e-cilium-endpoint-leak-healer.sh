#!/bin/sh
# Interim CI self-heal for the Cilium in-memory endpoint leak.
#
# This is the heal loop; it runs as an in-cluster Job (see
# hack/e2e-cilium-leak-healer.yaml), deployed during e2e install and left
# running for the whole cluster lifetime. That is deliberate: an earlier
# version ran this as a background process started in the install bats
# setup_file and killed in teardown_file, but bats *_file hooks fire per file,
# so it died when the install file finished — before any app test ran — and the
# leak recurred in the app phase (e.g. the tenant-Kubernetes CVMI importer)
# unhealed. An in-cluster Job is bound to the cluster, not to one bats file, so
# it covers install AND every app test.
#
# Symptom: on a churn-heavy run a pod sticks in ContainerCreating/Init because
# the cilium-agent rejects its sandbox with
#   [PUT /endpoint/{id}][400] putEndpointIdInvalid "IP ipv4:<X> is already in use"
# repeated until the pod is deleted or the agent is restarted. The IP has no
# live owner: IPAM and the ipcache released it, but the agent's in-memory
# endpointManager still holds a stale Endpoint for it (the previous pod's CNI
# DEL was skipped or raced, so unexpose()->removeReferencesLocked() never ran).
# Neither the operator's CiliumEndpoint-CRD GC nor the agent's veth-based
# endpoint GC reaps it, so it wedges the new pod and fails the run. Tracked
# upstream at cilium/cilium#38313 (closed stale); no released or pre-release
# Cilium fixes the leak and no flag disables it.
#
# This watchdog heals in two tiers, least-disruptive first:
#
#   Tier 1 (per-endpoint, default): evict only the orphaned endpoint via
#     cilium-dbg endpoint disconnect ipv4:<X>
#   which maps to DELETE /endpoint/{id} -> RemoveEndpoint -> unexpose ->
#   removeReferencesLocked, clearing the stale IP-keyed entry; or, when a
#   reserved/infra endpoint holds the IP, delete-reschedule the wedged pod onto
#   a free address. Blast radius is one dead endpoint or one pod.
#
#   Tier 2 (per-node escalation): on some variants the leak lives in agent
#   in-memory state that `endpoint disconnect` cannot reach — the IP is no longer
#   in the queryable endpoint registry (so Tier 1 logs "no endpoint holds ip" and
#   no-ops) yet the agent still rejects the sandbox, and IPAM keeps re-handing the
#   SAME IP, so a delete-rescheduled pod just re-wedges on it: an inescapable
#   loop. When the same (node,ip) survives ESCALATE_AFTER heal cycles of Tier 1,
#   escalate to the only remedy that clears an in-memory leak — restart the node's
#   cilium-agent (delete its pod; the DaemonSet recreates it, rebuilding from
#   CRDs/IPAM without the stale entry). This is heavier — it drops the node's
#   whole in-memory endpoint registry — and a production operator would avoid it,
#   but for a test-only, install-phase healer it is the effective, acceptable
#   remedy. It is bounded by a per-node cap (MAX_ESCALATIONS_PER_NODE) so it can
#   never loop-restart agents, and every action is logged loudly with its reason.
#
# It is READ-ONLY until it has positively confirmed an orphan: an endpoint in
# the owning node's registry holds the disputed IP, that endpoint does not back
# a live Running pod with that IP, and a different pod is currently wedged
# requesting the same IP. If anything is ambiguous it logs and does nothing —
# it never disconnects a live endpoint. The Tier-2 escalation never fires for a
# refused (genuine duplicate-IP) case: only repeated, confirmed leak candidates
# count toward it, so an agent restart can never mask a real product bug.
#
# REMOVE this script, hack/e2e-cilium-leak-healer.yaml, and the setup_file/
# teardown_file hooks in hack/e2e-install-cozystack.bats once a fixed Cilium
# ships. This is a CI band-aid, not a product fix.
set -u

CILIUM_NS="${CILIUM_NS:-cozy-cilium}"
POLL_INTERVAL="${CILIUM_HEALER_POLL_SEC:-15}"

# Tier-2 escalation tuning (see header). ESCALATE_AFTER is how many consecutive
# heal cycles the SAME (node,ip) must stay wedged under Tier-1 before the
# cilium-agent on that node is restarted; the per-node cap bounds how many times
# an agent may be restarted over the cluster lifetime so the watchdog can never
# loop-restart agents.
ESCALATE_AFTER="${CILIUM_HEALER_ESCALATE_AFTER:-3}"
MAX_ESCALATIONS_PER_NODE="${CILIUM_HEALER_MAX_AGENT_RESTARTS:-2}"
STATE_DIR="${CILIUM_HEALER_STATE_DIR:-/tmp/cilium-leak-healer}"

log() { echo "[cilium-leak-healer $(date -u +%H:%M:%S)] $*"; }

# cilium-agent pod running on a given node (DaemonSet selector k8s-app=cilium).
agent_on_node() {
  kubectl get pod -n "$CILIUM_NS" -l k8s-app=cilium \
    --field-selector "spec.nodeName=$1" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

# Per-(node,ip) recurrence and per-node escalation counters, kept on the pod's
# local filesystem. State is intentionally non-durable: if the watchdog crashes
# and the Job restarts it, counters reset to zero, which only costs a few extra
# heal cycles before re-escalating — never an unbounded restart loop, because the
# per-node cap is re-derived from the (then-empty) state and the leak has either
# cleared or recurs legibly.
keyfrag() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_'; }
node_dir() { printf '%s/%s' "$STATE_DIR" "$(keyfrag "$1")"; }
read_count() { c=$(cat "$1" 2>/dev/null); case "$c" in ''|*[!0-9]*) printf '0' ;; *) printf '%s' "$c" ;; esac; }

# bump_seen NODE IP -> increments and prints the (node,ip) recurrence count.
bump_seen() {
  d=$(node_dir "$1"); mkdir -p "$d" 2>/dev/null || true
  f="$d/seen-$(keyfrag "$2")"; n=$(read_count "$f"); n=$((n + 1)); echo "$n" > "$f"; printf '%s' "$n"
}
# reset_node_seen NODE -> clears every (node,*) recurrence counter. One agent
# restart clears the whole node's in-memory registry, so all its IPs get a fresh
# grace window afterwards instead of immediately re-escalating.
reset_node_seen() { rm -f "$(node_dir "$1")"/seen-* 2>/dev/null || true; }
# escalations NODE -> prints how many agent restarts already issued for the node.
escalations() { read_count "$(node_dir "$1")/escalations"; }
# record_escalation NODE -> increments the node's agent-restart counter.
record_escalation() {
  d=$(node_dir "$1"); mkdir -p "$d" 2>/dev/null || true
  f="$d/escalations"; n=$(read_count "$f"); n=$((n + 1)); echo "$n" > "$f"
}

mkdir -p "$STATE_DIR" 2>/dev/null || true
log "started (ns=$CILIUM_NS poll=${POLL_INTERVAL}s, escalate-after=${ESCALATE_AFTER} cycles, max-agent-restarts/node=${MAX_ESCALATIONS_PER_NODE}, in-cluster, runs for the cluster lifetime)"

# Endless loop: the pod is torn down with the ephemeral e2e cluster. Before
# Cilium is serving (early install) the kubectl/exec calls simply fail and the
# loop idles; once the CNI is up it begins healing. Host networking (see the
# manifest) keeps this pod itself immune to the very leak it repairs.
while true; do
  # Collect every "is already in use" rejection still attached to a pod sandbox.
  # The field-selector narrows to the relevant event reason; grep pins the exact
  # agent signature so unrelated sandbox failures are ignored.
  events=$(kubectl get events -A --field-selector reason=FailedCreatePodSandBox \
      -o jsonpath='{range .items[*]}{.involvedObject.namespace}{"|"}{.involvedObject.name}{"|"}{.message}{"\n"}{end}' 2>/dev/null \
    | grep -i "is already in use" | sort -u)

  [ -n "$events" ] && while IFS='|' read -r ns pod msg; do
    [ -n "$ns" ] && [ -n "$pod" ] || continue
    ip=$(printf '%s' "$msg" | grep -oE 'ipv4:[0-9]{1,3}(\.[0-9]{1,3}){3}' | head -1 | cut -d: -f2)
    [ -n "$ip" ] || continue

    # Only act on a pod that is still wedged (re-checked here = freshness gate).
    phase=$(kubectl get pod -n "$ns" "$pod" -o jsonpath='{.status.phase}' 2>/dev/null)
    case "$phase" in Running|Succeeded|"") continue ;; esac

    node=$(kubectl get pod -n "$ns" "$pod" -o jsonpath='{.spec.nodeName}' 2>/dev/null)
    [ -n "$node" ] || continue
    agent=$(agent_on_node "$node")
    [ -n "$agent" ] || { log "no cilium-agent on node=$node for $ns/$pod ip=$ip"; continue; }

    # Does an endpoint in this agent's registry hold the disputed IP at all?
    # (The wedged pod itself was never registered — CreateEndpoint rejected it
    # before exposing — so any endpoint holding the IP is the stale one.)
    ep_json=$(kubectl exec -n "$CILIUM_NS" "$agent" -c cilium-agent -- \
                cilium-dbg endpoint get "ipv4:$ip" -o json 2>/dev/null)
    epid=""; epns=""; eppod=""
    if [ -n "$ep_json" ] && [ "$ep_json" != "[]" ]; then
      epid=$(printf '%s' "$ep_json" | jq -r '.[0].id // empty' 2>/dev/null)
      epns=$(printf '%s' "$ep_json" | jq -r '.[0].status."external-identifiers"."k8s-namespace" // empty' 2>/dev/null)
      eppod=$(printf '%s' "$ep_json" | jq -r '.[0].status."external-identifiers"."k8s-pod-name" // empty' 2>/dev/null)

      # If that endpoint claims a pod that is alive on the cluster with this IP,
      # it is a LIVE endpoint -> a genuine duplicate-IP, NOT the leak. Refuse and
      # shout so the real problem is visible instead of silently broken. This
      # path `continue`s WITHOUT bumping the recurrence counter, so a genuine
      # duplicate never accrues toward the Tier-2 agent restart.
      #
      # Refuse for ANY non-terminal owner phase, not just Running: a Pending owner
      # can already hold a live IPAM allocation (status.podIP is populated once the
      # sandbox is created, before the pod goes Running), so disconnecting it would
      # evict a legitimate endpoint mid-startup. Only a Succeeded/Failed owner is
      # safely past needing its endpoint. (owner_ip == ip already gates on the pod
      # still existing with that IP, so an empty phase from a vanished pod can't
      # reach this branch.)
      if [ -n "$epns" ] && [ -n "$eppod" ]; then
        owner_ip=$(kubectl get pod -n "$epns" "$eppod" -o jsonpath='{.status.podIP}' 2>/dev/null)
        owner_phase=$(kubectl get pod -n "$epns" "$eppod" -o jsonpath='{.status.phase}' 2>/dev/null)
        if [ "$owner_ip" = "$ip" ] && [ "$owner_phase" != "Succeeded" ] && [ "$owner_phase" != "Failed" ]; then
          log "REFUSE ip=$ip on node=$node: held by non-terminal pod $epns/$eppod (phase=$owner_phase; genuine duplicate IP, not the leak) — investigate"
          continue
        fi
      fi
    fi

    # Past the REFUSE gate this is a genuine leak candidate: no endpoint holds the
    # IP (the stale entry is outside the queryable registry; disconnect can't reach
    # it), a reserved/infra endpoint holds it, or a stale pod endpoint holds it.
    # The Tier-1 remedies below cannot clear a leak that lives purely in the
    # agent's in-memory state when IPAM keeps re-handing the SAME IP — the
    # recreated pod re-wedges on it, an inescapable loop. Track recurrence per
    # (node,ip); once the same IP has survived ESCALATE_AFTER heal cycles,
    # escalate (Tier 2) to the only remedy that clears an in-memory leak:
    # restarting the node's cilium-agent. Bounded by a per-node cap so it can
    # never loop-restart agents.
    seen=$(bump_seen "$node" "$ip")
    if [ "$seen" -ge "$ESCALATE_AFTER" ]; then
      esc=$(escalations "$node")
      if [ "$esc" -lt "$MAX_ESCALATIONS_PER_NODE" ]; then
        log "ESCALATE node=$node agent=$agent ip=$ip wedged=$ns/$pod: leaked-IP reuse unhealed after $seen heal cycles of Tier-1 remedies (cilium/cilium#38313) -> restart cilium-agent (restart $((esc + 1))/$MAX_ESCALATIONS_PER_NODE for this node; drops its stale in-memory endpoint registry)"
        out=$(kubectl delete pod -n "$CILIUM_NS" "$agent" --wait=false 2>&1)
        log "  result: ${out:-<none>}"
        record_escalation "$node"
        # One restart clears every leaked IP on the node, so give the whole node a
        # fresh grace window rather than immediately re-escalating each IP.
        reset_node_seen "$node"
      else
        log "ESCALATION CAP reached node=$node (max=$MAX_ESCALATIONS_PER_NODE agent restarts): leaked ip=$ip for $ns/$pod still unhealed — surfacing the failure rather than restarting again (cilium/cilium#38313)"
      fi
      continue
    fi

    # Below the escalation threshold: apply the least-disruptive Tier-1 remedy.
    #
    # No endpoint holds the IP -> nothing for `endpoint disconnect` to act on (the
    # leak, if any, is outside the queryable registry). Log and wait; if it keeps
    # recurring on the same IP the Tier-2 escalation above takes over.
    if [ -z "$epid" ]; then
      log "no endpoint holds ip=$ip on node=$node (already cleared, or leak not in endpoint registry) for $ns/$pod [seen $seen/$ESCALATE_AFTER before agent restart]"
      continue
    fi

    # An endpoint that holds the IP but backs no k8s pod (empty k8s identity) is
    # a reserved/infra endpoint — e.g. reserved:ingress, the per-node Cilium
    # Ingress endpoint — that legitimately owns the IP; the duplicate is an IPAM
    # race that handed the same pod-CIDR IP to a pod. Such endpoints both cannot
    # be disconnected ("endpoint may not be associated reserved labels") and must
    # not be: evicting infra is wrong. The correct Tier-1 remedy is to reschedule
    # the wedged pod onto a free IP by deleting it; its controller recreates it and
    # the next CNI ADD picks a fresh IP (verified: the new pod lands on a clean
    # address while the ingress endpoint keeps the disputed one).
    if [ -z "$epns" ] || [ -z "$eppod" ]; then
      log "HEAL node=$node ep=$epid ip=$ip held by reserved/infra endpoint (no k8s pod) -> delete wedged pod $ns/$pod [seen $seen/$ESCALATE_AFTER before agent restart]"
      out=$(kubectl delete pod -n "$ns" "$pod" --wait=false 2>&1)
      log "  result: ${out:-<none>}"
      continue
    fi

    # Confirmed stale pod endpoint: endpoint $epid holds $ip, backs a pod that is
    # no longer live, and pod $ns/$pod is wedged requesting the same IP. Evict
    # only this endpoint.
    log "HEAL node=$node agent=$agent ep=$epid ip=$ip wedged=$ns/$pod -> endpoint disconnect ipv4:$ip [seen $seen/$ESCALATE_AFTER before agent restart]"
    out=$(kubectl exec -n "$CILIUM_NS" "$agent" -c cilium-agent -- \
            cilium-dbg endpoint disconnect "ipv4:$ip" 2>&1)
    log "  result: ${out:-<none>}"
    # Disconnect can still be rejected (e.g. the endpoint carries reserved labels
    # the identity probe above did not surface). Never leave the pod wedged: fall
    # back to rescheduling it onto a free IP.
    case "$out" in
      *Error*|*error*|*invalid*)
        log "  disconnect failed -> delete wedged pod $ns/$pod"
        out=$(kubectl delete pod -n "$ns" "$pod" --wait=false 2>&1)
        log "  result: ${out:-<none>}"
        ;;
    esac
  done <<EOF
$events
EOF

  sleep "$POLL_INTERVAL"
done
