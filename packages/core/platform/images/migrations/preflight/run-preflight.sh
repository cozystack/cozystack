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
#   PREFLIGHT_ADVISORY    - space/comma list of check ids whose FAILURES are
#                           downgraded to non-blocking warnings. Unlike skip the
#                           check still RUNS and reports; it just does not gate.
#                           Used for checks whose live semantics are not yet
#                           verified enough to hard-block an existing customer's
#                           upgrade (see the linstor check).
#   PREFLIGHT_STATUS_CONFIGMAP
#                         - name of the ConfigMap in $NAMESPACE the verdict is
#                           written to (default cozystack-preflight-status).
#                           Empty disables publishing.
#   NAMESPACE             - namespace the status ConfigMap is written to.
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
ADVISORY="${PREFLIGHT_ADVISORY:-}"
STATUS_CM="${PREFLIGHT_STATUS_CONFIGMAP:-cozystack-preflight-status}"
STATUS_NS="${NAMESPACE:-}"

# Passed through to the LINSTOR check; centralised here so all checks agree.
export LINSTOR_NS="${LINSTOR_NS:-cozy-linstor}"
export LINSTOR_DEPLOY="${LINSTOR_DEPLOY:-linstor-controller}"

if [ "$ENABLED" != "true" ]; then
  echo "Preflight disabled (PREFLIGHT_ENABLED=$ENABLED); skipping all health checks"
  exit 0
fi

# Normalise the id lists: accept comma-, space- or newline-separated ids.
SKIP=$(printf '%s' "$SKIP" | tr ',\n\t' '   ')
ADVISORY=$(printf '%s' "$ADVISORY" | tr ',\n\t' '   ')

# in_list <needle> <space-separated list> - membership test on an
# operator-supplied list.
#
# The list is matched as DATA, never re-expanded: it is the word being matched
# and the pattern is built from the check id, so a glob character in an
# operator's skipChecks entry cannot expand against the runner's working
# directory (/migrations in the image) and silently disable a check whose id
# happens to name a file there. That also drops the word-splitting the earlier
# "$@" form needed.
in_list() {
  needle=$1
  haystack=" $2 "
  case "$haystack" in
    *" $needle "*) return 0 ;;
  esac
  return 1
}

