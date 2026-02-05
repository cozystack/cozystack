# VM Adoption Guide

This guide explains how to fully integrate imported VMs into Cozystack's management system.

## Overview

When you import VMs using `vm-import`, they are created as native KubeVirt `VirtualMachine` resources. While these VMs are fully functional and can be managed via kubectl, you may want to bring them under Cozystack's Helm-based management for:

- Declarative configuration via Helm values
- Automated upgrades and lifecycle management
- Integration with Cozystack's instance types and preferences
- Cloud-init customization
- Network and storage management through Cozystack abstractions

## Adoption Methods

### Method 1: Automatic Labeling (Default)

By default (`enableAdoption: true`), imported VMs are automatically labeled with:

```yaml
metadata:
  labels:
    cozystack.io/adopted: "true"
    cozystack.io/source: "vm-import"
    forklift.konveyor.io/plan: "<import-name>"
```

These VMs appear in the Cozystack dashboard with:
- Read-only information (CPU, memory, disk, network)
- Basic operations (start, stop, restart, console access)
- Monitoring and metrics

**Limitations:**
- Cannot be reconfigured via Helm
- No cloud-init updates
- No instance type changes
- No automatic upgrades

### Method 2: Full Helm Adoption (Advanced)

Create a `vm-instance` Helm release that manages the existing VM. This provides full Cozystack capabilities.

#### Prerequisites

- Imported VM must be in a stable state (running or stopped)
- VM name must be unique within the namespace
- You need to extract the current VM configuration

#### Step-by-step Guide

**1. Extract VM configuration**

```bash
VM_NAME="my-imported-vm"
NAMESPACE="default"

# Get VM specs
kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o yaml > vm-spec.yaml

# Extract key information
INSTANCE_TYPE=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.instancetype.name}')
PREFERENCE=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.preference.name}')
RUNNING=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.running}')
```

**2. List attached disks**

```bash
# List DataVolumes
kubectl get dv -n "$NAMESPACE" -l forklift.konveyor.io/vm-name="$VM_NAME"

# Example output:
# NAME                     PHASE      PROGRESS
# my-imported-vm-disk-1    Succeeded  100%
# my-imported-vm-disk-2    Succeeded  100%
```

**3. Create `vm-instance` values file**

Create `adopted-vm-values.yaml`:

```yaml
# Basic settings
running: true  # or false based on extracted value

# Instance configuration
instanceType: u1.medium  # or extracted value
instanceProfile: ubuntu  # or extracted value

# Attach existing disks
disks:
  - name: disk-1  # Reference existing DataVolume
    bus: virtio
  - name: disk-2
    bus: virtio

# Network configuration
external: true
externalPorts:
  - 22
  - 80
  - 443

# (Optional) SSH keys
sshKeys:
  - "ssh-rsa AAAAB3... user@host"

# (Optional) Cloud-init
cloudInit: |
  #cloud-config
  packages:
    - qemu-guest-agent
  runcmd:
    - systemctl enable qemu-guest-agent
```

**4. Create the `vm-instance` release**

```bash
# Using kubectl + Helm
helm upgrade --install "$VM_NAME" cozystack/vm-instance \
  --namespace "$NAMESPACE" \
  --values adopted-vm-values.yaml \
  --set adoptExisting=true

# Or using Cozystack dashboard:
# 1. Navigate to "Applications" > "VM Instance"
# 2. Click "Create"
# 3. Fill in the form with values from step 3
# 4. Enable "Adopt Existing VM" option
```

**5. Verify adoption**

```bash
# Check Helm release
helm list -n "$NAMESPACE"

# Check VM still works
kubectl get vm "$VM_NAME" -n "$NAMESPACE"
kubectl get helmrelease "$VM_NAME" -n "$NAMESPACE"

# Verify labels
kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o yaml | grep -A 10 labels
```

The VM should now have additional labels:

```yaml
metadata:
  labels:
    app.kubernetes.io/name: vm-instance
    app.kubernetes.io/instance: <vm-name>
    app.kubernetes.io/managed-by: Helm
    helm.sh/chart: vm-instance-<version>
```

## Adoption Script (Helper)

We provide a helper script to automate the adoption process:

