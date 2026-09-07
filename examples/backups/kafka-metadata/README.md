# Kafka topic-metadata backup/restore demo

This demo backs up and restores the **topic metadata** of a Cozystack-managed `Kafka` application and proves integrity by round-tripping a per-run `retention.ms` sentinel through object storage. It runs a demo `BackupClass` + strategy that `run-all.sh` derives from the platform-shipped `cozy-default-kafka` (so the driver script stays a single source of truth), pointed at an S3 endpoint the in-cluster backup Pod can actually reach — the shipped strategy's advertised external ingress is not routable from inside every cluster.

## What is backed up

Strimzi ships no backup CRD and this strategy deliberately does not touch message data. The `cozy-default-kafka` strategy exports every non-internal topic's **definition** — partition count, replication factor, and non-default topic configs (retention, compaction, `min.insync.replicas`, ...) — through the Kafka Admin API (`kafka-topics --describe`), stores it in the system bucket, and restores it with `kafka-topics --create` + `kafka-configs --alter`.

Out of scope: **message payloads** (a volume-snapshot strategy, not this one), **consumer-group offsets** (a log position, only meaningful with a byte-identical data restore), **ACLs and quotas**, and **KafkaUsers**. There is no point-in-time recovery.

Restore is additive at the topic-set level — it recreates absent topics and (re)sets their configs, and never deletes topics that are live but not in the backup — but it does **not** silently accept a divergent topic. For a topic that already exists, restore compares the live shape against the backup: it grows the partition count with `--alter` when the backup asks for more, and **fails loudly** (non-zero, naming the topic and both values) when the backup asks for fewer partitions or a different replication factor, because Kafka cannot shrink partitions or change RF in place and a restore that cannot reach the recorded state must not report `Succeeded`. Restored topics are created directly through the Admin API, so on the target they are **unmanaged** (no `KafkaTopic` CR) — the accepted tradeoff for an Admin-API-only driver. This is the "bootstrap an empty cluster and apply the metadata state onto it" flow.

**Topics owned by a `KafkaTopic` CR.** If a topic is declared in the Kafka app's `spec.topics`, the Strimzi Topic Operator owns it and reconciles its config from the CR on every pass — it will revert a dynamic config that the Admin-API restore set but the CR does not list. So an in-place config restore is durable only for **out-of-band** topics (created directly, no CR); for CR-declared topics the `KafkaTopic` CR / GitOps is the source of truth and the restore path. A to-copy restore onto a fresh empty cluster is unaffected, because the target has no `KafkaTopic` CRs.

## Prerequisites

The platform default backups stack must be installed: the `backupstrategy-controller`, the `cozy-backups` bucket it provisions, and the `cozy-default-kafka` strategy it ships (the demo strategy is derived from it and reuses its bucket coordinates + the controller-projected `cozy-backups-creds`). No demo `Bucket` is created. Set `S3_ENDPOINT` to reach an in-cluster, TLS-verifiable endpoint when the advertised external ingress is not routable (CI uses `https://seaweedfs-s3.tenant-root.svc:8333`, and `run-all.sh` copies the self-signed seaweedfs CA so `curl` verifies it); leave it unset on a cluster whose external S3 endpoint resolves and uses a public CA.

## Flow

- `00-helpers.sh` — shared bash helpers (waiters, kafka seed/verify via a throwaway CLI pod, the demo strategy derivation + S3 CA copy).
- `03-backupclass.yaml` — the demo `BackupClass` (`kafka-metadata`) mapping `Kafka` to the derived `kafka-strategy-default`.
- `05-kafka-src.yaml` — the source application (no `backup:` block; the strategy carries the coordinates).
- `10-backupjob-adhoc.yaml` — an ad-hoc `BackupJob` routed to the demo `kafka-metadata` BackupClass.
- `15-plan.yaml` — a cron `Plan` for scheduled backups.
- `20-kafka-target.yaml` — a separate, empty application for restore-to-copy.
- `25-restorejob-in-place.yaml` / `30-restorejob-to-copy.yaml` — the two restore flows.

Run the whole round-trip:

```bash
./run-all.sh          # backup, in-place restore, then restore into a fresh copy
./cleanup.sh          # remove this demo's objects (idempotent)
```

`SKIP_RESTORE=1 ./run-all.sh` stops after a successful backup (backup-only smoke). Override `NAMESPACE` (default `tenant-root`) to run elsewhere.

## Limitations

- **RF is the source's, applied as-is.** Restoring into a cluster with fewer brokers than a topic's replication factor fails at `--create`, and a to-copy restore onto a target with a different broker count aborts on the first replication-factor mismatch. The demo uses single-broker clusters (RF 1).
- **Restore is not transactional.** Topics are applied one at a time as the object is read, so a failure part-way through (an RF mismatch, an unreachable broker) leaves the topics already created behind. Re-running the restore is safe — existing topics are reconciled, not recreated.
- **Compacted/transactional specifics.** Only the topic definition and its non-default configs are captured; nothing about message contents or transaction state.
- **"Non-internal" is Kafka's own definition.** The backup excludes Kafka's internal topics (`__consumer_offsets`, `__transaction_state`, `__share_group_state`) but not operator bookkeeping topics that are not `__`-prefixed — `strimzi-store-topic` and friends are listed by the Admin API and captured. They are harmless to recreate (the operator owns them), but a to-copy restore will materialise them on the target.
- **Latest-only per key.** Each backup writes `s3://<bucket>/<ns>/<app>/<backup-name>/kafka-metadata.txt` — distinct backups do not overwrite each other, and a restore reads the exact object its `Backup` recorded. The object is deleted from the bucket when its `Backup` is deleted (best-effort, via a one-shot Job the removal waits on), so a retention-pruned `Plan` does not accumulate objects.
