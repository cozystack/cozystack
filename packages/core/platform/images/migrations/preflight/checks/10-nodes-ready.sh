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

# kubectl stderr is intentionally NOT silenced: on failure the real API error
# (forbidden, connection refused, timeout) must reach the Job log so the
# operator can see WHY the gate could not verify the cluster, instead of a bare
# "failed" line. kubectl_t bounds the call so a wedged API server cannot hang
# the hook; a timeout exits non-zero here and fails closed (exit 2).
if ! nodes_json=$(kubectl_t get nodes -o json); then
  pf_log "cannot list nodes (kubectl get nodes failed or timed out; see the kubectl error above) — refusing to assume the cluster is healthy"
  exit 2
fi

# A node is NOT ready unless it carries a Ready condition explicitly set to
# "True". Keying on the presence of Ready=="True" (rather than on Ready!="True")
# also flags a node that reports NO Ready condition at all — a brand-new or
# partially-registered node — instead of passing it vacuously.
not_ready=$(printf '%s' "$nodes_json" | jq -r '
  .items[]
  | select((any(.status.conditions[]?; .type == "Ready" and .status == "True")) | not)
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
