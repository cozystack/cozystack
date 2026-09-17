#!/bin/bash
# Step 05: Restore the standalone VMDisk in-place from its backup.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 5: Restore VMDisk In-Place"

log_step "Creating RestoreJob 'vmdisk-restore-in-place' in namespace $NAMESPACE..."
log_info "In-place restore: the original PVC is renamed and the disk is restored from backup"
log_command "kubectl apply -f - (RestoreJob: vmdisk-restore-in-place)"

# In-place restore of a standalone VMDisk. The controller prepares the target the
# same way it does for a VMInstance disk — suspends the vm-disk-<name> HelmRelease
# and deletes its DataVolume so Flux/CDI do not race the data mover, and (with
# keepOriginalPVC) renames the live PVC to <name>-orig-<hash> — then the data
# mover recreates and repopulates the PVC from the backup.
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
    keepOriginalPVC: true # rename the original PVC to <name>-orig-<hash> before restoring
EOF

log_success "RestoreJob created"

log_step "Waiting for RestoreJob to complete..."
wait_for_field restorejob vmdisk-restore-in-place '{.status.phase}' Succeeded "$NAMESPACE" "${RESTORE_WAIT:-1800}" Failed

# The RestoreJob reaching Succeeded means Velero re-created the objects; prove the
# restored VMDisk reconciles back to a Bound, Ready disk.
log_step "Waiting for the restored VMDisk to become Ready again..."
kubectl -n "$NAMESPACE" wait hr vm-disk-backup-src --for=condition=ready --timeout="${RESTORE_WAIT:-600}s"
wait_for_field pvc vm-disk-backup-src '{.status.phase}' Bound "$NAMESPACE" 300

# The waits above pass on a Bound disk of any provenance; a restore that moved no
# bytes would leave nothing to see. Velero stamps every object it restores with
# velero.io/restore-name, so require the recreated PVC to carry it: this is the
# one signal only an actual restore produces, and it goes red if the PVC was not
# recreated from the backup.
restore_label=$(kubectl -n "$NAMESPACE" get pvc vm-disk-backup-src \
    -o "jsonpath={.metadata.labels.velero\.io/restore-name}")
[[ -n "$restore_label" ]] || {
    log_error "restored PVC carries no velero.io/restore-name label — the disk was not recreated from the backup"
    exit 1
}

kubectl get restorejob vmdisk-restore-in-place -n "$NAMESPACE" -o wide >&2 || true
log_success "Restore completed; VMDisk 'backup-src' recreated from backup (${restore_label}), Bound and Ready"