run_checks() {
  echo "===== Cozystack upgrade preflight ====="

  ok_checks=""
  na_checks=""
  skip_checks=""
  fail_checks=""
  advisory_checks=""

  discovered=0

  for chk in "$CHECKS_DIR"/*; do
    [ -f "$chk" ] || continue
    discovered=$((discovered + 1))
    # id = filename with the numeric ordering prefix and .sh suffix stripped.
    id=$(basename "$chk" | sed 's/^[0-9]*-//; s/\.sh$//')

    if in_list "$id" "$SKIP"; then
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
      *)
        if in_list "$id" "$ADVISORY"; then
          echo "    (advisory: $id reported a problem but is non-blocking by config; not gating the upgrade)"
          advisory_checks="$advisory_checks $id"
        else
          fail_checks="$fail_checks $id"
        fi
        ;;
    esac
  done

  # A gate that ran nothing must not report the verdict it exists to withhold.
  # The image build already fails when checks/ is empty (RUN chmod +x
  # /preflight/checks/*), but the fail-closed property must hold in the runner
  # itself and not depend on a neighbouring build step: a refactor of the copy
  # layout, or an operator pointing PREFLIGHT_CHECKS_DIR somewhere wrong, must
  # block rather than wave the upgrade through.
  if [ "$discovered" -eq 0 ]; then
    echo ""
    echo "No preflight checks were found in $CHECKS_DIR."
    echo "The gate cannot vouch for a cluster it never inspected, so the upgrade"
    echo "is BLOCKED. Check the image contents and PREFLIGHT_CHECKS_DIR."
    return 1
  fi

  echo ""
  echo "===== PREFLIGHT SUMMARY ====="
  echo "  OK:                     ${ok_checks:- (none)}"
  echo "  N/A:                    ${na_checks:- (none)}"
  echo "  skipped:                ${skip_checks:- (none)}"
  echo "  advisory (non-blocking):${advisory_checks:- (none)}"
  echo "  FAILED (blocking):      ${fail_checks:- (none)}"

  # Hand the outcome lists back to the parent shell. This function runs in a
  # subshell (it is the left leg of the tee pipeline below), so plain variables
  # would not survive; the facts file is how the status publisher sees them.
  write_facts

  if [ -n "$fail_checks" ]; then
    if [ "$OVERRIDE" = "true" ]; then
      echo ""
      echo "!! Preflight FAILED for:${fail_checks}"
      echo "!! preflight.override=true is set — the operator has explicitly accepted"
      echo "!! the risk, so the failing checks are downgraded to warnings and the"
      echo "!! upgrade will proceed. This is logged for the audit trail."
      return 0
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
    return 1
  fi

  echo ""
  if [ -n "$advisory_checks" ]; then
    echo "Preflight passed (blocking checks). Advisory checks reported problems"
    echo "(${advisory_checks} ) but are non-blocking; review the warnings above."
  else
    echo "Preflight passed — cluster is healthy, proceeding with the upgrade."
  fi
  return 0
}

# --- durable verdict --------------------------------------------------------
# WHY THIS EXISTS: the failed Job is NOT a durable record of the block. The
# platform HelmRelease is generated with Upgrade.Strategy=RetryOnFailure and a
# retry interval that defaults to 30s (internal/operator/package_reconciler.go,
# cmd/cozystack-operator --helmrelease-retry-interval), and helm deletes a
# before-hook-creation hook at the top of every attempt. So a blocking verdict
# and its Job log live for about one retry interval before being replaced by an
# identical run, and the only thing left on the HelmRelease is a generic
# "pre-upgrade hooks failed" that names neither the check nor the node.
#
# The verdict is therefore mirrored into a ConfigMap that no hook lifecycle
# touches. It is written twice: once when the run starts (so a Job killed by
# activeDeadlineSeconds still leaves evidence that it started and when), and
# once with the outcome. Publishing is strictly best-effort: it must never
# change the gate's answer, so every failure here is a warning, not a verdict.
FACTS=$(mktemp 2>/dev/null || echo /tmp/preflight-facts)
RUN_LOG=$(mktemp 2>/dev/null || echo /tmp/preflight-log)
RC_FILE=$(mktemp 2>/dev/null || echo /tmp/preflight-rc)

write_facts() {
  {
    printf 'ok=%s\n' "${ok_checks# }"
    printf 'na=%s\n' "${na_checks# }"
    printf 'skipped=%s\n' "${skip_checks# }"
    printf 'advisory=%s\n' "${advisory_checks# }"
    printf 'failed=%s\n' "${fail_checks# }"
  } >"$FACTS"
}

fact() {
  sed -n "s/^$1=//p" "$FACTS" 2>/dev/null || true
}

# publish_status <verdict> [log-file]
publish_status() {
  verdict=$1
  logfile=${2:-}

  if [ -z "$STATUS_CM" ] || [ -z "$STATUS_NS" ]; then
    return 0
  fi

  set -- configmap "$STATUS_CM" --namespace "$STATUS_NS" \
    --from-literal=verdict="$verdict" \
    --from-literal=completedAt="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --from-literal=failedChecks="$(fact failed)" \
    --from-literal=advisoryChecks="$(fact advisory)" \
    --from-literal=skippedChecks="$(fact skipped)" \
    --from-literal=okChecks="$(fact ok)" \
    --from-literal=naChecks="$(fact na)"

  # The log is the operator-facing part, so keep it, but bounded: a ConfigMap
  # caps at ~1 MiB and a rejected write would lose the verdict fields too.
  if [ -n "$logfile" ] && [ -f "$logfile" ]; then
    tail -c 100000 "$logfile" >"$RUN_LOG.trunc" 2>/dev/null || true
    set -- "$@" --from-file=log="$RUN_LOG.trunc"
  fi

  if kubectl create "$@" --dry-run=client --output=yaml 2>/dev/null |
     kubectl apply --filename - >/dev/null 2>&1; then
    return 0
  fi
  echo "WARNING: could not publish the preflight verdict to configmap/$STATUS_CM"
  echo "         in namespace $STATUS_NS; the verdict below still stands."
  return 0
}

: >"$FACTS"
publish_status running

# The left leg of the pipe is a subshell, so the outcome travels back through a
# file. `|| rc_inner=$?` is what keeps `set -e` from killing that subshell on a
# blocking verdict before it can record one.
: >"$RC_FILE"
{ rc_inner=0; run_checks || rc_inner=$?; echo "$rc_inner" >"$RC_FILE"; } 2>&1 | tee "$RUN_LOG"
rc=$(cat "$RC_FILE" 2>/dev/null || true)
# An empty file means the runner died without recording an outcome (killed
# mid-run, disk full). Fail closed.
[ -n "$rc" ] || rc=1

case "$rc" in
  0) publish_status passed "$RUN_LOG" ;;
  *) publish_status blocked "$RUN_LOG" ;;
esac

exit "$rc"
