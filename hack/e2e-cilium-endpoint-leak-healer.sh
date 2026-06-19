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
# This watchdog applies the least-disruptive known remedy: instead of
# restarting the agent (which drops the whole node's in-memory registry), it
# evicts only the orphaned endpoint via
#   cilium-dbg endpoint disconnect ipv4:<X>
# which maps to DELETE /endpoint/{id} -> RemoveEndpoint -> unexpose ->
# removeReferencesLocked, clearing the stale IP-keyed entry. The wedged pod's
# next CNI ADD retry then succeeds. Blast radius is one dead endpoint.
#
# It is READ-ONLY until it has positively confirmed an orphan: an endpoint in
# the owning node's registry holds the disputed IP, that endpoint does not back
# a live Running pod with that IP, and a different pod is currently wedged
# requesting the same IP. If anything is ambiguous it logs and does nothing —
# it never disconnects a live endpoint.
#
# REMOVE this script, hack/e2e-cilium-leak-healer.yaml, and the setup_file/
# teardown_file hooks in hack/e2e-install-cozystack.bats once a fixed Cilium
# ships. This is a CI band-aid, not a product fix.
set -u

CILIUM_NS="${CILIUM_NS:-cozy-cilium}"
POLL_INTERVAL="${CILIUM_HEALER_POLL_SEC:-15}"

log() { echo "[cilium-leak-healer $(date -u +%H:%M:%S)] $*"; }

# cilium-agent pod running on a given node (DaemonSet selector k8s-app=cilium).
agent_on_node() {
  kubectl get pod -n "$CILIUM_NS" -l k8s-app=cilium \
    --field-selector "spec.nodeName=$1" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

log "started (ns=$CILIUM_NS poll=${POLL_INTERVAL}s, in-cluster, runs for the cluster lifetime)"

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
    [ -n "$ep_json" ] && [ "$ep_json" != "[]" ] || \
      { log "no endpoint holds ip=$ip on node=$node (already cleared?) for $ns/$pod"; continue; }

    epid=$(printf '%s' "$ep_json" | jq -r '.[0].id // empty' 2>/dev/null)
    epns=$(printf '%s' "$ep_json" | jq -r '.[0].status."external-identifiers"."k8s-namespace" // empty' 2>/dev/null)
    eppod=$(printf '%s' "$ep_json" | jq -r '.[0].status."external-identifiers"."k8s-pod-name" // empty' 2>/dev/null)

    # If that endpoint claims a pod that is alive on the cluster with this IP,
    # it is a LIVE endpoint -> a genuine duplicate-IP, NOT the leak. Refuse and
    # shout so the real problem is visible instead of silently broken.
    if [ -n "$epns" ] && [ -n "$eppod" ]; then
      owner_ip=$(kubectl get pod -n "$epns" "$eppod" -o jsonpath='{.status.podIP}' 2>/dev/null)
      owner_phase=$(kubectl get pod -n "$epns" "$eppod" -o jsonpath='{.status.phase}' 2>/dev/null)
      if [ "$owner_ip" = "$ip" ] && [ "$owner_phase" = "Running" ]; then
        log "REFUSE ip=$ip on node=$node: held by LIVE pod $epns/$eppod (genuine duplicate IP, not the leak) — investigate"
        continue
      fi
    fi

    # An endpoint that holds the IP but backs no k8s pod (empty k8s identity) is
    # a reserved/infra endpoint — e.g. reserved:ingress, the per-node Cilium
    # Ingress endpoint — that legitimately owns the IP; the duplicate is an IPAM
    # race that handed the same pod-CIDR IP to a pod. Such endpoints both cannot
    # be disconnected ("endpoint may not be associated reserved labels") and must
    # not be: evicting infra is wrong. The correct remedy is to reschedule the
    # wedged pod onto a free IP by deleting it; its controller recreates it and
    # the next CNI ADD picks a fresh IP (verified: the new pod lands on a clean
    # address while the ingress endpoint keeps the disputed one).
    if [ -z "$epns" ] || [ -z "$eppod" ]; then
      log "HEAL node=$node ep=$epid ip=$ip held by reserved/infra endpoint (no k8s pod) -> delete wedged pod $ns/$pod"
      out=$(kubectl delete pod -n "$ns" "$pod" --wait=false 2>&1)
      log "  result: ${out:-<none>}"
      continue
    fi

    # Confirmed stale pod endpoint: endpoint $epid holds $ip, backs a pod that is
    # no longer live, and pod $ns/$pod is wedged requesting the same IP. Evict
    # only this endpoint.
    log "HEAL node=$node agent=$agent ep=$epid ip=$ip wedged=$ns/$pod -> endpoint disconnect ipv4:$ip"
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
