#!/bin/bash
# Step 05: Create a BackupJob and wait for it to succeed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 05: Create BackupJob '${BACKUPJOB_NAME}'"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${BACKUPJOB_NAME}
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: ClickHouse
    name: ${CLICKHOUSE_NAME}
  backupClassName: ${BACKUPCLASS_NAME}
EOF

log_substep "Waiting for BackupJob to Succeed (this triggers clickhouse-backup create_remote)..."
# Skip an explicit "wait Running" gate: a fast-completing strategy Pod can
# transition Pending -> Succeeded between two API observations, in which case
# `kubectl wait --for=jsonpath='{.status.phase}'=Running` would time out even
# though the BackupJob actually finished. wait_for_field already polls until
# the terminal phase, which covers both the slow and the fast path.
wait_for_field backupjob "$BACKUPJOB_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
[[ -n "$backup_ref" ]] || { log_error "BackupJob succeeded but BackupRef is empty"; exit 1; }
log_success "Backup '${backup_ref}' is Ready."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./06-restore-in-place.sh"
