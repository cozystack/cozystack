# VM Adoption Guide

This guide explains how imported VMs are integrated into Cozystack's management system.

## Automatic Adoption (Default)

When `enableAdoption: true` (default), the **vm-adoption-controller** automatically adopts imported VMs:

1. Detects VMs with the `forklift.konveyor.io/plan` label
2. Annotates the existing VM with Helm ownership metadata
3. Creates a `VMInstance` CRD via the Cozystack aggregated API
4. The standard cozystack-controller creates a HelmRelease
5. Helm adopts the existing VM (no duplicate created)

Adopted VMs appear in the Cozystack dashboard with **full management capabilities**:

- Start/stop/restart
- Console access
- Resource modification (CPU, memory, instance type)
- Disk management
- Network configuration
- Cloud-init customization

This happens automatically within ~30 seconds of import completion.

### Disabling automatic adoption

Set `enableAdoption: false` in the vm-import values to disable automatic adoption.
VMs will still be imported as KubeVirt resources but won't appear in the dashboard.

## Manual Adoption (Fallback)

If automatic adoption is disabled or fails, you can manually adopt a VM.

### Using the helper script

```bash
./docs/scripts/adopt-vm.sh <vm-name> <namespace> [instance-type] [profile]
```

This generates a Helm values file that you can review and apply.

### Step-by-step

**1. Extract VM configuration**

```bash
VM_NAME="my-imported-vm"
NAMESPACE="default"

INSTANCE_TYPE=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.instancetype.name}')
PREFERENCE=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.preference.name}')
```

**2. List attached disks**

```bash
kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o json | \
  jq -r '.spec.template.spec.volumes[] | select(.dataVolume) | .dataVolume.name'
```

**3. Add Helm ownership annotations to the existing VM**

This is required so Helm adopts the VM instead of failing with "already exists":

```bash
RELEASE_NAME="vm-instance-${VM_NAME}"

kubectl label vm "$VM_NAME" -n "$NAMESPACE" \
  app.kubernetes.io/managed-by=Helm

kubectl annotate vm "$VM_NAME" -n "$NAMESPACE" \
  meta.helm.sh/release-name="$RELEASE_NAME" \
  meta.helm.sh/release-namespace="$NAMESPACE"
```

**4. Create the VMInstance via Cozystack API or dashboard**

Create a VMInstance with:
- `fullnameOverride` set to the original VM name
- `dvName` set on each disk to reference the actual Forklift DataVolume names
- `cloudInitSeed: ""` to preserve the existing firmware UUID

## Important Considerations

### Disk References

Imported disks use `dvName` to reference the actual Forklift DataVolume name directly,
bypassing the default `vm-disk-<name>` convention:

```yaml
disks:
  - name: imported-0
    dvName: web-server-disk-0   # actual DataVolume name from Forklift
    bus: virtio
```

### Network Configuration

Multus networks from the original VM are automatically extracted and passed as subnets.
A pod network is always added by the vm-instance template.

### State Preservation

- **VM state** (running/stopped) is preserved via `runStrategy`
- **VM disks and data** are never modified during adoption
- **Firmware UUID** is preserved (`cloudInitSeed: ""`) to avoid cloud-init re-execution
- **Network configuration** is preserved

### Rollback

If adoption causes issues:

```bash
# Delete the VMInstance (this removes the HelmRelease but preserves the VM
# if the VirtualMachine has no ownerReference to the HelmRelease)
kubectl delete vminstance "$VM_NAME" -n "$NAMESPACE"

# Remove Helm labels/annotations from the VM
kubectl label vm "$VM_NAME" -n "$NAMESPACE" \
  app.kubernetes.io/managed-by- \
  app.kubernetes.io/name- \
  app.kubernetes.io/instance-

kubectl annotate vm "$VM_NAME" -n "$NAMESPACE" \
  meta.helm.sh/release-name- \
  meta.helm.sh/release-namespace-
```

## Further Reading

- [VM Import README](../README.md)
- [Adoption Design](../ADOPTION_DESIGN.md)
- [Forklift Documentation](https://forklift.konveyor.io/)
