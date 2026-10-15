#!/bin/bash
# Step 04: Delete the Kubernetes application and its worker pool, which also
# deletes the cluster's etcd prefix and volumes, then rebuild it from the
# backup under the same name and CA.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 04: Delete '${CLUSTER_NAME}' and rebuild it from the backup"

kubectl -n "$NAMESPACE" delete kubernetesnodeses.apps.cozystack.io "${CLUSTER_NAME}-${POOL_NAME}" --ignore-not-found --wait=false
kubectl -n "$NAMESPACE" delete kuberneteses.apps.cozystack.io "$CLUSTER_NAME" --wait=false
wait_deleted helmrelease "$RELEASE" "$NAMESPACE" 1200
wait_deleted tenantcontrolplane "$RELEASE" "$NAMESPACE" 600
log_substep "Removing what the application's deletion leaves behind, so the rebuild proves itself..."
kubectl -n "$NAMESPACE" delete secret "${RELEASE}-talos-ca" "${RELEASE}-talos-tls-cert" "${RELEASE}-admin-kubeconfig" --ignore-not-found

backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_RECREATE_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${backup_ref}
EOF

wait_for_field restorejob "$RESTOREJOB_RECREATE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 2400 Failed
wait_hr_ready "$RELEASE" 1200

tenant_connect
wait_tenant_marker 1500
[[ "$(cluster_ca_fingerprint)" == "$(cat "$SCRIPT_DIR/.ca-fingerprint")" ]] \
    || { log_error "cluster CA changed across the rebuild"; exit 1; }
log_success "cluster rebuilt with the same CA, marker back in etcd and on the volume"
tenant_disconnect
