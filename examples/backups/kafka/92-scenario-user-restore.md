# Scenario: User restores a Kafka topic-data backup

Two restore variants are supported. Both append: the strategy recreates a
topic only if it is absent and then replays every record, so it refuses a
target topic that already holds data.

## A. In-place restore

Restore back into the same Kafka application.

```bash
./06-restore-in-place.sh
```

The script:

1. Deletes the topic on the source instance (simulating data loss) and waits
   for it to disappear. The topic **must be absent or empty** before the
   `RestoreJob` is submitted — the caller is responsible for that.
2. Creates a `RestoreJob` whose `spec.backupRef.name` is the Backup name (from
   `.backup-name.env`) and does **not** set `spec.targetApplicationRef`. Per
   the API contract, the driver restores into `backup.spec.applicationRef`.
3. The Job driver renders the strategy Pod with `.Mode="restore"`. The Pod
   `GET`s `s3://<bucket>/<namespace>/<source>/kafka-topics.tar`, untars it,
   validates the manifest, recreates each topic with its original partition
   count and replication factor (or the `replicationFactor` override),
   confirms the topic's shape and that it is empty, then replays the
   partition files topic by topic with `kafka-console-producer` and checks
   the record count that landed. In-place, the source and target are the
   same app, so the S3 key resolves to the original.
4. After the Pod succeeds, the script verifies the record count, diffs the
   content against the snapshot taken in step 04, and checks that the
   restored topic carries the replication factor captured from the source.
5. It then submits a second `RestoreJob` against the now-populated topic and
   requires it to settle `Failed` — every strategy Pod attempt stops at the
   emptiness check — with the topic unchanged.

## B. To-copy restore

Restore into a **different** Kafka application instance.

```bash
./07-restore-to-copy.sh
```

The script:

1. Provisions a second `Kafka/kafka-restore` with the same broker count in the
   **same** namespace as the RestoreJob, and creates its `<target>-backup-s3`
   Secret pointing at the same bucket. `RestoreJob.spec.targetApplicationRef`
   is `corev1.TypedLocalObjectReference` (no `namespace` field), so the driver
   always restores into `restoreJob.Namespace`; cross-namespace restore is
   intentionally not supported.
2. Creates a `RestoreJob` with
   `spec.targetApplicationRef = { kind: Kafka, name: kafka-restore }`.
3. The Job driver fetches the *target* application and renders the Pod with
   `.Release.Name=kafka-restore` (so it connects to the target and reads the
   target's S3 Secret), but keys the S3 object by the *source* via
   `.Backup.ApplicationRef.Name` — so the copy reads what the source wrote.
4. The script verifies the record count, content and replication factor on
   the copy.

## Notes

- Both flows rely on the strategy template branching on `.Mode`. See
  `01-create-strategy.sh`.
- A restore Pod that dies mid-replay is retried by the Job's `backoffLimit`,
  and the retry stops at the emptiness check on the first topic the dead Pod
  had already replayed. The Pod log names each topic before its replay and
  again once it landed; empty or delete every topic it shows as started, not
  only the one the refusal names, then re-run.
- The tarball in object storage is not managed by the Cozystack `Backup`
  lifecycle: deleting the `Backup` CR removes the reference but leaves the S3
  object in place. Tenants who need to reclaim space should delete the object
  (or set a bucket lifecycle policy).
- Consumer-group offsets, topic configs, ACLs and users are not part of the
  backup; consumers start from the restored topic's fresh offsets.
