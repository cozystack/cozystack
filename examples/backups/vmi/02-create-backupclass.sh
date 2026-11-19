#!/bin/bash
# Step 02: Create BackupClass that binds strategies to application types
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 2: Create BackupClass"

log_step "Creating BackupClass 'velero'..."
log_info "BackupClass maps application kinds (VMInstance, VMDisk) to their Velero strategies"
log_command "kubectl apply -f - (BackupClass: velero)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: velero
spec:
  strategies:
    - strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Velero
        name: vminstance-strategy
      application:
        kind: VMInstance
        apiGroup: apps.cozystack.io
      parameters:
        backupStorageLocationName: ${BACKUP_STORAGE_LOCATION}
    - strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Velero
        name: vmdisk-strategy
      application:
        kind: VMDisk
        apiGroup: apps.cozystack.io
      parameters:
        backupStorageLocationName: ${BACKUP_STORAGE_LOCATION}
EOF

log_success "BackupClass created"

separator

log_step "Verifying BackupClass..."
log_command "kubectl get backupclass velero -o yaml"
kubectl get backupclass velero -o yaml

separator

log_success "BackupClass is ready"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./03-create-vmdisk.sh"
