# Scenario: User backs up a ClickHouse instance

This scenario assumes the admin steps (`01-...`, `02-...`) have already run.

## Prerequisites

- Tenant namespace (default: `tenant-test`).
- A Cozystack `Bucket` (or any S3-compatible coordinates supplied via
  `spec.backup.*` on the ClickHouse application). The chart materialises
  `<release>-backup-s3` Secret in the tenant namespace and an in-pod
  `clickhouse-backup` sidecar that consumes it.

## Steps

```bash
./03-create-bucket.sh        # Provision Bucket and cache its credentials
./04-create-clickhouse.sh    # Provision ClickHouse with backup.enabled=true and write a sentinel row
./05-create-backupjob.sh     # Submit BackupJob and wait for Succeeded
```

## What happens during backup

1. The user submits a `BackupJob` referencing the ClickHouse instance and the
   `clickhouse-backup` BackupClass.
2. The backup-controller resolves the BackupClass → Altinity strategy and
   renders the Pod template against the user's ClickHouse.
3. A `batch/v1.Job` (owned by the BackupJob via `OwnerReferences`) is created
   in the tenant namespace. It runs the small alpine + `curl + jq` client
   that calls `POST http://chi-clickhouse-<release>-clickhouse-0-0:7171/backup/create_remote?name=<release>-<timestamp>`
   on the in-pod sidecar.
4. The `clickhouse-backup` sidecar (which shares `/var/lib/clickhouse` with
   the ClickHouse server) freezes MergeTree parts and uploads them to S3
   along with the table schemas.
5. The strategy Pod polls `/backup/actions` until the operation reaches
   `success` (or `error`).
6. On success, the controller creates a `Backup` CR in the same namespace.
   Its `spec.driverMetadata` records any BackupClass parameters under the
   `parameter/` prefix.

## Result

| Resource | Where | Purpose |
|---|---|---|
| `BackupJob/<bj-name>` | tenant namespace | Records the run; `status.phase=Succeeded`, `status.backupRef` points at the Backup |
| `Backup/<bj-name>` | tenant namespace | The restorable artifact reference (the actual data lives in S3 under the upstream tool's retention) |
| `batch/v1.Job/<bj-name>-backup` | tenant namespace | The completed strategy Pod |
