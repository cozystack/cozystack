#!/bin/bash
# Clean up all resources created by the demo. Non-interactive: safe to call from
# the e2e harness's finally block, and idempotent (a no-op when a gated-out run
# created nothing). --ignore-not-found returns success for an absent resource but
# does NOT mask a stuck delete: the delete still blocks and, under set -e, a
# teardown that never settles fails this script instead of leaking state into the
# next run (docs/agents/e2e-testing.md convention 4).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup Demo Resources"

# Namespaced request objects first. Deleting the cozystack BackupJob/RestoreJob
# and Backup CRs cascades to the Velero Backup/Restore the controller created.
log_step "Deleting RestoreJob..."
kubectl delete restorejob vmdisk-restore-in-place -n "$NAMESPACE" --ignore-not-found

log_step "Deleting BackupJob and Backup..."
kubectl delete backupjob vmdisk-backup -n "$NAMESPACE" --ignore-not-found
kubectl delete backup vmdisk-backup -n "$NAMESPACE" --ignore-not-found

separator

log_step "Deleting VMDisk (and any renamed original PVC left by the restore)..."
kubectl delete vmdisk backup-src -n "$NAMESPACE" --ignore-not-found
kubectl delete pvc -n "$NAMESPACE" \
    -l app.kubernetes.io/instance=vm-disk-backup-src --ignore-not-found

separator

log_step "Deleting BackupClass and Velero strategy..."
kubectl delete backupclass vmdisk-velero --ignore-not-found
kubectl delete velero.strategy.backups.cozystack.io vmdisk-backup-strategy --ignore-not-found

separator

log_success "Cleanup complete"
