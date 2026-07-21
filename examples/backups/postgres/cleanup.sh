#!/bin/bash
# Best-effort teardown of everything the demo creates. Idempotent;
# missing resources are skipped silently.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup"

# The CNPG driver suspends the source/target Postgres HelmRelease while a
# RestoreJob runs. If the demo was aborted mid-restore the HR is left
# suspended; deleting the RestoreJob without resuming it first strands the app.
# Resume both best-effort BEFORE deleting the RestoreJobs.
for app in "$PG_SRC_NAME" "$PG_TARGET_NAME"; do
    if kubectl -n "$NAMESPACE" get hr "postgres-${app}" >/dev/null 2>&1; then
        log_substep "Resuming HelmRelease postgres-${app} (no-op if not suspended)..."
        kubectl -n "$NAMESPACE" patch hr "postgres-${app}" --type=merge \
            -p '{"spec":{"suspend":false}}' >/dev/null || true
    fi
done

log_substep "Deleting RestoreJobs..."
kubectl -n "$NAMESPACE" delete restorejob.backups.cozystack.io \
    "$RESTOREJOB_TOCOPY_NAME" "$RESTOREJOB_PITR_NAME" "$RESTOREJOB_UNREACHABLE_NAME" --ignore-not-found

log_substep "Deleting Plan + BackupJob + derived Backup artefacts..."
kubectl -n "$NAMESPACE" delete plan.backups.cozystack.io "$PLAN_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob.backups.cozystack.io \
    "$BACKUPJOB_NAME" "$BACKUPJOB_POSTMARKER_NAME" --ignore-not-found
# Only the Backups of THIS demo's apps — never `--all`: on a shared cluster the
# namespace can hold Backup artefacts of other applications and flows.
for b in $(kubectl -n "$NAMESPACE" get backups.backups.cozystack.io \
        -o jsonpath='{range .items[*]}{.metadata.name} {.spec.applicationRef.name}{"\n"}{end}' 2>/dev/null |
        awk -v src="$PG_SRC_NAME" -v tgt="$PG_TARGET_NAME" '$2 == src || $2 == tgt {print $1}'); do
    kubectl -n "$NAMESPACE" delete backups.backups.cozystack.io "$b" --ignore-not-found
done

log_substep "Deleting Postgres apps..."
kubectl -n "$NAMESPACE" delete postgres.apps.cozystack.io "$PG_SRC_NAME" "$PG_TARGET_NAME" --ignore-not-found

log_substep "Deleting per-app backup Secrets..."
kubectl -n "$NAMESPACE" delete secret \
    "${PG_SRC_NAME}-cnpg-backup-creds" "${PG_SRC_NAME}-cnpg-backup-ca" \
    "${PG_TARGET_NAME}-cnpg-backup-creds" "${PG_TARGET_NAME}-cnpg-backup-ca" --ignore-not-found

log_substep "Deleting Bucket..."
kubectl -n "$NAMESPACE" delete bucket.apps.cozystack.io "$BUCKET_NAME" --ignore-not-found

log_substep "Deleting BackupClass + CNPG strategy (cluster-scoped)..."
kubectl delete backupclass.backups.cozystack.io "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete cnpg.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found

log_success "Cleanup complete."
