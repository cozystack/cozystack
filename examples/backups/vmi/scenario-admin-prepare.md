# Scenario: Cluster Administrator Configures Backups

This scenario walks through the cluster-level setup required before users can
back up and restore their virtual machines. A cluster administrator creates
Velero strategies and a BackupClass that binds those strategies to Cozystack
application types.

## Prerequisites

- A running Cozystack cluster with the backup-controller installed.
- Velero deployed in the `cozy-velero` namespace with a configured
  BackupStorageLocation (default: `default`).
- `kubectl` access with cluster-admin privileges.

## Steps

### 1. Create Velero Strategies

```bash
./01-create-strategies.sh
```

This creates two cluster-scoped `Velero` strategy objects:

| Strategy | Purpose |
|---|---|
| `vminstance-strategy` | Defines backup and restore templates for `VMInstance` applications. Includes VirtualMachine, PVCs, HelmReleases, pods (required by kubevirt-velero-plugin), and supporting resources. Uses `snapshotMoveData: true` for portable volume snapshots. |
| `vmdisk-strategy` | Defines backup and restore templates for standalone `VMDisk` applications. Covers HelmReleases, PVCs, and ConfigMaps. |

Both strategies use Go templates with `{{ .Application.metadata.name }}`,
`{{ .Application.metadata.namespace }}`, and `{{ .Parameters.backupStorageLocationName }}`
so they can be reused across any application instance.

**Key design details:**

- `orLabelSelectors` scope backups to only the resources belonging to a
  specific application, preventing cross-contamination between VMs in the
  same namespace.
- The restore spec excludes `datavolumes.cdi.kubevirt.io` to avoid conflicts
  when the HelmRelease of VMDisk recreates DataVolumes after restore.
- `existingResourcePolicy: update` allows restoring over existing resources
  during in-place restore.

### 2. Create BackupClass

```bash
./02-create-backupclass.sh
```

Creates a `BackupClass` named `velero` that maps application types to their
strategies:

| Application Kind | Strategy | Parameters |
|---|---|---|
| `VMInstance` (`apps.cozystack.io`) | `vminstance-strategy` | `backupStorageLocationName: default` |
| `VMDisk` (`apps.cozystack.io`) | `vmdisk-strategy` | `backupStorageLocationName: default` |

The `backupStorageLocationName` parameter can be overridden via the
`BACKUP_STORAGE_LOCATION` environment variable if your Velero installation
uses a non-default storage location.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `BACKUP_STORAGE_LOCATION` | `default` | Velero BackupStorageLocation name |

## Result

After completing these steps the cluster is ready for users to create backups
of their `VMInstance` and `VMDisk` applications by referencing the `velero`
BackupClass. No further admin action is required for individual backup or
restore operations.
