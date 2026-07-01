# Scenario: User Creates a VM Backup

This scenario demonstrates how a tenant user provisions a virtual machine and
creates a backup of it. The backup captures the VM definition, its disks, and
network identity so it can be restored later.

## Prerequisites

- Cluster administrator has completed the setup from
  [scenario-admin.md](scenario-admin.md) (strategies and BackupClass exist).
- A tenant namespace (default: `tenant-root`).
- `kubectl` access scoped to the tenant namespace.

## Steps

### 1. Create a VMDisk

```bash
./03-create-vmdisk.sh
```

Creates a `VMDisk` named `ubuntu-source` that downloads the Ubuntu Noble cloud
image and provisions a 20Gi replicated PVC. The disk serves as the boot volume
for the virtual machine.

Wait for the image download to complete before proceeding. You can monitor
progress with:

```bash
kubectl get vmdisk ubuntu-source -n tenant-root -w
```

### 2. Create a VMInstance

```bash
./04-create-vminstance.sh
```

Creates a `VMInstance` named `test` that references the `ubuntu-source` disk.
The VM boots with the `ubuntu` instance profile on a `u1.medium` instance type.

Verify the VM is running:

```bash
kubectl get vmi -n tenant-root
```

### 3. Create a BackupJob

```bash
./05-create-backupjob.sh
```

Creates a `BackupJob` named `test-backup` that triggers a full backup of the
`test` VMInstance. The backup controller:

1. Resolves the `velero` BackupClass to find the matching `vminstance-strategy`.
2. Discovers underlying resources (DataVolumes, OVN IP/MAC from the VM pod).
3. Creates a Velero Backup in the `cozy-velero` namespace with label selectors
   scoped to this specific VM and its disks.
4. Velero snapshots and moves the volume data to the configured storage
   location.

The script waits up to 10 minutes for the BackupJob to reach the `Succeeded`
phase. On completion, a `Backup` object is created in the same namespace
containing:

- `spec.applicationRef` — reference to the backed-up VMInstance.
- `spec.strategyRef` — reference to the Velero strategy used.
- `spec.driverMetadata` — Velero backup name for later restore.
- `status.underlyingResources` — captured DataVolume names and OVN IP/MAC
  addresses.

You can inspect the resulting Backup:

```bash
kubectl get backups -n tenant-root
kubectl get backup test-backup -n tenant-root -o yaml
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `tenant-root` | Tenant namespace for all resources |

## Result

After completing these steps you have a `Backup` artifact that can be used to
restore the VM either in-place or to a different namespace. See
[scenario-user-restore.md](scenario-user-restore.md) for restore options.
