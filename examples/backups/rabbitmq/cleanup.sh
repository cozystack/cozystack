#!/bin/bash
# Idempotent teardown of the RabbitMQ backup demo. Removes only this demo's
# objects — never a blanket --all — so it is safe to run before a re-run and in
# a chainsaw finally step. Order: restore/backup jobs and Plan, then this demo's
# Backup artifacts, then the applications. The platform cozy-backups bucket and
# cozy-default-rabbitmq strategy are platform-owned and left alone.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleaning up RabbitMQ backup demo (namespace: $NAMESPACE)"

kubectl -n "$NAMESPACE" delete restorejob.backups.cozystack.io \
    "$RESTOREJOB_TOCOPY_NAME" "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete plan.backups.cozystack.io "$PLAN_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob.backups.cozystack.io "$BACKUPJOB_NAME" --ignore-not-found

# Prune only the Backup artifacts belonging to this demo's applications.
for app in "$RABBITMQ_SRC_NAME" "$RABBITMQ_TARGET_NAME"; do
    kubectl -n "$NAMESPACE" get backups.backups.cozystack.io \
        -o jsonpath='{range .items[?(@.spec.applicationRef.name=="'"$app"'")]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
        | while read -r b; do
            [[ -n "$b" ]] && kubectl -n "$NAMESPACE" delete backups.backups.cozystack.io "$b" --ignore-not-found
        done
done

kubectl -n "$NAMESPACE" delete rabbitmq.apps.cozystack.io \
    "$RABBITMQ_SRC_NAME" "$RABBITMQ_TARGET_NAME" --ignore-not-found

log_success "cleanup complete"
