#!/bin/sh
# Preflight check: every Kubernetes Node is Ready.
#
# A NotReady node before an upgrade means its workloads cannot be drained or
# rescheduled and the platform HelmRelease rollout can wedge mid-flight. That is
# a hard gate (exit 2). Cordoned-but-Ready nodes (spec.unschedulable) are a
# deliberate operator action during maintenance, so they are only a warning, not
# a block.
#
# Exit codes (the preflight runner contract):
#   0 - OK
#   2 - FAIL (gate the upgrade)
#   3 - N/A (not applicable / skipped)
set -eu

# shellcheck disable=SC2034  # PF_CHECK is read by pf_log in the sourced lib below
PF_CHECK=nodes-ready
# shellcheck source=../lib/preflight-lib.sh
. "$(dirname "$0")/../lib/preflight-lib.sh"

nodes_json=$(kubectl get nodes -o json 2>/dev/null) || {
  pf_log "cannot list nodes (kubectl get nodes failed) — refusing to assume the cluster is healthy"
  exit 2
}

not_ready=$(printf '%s' "$nodes_json" | jq -r '
  .items[]
  | select(any(.status.conditions[]?; .type == "Ready" and .status != "True"))
  | .metadata.name' 2>/dev/null || true)

if [ -n "$not_ready" ]; then
  pf_log "the following nodes are NOT Ready:"
  printf '%s\n' "$not_ready" | while IFS= read -r n; do
    [ -n "$n" ] && pf_log "  - $n"
  done
  exit 2
fi

cordoned=$(printf '%s' "$nodes_json" | jq -r '
  .items[] | select(.spec.unschedulable == true) | .metadata.name' 2>/dev/null || true)

if [ -n "$cordoned" ]; then
  pf_log "WARNING: the following nodes are cordoned (SchedulingDisabled); not blocking:"
  printf '%s\n' "$cordoned" | while IFS= read -r n; do
    [ -n "$n" ] && pf_log "  - $n"
  done
fi

pf_log "all nodes are Ready"
exit 0
