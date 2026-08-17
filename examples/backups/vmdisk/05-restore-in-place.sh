#!/bin/bash
# Step 05: Restore the standalone VMDisk in-place from its backup.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 5: Restore VMDisk In-Place"

log_step "Creating RestoreJob 'vmdisk-restore-in-place' in namespace $NAMESPACE..."
log_info "In-place restore: the original PVC is renamed and the disk is restored from backup"
log_command "kubectl apply -f - (RestoreJob: vmdisk-restore-in-place)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: vmdisk-restore-in-place
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: vmdisk-backup
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: VMDisk
    name: backup-src
  options:
    failIfTargetExists: true # fail if the restore target already exists
    keepOriginalPVC: true # rename the original PVC to <name>-orig-<hash> before restoring
EOF

log_success "RestoreJob created"

log_step "Waiting for RestoreJob to complete..."
wait_for_field restorejob vmdisk-restore-in-place '{.status.phase}' Succeeded "$NAMESPACE" 1800 Failed

# The RestoreJob reaching Succeeded means Velero re-created the objects; prove the
# restored VMDisk reconciles back to a Bound, ready disk. A standalone disk is not
# mounted anywhere, so a Ready HelmRelease + Bound PVC is the round-trip proof
# (in-guest data is exercised by the VMInstance round-trip, which boots the VM).
log_step "Waiting for the restored VMDisk to become Ready again..."
kubectl -n "$NAMESPACE" wait hr vm-disk-backup-src --for=condition=ready --timeout=600s
wait_for_field pvc vm-disk-backup-src '{.status.phase}' Bound "$NAMESPACE" 300

kubectl get restorejob vmdisk-restore-in-place -n "$NAMESPACE" -o wide >&2 || true
log_success "In-place restore completed; VMDisk 'backup-src' is Bound and Ready"
