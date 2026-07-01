# Scenario: User backs up a NATS JetStream stream

This scenario assumes the admin steps (`01-...`, `02-...`) have already run.

## Prerequisites

- Tenant namespace (default: `tenant-test`).
- A Cozystack `Bucket` (step 03) and the `<app>-backup-s3` Secret derived from
  it (step 04). The generic `Job` strategy has no chart support to emit this
  Secret, so the tenant creates it from the bucket's `BucketInfo` (see
  `create_s3_secret` in `00-helpers.sh`).
- A `NATS` application with JetStream enabled and a stream holding data.

## Steps

```bash
./03-create-bucket.sh        # Provision Bucket and cache its credentials
./04-create-nats.sh          # Provision NATS, create the S3 Secret, seed a stream
./05-create-backupjob.sh     # Submit BackupJob and wait for Succeeded
```

## What happens during backup

1. The user submits a `BackupJob` referencing the NATS instance and the
   `nats-backup` BackupClass.
2. The backup-controller resolves the BackupClass → `Job` strategy and renders
   the Pod template against the user's NATS application, with `.Mode="backup"`.
3. A `batch/v1.Job` (owned by the BackupJob via `OwnerReferences`) is created
   in the tenant namespace. Its single `natsio/nats-box` container:
   - runs `nats stream backup <stream> /tmp/bk` against
     `<release>.<namespace>.svc:4222`, authenticating with the `<release>-credentials` Secret;
   - `tar`s the snapshot and `PUT`s it to
     `s3://<bucket>/<release>/<stream>.tar` using `curl --aws-sigv4`.
4. The controller watches the Job's terminal condition. On `Complete` it
   creates a `Backup` CR in the same namespace, recording the BackupClass
   parameters under `spec.driverMetadata` (the `parameter/` prefix), so a later
   restore re-renders the strategy with the same `stream`/`natsUser`.

## Result

| Resource | Where | Purpose |
|---|---|---|
| `BackupJob/<bj-name>` | tenant namespace | Records the run; `status.phase=Succeeded`, `status.backupRef` points at the Backup |
| `Backup/<bj-name>` | tenant namespace | The restorable artifact reference (the tarball lives in S3 under `<release>/<stream>.tar`) |
| `batch/v1.Job/<bj-name>-backup` | tenant namespace | The completed strategy Pod |
