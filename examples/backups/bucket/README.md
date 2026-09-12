# S3 Bucket backup/restore

This directory demonstrates backing up an `apps.cozystack.io/Bucket` (SeaweedFS-backed S3 storage) with the `strategy.backups.cozystack.io/Bucket` driver, which copies objects S3-to-S3 into a destination bucket and mirrors them back on restore.

Unlike the database examples, this demo provisions its own destination bucket (`01-bucket-dest.yaml`) and binds a test-scoped `BackupClass` (`15-backupclass.yaml`) rather than using the platform `cozy-default` BackupClass and the shared `cozy-backups` system bucket, so it runs on a CI cluster where neither is provisioned. On a real cluster with the platform bucket present you would instead bind `Kind: Bucket` through the shipped `cozy-default` BackupClass and drop the local strategy.

## Run

```
NAMESPACE=tenant-root ./run-all.sh
./cleanup.sh
```

`run-all.sh` performs an honest round-trip: it seeds a sentinel object into the source bucket, runs a `BackupJob`, deletes the sentinel, restores in-place, and asserts the exact bytes came back through object storage, then repeats the assertion for a to-copy restore into a separate bucket (`30-bucket-target.yaml`).

## Files

- `00-bucket-src.yaml` / `01-bucket-dest.yaml` — source and destination buckets.
- `10-bucket-strategy.yaml` — the Bucket strategy, retargeted at the destination bucket (placeholders filled by `run-all.sh`).
- `15-backupclass.yaml` — binds `Kind: Bucket` to the strategy.
- `20-plan.yaml` / `25-backupjob-adhoc.yaml` — scheduled and ad-hoc backups.
- `30-bucket-target.yaml` — the to-copy restore target.
- `35-restorejob-in-place.yaml` / `40-restorejob-to-copy.yaml` — the two restore flows.
- `run-all.sh` / `cleanup.sh` / `00-helpers.sh` — the harness the Chainsaw e2e drives.

There is no point-in-time recovery: object storage keeps discrete snapshots with no continuous log to replay, so a restore replays one backup.
