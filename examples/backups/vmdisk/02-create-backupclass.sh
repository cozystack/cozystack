#!/bin/bash
# Step 02: Create a BackupClass binding VMDisk to the Velero strategy.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 2: Create BackupClass"

log_step "Creating BackupClass 'vmdisk-velero'..."
log_command "kubectl apply -f - (BackupClass: vmdisk-velero)"

# BackupClass is cluster-scoped; a single instance covers every namespace.
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: vmdisk-velero
spec:
  strategies:
    - application:
        apiGroup: apps.cozystack.io
        kind: VMDisk
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Velero
        name: vmdisk-backup-strategy
      parameters:
        backupStorageLocationName: ${BACKUP_STORAGE_LOCATION}
EOF

log_success "BackupClass created"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./03-create-vmdisk.sh" >&2
