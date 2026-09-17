# VMDisk backup/restore example

This directory shows how to back up and restore a standalone Cozystack `VMDisk` (a HelmRelease plus a DataVolume-backed PVC, with no VMInstance attached) using the cluster's `Velero` backup strategy driver. Each `BackupJob` drives a Velero CSI-snapshot backup whose data mover (kopia) streams the LINSTOR block volume to S3; each in-place `RestoreJob` recreates the disk from that artefact.

The numbered scripts run in order (or via `run-all.sh`):

1. `01-create-strategy.sh` — a `Velero` strategy for the `VMDisk` kind.
2. `02-create-backupclass.sh` — a `BackupClass` binding `VMDisk` to it.
3. `03-create-vmdisk.sh` — a standalone `VMDisk`, waited until imported.
4. `04-create-backupjob.sh` — an ad-hoc `BackupJob`, waited to `Succeeded`.
5. `05-restore-in-place.sh` — an in-place `RestoreJob`, then asserts that the PVC was recreated from the backup: it has a new uid, carries Velero's `velero.io/restore-name` label, and the original survives as `<name>-orig-<hash>`.

`cleanup.sh` tears everything down non-interactively.

## In-place restore

The driver prepares a `VMDisk` restore target the same way it does a VMInstance disk: it suspends the `vm-disk-<name>` HelmRelease and deletes its DataVolume (so Flux/CDI do not recreate the PVC and race the data mover), and, with `keepOriginalPVC: true`, renames the live PVC to `<name>-orig-<hash>` so that Velero creates a fresh PVC under the original name. `cleanup.sh` resumes the HelmRelease and reclaims the renamed PVC.

## Detached disks and WaitForFirstConsumer

On a StorageClass with `volumeBindingMode: WaitForFirstConsumer` (the LINSTOR classes), the Velero data mover does not start until a scheduled Pod has selected a node for the restored PVC. A VMInstance restore gets that from its virt-launcher Pod. A detached disk has no consumer, so its `RestoreJob` stays `Running` until something mounts the PVC, and fails once Velero's expose timeout passes. `05-restore-in-place.sh` therefore starts a minimal `pause` Pod on the restored PVC, standing in for the VM that would attach the disk, and deletes it once the restore has succeeded. On a real cluster, attaching the disk to a VMInstance does the same.

## Overrides

Environment variables (see `00-helpers.sh`) tune the flow: `NAMESPACE`, `BACKUP_STORAGE_LOCATION` (defaults to the platform `cozy-default` BSL), `VMI_DISK_STORAGE_CLASS` (defaults to `replicated`; set it to your cluster's class), and the `IMPORT_WAIT` / `BACKUP_WAIT` / `RESTORE_WAIT` budgets. `hack/e2e-chainsaw/vminstance/` drives this directory as the `vmdisk-2-backup-roundtrip` e2e test.
