#!/bin/sh
# Preflight check: LINSTOR/DRBD storage is healthy.
#
# WHY THIS MUST RUN AGAINST A LIVE CONTROLLER:
# The LINSTOR k8s-backend (internal.linstor.linbit.com CRDs) persists only
# static flags (DELETE / DISKLESS / TIE_BREAKER). Live DRBD replication health
# (Inconsistent, Outdated, Failed volumes, offline satellites) lives ONLY in
# satellite memory and is reachable exclusively by asking a running controller.
# A static backend dump therefore cannot answer "is the storage healthy right
# now?" — so this gate queries the live controller BEFORE any upgrade step can
# stop it.
#
# SCOPE (v1): all satellites ONLINE + no volume in an unambiguously-bad
# disk_state. The bad states are a DENYLIST (Inconsistent/Failed/DUnknown/
# Outdated), never an allowlist, so an unknown-but-healthy state can never
# false-positive and block a good cluster. StandAlone-connection and
# quorum-detail reporting are a deliberate follow-up, not covered here.
#
# SCHEMA NOTE: parsing uses `linstor -m` (machine/JSON output — the stable API
# contract) and recursive descent (`.. | objects`), so it is independent of the
# -m array nesting. The connection_status / disk_state field values are LINBIT
# REST API v1 values that have NOT yet been confirmed against a live controller
# in e2e. Because of that, this check is ADVISORY by default (see
# preflight.advisoryChecks in values.yaml): a wrong assumption here warns but
# does not hard-block a healthy upgrade. Once cozystack-pr-test confirms the
# field names/values on a live controller, a follow-up flips it to enforcing.
#
# Exit codes (the preflight runner contract):
#   0 - OK
#   2 - FAIL (gate the upgrade)
#   3 - N/A (LINSTOR not installed)
set -eu

# shellcheck disable=SC2034  # PF_CHECK is read by pf_log in the sourced lib below
PF_CHECK=linstor
# shellcheck source=../lib/preflight-lib.sh
. "$(dirname "$0")/../lib/preflight-lib.sh"

LINSTOR_NS="${LINSTOR_NS:-cozy-linstor}"
LINSTOR_DEPLOY="${LINSTOR_DEPLOY:-linstor-controller}"

if ! kubectl --request-timeout=30s get deploy --namespace "$LINSTOR_NS" "$LINSTOR_DEPLOY" >/dev/null 2>&1; then
  pf_log "LINSTOR not installed (no deploy/$LINSTOR_DEPLOY in $LINSTOR_NS); skipping"
  exit 3
fi

# lx runs the linstor CLI inside the controller pod in machine-readable mode.
# --request-timeout bounds the exec: a wedged controller (the very state this
# check targets) must not hang the pre-upgrade hook. A timeout makes lx exit
# non-zero, which the check treats as a query failure (exit 2); since linstor is
# advisory by default the runner then downgrades that to a warning rather than
# letting the hang block the upgrade.
lx() {
  kubectl --request-timeout=30s exec --namespace "$LINSTOR_NS" "deploy/$LINSTOR_DEPLOY" -- linstor -m "$@"
}

# --- satellites all ONLINE ---------------------------------------------------
nodes_json=$(lx node list 2>/dev/null) || {
  pf_log "cannot query LINSTOR nodes (kubectl exec ... linstor node list failed)"
  exit 2
}
offline=$(printf '%s' "$nodes_json" | jq -r '
  .. | objects
  | select(has("connection_status"))
  | select(.connection_status != "ONLINE")
  | .name' 2>/dev/null || true)

if [ -n "$offline" ]; then
  pf_log "the following LINSTOR satellites are NOT online:"
  printf '%s\n' "$offline" | while IFS= read -r n; do
    [ -n "$n" ] && pf_log "  - $n"
  done
  exit 2
fi

# --- no faulty DRBD volume states --------------------------------------------
vol_json=$(lx volume list 2>/dev/null) || {
  pf_log "cannot query LINSTOR volumes (kubectl exec ... linstor volume list failed)"
  exit 2
}
faulty=$(printf '%s' "$vol_json" | jq -r '
  [ .. | objects | (.disk_state? // empty) ]
  | map(ascii_upcase)
  | map(select(. == "INCONSISTENT" or . == "FAILED" or . == "DUNKNOWN" or . == "OUTDATED"))
  | unique | .[]' 2>/dev/null || true)

if [ -n "$faulty" ]; then
  pf_log "faulty DRBD volume state(s) present on the cluster:"
  printf '%s\n' "$faulty" | while IFS= read -r s; do
    [ -n "$s" ] && pf_log "  - $s"
  done
  pf_log "inspect with: kubectl exec -n $LINSTOR_NS deploy/$LINSTOR_DEPLOY -- linstor resource list --faulty"
  exit 2
fi

pf_log "LINSTOR healthy (all satellites ONLINE, no faulty volume states)"
exit 0
