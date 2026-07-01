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
- The actual archive in object storage is owned by `clickhouse-backup`'s
  retention policy on the sidecar, not by the Cozystack `Backup` lifecycle.
  Deleting the Cozystack `Backup` removes the CR but leaves the S3 archive
  untouched; tenants who need to purge an archive should call
  `DELETE /backup/<name>/remote` on the sidecar (or use the upstream tool's
  retention configuration).
