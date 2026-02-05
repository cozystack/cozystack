# VM Adoption Controller

Automatically adopts Forklift-imported VirtualMachines into Cozystack VMInstance resources.

## Overview

When Forklift imports VMs from VMware, it creates native KubeVirt `VirtualMachine` resources. This controller watches for these VMs and automatically creates corresponding Cozystack `VMInstance` CRDs, making them visible and manageable through the Cozystack dashboard.

## How it works

1. **Watches** VirtualMachines with label `forklift.konveyor.io/plan`
2. **Checks** if the Plan has annotation `vm-import.cozystack.io/adoption-enabled: "true"`
3. **Extracts** VM specs (instance type, disks, network, etc.)
4. **Creates** a `VMInstance` CRD with the extracted configuration
5. **Labels** the original VM with `cozystack.io/adopted: "true"`

## Adoption Process

```
Forklift imports VM
       ↓
VirtualMachine (KubeVirt) created
       ↓
Controller detects it
       ↓
VMInstance (Cozystack) created
       ↓
cozystack-controller creates HelmRelease
       ↓
VM visible in dashboard ✓
```

## Configuration

### values.yaml

```yaml
# Watch interval
controller:
  watchInterval: 30  # Check for new VMs every 30 seconds

# Optional: watch specific namespace only
controller:
  watchNamespace: "tenant-production"

# Optional: prefix for created VMInstance names
controller:
  namePrefix: "imported-"
```

### Disabling adoption

To disable adoption for a specific vm-import:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: VMImport
metadata:
  name: my-import
spec:
  enableAdoption: false  # Disable automatic adoption
  vms: [...]
```

## Labels and Annotations

### Labels added to VirtualMachine

- `cozystack.io/adopted: "true"` - Indicates VM has been adopted
- `cozystack.io/adopted-by: "<vminstance-name>"` - Links to VMInstance

### Labels on created VMInstance

- `cozystack.io/source: "vm-import"` - Source of VM
- `forklift.konveyor.io/plan: "<plan-name>"` - Original migration plan

### Annotations on created VMInstance

- `vm-import.cozystack.io/original-vm-name: "<vm-name>"` - Original VM name
- `vm-import.cozystack.io/adopted-at: "<timestamp>"` - When adopted

## RBAC

The controller requires cluster-wide permissions:

- **kubevirt.io/virtualmachines**: get, list, watch, update (to label adopted VMs)
- **forklift.konveyor.io/plans**: get, list, watch (to check adoption settings)
- **apps.cozystack.io/vminstances**: get, list, watch, create (to create VMInstances)
- **apps.cozystack.io/vmdisks**: get, list, watch, create (for disk adoption)
- **cdi.kubevirt.io/datavolumes**: get, list, watch (to discover disks)

## Troubleshooting

### VMs not being adopted

1. Check controller logs:
   ```bash
   kubectl logs -n cozy-forklift deployment/vm-adoption-controller
   ```

2. Verify VirtualMachine has Forklift label:
   ```bash
   kubectl get vm <vm-name> -n <namespace> -o jsonpath='{.metadata.labels.forklift\.konveyor\.io/plan}'
   ```

3. Check Plan annotation:
   ```bash
   kubectl get plan <plan-name> -n <namespace> -o jsonpath='{.metadata.annotations.vm-import\.cozystack\.io/adoption-enabled}'
   ```

### VMInstance already exists error

The controller skips VMs that already have a corresponding VMInstance. Check:

```bash
kubectl get vminstance -n <namespace>
```

## Manual adoption

If the controller is disabled or fails, you can manually adopt a VM using the provided script:

```bash
cd packages/apps/vm-import
./docs/scripts/adopt-vm.sh <vm-name> <namespace>
```

This generates a `VMInstance` YAML that you can apply manually.

## Architecture

The controller is a simple Go application that:
- Uses Kubernetes client-go for API access
- Runs as a Deployment with 1 replica (no leader election needed)
- Periodically polls for new VMs (no complex watch/cache mechanism)
- Creates resources using the dynamic client (unstructured)

## Deployment

Automatically deployed as a dependency of `vm-import-application`.

Or deploy standalone:

```bash
helm install vm-adoption-controller packages/system/vm-adoption-controller \
  -n cozy-forklift \
  --create-namespace
```

## Related Resources

- [VM Import Documentation](../../apps/vm-import/README.md)
- [VM Adoption Guide](../../apps/vm-import/docs/adoption.md)
- [Forklift Documentation](https://forklift.konveyor.io/)
