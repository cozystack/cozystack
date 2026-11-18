#!/bin/bash
# Clean up all resources created by the demo. Non-interactive: safe to call from
# the e2e harness's finally block, and idempotent (a no-op when a gated-out run
# created nothing). --ignore-not-found returns success for an absent resource
# but does NOT mask a stuck delete: the delete still blocks and, under set -e,
# a teardown that never settles fails this script instead of leaking state into
# the next run (docs/agents/e2e-testing.md convention 4).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

TARGET_NAMESPACE="${TARGET_NAMESPACE:-tenant-root-copy}"

print_header "Cleanup Demo Resources"

# The driver suspends the VMInstance/VMDisk HelmReleases while a RestoreJob runs
# and never resumes them (suspendHelmRelease only ever sets suspend=true), so an
# aborted or completed restore leaves them suspended. Resume best-effort BEFORE
# deleting the RestoreJobs — deleting a RestoreJob without resuming strands the
# app suspended (same reasoning as examples/backups/postgres/cleanup.sh).
for hr in vm-instance-test vm-disk-ubuntu-source; do
    if kubectl -n "$NAMESPACE" get hr "$hr" >/dev/null 2>&1; then
        log_substep "Resuming HelmRelease $hr (no-op if not suspended)..."
        kubectl -n "$NAMESPACE" patch hr "$hr" --type=merge \
            -p '{"spec":{"suspend":false}}' >/dev/null || true
    fi
done

# Namespaced request objects first. Deleting the cozystack BackupJob/RestoreJob
# and Backup CRs cascades to the Velero Backup/Restore the controller created.
log_step "Deleting RestoreJobs..."
kubectl delete restorejob restore-to-copy-test restore-in-place-test \
    -n "$NAMESPACE" --ignore-not-found

log_step "Deleting BackupJob and Backup..."
kubectl delete backupjob test-backup -n "$NAMESPACE" --ignore-not-found
kubectl delete backup test-backup -n "$NAMESPACE" --ignore-not-found

separator

log_step "Deleting VMInstance and VMDisk..."
kubectl delete vminstance test -n "$NAMESPACE" --ignore-not-found
kubectl delete vmdisk ubuntu-source -n "$NAMESPACE" --ignore-not-found

# keepOriginalPVC renames the VMInstance's disk PVC to <name>-orig-<hash> with a
# bare ObjectMeta (no labels, no ownerRefs) and flips its PV to Retain. Nothing
# above reclaims it: the app delete does not own it and no label selector matches
# it, so under skipDelete a LINSTOR volume and a Released PV leak on every run.
# List loudly (a failed query must abort, not silently skip the reclaim it exists
# for); grep's no-match stays tolerant.
if ! all_pvcs=$(kubectl -n "$NAMESPACE" get pvc -o name 2>&1); then
    log_error "cannot list PVCs to reclaim orphaned disks: $all_pvcs"
    exit 1
fi
orig_pvcs=$(printf '%s\n' "$all_pvcs" | grep -E '/vm-disk-ubuntu-source-orig-' || true)
for pvc in $orig_pvcs; do
    # Flip the retained PV back to Delete BEFORE removing the PVC, so releasing it
    # makes the CSI provisioner call DeleteVolume and reclaim the LINSTOR volume —
    # deleting the PV object alone would drop the record and strand the volume. Let
    # both the lookup and the patch fail the script: swallowing them would delete
    # the PVC anyway and strand the volume with no PVC left to find it by.
    pv=$(kubectl -n "$NAMESPACE" get "$pvc" -o jsonpath='{.spec.volumeName}')
    log_substep "Reclaiming orphaned ${pvc#*/} and its LINSTOR volume..."
    if [[ -n "$pv" ]]; then
        kubectl patch pv "$pv" --type=merge \
            -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null
    fi
    kubectl -n "$NAMESPACE" delete "$pvc" --ignore-not-found
done

separator

# Cluster-scoped BackupClass + strategies.
log_step "Deleting BackupClass and Velero strategies..."
kubectl delete backupclass velero --ignore-not-found
kubectl delete velero.strategy.backups.cozystack.io \
    vminstance-strategy vmdisk-strategy --ignore-not-found

separator

log_step "Deleting target namespace..."
kubectl delete namespace "$TARGET_NAMESPACE" --ignore-not-found

separator

log_success "Cleanup complete"
