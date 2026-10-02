# Scenario: Cluster admin prepares Kafka topic-data backups

A cluster administrator performs the cluster-level preparation once. Tenants
will then be able to back up and restore the topic data of their Kafka
applications by referencing the BackupClass created here, with no further
admin action.

## Prerequisites

- A running Cozystack cluster with the backup-controller and
  backupstrategy-controller installed.
- `kubectl` access with permissions to create cluster-scoped
  `Job.strategy.backups.cozystack.io` and `BackupClass.backups.cozystack.io`.
- Reachable S3-compatible storage. The demo uses the in-cluster `Bucket` app
  (step 03); the tenant turns its coordinates and the endpoint's CA into a
  `<app>-backup-s3` Secret that the strategy Pod reads (step 04 / 07).

## Steps

```bash
./01-create-strategy.sh        # Job strategy: stock Strimzi Kafka image + a shell script
./02-create-backupclass.sh     # Maps Kind=Kafka -> the strategy, with the topics parameter
```

## What gets created

| Resource | Scope | Purpose |
|---|---|---|
| `Job.strategy.backups.cozystack.io/kafka-job` | Cluster | PodTemplateSpec for a `quay.io/strimzi/kafka` Pod that drains the topics with `kafka-console-consumer`, replays them with `kafka-console-producer`, and moves a tarball to/from S3 with `curl --aws-sigv4` |
| `BackupClass/kafka-backup` | Cluster | Maps `apps.cozystack.io/Kafka` to the strategy. `parameters.topics` selects the topics (empty = every non-internal topic); `parameters.replicationFactor`, when set, overrides the replication factor the backup captured when restore recreates a topic |

## How the strategy template gets rendered

The Job driver renders the `Job.spec.template` PodTemplateSpec with this
context for every BackupJob/RestoreJob run:

| Variable | Source |
|---|---|
| `.Application` | The `Kafka` (`apps.cozystack.io`) object referenced by the BackupJob (or `targetApplicationRef` on restore) — available to templates, though this one reads everything it needs from env |
| `.Release.Name` / `.Release.Namespace` | `metadata.name` / `metadata.namespace` of `.Application` |
| `.Parameters` | `BackupClass.spec.strategies[].parameters` (`topics`, `replicationFactor`); snapshotted onto the Backup's `driverMetadata` so restore re-renders with the same values |
| `.Mode` | `"backup"` or `"restore"` |
| `.Backup` | `{ Name, Namespace, ApplicationRef.{APIGroup,Kind,Name} }` (only for restore runs; `.ApplicationRef` points at the *source* release so to-copy restores read the source's S3 key) |

The same template renders both backup and restore Pods. The engine templates
each string field independently and cannot restructure the Pod per mode, so
the single Kafka container branches at runtime: `MODE` (from `{{ .Mode }}`)
selects drain + S3 `PUT` vs. S3 `GET` + recreate + replay.
