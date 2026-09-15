#!/bin/bash
# Step 04: Back up the standalone VMDisk via a BackupJob.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 4: Create BackupJob"

log_step "Creating BackupJob 'vmdisk-backup' in namespace $NAMESPACE..."
log_command "kubectl apply -f - (BackupJob: vmdisk-backup)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: vmdisk-backup
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: VMDisk
    name: backup-src
  backupClassName: vmdisk-velero
EOF

log_success "BackupJob created"

log_step "Waiting for BackupJob to complete..."
# 1800s: the Velero CSI data mover copies the whole disk (5Gi) to S3. Fail fast
# if the job flips to Failed instead of polling out the budget.
wait_for_field backupjob vmdisk-backup '{.status.phase}' Succeeded "$NAMESPACE" 1800 Failed

BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjob vmdisk-backup \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
log_success "BackupJob completed; backup artefact: ${BACKUP_NAME}"

kubectl get backups -n "$NAMESPACE" >&2 || true
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./05-restore-in-place.sh" >&2
