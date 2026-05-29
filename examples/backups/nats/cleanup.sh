#!/bin/bash
# Cleanup: tear down everything provisioned by the demo so the cluster returns
# to its previous state.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup NATS backup demo"

kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backup "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete secret "${NATS_RESTORE_NAME}-backup-s3" --ignore-not-found
kubectl -n "$NAMESPACE" delete secret "${NATS_NAME}-backup-s3" --ignore-not-found
kubectl -n "$NAMESPACE" delete nats "$NATS_RESTORE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete nats "$NATS_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete bucket "$BUCKET_NAME" --ignore-not-found
rm -f "$SCRIPT_DIR/.bucket-info.env"
kubectl delete backupclass "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete job.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found

log_success "Cleanup complete."
