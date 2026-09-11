#!/bin/bash
# Step 01: Create the Velero backup/restore strategy for a standalone VMDisk.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 1: Create VMDisk Velero strategy"

log_step "Creating Velero strategy 'vmdisk-backup-strategy'..."
log_command "kubectl apply -f - (Velero strategy: vmdisk-backup-strategy)"

# A standalone VMDisk is just an HelmRelease + a DataVolume-backed PVC (+ its
# configmaps), all labelled app.kubernetes.io/instance: vm-disk-<name>. Unlike a
# VMInstance there is no VirtualMachine / VMI / launcher pod to capture. With
# snapshotMoveData the CSI data mover streams the volume through the
# BackupStorageLocation, so no VolumeSnapshotLocation is required (matching the
# platform's own cozy-default-velero-vmdisk strategy).
kubectl apply -f - <<'EOF'
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Velero
metadata:
  name: vmdisk-backup-strategy
spec:
  template:
    restoreSpec:
      existingResourcePolicy: update
      includedNamespaces:
        - '{{ .Application.metadata.namespace }}'
      orLabelSelectors:
        - matchLabels:
            app.kubernetes.io/instance: 'vm-disk-{{ .Application.metadata.name }}'
        - matchLabels:
            apps.cozystack.io/application.kind: '{{ .Application.kind }}'
            apps.cozystack.io/application.name: '{{ .Application.metadata.name }}'
      includedResources:
        - helmreleases.helm.toolkit.fluxcd.io
        - persistentvolumeclaims
        - configmaps
      includeClusterResources: false
    spec:
      includedNamespaces:
        - '{{ .Application.metadata.namespace }}'
      orLabelSelectors:
        - matchLabels:
            app.kubernetes.io/instance: 'vm-disk-{{ .Application.metadata.name }}'
        - matchLabels:
            apps.cozystack.io/application.kind: '{{ .Application.kind }}'
            apps.cozystack.io/application.name: '{{ .Application.metadata.name }}'
      includedResources:
        - helmreleases.helm.toolkit.fluxcd.io
        - persistentvolumeclaims
        - configmaps
      includeClusterResources: false
      storageLocation: '{{ .Parameters.backupStorageLocationName }}'
      snapshotVolumes: true
      snapshotMoveData: true
      ttl: 720h0m0s
      itemOperationTimeout: 24h0m0s
EOF

log_success "Velero strategy created"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./02-create-backupclass.sh" >&2
