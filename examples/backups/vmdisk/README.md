# VMDisk backup/restore example

This directory shows how to back up and restore a standalone Cozystack
`VMDisk` (a HelmRelease plus a DataVolume-backed PVC, with no VMInstance
attached) using the cluster's `Velero` backup strategy driver. Each
`BackupJob` drives a Velero CSI-snapshot backup whose data mover (kopia)
streams the LINSTOR block volume to S3; each in-place `RestoreJob`
recreates the disk from that artefact.

The numbered scripts run in order (or via `run-all.sh`):

1. `01-create-strategy.sh` — a `Velero` strategy for the `VMDisk` kind.
2. `02-create-backupclass.sh` — a `BackupClass` binding `VMDisk` to it.
3. `03-create-vmdisk.sh` — a standalone `VMDisk`, waited until imported.
4. `04-create-backupjob.sh` — an ad-hoc `BackupJob`, waited to `Succeeded`.
5. `05-restore-in-place.sh` — an in-place `RestoreJob`, then asserts the
   recreated PVC carries Velero's `velero.io/restore-name` label (the one
   signal only a real restore produces).

`cleanup.sh` tears everything down non-interactively.

## In-place restore

The driver prepares a `VMDisk` restore target the same way it does a
VMInstance disk: it suspends the `vm-disk-<name>` HelmRelease and deletes
its DataVolume (so Flux/CDI do not recreate the PVC and race the data
mover), and — with the default `keepOriginalPVC: true` — renames the live
PVC to `<name>-orig-<hash>` before the data mover repopulates it. `cleanup.sh`
resumes the HelmRelease and reclaims that renamed PVC.

## Overrides

Environment variables (see `00-helpers.sh`) tune the flow: `NAMESPACE`,
`BACKUP_STORAGE_LOCATION` (defaults to the platform `cozy-default` BSL),
`VMI_DISK_STORAGE_CLASS` (defaults to `replicated`; set it to your cluster's
class), and the `IMPORT_WAIT` / `BACKUP_WAIT` / `RESTORE_WAIT` budgets.
`hack/e2e-chainsaw/vminstance/` drives this directory as the
`vmdisk-2-backup-roundtrip` e2e test.
