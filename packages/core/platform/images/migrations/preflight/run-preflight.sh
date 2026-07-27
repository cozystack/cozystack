#!/bin/sh
# Cozystack upgrade preflight health gate.
#
# Runs as a Helm pre-upgrade hook (weight 0) ahead of the migration hook
# (weight 1). It executes every check under preflight/checks/ and refuses to let
# the upgrade proceed when the cluster is not in a safe state — UNLESS the
# operator explicitly overrides. This is the base "fails exist = do not start"
# guard: a static backend dump cannot see live storage/DRBD health, so the check
# is taken from the live cluster before any upgrade step runs.
#
# Behaviour is configured entirely through env (rendered from Helm values by
# templates/preflight-hook.yaml):
#   PREFLIGHT_ENABLED     - "true"/"false" master toggle (default true)
#   PREFLIGHT_OVERRIDE    - "true" downgrades every FAIL to a warning and
#                           proceeds; the operator has accepted the risk
#   PREFLIGHT_SKIP        - space/comma list of check ids to skip entirely
#   PREFLIGHT_CHECKS_DIR  - directory of check scripts (default /preflight/checks)
#
# Each check exits: 0 = OK, 2 = FAIL (gate), 3 = N/A. Any other non-zero exit is
# treated as FAIL — a check that cannot determine health must not be assumed
# healthy.
set -eu

CHECKS_DIR="${PREFLIGHT_CHECKS_DIR:-/preflight/checks}"
ENABLED="${PREFLIGHT_ENABLED:-true}"
OVERRIDE="${PREFLIGHT_OVERRIDE:-false}"
SKIP="${PREFLIGHT_SKIP:-}"

# Passed through to the LINSTOR check; centralised here so all checks agree.
export LINSTOR_NS="${LINSTOR_NS:-cozy-linstor}"
export LINSTOR_DEPLOY="${LINSTOR_DEPLOY:-linstor-controller}"

if [ "$ENABLED" != "true" ]; then
  echo "Preflight disabled (PREFLIGHT_ENABLED=$ENABLED); skipping all health checks"
  exit 0
fi

# Normalise the skip list: accept both comma- and space-separated ids.
SKIP=$(printf '%s' "$SKIP" | tr ',' ' ')
is_skipped() {
  for s in $SKIP; do
    [ "$s" = "$1" ] && return 0
  done
  return 1
}

echo "===== Cozystack upgrade preflight ====="

ok_checks=""
na_checks=""
skip_checks=""
fail_checks=""

for chk in "$CHECKS_DIR"/*; do
  [ -f "$chk" ] || continue
  # id = filename with the numeric ordering prefix and .sh suffix stripped.
  id=$(basename "$chk" | sed 's/^[0-9]*-//; s/\.sh$//')

  if is_skipped "$id"; then
    echo "--- SKIP (operator-skipped via preflight.skipChecks): $id"
    skip_checks="$skip_checks $id"
    continue
  fi

  echo "--- running check: $id"
  chmod +x "$chk" 2>/dev/null || true
  rc=0
  "$chk" || rc=$?
  case "$rc" in
    0) ok_checks="$ok_checks $id" ;;
    3) na_checks="$na_checks $id" ;;
    *) fail_checks="$fail_checks $id" ;;
  esac
done

echo ""
echo "===== PREFLIGHT SUMMARY ====="
echo "  OK:       ${ok_checks:- (none)}"
echo "  N/A:      ${na_checks:- (none)}"
echo "  skipped:  ${skip_checks:- (none)}"
echo "  FAILED:   ${fail_checks:- (none)}"

if [ -n "$fail_checks" ]; then
  if [ "$OVERRIDE" = "true" ]; then
    echo ""
    echo "!! Preflight FAILED for:${fail_checks}"
    echo "!! preflight.override=true is set — the operator has explicitly accepted"
    echo "!! the risk, so the failing checks are downgraded to warnings and the"
    echo "!! upgrade will proceed. This is logged for the audit trail."
    exit 0
  fi
  echo ""
  echo "Preflight FAILED for:${fail_checks}"
  echo "The upgrade is BLOCKED because the cluster is not in a safe state."
  echo ""
  echo "Fix the issues reported above and retry the upgrade."
  echo "If this degraded state is expected and you accept the risk, re-run with"
  echo "either:"
  echo "  preflight.override: true            # proceed past all failing checks"
  echo "  preflight.skipChecks: [<check-id>]  # skip specific checks only"
  exit 1
fi

echo ""
echo "Preflight passed — cluster is healthy, proceeding with the upgrade."
exit 0
