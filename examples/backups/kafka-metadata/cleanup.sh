#!/bin/bash
# Remove everything the demo created. Idempotent (--ignore-not-found).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup Kafka metadata backup demo"

kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete plan "$PLAN_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backup "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete kafka.apps.cozystack.io "$KAFKA_TARGET_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete kafka.apps.cozystack.io "$KAFKA_SRC_NAME" --ignore-not-found

log_success "Cleanup complete."
