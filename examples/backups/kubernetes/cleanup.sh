#!/bin/bash
# Remove everything the walkthrough created. Deleting the BackupJob's Backup
# removes its Velero backup and its etcd dump object.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup"
tenant_disconnect
kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_INPLACE_NAME" "$RESTOREJOB_RECREATE_NAME" --ignore-not-found
backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}' 2>/dev/null || true)
kubectl -n "$NAMESPACE" delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
[[ -n "$backup_ref" ]] && kubectl -n "$NAMESPACE" delete backup "$backup_ref" --ignore-not-found
kubectl -n "$NAMESPACE" delete kubernetesnodeses.apps.cozystack.io "${CLUSTER_NAME}-${POOL_NAME}" --ignore-not-found
kubectl -n "$NAMESPACE" delete kuberneteses.apps.cozystack.io "$CLUSTER_NAME" --ignore-not-found
rm -f "$TENANT_KUBECONFIG" "$SCRIPT_DIR/.ca-fingerprint"
log_success "done"
