#!/bin/bash
# Step 03: Lose the demo namespace inside the live cluster and roll the cluster
# back. The restore stops the control plane and the workers, writes the etcd
# prefix and the volumes back, and returns the cluster to its replica counts.
# Set RESTORE_OPTIONS='{"volumes":false}' (or '{"etcd":false}') to restore one
# half only.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 03: Restore '${CLUSTER_NAME}' in place"

tenant_connect
log_substep "Deleting namespace demo inside the tenant cluster..."
tkubectl delete namespace demo --wait=true --timeout=10m
tenant_disconnect

backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
options="${RESTORE_OPTIONS:-}"
[[ -n "$options" ]] || options='{}'
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_INPLACE_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${backup_ref}
  options: ${options}
EOF

wait_for_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 2400 Failed
wait_hr_ready "$RELEASE" 1200

tenant_connect
wait_tenant_marker 1200
[[ "$(cluster_ca_fingerprint)" == "$(cat "$SCRIPT_DIR/.ca-fingerprint")" ]] \
    || { log_error "cluster CA changed across the restore"; exit 1; }
log_success "same cluster CA, marker back in etcd and on the volume"
tenant_disconnect

echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-restore-after-delete.sh"
