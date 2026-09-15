# Kafka topic-data backup/restore example

This directory shows how to back up and restore the **topic data** of a Cozystack-managed `Kafka` application using the **generic `Job` backup strategy**. Unlike the app-specific drivers (Altinity for ClickHouse, CNPG for Postgres, ...), the `Job` strategy has no built-in knowledge of the application: it runs a Pod the operator supplies. Here that Pod is a stock [Strimzi Kafka image][strimzi-image] (which carries the `kafka-*.sh` CLI plus `bash`, `curl` and `tar`) running one shell script:

- **backup**: freeze each partition's end offset, drain `[begin, end)` with `kafka-console-consumer` → `tar` → `PUT` the tarball to S3.
- **restore**: `GET` the tarball from S3 → `tar -x` → recreate the topics with their original partition count → replay every partition file with `kafka-console-producer`.

The same `PodTemplateSpec` serves both directions. The strategy engine templates each string field independently — it cannot add or remove containers per mode — so the single image branches at runtime on `{{ .Mode }}` (rendered into the `MODE` env var). Backup pushes to a key scoped by the source app (`{{ .Release.Name }}`); restore reads the key scoped by the backup's source (`{{ .Backup.ApplicationRef.Name }}`), so a to-copy restore lands the source's data into a differently-named target. The upload uses `curl --aws-sigv4`, so no purpose-built backup image is needed — a stock client image plus a shell script is the whole driver.

## Consistency model

The backup takes a **frozen-end-offset cut**. For each topic the driver reads every partition's end offset once (`kafka-get-offsets --time -1`) and its begin offset (`--time -2`), then drains only up to the frozen end. Anything produced after that read (offset ≥ the frozen end) is excluded, so a topic's partitions are captured as of the same instant — a coherent point-in-time view of that topic. The reads happen per topic, so the cut is atomic within a topic but not across topics (Kafka offers no cross-topic guarantee regardless). This needs no dependency on record timestamps: it is a cut by log position, not by wall-clock time.

## Scope and limitations

This is a **logical DATA backup**, deliberately narrow — it captures topic records (key + value) and nothing else:

