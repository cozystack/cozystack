# Scenario: recover a deleted object from a bucket backup

A tenant stores objects in an `apps.cozystack.io/Bucket` and enables backups by binding `Kind: Bucket` to a Bucket strategy (the platform ships `cozy-default-bucket`). Each `BackupJob` mirrors the whole bucket into the platform `cozy-backups` bucket under `<namespace>/<bucket>/<backupjob>/`.

## In-place restore

After an accidental deletion the tenant creates a `RestoreJob` with `backupRef` set and no `targetApplicationRef` (`35-restorejob-in-place.yaml`). The driver provisions a read-write `BucketAccess` on the source bucket and mirrors the chosen backup snapshot back over the live contents, deleting objects the snapshot does not contain, so the bucket ends up byte-for-byte as it was at backup time. This is destructive to anything written after the backup — take a fresh backup first if the current state matters.

## To-copy restore

To inspect a backup without touching the live bucket, the tenant deploys a second, empty `Bucket` and creates a `RestoreJob` whose `targetApplicationRef` points at it (`40-restorejob-to-copy.yaml`). The driver re-points the copy at the target's own credentials and mirrors the snapshot into it while the source bucket keeps serving traffic.

## Why no point-in-time recovery

Object storage has no continuous change log the way a database has WAL/oplog, so there is nothing to replay to an arbitrary timestamp. A backup is a discrete snapshot; restore replays exactly one. Keep more restore points by running backups more often (each lands under its own `<backupjob>` prefix).
