#!/bin/bash
# Step 05: Create a BackupJob to back up the VMInstance
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 5: Create BackupJob"

log_step "Creating BackupJob 'test-backup' in namespace $NAMESPACE..."
log_info "This triggers a Velero backup of VMInstance 'test' and all its disks"
log_command "kubectl apply -f - (BackupJob: test-backup)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: test-backup
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: VMInstance
    name: test
  backupClassName: velero
EOF

log_success "BackupJob created"

separator

log_step "Waiting for BackupJob to complete..."
wait_for_field backupjob test-backup '{.status.phase}' Succeeded "$NAMESPACE" 600

separator

log_step "Verifying BackupJob result..."
log_command "kubectl get backupjob test-backup -n $NAMESPACE -o yaml"
kubectl get backupjob test-backup -n "$NAMESPACE" -o wide

separator

log_step "Checking created Backup..."
log_command "kubectl get backups -n $NAMESPACE"
kubectl get backups -n "$NAMESPACE"

separator

log_success "BackupJob completed successfully"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./06-restore-in-place.sh"
