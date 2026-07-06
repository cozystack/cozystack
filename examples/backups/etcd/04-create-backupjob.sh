#!/bin/bash
# Step 04: Submit an ad-hoc BackupJob, wait for it to land a snapshot in
# S3 (BackupJob.status.phase=Succeeded), and print the resulting
# Backup's driverMetadata so the operator can see the S3 coordinates the
# RestoreJob in step 05 will replay against.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 04: Submit BackupJob '${BACKUPJOB_NAME}' and wait for Succeeded"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${BACKUPJOB_NAME}
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Etcd
    name: ${ETCD_NAME}
  backupClassName: ${BACKUPCLASS_NAME}
EOF

wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 1200 Failed

BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }

log_substep "Backup artefact: ${BACKUP_NAME}"
# Stable, indentation-free projection of the fields a tenant needs to
# verify the backup landed correctly. Previously a `grep -E '^\s+...':`
# pattern walked the YAML, which broke whenever kubectl changed its
# indent depth or wrapped a value. jsonpath survives both.
kubectl -n "$NAMESPACE" get backups.backups.cozystack.io "$BACKUP_NAME" -o json | jq -r '
    {
      phase: .status.phase,
      takenAt: .spec.takenAt,
      applicationRef: .spec.applicationRef,
      strategyRef: .spec.strategyRef,
      driverMetadata: .spec.driverMetadata,
    }'

umask 077
echo "export BACKUP_NAME=${BACKUP_NAME}" > "$SCRIPT_DIR/.backup-name.env"
chmod 600 "$SCRIPT_DIR/.backup-name.env"

log_success "Backup '${BACKUP_NAME}' Ready. Cached in $(basename "$SCRIPT_DIR")/.backup-name.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-restore-in-place.sh"
