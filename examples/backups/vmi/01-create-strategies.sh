#!/bin/bash
# Step 01: Create Velero backup/restore strategies for VMInstance and VMDisk
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 1: Create Velero Strategies"

log_step "Creating VMInstance strategy..."
log_command "kubectl apply -f - (Velero strategy: vminstance-strategy)"

kubectl apply -f - <<'EOF'
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Velero
metadata:
  name: vminstance-strategy
spec:
  template:
    # Symmetric restore filters: kubevirt-velero-plugin requires launcher pods in the backup,
    # but restore orLabelSelectors limit what is applied from the tarball (e.g. skip another
    # VM's pod that Velero added via PVC item action). VMDisk OR branches are appended by
    # the controller from backup.status.underlyingResources or the Velero backup annotation.
    restoreSpec:
      existingResourcePolicy: update
      includedNamespaces:
        - '{{ .Application.metadata.namespace }}'
      orLabelSelectors:
        - matchLabels:
            app.kubernetes.io/instance: 'vm-instance-{{ .Application.metadata.name }}'
        - matchLabels:
            apps.cozystack.io/application.kind: '{{ .Application.kind }}'
            apps.cozystack.io/application.name: '{{ .Application.metadata.name }}'
      includedResources:
        - helmreleases.helm.toolkit.fluxcd.io
        - virtualmachines.kubevirt.io
        - virtualmachineinstances.kubevirt.io
        - pods
        - persistentvolumeclaims
        - configmaps
        - secrets
        - controllerrevisions.apps
      includeClusterResources: false
      excludedResources:
        # Required to avoid conflict with restored DV from HR VMDisk
        - datavolumes.cdi.kubevirt.io

    spec: # see https://velero.io/docs/v1.17/api-types/backup/
      includedNamespaces:
        - '{{ .Application.metadata.namespace }}'
      orLabelSelectors:
        # VM resources (VirtualMachine, DataVolume, PVC, etc.)
        - matchLabels:
            app.kubernetes.io/instance: 'vm-instance-{{ .Application.metadata.name }}'
        # HelmRelease (the Cozystack app object)
        - matchLabels:
            apps.cozystack.io/application.kind: '{{ .Application.kind }}'
            apps.cozystack.io/application.name: '{{ .Application.metadata.name }}'
      includedResources:
        - helmreleases.helm.toolkit.fluxcd.io
        - virtualmachines.kubevirt.io
        - virtualmachineinstances.kubevirt.io
        # Required by kubevirt-velero-plugin for running VMs ("launcher pod must be in backup").
        - pods
        # Required by kubevirt-velero-plugin requires DV to be in backup of VM, but it excludes in restores
        - datavolumes.cdi.kubevirt.io
        - persistentvolumeclaims
        - configmaps
        - secrets
        - controllerrevisions.apps
      includeClusterResources: false
      storageLocation: '{{ .Parameters.backupStorageLocationName }}'
      volumeSnapshotLocations:
        - '{{ .Parameters.backupStorageLocationName }}'
      snapshotVolumes: true
      snapshotMoveData: true
      ttl: 720h0m0s
      itemOperationTimeout: 24h0m0s
EOF

log_success "VMInstance strategy created"

separator

log_step "Creating VMDisk strategy..."
log_command "kubectl apply -f - (Velero strategy: vmdisk-strategy)"

kubectl apply -f - <<'EOF'
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Velero
metadata:
  name: vmdisk-strategy
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
      volumeSnapshotLocations:
        - '{{ .Parameters.backupStorageLocationName }}'
      snapshotVolumes: true
      snapshotMoveData: true
      ttl: 720h0m0s
      itemOperationTimeout: 24h0m0s
EOF

log_success "VMDisk strategy created"

separator

log_step "Verifying strategies..."
log_command "kubectl get velero.strategy.backups.cozystack.io"
kubectl get velero.strategy.backups.cozystack.io

separator

log_success "Velero strategies are ready"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./02-create-backupclass.sh"
