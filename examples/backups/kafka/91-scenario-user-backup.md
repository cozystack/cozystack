# Scenario: User backs up Kafka topic data

This scenario assumes the admin steps (`01-...`, `02-...`) have already run.

## Prerequisites

- Tenant namespace (default: `tenant-root`, the tenant whose Pods can reach
  the in-cluster seaweedfs; see the README).
- A Cozystack `Bucket` (step 03) and the `<app>-backup-s3` Secret derived from
  it (step 04). The generic `Job` strategy has no chart support to emit this
  Secret, so the tenant creates it from the bucket's `BucketInfo` plus the S3
  endpoint's CA (see `create_s3_secret` in `00-helpers.sh`).
- A `Kafka` application with at least one plain (neither compacted nor
  transactional) topic holding data.

## Steps

```bash
./03-create-bucket.sh        # Provision Bucket, cache its credentials and the endpoint CA
./04-create-kafka.sh         # Provision Kafka, create the S3 Secret, seed a topic
./05-create-backupjob.sh     # Submit BackupJob and wait for Succeeded
```

## What happens during backup

1. The user submits a `BackupJob` referencing the Kafka instance and the
   `kafka-backup` BackupClass.
2. The backup-controller resolves the BackupClass → `Job` strategy and renders
   the Pod template against the user's Kafka application, with `.Mode="backup"`.
3. A `batch/v1.Job` (owned by the BackupJob via `OwnerReferences`) is created
   in the tenant namespace. Its single Strimzi Kafka container:
   - resolves the topic list from `parameters.topics` (or every non-internal
     topic), refusing a name outside Kafka's charset or listed twice;
   - for each topic reads every partition's begin and end offset once, its
     `PartitionCount` and `ReplicationFactor`, and refuses the run if the
     offset listing is short of the partition count;
   - drains `[begin, end)` of each partition with `kafka-console-consumer`
     into one `key<TAB>value` file per partition, refusing the run on a short
     read;
   - writes a manifest (`topic partition begin end replicationFactor` per
     line), `tar`s everything and `PUT`s it to
     `s3://<bucket>/<namespace>/<release>/kafka-topics.tar` using
     `curl --aws-sigv4`, verified against the CA from the Secret.
4. The controller watches the Job's terminal condition. On `Complete` it
   creates a `Backup` CR in the same namespace, recording the BackupClass
   parameters under `spec.driverMetadata` (the `parameter/` prefix), so a later
   restore re-renders the strategy with the same `topics` and
   `replicationFactor`.

## Result

| Resource | Where | Purpose |
|---|---|---|
| `BackupJob/<bj-name>` | tenant namespace | Records the run; `status.phase=Succeeded`, `status.backupRef` points at the Backup |
| `Backup/<bj-name>` | tenant namespace | The restorable artifact reference (the tarball lives in S3 under `<namespace>/<release>/kafka-topics.tar`); step 05 caches its name in `.backup-name.env` for the restore steps |
| `batch/v1.Job/<bj-name>-backup` | tenant namespace | The completed strategy Pod |
