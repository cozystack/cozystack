#!/bin/bash
# Step 06: In-place restore. Drop the sentinel table on the source instance,
# then ask the Job driver to restore the most recent backup back into the same
# ClickHouse application.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 06: In-place restore from '${BACKUPJOB_NAME}'"

log_substep "Dropping sentinel table to simulate data loss..."
clickhouse_query "$CLICKHOUSE_NAME" "DROP TABLE IF EXISTS default.sentinel SYNC"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_INPLACE_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${BACKUPJOB_NAME}
EOF

log_substep "Waiting for in-place RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

log_substep "Verifying sentinel data is restored..."
count=$(clickhouse_query "$CLICKHOUSE_NAME" "SELECT count() FROM default.sentinel" | tr -d '[:space:]')
# Guard the numeric comparison: if the SELECT failed (e.g. table missing,
# auth error, network blip) the variable carries an error string, and the
# bash arithmetic comparison below silently treats it as 0 - which would
# misreport restore as failed for the wrong reason, or worse, succeed
# against an empty string when the operator switches to (( count >= 1 )).
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric sentinel count: '${count}'"; exit 1; }
if (( count < 1 )); then
    log_error "Sentinel row count after in-place restore is ${count}; expected >= 1"
    exit 1
fi
log_success "In-place restore verified: ${count} sentinel row(s)."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./07-restore-to-copy.sh"
