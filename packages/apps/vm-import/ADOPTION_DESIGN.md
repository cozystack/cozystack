# VM Import Adoption Design

## Context

Forklift imports VMs from VMware and creates native KubeVirt `VirtualMachine` resources.
Cozystack manages VMs via Helm applications (`vm-instance`, `vm-disk`).

**Problem**: Imported VMs are not visible/manageable through the Cozystack dashboard because they are not created through standard Helm applications.

## Solution

### 1. Resource Lifecycle

#### Helm Resources (managed by vm-import)
- `Provider` (source and destination)
- `NetworkMap`
- `StorageMap`
- `Plan`
- `Migration`

**On vm-import deletion**:
- `Providers` are kept with `helm.sh/resource-policy: keep` (reusable for future imports)
- Other resources (Plan, Migration, Maps) are deleted (temporary migration objects)

#### Resources created by Forklift (NOT managed by Helm)
- `VirtualMachine` (KubeVirt)
- `DataVolume` (CDI)
- `PersistentVolumeClaim`

**On vm-import deletion**:
- **NEVER deleted** as they are not part of the Helm release
- VMs remain operational and manageable via `kubectl`

### 2. Adoption Mechanism

#### Phase 1: Forklift Labels (automatic)

VMs created by Forklift already have labels:
```yaml
metadata:
  labels:
    forklift.konveyor.io/plan: <plan-name>
    forklift.konveyor.io/vm-name: <vm-name>
```

The Plan gets adoption annotations when `enableAdoption: true`:
```yaml
metadata:
  annotations:
    vm-import.cozystack.io/adoption-enabled: "true"
    vm-import.cozystack.io/target-namespace: {{ .Release.Namespace }}
```

#### Phase 2: VM Adoption Controller

A dedicated controller (`vm-adoption-controller`) deployed as a platform package:

1. **Watches** VMs with label `forklift.konveyor.io/plan`
2. **Checks** the Plan annotation `vm-import.cozystack.io/adoption-enabled`
3. **Creates** a `VMInstance` CRD via the Cozystack aggregated API
4. **Labels** the original VM as adopted:
   ```yaml
   labels:
     cozystack.io/adopted: "true"
     cozystack.io/adopted-by: "<vminstance-name>"
   ```

The `VMInstance` creation triggers the standard Cozystack controller flow:
- `cozystack-controller` creates a `HelmRelease` for the VMInstance
- The VM becomes visible and manageable in the Cozystack dashboard

#### Phase 3: Dashboard Visibility

Adopted VMs appear in the dashboard as regular VM Instances with full management capabilities:
- Start/stop/restart
- Console access
- Resource modification
- Disk management
- Network configuration

### 3. Manual Adoption (fallback)

If automatic adoption is disabled or fails, users can manually adopt a VM using:

```bash
./docs/scripts/adopt-vm.sh <vm-name> <namespace> [instance-type] [profile]
```

This generates a values file for creating a `VMInstance` Helm release.

## Architecture

```text
Forklift imports VM
       ↓
VirtualMachine (KubeVirt) created
       ↓
vm-adoption-controller detects it
       ↓
VMInstance (Cozystack CRD) created
       ↓
cozystack-controller creates HelmRelease
       ↓
VM visible in dashboard
```

## Advantages

1. **Non-destructive**: Deleting vm-import never deletes VMs
2. **Automatic**: VMs appear in dashboard within ~30 seconds
3. **Traceable**: Labels and annotations track the import source
4. **Standard**: Uses the same VMInstance CRD as manually created VMs
5. **Compatible**: VMs remain standard KubeVirt objects