- **Not captured**: topic configs (retention, compaction, `min.insync.replicas`), ACLs, SCRAM users, and **consumer-group offsets**. Restore recreates each topic with only its original partition count and the configured replication factor (`replicationFactor`, default `1`) — the source RF is not captured, so restoring into a multi-broker cluster without raising that parameter is a silent durability downgrade.
- **Offsets change on restore**: restore re-produces records, so the target assigns fresh offsets. A consumer group's committed position from the source is therefore meaningless on the restored cluster. Preserving consumer offsets requires either offset translation or a byte-identical volume-snapshot restore (Velero/CSI) — neither is in scope here.
- **Fidelity**: records are round-tripped as `key<TAB>value` text via the console tools, so keys and values must be newline- and tab-free. Message headers and record timestamps are not preserved. A production driver would use a binary-safe client (e.g. a purpose-built image) for arbitrary payloads.
- **Compacted and transactional topics are out of scope**: the drain reads exactly `end - begin` records per partition, which equals the consumable count only on a plain topic. On a compacted topic (sparse offsets) or one written by transactional producers (control markers occupy offsets but are never delivered), fewer records are consumable. `kafka-console-consumer` does not report this — it idles to its `--timeout-ms` and still exits 0 — so the strategy compares the drained line count against `end - begin` and **fails the backup** on any shortfall rather than uploading a partial tarball. Note the interaction with the parameter default: an empty `topics` backs up *every* non-internal topic, so the all-topics setting is the riskiest, not the safest — one compacted or transactional topic in the namespace aborts the whole run. Restrict `topics` to topics you know are plain, or accept that the default may fail closed.
- **Partition placement**: keyed records re-produce into the same partition because the topic is recreated with the same partition count and the default (murmur2) partitioner is deterministic. Null-key records fare worse: the dump renders a null key as the literal string `null` (the console formatter's default) and the replay reads it back as a four-byte `null` key — so the record not only lands in an unpredictable partition but has its key changed from null to `"null"`, which shifts its compaction identity downstream.
- **Restore appends, it does not replace**: the driver creates the topic only if absent (`--create --if-not-exists`) and then replays every record, so restoring into a topic that still holds data adds to it rather than overwriting. Restore is safe only against an absent or empty topic — the in-place step below deletes the topic first for exactly this reason. Note too that the strategy Job's `backoffLimit: 2` means a restore Pod that dies mid-replay is retried and re-appends from the start, so a partially-failed restore can leave duplicated records.
- **Each backup overwrites the previous one**: the S3 object key is `<app>/kafka-topics.tar` with no run identifier, so every `Backup` of the same application resolves to the same tarball and restoring an older `Backup` restores the latest data. The generic `Job` driver exposes no unique per-run handle at backup time — the `Backup` artifact is created only after the Job completes — so a per-run key would need a driver change.
- **Local disk usage is unbounded**: the drain stages the whole topic under `/tmp` and then again as a tarball on the Pod's writable layer, and the strategy Pod requests no `ephemeral-storage`. A large topic can fill the node's ephemeral storage and evict other Pods; set an `ephemeral-storage` request and limit sized to your data before backing up anything substantial.
- **Pod Security**: the strategy Pod and the demo's throwaway CLI Pods set `runAsNonRoot`, `allowPrivilegeEscalation: false`, drop all capabilities and a `RuntimeDefault` seccomp profile, so they satisfy the `restricted` Pod Security Standard on the stock Strimzi image, which runs as non-root UID 1001. Pinning an image that runs as root would need a `runAsUser` suited to that image instead.

For a faithful, offset-preserving Kafka backup, prefer a volume-snapshot strategy over this logical one; this example exists to show the generic `Job` path end to end.

## Step order

| File | Role | Triggered by |
|---|---|---|
| `00-helpers.sh` | Shared bash helpers, env defaults, and the kafka-CLI / S3-secret helpers; sourced by every step. | n/a |
| `01-create-strategy.sh` | Creates the cluster-scoped `Job` strategy (the Kafka backup/restore `PodTemplateSpec`). | admin |
| `02-create-backupclass.sh` | Maps `apps.cozystack.io/Kafka` to that strategy, with `topics`/`replicationFactor` parameters. | admin |
| `03-create-bucket.sh` | Provisions a `Bucket` and caches its S3 coordinates into `.bucket-info.env` (chmod 600; raw access keys). `cleanup.sh` removes this file. | tenant |
| `04-create-kafka.sh` | Provisions a single-broker `Kafka`, creates the `<app>-backup-s3` Secret, seeds a topic with sentinel messages. | tenant |
| `05-create-backupjob.sh` | Submits a `BackupJob` and waits for Succeeded. | tenant |
| `06-restore-in-place.sh` | Deletes the topic and restores it into the same instance via `RestoreJob`. | tenant |
| `07-restore-to-copy.sh` | Provisions a second `Kafka` and restores into it via `RestoreJob.spec.targetApplicationRef`. | tenant |
| `cleanup.sh` | Removes everything created by the demo. | admin or tenant |
| `run-all.sh` | Convenience runner that executes 01..07 in order. | demo |

## Running

```sh
# Uses NAMESPACE=tenant-test by default; override any knob via the environment.
./run-all.sh
# ... or step by step, in numeric order.
./cleanup.sh
```

By default the scripts resolve `KAFKA_IMAGE` from the running Strimzi operator — the newest image in its `STRIMZI_KAFKA_IMAGES` map — so the backup Pod reuses an image already cached on the nodes with no extra pull or version skew. Set `KAFKA_IMAGE` explicitly to pin a different one, for example on an air-gapped clone where the operator cannot be read (the backup Pod only needs the `kafka-*.sh` CLI plus `curl` and `tar`). To see what the operator ships:

```sh
kubectl -n cozy-kafka-operator get deploy strimzi-cluster-operator \
  -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' | grep KAFKA_IMAGES
```

[strimzi-image]: https://quay.io/repository/strimzi/kafka
