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

# The controller suspends the VMDisk's HelmRelease while a RestoreJob runs and
# never resumes it (suspendHelmRelease only ever sets suspend=true), so resume it
# best-effort BEFORE deleting the RestoreJob — otherwise the disk is left
# suspended (same reasoning as examples/backups/vmi/cleanup.sh).
if kubectl -n "$NAMESPACE" get hr vm-disk-backup-src >/dev/null 2>&1; then
    log_substep "Resuming HelmRelease vm-disk-backup-src (no-op if not suspended)..."
    kubectl -n "$NAMESPACE" patch hr vm-disk-backup-src --type=merge \
        -p '{"spec":{"suspend":false}}' >/dev/null || true
fi

# Namespaced request objects first. Deleting the cozystack BackupJob/RestoreJob
# and Backup CRs cascades to the Velero Backup/Restore the controller created.
log_step "Deleting RestoreJob..."
kubectl delete restorejob vmdisk-restore-in-place -n "$NAMESPACE" --ignore-not-found

log_step "Deleting BackupJob and Backup..."
kubectl delete backupjob vmdisk-backup -n "$NAMESPACE" --ignore-not-found
kubectl delete backup vmdisk-backup -n "$NAMESPACE" --ignore-not-found

separator

log_step "Deleting VMDisk and its PVC..."
kubectl delete vmdisk backup-src -n "$NAMESPACE" --ignore-not-found
kubectl delete pvc -n "$NAMESPACE" \
    -l app.kubernetes.io/instance=vm-disk-backup-src --ignore-not-found

# keepOriginalPVC renamed the pre-restore PVC to <name>-orig-<hash> with a bare
# ObjectMeta (no labels/owner) and flipped its PV to Retain, so the label delete
# above misses it. Reclaim it by name and flip the PV back to Delete BEFORE
# removing the PVC so the CSI provisioner reclaims the LINSTOR volume rather than
# stranding it. Let the lookup and patch fail the script (see the vmi cleanup).
if ! all_pvcs=$(kubectl -n "$NAMESPACE" get pvc -o name 2>&1); then
    log_error "cannot list PVCs to reclaim orphaned disks: $all_pvcs"
    exit 1
fi
orig_pvcs=$(printf '%s\n' "$all_pvcs" | grep -E '/vm-disk-backup-src-orig-' || true)
for pvc in $orig_pvcs; do
    pv=$(kubectl -n "$NAMESPACE" get "$pvc" -o jsonpath='{.spec.volumeName}')
    log_substep "Reclaiming orphaned ${pvc#*/} and its LINSTOR volume..."
    if [[ -n "$pv" ]]; then
        kubectl patch pv "$pv" --type=merge \
            -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null
    fi
    kubectl -n "$NAMESPACE" delete "$pvc" --ignore-not-found
done

separator

log_step "Deleting BackupClass and Velero strategy..."
kubectl delete backupclass vmdisk-velero --ignore-not-found
kubectl delete velero.strategy.backups.cozystack.io vmdisk-backup-strategy --ignore-not-found

separator

log_success "Cleanup complete"
