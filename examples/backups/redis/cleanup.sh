#!/bin/bash
# Cleanup: tear down everything provisioned by the demo so the cluster returns
# to its previous state. Idempotent — safe to run before a fresh round (a stale
# Succeeded BackupJob would otherwise falsely satisfy the wait).
# -e so a real API/permission failure aborts loudly instead of printing "Cleanup
# complete." over a half-torn-down demo; every delete below is
# --ignore-not-found, so an already-gone resource is not an error.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup Redis backup demo"

kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backup "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete redis "$REDIS_RESTORE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete redis "$REDIS_NAME" --ignore-not-found

# Demo-owned backups plumbing: the BackupClass and strategy CR are cluster-scoped
# (no -n). Deleting the Bucket releases its S3 objects and the projected keys.
kubectl delete backupclass "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete redis.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete secret "$CREDS_SECRET" --ignore-not-found
kubectl -n "$NAMESPACE" delete secret "$CA_SECRET" --ignore-not-found
kubectl -n "$NAMESPACE" delete bucket "$BUCKET_NAME" --ignore-not-found

log_success "Cleanup complete."
