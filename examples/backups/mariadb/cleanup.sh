#!/bin/bash
# Best-effort teardown of everything the demo creates. Idempotent;
# missing resources are skipped silently.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup"

log_substep "Deleting RestoreJob..."
kubectl -n "$NAMESPACE" delete restorejob.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found

log_substep "Deleting Plan + BackupJob + derived Backup artefacts..."
kubectl -n "$NAMESPACE" delete plan.backups.cozystack.io "$PLAN_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob.backups.cozystack.io "$BACKUPJOB_NAME" --ignore-not-found
# Only the Backups of THIS demo's apps — never `--all`: on a shared cluster the
# namespace can hold Backup artefacts of other applications and flows.
for b in $(kubectl -n "$NAMESPACE" get backups.backups.cozystack.io \
        -o jsonpath='{range .items[*]}{.metadata.name} {.spec.applicationRef.name}{"\n"}{end}' 2>/dev/null |
        awk -v src="$MARIADB_SRC_NAME" -v tgt="$MARIADB_TARGET_NAME" '$2 == src || $2 == tgt {print $1}'); do
    kubectl -n "$NAMESPACE" delete backups.backups.cozystack.io "$b" --ignore-not-found
done

# The Cozystack BackupJob/RestoreJob own their operator-side k8s.mariadb.com
# Backup/Restore CRs only by the OwningJob labels (not OwnerReferences), so
# deleting the Cozystack jobs does not cascade to them. Left behind, a stale
# operator-side Backup would be reused by findMariaDBBackupForJob on the next
# run against a bucket path that no longer exists. Prune them by owner.
log_substep "Deleting operator-side k8s.mariadb.com Backup/Restore CRs..."
for owner in "$BACKUPJOB_NAME" "$RESTOREJOB_TOCOPY_NAME"; do
    kubectl -n "$NAMESPACE" delete backups.k8s.mariadb.com,restores.k8s.mariadb.com \
        -l "backups.cozystack.io/owned-by.BackupJobName=${owner}" --ignore-not-found
done

log_substep "Deleting MariaDB apps..."
kubectl -n "$NAMESPACE" delete mariadb.apps.cozystack.io "$MARIADB_SRC_NAME" "$MARIADB_TARGET_NAME" --ignore-not-found

log_substep "Deleting per-app backup Secrets..."
kubectl -n "$NAMESPACE" delete secret \
    "${MARIADB_SRC_NAME}-mariadb-backup-creds" "${MARIADB_SRC_NAME}-mariadb-backup-ca" \
    "${MARIADB_TARGET_NAME}-mariadb-backup-creds" "${MARIADB_TARGET_NAME}-mariadb-backup-ca" --ignore-not-found

log_substep "Deleting Bucket..."
kubectl -n "$NAMESPACE" delete bucket.apps.cozystack.io "$BUCKET_NAME" --ignore-not-found

log_substep "Deleting BackupClass + MariaDB strategy (cluster-scoped)..."
kubectl delete backupclass.backups.cozystack.io "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete mariadb.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found

log_success "Cleanup complete."
