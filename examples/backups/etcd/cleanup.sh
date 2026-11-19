#!/bin/bash
# Best-effort teardown of everything the demo creates. Idempotent;
# missing resources are skipped silently.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup"

# The driver suspends the source Etcd HelmRelease for the duration of an
# in-place restore. If the demo was aborted mid-restore (Ctrl-C, expired
# kubeconfig, ...), the HR is left suspended; deleting the RestoreJob
# without first resuming the HR strands the Etcd app indefinitely and
# the next "kubectl apply" of the chart fails to reconcile. Resume the
# HR best-effort BEFORE deleting the RestoreJob.
log_substep "Resuming Etcd HelmRelease (no-op if not suspended)..."
if kubectl -n "$NAMESPACE" get hr "$ETCD_NAME" >/dev/null 2>&1; then
    kubectl -n "$NAMESPACE" patch hr "$ETCD_NAME" --type=merge -p '{"spec":{"suspend":false}}' >/dev/null || true
fi

log_substep "Deleting ephemeral etcdctl pod (no-op if absent)..."
kubectl -n "$NAMESPACE" delete pod "$ETCDCTL_POD" --ignore-not-found

log_substep "Deleting RestoreJob..."
kubectl -n "$NAMESPACE" delete restorejob.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" --ignore-not-found

log_substep "Deleting BackupJob + derived Backup artefact..."
kubectl -n "$NAMESPACE" delete backupjob.backups.cozystack.io "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backups.backups.cozystack.io --all --ignore-not-found

log_substep "Deleting Etcd app..."
kubectl -n "$NAMESPACE" delete etcd.apps.cozystack.io "$ETCD_NAME" --ignore-not-found

log_substep "Deleting per-app credentials Secret..."
kubectl -n "$NAMESPACE" delete secret "${ETCD_NAME}-etcd-backup-creds" --ignore-not-found

log_substep "Deleting Bucket..."
kubectl -n "$NAMESPACE" delete bucket.apps.cozystack.io "$BUCKET_NAME" --ignore-not-found

log_substep "Deleting BackupClass + Etcd strategy (cluster-scoped)..."
kubectl delete backupclass.backups.cozystack.io "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete etcd.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found

rm -f "$SCRIPT_DIR/.bucket-info.env" "$SCRIPT_DIR/.sentinel.env" "$SCRIPT_DIR/.backup-name.env"
log_success "Cleanup complete."