```bash
#!/bin/bash
# adopt-vm.sh - Adopt an imported VM into Cozystack management

set -e

VM_NAME="${1:?VM name required}"
NAMESPACE="${2:?Namespace required}"
INSTANCE_TYPE="${3:-u1.medium}"
PROFILE="${4:-ubuntu}"

echo "Adopting VM: $VM_NAME in namespace $NAMESPACE"

# Extract current configuration
echo "Extracting current VM configuration..."
RUNNING=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.running}' 2>/dev/null || echo "false")

# Find attached DataVolumes
echo "Discovering attached disks..."
DISKS=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o json | \
  jq -r '.spec.template.spec.volumes[] | select(.dataVolume) | .dataVolume.name' | \
  awk '{print "  - name: " $1 "\n    bus: virtio"}')

if [ -z "$DISKS" ]; then
  echo "Warning: No DataVolumes found attached to VM"
  DISKS="  # No disks found - add manually"
fi

# Generate values file
cat > "/tmp/adopt-${VM_NAME}-values.yaml" <<EOF
running: ${RUNNING}
instanceType: ${INSTANCE_TYPE}
instanceProfile: ${PROFILE}

disks:
${DISKS}

external: false
sshKeys: []

# Add your cloud-init configuration here if needed
cloudInit: ""
EOF

echo "Generated values file: /tmp/adopt-${VM_NAME}-values.yaml"
echo ""
echo "Review and edit the values file, then apply with:"
echo ""
echo "  helm upgrade --install \"$VM_NAME\" cozystack/vm-instance \\"
echo "    --namespace \"$NAMESPACE\" \\"
echo "    --values /tmp/adopt-${VM_NAME}-values.yaml"
echo ""
```

Save as `adopt-vm.sh`, make executable, and run:

```bash
chmod +x adopt-vm.sh
./adopt-vm.sh my-imported-vm default
```

## Important Considerations

### Disk Management

**Existing disks are preserved** but you need to reference them correctly:

```yaml
disks:
  - name: existing-disk-name  # Must match existing DataVolume name
    bus: virtio
```

If you want to add a **new disk** to an adopted VM:

1. Create a new `vm-disk` application
2. Add it to the `disks` list in `vm-instance` values

### Network Configuration

Imported VMs may have network configuration from VMware. Check existing networks:

```bash
kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.networks[*].name}'
```

Update your values accordingly:

```yaml
subnets:
  - name: existing-multus-network  # If multus networks were mapped
```

### Instance Types and Preferences

The VM may have specific CPU/memory requirements from VMware. Ensure you choose an appropriate instance type:

```bash
# List available instance types
kubectl get virtualmachineclusterinstancetype

# List available preferences
kubectl get virtualmachineclusterpreference
```

Or create a custom one in the `vm-instance` values:

```yaml
resources:
  cpu: 4
  memory: 8Gi
  sockets: 1
```

### State Preservation

- **VM state** (running/stopped) is preserved
- **VM disks and data** are never modified during adoption
- **Network configuration** is preserved (cloud-init should not re-apply network config)
- **Cloud-init** will only run on first boot unless you change `cloudInitSeed`

### Rollback

If adoption fails or causes issues, you can rollback:

```bash
# Delete the Helm release
helm uninstall "$VM_NAME" -n "$NAMESPACE"

# The VM and disks remain unchanged
# You may need to remove Helm-added labels:
kubectl label vm "$VM_NAME" -n "$NAMESPACE" \
  app.kubernetes.io/name- \
  app.kubernetes.io/instance- \
  app.kubernetes.io/managed-by- \
  helm.sh/chart-
```

## Best Practices

1. **Test adoption on non-production VMs first**
2. **Create a snapshot** or backup before adoption (if your storage supports it)
3. **Document the original configuration** before making changes
4. **Use version control** for your Helm values files
5. **Monitor the VM** after adoption to ensure it remains stable

## Troubleshooting

### VM won't start after adoption

```bash
# Check VM events
kubectl describe vm "$VM_NAME" -n "$NAMESPACE"

# Check pods
kubectl get pods -n "$NAMESPACE" -l kubevirt.io/vm="$VM_NAME"

# Check virt-launcher logs
kubectl logs -n "$NAMESPACE" virt-launcher-${VM_NAME}-xxxxx
```

### Disk attachment issues

```bash
# Verify DataVolume exists and is bound
kubectl get dv -n "$NAMESPACE"
kubectl get pvc -n "$NAMESPACE"

# Check if disk names match
kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o yaml | grep -A 5 dataVolume
```

### Helm release conflicts

```bash
# Check if there's an existing Helm release with the same name
helm list -n "$NAMESPACE" | grep "$VM_NAME"

# If needed, delete the conflicting release
helm uninstall "$VM_NAME" -n "$NAMESPACE"
```

## Further Reading

- [KubeVirt VirtualMachine API](https://kubevirt.io/api-reference/main/definitions.html#_v1_virtualmachine)
- [Cozystack VM Instance Documentation](../../vm-instance/README.md)
- [Forklift Migration Guide](https://forklift.konveyor.io/docs/latest/)
