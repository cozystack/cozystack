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
# -m array nesting. The connection_status / disk_state field names were
# confirmed on a live controller (node objects carry connection_status="ONLINE";
# volume objects carry disk_state with values such as UpToDate/Diskless). This
# check is ADVISORY by default (see preflight.advisoryChecks in values.yaml)
# until its FAULTY-state detection is also exercised against a genuinely
# degraded cluster in e2e; a follow-up then flips it to enforcing.
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

# Probe for the controller. Distinguish "genuinely not installed" (NotFound →
# N/A) from "could not ask" (RBAC / timeout / API error → FAIL): treating a
# failed query as "not installed" would silently skip a check the cluster
# actually needs, so only a real NotFound is an N/A.
if probe_out=$(kubectl_t get deploy --namespace "$LINSTOR_NS" "$LINSTOR_DEPLOY" 2>&1); then
  : # installed, continue
else
  case "$probe_out" in
    *NotFound* | *"not found"*)
      pf_log "LINSTOR not installed (no deploy/$LINSTOR_DEPLOY in $LINSTOR_NS); skipping"
      exit 3
      ;;
    *)
      pf_log "cannot determine LINSTOR state (kubectl get deploy failed): $probe_out"
      exit 2
      ;;
  esac
fi

# lx runs the linstor CLI inside the controller pod in machine-readable mode,
# bounded by kubectl_t so a wedged controller (the very state this check targets)
# cannot hang the pre-upgrade hook. kubectl stderr is not silenced so a query
# failure surfaces its real cause in the Job log. Because linstor is advisory by
# default, a query failure (exit 2) is downgraded to a warning by the runner
# rather than blocking the upgrade.
lx() {
  kubectl_t exec --namespace "$LINSTOR_NS" "deploy/$LINSTOR_DEPLOY" -- linstor -m "$@"
}

# --- satellites all ONLINE ---------------------------------------------------
if ! nodes_json=$(lx node list); then
  pf_log "cannot query LINSTOR nodes (see the kubectl error above)"
  exit 2
fi
# Fail CLOSED on a jq parse error (see the nodes-ready check): a malformed
# response must not collapse into an empty "all online" result.
if ! offline=$(printf '%s' "$nodes_json" | jq -r '
  .. | objects
  | select(has("connection_status"))
  | select(.connection_status != "ONLINE")
  | .name' 2>&1); then
  pf_log "cannot parse LINSTOR node list output as expected JSON ($offline)"
  exit 2
fi

if [ -n "$offline" ]; then
  pf_log "the following LINSTOR satellites are NOT online:"
  printf '%s\n' "$offline" | while IFS= read -r n; do
    [ -n "$n" ] && pf_log "  - $n"
  done
  exit 2
fi

# --- no faulty DRBD volume states --------------------------------------------
if ! vol_json=$(lx volume list); then
  pf_log "cannot query LINSTOR volumes (see the kubectl error above)"
  exit 2
fi
if ! faulty=$(printf '%s' "$vol_json" | jq -r '
  [ .. | objects | (.disk_state? // empty) ]
  | map(ascii_upcase)
  | map(select(. == "INCONSISTENT" or . == "FAILED" or . == "DUNKNOWN" or . == "OUTDATED"))
  | unique | .[]' 2>&1); then
  pf_log "cannot parse LINSTOR volume list output as expected JSON ($faulty)"
  exit 2
fi

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
