# Monitoring an Import

This guide shows how to monitor the progress of a VM import operation.

## Check Import Status

### View the VMImport resource

```bash
kubectl get vmimport -n production
kubectl describe vmimport production-migration -n production
```

### Check Migration Plan status

```bash
kubectl get plans -n production
kubectl describe plan production-migration -n production
```

Example output:

```yaml
Status:
  Conditions:
    - Type: Ready
      Status: "True"
      Reason: AllVirtualMachinesMigrated
  Migration:
    Completed: 4
    Failed: 0
    Running: 0
    Total: 4
```

### Check individual Migrations

```bash
kubectl get migrations -n production
kubectl describe migration production-migration -n production
```

### Monitor VM creation

As VMs are migrated, they appear as KubeVirt VirtualMachines:

```bash
# Watch for new VMs
kubectl get vm -n production --watch

# Filter by import (Forklift labels VMs with the Plan UID, not its name)
PLAN_UID=$(kubectl get plan production-migration -n production -o jsonpath='{.metadata.uid}')
kubectl get vm -n production -l plan=$PLAN_UID

# Check adoption labels
kubectl get vm -n production -l cozystack.io/adopted=true
```

## Detailed Progress Tracking

### Monitor disk transfers

```bash
# Watch DataVolumes being created
kubectl get dv -n production --watch

# Check CDI importer pods
kubectl get pods -n production -l app=containerized-data-importer

# View importer logs
kubectl logs -n production -l app=containerized-data-importer --tail=50 -f
```

### Check Forklift controller logs

```bash
# Main controller
kubectl logs -n konveyor-forklift deployment/forklift-controller -f

# Validation service
kubectl logs -n konveyor-forklift deployment/forklift-validation -f
```

## Troubleshooting Import Issues

### VM stuck in "Preparing" state

```bash
# Check Plan events
kubectl describe plan production-migration -n production | grep -A 10 Events

# Check provider connectivity
kubectl get providers -n production
kubectl describe provider production-migration-source -n production
```

### Network mapping failures

```bash
# Verify NetworkMap
kubectl get networkmap production-migration -n production -o yaml

# Check Multus networks exist
kubectl get network-attachment-definitions -n production

# Verify network IDs match VMware
```

### Storage mapping errors

```bash
# Verify StorageMap
kubectl get storagemap production-migration -n production -o yaml

# Check StorageClasses
kubectl get storageclass

# Check PVC creation
kubectl get pvc -n production
```

### Authentication failures

```bash
# Test vCenter credentials
kubectl get secret prod-vcenter-creds -n production -o jsonpath='{.data.user}' | base64 -d
kubectl get secret prod-vcenter-creds -n production -o jsonpath='{.data.password}' | base64 -d

# Check Provider status
kubectl describe provider production-migration-source -n production | grep -A 5 "Conditions:"
```

## Post-Migration Validation

### Verify all VMs are created

```bash
# Count expected VMs
EXPECTED=4

# Count created VMs
PLAN_UID=$(kubectl get plan production-migration -n production -o jsonpath='{.metadata.uid}')
ACTUAL=$(kubectl get vm -n production -l plan=$PLAN_UID --no-headers | wc -l)

echo "Expected: $EXPECTED, Actual: $ACTUAL"
```

### Check VM functionality

```bash
# Start VMs
kubectl patch vm nginx-frontend-01 -n production --type merge -p '{"spec":{"running":true}}'

# Wait for VM to be ready
kubectl wait --for=condition=Ready vm/nginx-frontend-01 -n production --timeout=300s

# Access VM console
virtctl console nginx-frontend-01 -n production
```

### Verify network connectivity

```bash
# Check VM interfaces
kubectl get vm nginx-frontend-01 -n production -o jsonpath='{.status.interfaces}'

# Test connectivity from VM
virtctl ssh nginx-frontend-01 -n production -- ping -c 3 example.com
```

### Verify storage

```bash
# List attached disks
kubectl get vm nginx-frontend-01 -n production -o jsonpath='{.spec.template.spec.volumes[*].name}'

# Check DataVolume status
kubectl get dv -n production

# Verify PVC binding
kubectl get pvc -n production
```

### Check adoption status

```bash
# List adopted VMs
kubectl get vm -n production -l cozystack.io/adopted=true -o custom-columns=NAME:.metadata.name,RUNNING:.status.printableStatus,ADOPTED:.metadata.labels.cozystack\\.io/adopted

# Verify dashboard visibility
# (Check Cozystack dashboard UI under "Imported VMs" section)
```

## Timeline Expectations

- Small VM (20GB): 5-15 minutes
- Medium VM (100GB): 20-45 minutes
- Large VM (500GB): 1-3 hours

**Note**: Times vary based on network bandwidth, storage speed, and vCenter load.
