#!/bin/bash
# Cleanup: tear down everything provisioned by the demo so the cluster returns
# to its previous state. Idempotent — safe to run before a fresh round (a stale
# Succeeded BackupJob would otherwise falsely satisfy the wait).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup Redis backup demo"

kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backup "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete redis "$REDIS_RESTORE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete redis "$REDIS_NAME" --ignore-not-found

log_success "Cleanup complete."
