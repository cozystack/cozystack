#!/bin/bash
# Step 06: Restore the VMInstance in-place (same namespace, same application)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 6: Restore VMInstance In-Place"

log_step "Creating RestoreJob 'restore-in-place-test' in namespace $NAMESPACE..."
log_info "In-place restore: the VM will be halted, PVCs renamed, and data restored from backup"
log_command "kubectl apply -f - (RestoreJob: restore-in-place-test)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: restore-in-place-test
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: test-backup
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: VMInstance
    name: test
EOF

log_success "RestoreJob created"

separator

log_step "Waiting for RestoreJob to complete..."
wait_for_field restorejob restore-in-place-test '{.status.phase}' Succeeded "$NAMESPACE" 600

separator

log_step "Verifying RestoreJob result..."
log_command "kubectl get restorejob restore-in-place-test -n $NAMESPACE -o yaml"
kubectl get restorejob restore-in-place-test -n "$NAMESPACE" -o wide

separator

log_success "In-place restore completed successfully"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./07-restore-to-copy.sh"
