#!/bin/sh
# Preflight check: every Kubernetes Node is Ready.
#
# A NotReady node before an upgrade means its workloads cannot be drained or
# rescheduled and the platform HelmRelease rollout can wedge mid-flight. That is
# a hard gate (exit 2).
#
# Cordoned nodes (spec.unschedulable) are exempt from that gate REGARDLESS of
# readiness, and reported as a warning instead. The cordon is the operator
# saying "this node is deliberately out of service", and the rationale above
# does not apply to it: the normal maintenance sequence is cordon, drain, power
# off, which leaves a cordoned node at Ready=Unknown with its workloads already
# moved. Blocking there would mean no cluster with a node out for a disk swap
# could take a version-advancing upgrade without an override. The trade-off is
# stated in values.yaml: a node that failed and was then cordoned by an
# operator or an automation is waved through on the same signal.
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
# partially-registered node — instead of passing it vacuously. Cordoned nodes
# are filtered out here, not later: see the exemption in the header.
# Fail CLOSED on a jq parse error: a malformed or schema-incompatible response
# from a "successful" kubectl call must not be silently turned into an empty
# result that reads as "all nodes Ready". Capture jq stderr and gate on its exit.
if ! not_ready=$(printf '%s' "$nodes_json" | jq -r '
  .items[]
  | select(.spec.unschedulable != true)
  | select((any(.status.conditions[]?; .type == "Ready" and .status == "True")) | not)
  | .metadata.name' 2>&1); then
  pf_log "cannot parse 'kubectl get nodes' output as expected JSON ($not_ready) — refusing to assume the cluster is healthy"
  exit 2
fi

if [ -n "$not_ready" ]; then
  pf_log "the following nodes are NOT Ready:"
  printf '%s\n' "$not_ready" | while IFS= read -r n; do
    [ -n "$n" ] && pf_log "  - $n"
  done
  exit 2
fi

# Cordoned nodes are reported with their readiness, because the exemption above
# means this warning is the only place a cordoned-and-NotReady node shows up.
# Fail closed here too: the blocking query already excluded these nodes, so a
# silent parse failure would hide exactly the set that was waved through.
if ! cordoned=$(printf '%s' "$nodes_json" | jq -r '
  .items[]
  | select(.spec.unschedulable == true)
  | .metadata.name + " (Ready="
    + (first(.status.conditions[]? | select(.type == "Ready") | .status) // "unknown")
    + ")"' 2>&1); then
  pf_log "cannot parse the cordoned-node list from 'kubectl get nodes' ($cordoned)"
  exit 2
fi

if [ -n "$cordoned" ]; then
  pf_log "WARNING: the following nodes are cordoned (SchedulingDisabled) and are"
  pf_log "WARNING: exempt from the readiness gate; not blocking:"
  printf '%s\n' "$cordoned" | while IFS= read -r n; do
    [ -n "$n" ] && pf_log "  - $n"
  done
fi

pf_log "all nodes are Ready"
exit 0
