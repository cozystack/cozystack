#!/bin/bash
# Step 04: Submit a BackupJob and wait for it to land a restorable
# snapshot. The driver materialises a per-BackupJob FoundationDBBackup CR
# (one running backup directory per cluster); the BackupJob flips to
# Succeeded once backupDetails.snapshotTime > 0 and the operator has
# reconciled the latest spec generation.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 04: Create BackupJob '${BACKUPJOB_NAME}'"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${BACKUPJOB_NAME}
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: FoundationDB
    name: ${FDB_NAME}
  backupClassName: ${BACKUPCLASS_NAME}
EOF

# 45 minutes max (matches the driver-side deadline). The first snapshot has
# to do range-file rotation through the backup_agent before snapshotTime
# advances past zero.
log_substep "Waiting for BackupJob to Succeed (first FoundationDBBackup snapshot)..."
wait_for_field backupjob "$BACKUPJOB_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 2700

backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
[[ -n "$backup_ref" ]] || { log_error "BackupJob succeeded but BackupRef is empty"; exit 1; }
log_success "Backup '${backup_ref}' is Ready."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-restore-in-place.sh"
