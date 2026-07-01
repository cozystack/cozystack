# Scenario: User restores a NATS JetStream backup

Two restore variants are supported.

## A. In-place restore

Restore back into the same NATS application.

```bash
./06-restore-in-place.sh
```

The script:

1. Deletes the stream on the source instance (simulating data loss).
   `nats stream restore` recreates the stream, so it **must not already
   exist** — the caller is responsible for removing it before submitting the
   `RestoreJob`.
2. Creates a `RestoreJob` whose `spec.backupRef.name` is the Backup name and
   does **not** set `spec.targetApplicationRef`. Per the API contract, the
   driver restores into `backup.spec.applicationRef`.
3. The Job driver renders the strategy Pod with `.Mode="restore"`. The Pod
   `GET`s `s3://<bucket>/<source>/<stream>.tar`, untars it, and runs
   `nats stream restore`. In-place, the source and target are the same app, so
   the S3 key resolves to the original.
4. After the Pod succeeds, the script verifies the message count is back.

## B. To-copy restore

Restore into a **different** NATS application instance.

```bash
./07-restore-to-copy.sh
```

The script:

1. Provisions a second `NATS/nats-restore` in the **same** namespace as the
   RestoreJob, and creates its `<target>-backup-s3` Secret pointing at the same
   bucket. `RestoreJob.spec.targetApplicationRef` is
   `corev1.TypedLocalObjectReference` (no `namespace` field), so the driver
   always restores into `restoreJob.Namespace`; cross-namespace restore is
   intentionally not supported.
2. Creates a `RestoreJob` with
   `spec.targetApplicationRef = { kind: NATS, name: nats-restore }`.
3. The Job driver fetches the *target* application and renders the Pod with
   `.Release.Name=nats-restore` (so it connects to the target and reads the
   target's credentials/S3 Secret), but keys the S3 object by the *source* via
   `.Backup.ApplicationRef.Name` — so the copy reads what the source wrote.
4. The script verifies the message count exists on the copy.

## Notes

- Both flows rely on the strategy template branching on `.Mode`. See
  `01-create-strategy.sh`.
- The tarball in object storage is not managed by the Cozystack `Backup`
  lifecycle: deleting the `Backup` CR removes the reference but leaves the S3
  object in place. Tenants who need to reclaim space should delete the object
  (or set a bucket lifecycle policy).
- `nats stream restore` recreates the stream's configuration from the snapshot;
  consumers are restored as captured at backup time.
