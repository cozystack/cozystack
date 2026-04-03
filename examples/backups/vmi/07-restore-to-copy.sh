#!/bin/bash
# Step 07: Restore the VMInstance to a copy in a different namespace
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

TARGET_NAMESPACE="${TARGET_NAMESPACE:-tenant-root-copy}"

print_header "Step 7: Restore VMInstance to Copy (Cross-Namespace)"

log_info "Restoring to the same namespace with a different app name is not supported"
log_info "due to Velero DataUpload limitations. Cross-namespace restore uses Velero's namespaceMapping."

separator

log_step "Ensuring target namespace '$TARGET_NAMESPACE' exists..."
kubectl create namespace "$TARGET_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
log_success "Namespace '$TARGET_NAMESPACE' is ready"

separator

log_step "Creating RestoreJob 'restore-to-copy-test' in namespace $NAMESPACE..."
log_info "The backup will be restored into namespace '$TARGET_NAMESPACE' using Velero namespaceMapping"
log_command "kubectl apply -f - (RestoreJob: restore-to-copy-test)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: restore-to-copy-test
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: test-backup
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: VMInstance
    name: test
  options: # runtime.RawExtension, typed based on targetApplicationRef and current controller implementation (for additional restore options)
    targetNamespace: ${TARGET_NAMESPACE} # when set to a different namespace, triggers cross-namespace restore via Velero namespaceMapping
    failIfTargetExists: true # if true, restore will fail when the target resource already exists
    keepOriginalPVC: false # renames original VMI PVC before restore to `<name>-orig-<hash>`, only for in-place restore
    keepOriginaIpAndMac: false # restores original IP and MAC address of VMI via OVN annotations
EOF

log_success "RestoreJob created"

separator

log_step "Waiting for RestoreJob to complete..."
wait_for_field restorejob restore-to-copy-test '{.status.phase}' Succeeded "$NAMESPACE" 600

separator

log_step "Verifying RestoreJob result..."
log_command "kubectl get restorejob restore-to-copy-test -n $NAMESPACE -o yaml"
kubectl get restorejob restore-to-copy-test -n "$NAMESPACE" -o wide

separator

log_step "Checking resources in target namespace..."
log_command "kubectl get all -n $TARGET_NAMESPACE"
kubectl get all -n "$TARGET_NAMESPACE" 2>/dev/null || log_warning "No resources found in $TARGET_NAMESPACE"

separator

log_success "Cross-namespace restore completed successfully"
