#!/bin/bash
# Tear down everything the demo created. Best-effort: a missing object is
# not an error.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup: removing FoundationDB backup demo resources"

# Cozystack RestoreJob/BackupJob/Backup artefacts in the tenant namespace.
kubectl -n "$NAMESPACE" delete --ignore-not-found --wait=false \
    "restorejob.backups.cozystack.io/${RESTOREJOB_INPLACE_NAME}" \
    "restorejob.backups.cozystack.io/${RESTOREJOB_TOCOPY_NAME}" \
    "backupjob.backups.cozystack.io/${BACKUPJOB_NAME}" \
    "backup.backups.cozystack.io/${BACKUPJOB_NAME}" || true

# Operator-side FoundationDBBackup / FoundationDBRestore CRs labelled by
# the OwningJob name/namespace. RestoreJob/BackupJob deletion does NOT
# cascade to the operator CRs (the driver labels them for idempotent
# ensure-by-label semantics, not OwnerReferences); leftover stale CRs
# would be reused on a re-run.
for owner in "${BACKUPJOB_NAME}" "${RESTOREJOB_INPLACE_NAME}" "${RESTOREJOB_TOCOPY_NAME}"; do
    kubectl -n "$NAMESPACE" delete --ignore-not-found --wait=false \
        foundationdbbackups.apps.foundationdb.org,foundationdbrestores.apps.foundationdb.org \
        -l "backups.cozystack.io/owned-by.BackupJobName=${owner}" || true
done

# FoundationDB applications (HelmReleases). Flux uninstalls the chart,
# which drops the FoundationDBCluster CR + PVCs.
kubectl -n "$NAMESPACE" delete --ignore-not-found --wait=false \
    "hr/foundationdb-${FDB_NAME}" "hr/foundationdb-${FDB_RESTORE_NAME}" || true

# Bucket HR + COSI BucketClaim/BucketAccess + per-user creds Secret.
kubectl -n "$NAMESPACE" delete --ignore-not-found --wait=false \
    "hr/bucket-${BUCKET_NAME}" || true

# Per-app blob_credentials.json Secrets materialised by step 02.
kubectl -n "$NAMESPACE" delete --ignore-not-found \
    "secret/${FDB_NAME}-fdb-backup-creds" \
    "secret/${FDB_RESTORE_NAME}-fdb-backup-creds" || true

# Cluster-scoped: BackupClass + FoundationDB strategy.
kubectl delete --ignore-not-found \
    "backupclass.backups.cozystack.io/${BACKUPCLASS_NAME}" \
    "foundationdbs.strategy.backups.cozystack.io/${STRATEGY_NAME}" || true

rm -f "$SCRIPT_DIR/.bucket-info.env"
log_success "Cleanup complete."
