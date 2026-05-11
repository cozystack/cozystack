# FoundationDB backup/restore example

This directory shows how to back up and restore a Cozystack-managed
`FoundationDB` application using the cluster's `FoundationDB` backup
strategy driver. The driver delegates to the [FoundationDB Kubernetes
operator][fdb-operator]: each Cozystack `BackupJob` materialises an
`apps.foundationdb.org/v1beta2 FoundationDBBackup` CR (a continuous
`backup_agent` Deployment that streams data to S3), and each `RestoreJob`
materialises a `FoundationDBRestore` CR (a one-shot `fdbrestore` against
the destination cluster).

Because the operator only permits one running backup directory per
cluster, the driver flips any prior `FoundationDBBackup` to
`backupState=Stopped` before starting a new one. Each Cozystack
`BackupJob` therefore owns a discrete blob-store directory keyed by its
name.

## Step order

| File | Role | Triggered by |
|---|---|---|
| `00-helpers.sh` | Shared bash helpers and env defaults; sourced by every step. | n/a |
| `01-create-strategy.sh` | Creates the cluster-scoped `FoundationDB` strategy. | admin |
| `02-create-bucket.sh` | Provisions a `Bucket`, mints `<app>-fdb-backup-creds` Secrets, and creates the `BackupClass` bound to the FoundationDB strategy with the resolved S3 coordinates. Caches raw creds in `.bucket-info.env` (chmod 600). `cleanup.sh` removes it. | tenant |
| `03-create-foundationdb-src.sh` | Provisions the source FoundationDB and writes a sentinel key/value. | tenant |
| `04-create-backupjob.sh` | Submits a `BackupJob` and waits for the first snapshot. | tenant |
| `05-restore-in-place.sh` | Clears the sentinel and restores into the same cluster via `RestoreJob`. | tenant |
| `06-restore-to-copy.sh` | Provisions a second FoundationDB and restores into it via `RestoreJob.spec.targetApplicationRef`. | tenant |
| `cleanup.sh` | Removes everything created by the demo. | admin or tenant |
| `run-all.sh` | Convenience runner that executes 01..06 in order. | demo |
| `90-scenario-admin-prepare.md` | Narrative for the admin preparation steps. | docs |
| `91-scenario-user-backup.md` | Narrative for the user backup flow. | docs |
| `92-scenario-user-restore.md` | Narrative for the user restore flow (in-place and to-copy). | docs |

> The `BackupClass` is created in step 02 with already-resolved bucket
> coordinates rather than a placeholder pre-applied by an earlier admin
> step. An earlier draft of this demo split that across two steps and
> wrote `accountName: "REPLACE_ME"` to the cluster first, which passed
> the operator's `MinLength=1` validation but failed at backup_agent
> runtime — a tenant who skipped the patch step burned the 45-minute
> backup deadline on a half-configured class. The current step 02 fold
> avoids that trap; the BackupClass only ever exists with real
> coordinates.

## Environment variables (overrides)

All variables come from `00-helpers.sh`:

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `tenant-root` | Tenant namespace for the demo. |
| `FDB_NAME` | `fdb-src` | Source FoundationDB application name. |
| `FDB_RESTORE_NAME` | `fdb-dst` | Target FoundationDB for the to-copy restore. |
| `BUCKET_NAME` | `foundationdb-backups` | Cozystack `Bucket` to provision. |
| `BACKUPCLASS_NAME` | `foundationdb-default` | BackupClass name (cluster-scoped). |
| `STRATEGY_NAME` | `foundationdb-strategy-default` | Strategy name (cluster-scoped). |
| `BACKUPJOB_NAME` | `foundationdb-backup-job` | BackupJob name. |
| `RESTOREJOB_INPLACE_NAME` | `foundationdb-restore-inplace` | In-place RestoreJob name. |
| `RESTOREJOB_TOCOPY_NAME` | `foundationdb-restore-to-copy` | To-copy RestoreJob name. |

## Prerequisites

- Cozystack cluster with `backup-controller` and `backupstrategy-controller`
  running, with the FoundationDB dispatch wired (see
  `internal/backupcontroller/foundationdbstrategy_controller.go`).
- `kubectl`, `jq`.
- The `apps.foundationdb.org` CRDs installed by
  `packages/system/foundationdb-operator` (`FoundationDBCluster`,
  `FoundationDBBackup`, `FoundationDBRestore`).
- The Cozystack `foundationdb-rd` ApplicationDefinition installed so
  `apps.cozystack.io/FoundationDB` renders an HR.

## Notes

- The chart-level `backup.*` values block is marked `DEPRECATED`. New
  tenants should use this BackupClass + FoundationDB strategy flow
  instead. Existing tenants with `backup.enabled=true` continue to render
  the legacy `FoundationDBBackup` CR unchanged.
- The strategy template references a per-app blob credentials Secret named
  `{{ .Application.metadata.name }}-fdb-backup-creds`. Step 02
  materialises one Secret per app instance (source and to-copy target).
- The to-copy variant is the stronger restore proof: it witnesses the
  sentinel key landing on an independent cluster.

[fdb-operator]: https://github.com/FoundationDB/fdb-kubernetes-operator
