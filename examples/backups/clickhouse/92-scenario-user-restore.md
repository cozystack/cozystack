# Scenario: User restores a ClickHouse backup

Two restore variants are supported.

## A. In-place restore

Restore back into the same ClickHouse application.

```bash
./06-restore-in-place.sh
```

The script:

1. Drops the sentinel table on the source instance (simulating data loss).
   The strategy template does not pass clickhouse-backup's `--rm` flag, so
   the **caller is responsible** for ensuring no conflicting tables exist
   before submitting the `RestoreJob`.
2. Creates a `RestoreJob` whose `spec.backupRef.name` is the Backup name and
   does **not** set `spec.targetApplicationRef`. Per the API contract, the
   driver restores into `backup.spec.applicationRef`.
3. The Altinity driver renders the strategy Pod with `.Mode="restore"`. The
   Pod calls the in-pod `clickhouse-backup` sidecar over HTTP:
   `POST /backup/restore_remote/<name>` with no query params, which restores
   both schema and data parts.
4. After the Pod succeeds, the script verifies that the sentinel row is back.

## B. To-copy restore

Restore into a **different** ClickHouse application instance.

```bash
./07-restore-to-copy.sh
```

The script:

1. Provisions a second `ClickHouse/clickhouse-restore` in the **same**
   namespace as the RestoreJob. `RestoreJob.spec.targetApplicationRef`
   is `corev1.TypedLocalObjectReference` (no `namespace` field), so the
   driver always restores into `restoreJob.Namespace`; cross-namespace
   restore is intentionally not supported.
2. Creates a `RestoreJob` with
   `spec.targetApplicationRef = { kind: ClickHouse, name: clickhouse-restore }`.
3. The Altinity driver fetches the *target* application object and renders
   the Pod template with `.Release.Name=clickhouse-restore`. The same backup
   is applied to the new instance.
4. The script verifies the sentinel row exists on the copy.

## Notes

- Both flows assume the strategy template uses `.Mode` to switch between
  `create_remote` and `restore_remote`. See `01-create-strategy.sh`.
- On `backup.useSystemBucket: true`, deleting the Cozystack `Backup` deletes its archive through the sidecar and the `Backup` stays until that delete finished. When the ClickHouse application is already gone or no longer uses the system bucket, or the archive is named without its BackupJob's UID (a backup still running when the controller was upgraded, whose name another run of the same second may share), the `Backup` is released with an `ArtifactNotDeleted` Warning Event naming the archive left in the bucket; a delete that fails is retried, after a wait that starts at 30 seconds and doubles with each failure up to 30 minutes, and each failure is reported as an `ArtifactCleanupFailed` Warning Event; the `backups.cozystack.io/skip-artifact-cleanup: "true"` annotation releases it without starting a delete, though a delete the sidecar already accepted still runs to the end. A `Backup` taken before the archive name was recorded, or by a strategy that does not use `.ArchiveName`, does not name its archive and leaves it behind. So does a `Backup` that names its archive but not the bucket setting, as one recorded by a controller older than this behaviour does; it is released without an Event.
- On the tenant's own bucket the archive's lifecycle stays the tenant's: deleting the `Backup` leaves it, and `POST /backup/delete/remote/<name>` on the sidecar (port 7171, basic auth from the `<release>-backup-api-auth` Secret) deletes one by hand.
