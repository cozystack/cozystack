#!/bin/bash
# Step 03: Create a standalone VMDisk and wait for it to be ready to back up.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 3: Create VMDisk 'backup-src'"

log_step "Creating VMDisk 'backup-src' in namespace $NAMESPACE..."
log_info "Imports the Ubuntu Noble cloud image into a 5Gi replicated disk"
log_command "kubectl apply -f - (VMDisk: backup-src)"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: backup-src
  namespace: ${NAMESPACE}
spec:
  optical: false
  source:
    http:
      url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  storage: 5Gi
  storageClass: replicated
EOF

log_success "VMDisk created"

# Back up only a fully imported disk: the DataVolume import must finish and the
# PVC bind before the backup runs, or Velero captures an empty/partial volume.
log_step "Waiting for the VMDisk to import (HelmRelease Ready, DataVolume Ready, PVC Bound)..."
kubectl -n "$NAMESPACE" wait hr vm-disk-backup-src --for=condition=ready --timeout=600s
wait_for_field datavolume.cdi.kubevirt.io vm-disk-backup-src \
    '{.status.phase}' Succeeded "$NAMESPACE" 600
wait_for_field pvc vm-disk-backup-src '{.status.phase}' Bound "$NAMESPACE" 300

log_success "VMDisk 'backup-src' is imported and ready to back up"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./04-create-backupjob.sh" >&2
