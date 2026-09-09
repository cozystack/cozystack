# Managed Kafka Service

## Parameters

### Common parameters

| Name          | Description                                                                                                                                                                                                                                                                                                                                                                                         | Type     | Value   |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `external`    | Enable external access from outside the cluster.                                                                                                                                                                                                                                                                                                                                                    | `bool`   | `false` |
| `tls`         | TLS configuration. Strimzi manages the cluster PKI automatically (no cert-manager is involved for this chart): the operator auto-creates `<release>-cluster-ca-cert` and `<release>-clients-ca-cert` secrets, both exposed for client trust setup. The internal TLS listener on 9093 is always on; this toggle only controls the external listener on 9094.                                         | `object` | `{}`    |
| `tls.enabled` | Enable TLS on the external listener. When unset, inherits the value of `external` (TLS is on when external access is enabled). Warning: setting this to false while external is true exposes Kafka over plaintext on a public IP via LoadBalancer. Strimzi does not provide authentication on this listener unless SCRAM, mTLS, or OAuth is separately configured. Use only in controlled networks. | `*bool`  | `null`  |
| `version`     | Kafka version to deploy. Upgrade-only: once a cluster's KRaft metadata is at a given version, Strimzi refuses an in-place downgrade, so lowering this on a running cluster (e.g. v3.9 back to v3.8) leaves the Kafka CR stuck in a reconcile error. Pick the target version at creation and only ever raise it.                                                                                     | `string` | `v3.9`  |


### Application-specific parameters

| Name                   | Description           | Type       | Value |
| ---------------------- | --------------------- | ---------- | ----- |
| `topics`               | Topics configuration. | `[]object` | `[]`  |
| `topics[i].name`       | Topic name.           | `string`   | `""`  |
| `topics[i].partitions` | Number of partitions. | `int`      | `0`   |
| `topics[i].replicas`   | Number of replicas.   | `int`      | `0`   |
| `topics[i].config`     | Topic configuration.  | `object`   | `{}`  |


### Kafka configuration

| Name                          | Description                                                                                              | Type       | Value      |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `kafka`                       | Kafka configuration.                                                                                     | `object`   | `{}`       |
| `kafka.replicas`              | Number of Kafka replicas.                                                                                | `int`      | `3`        |
| `kafka.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `kafka.resources.cpu`         | CPU available to each replica.                                                                           | `quantity` | `""`       |
| `kafka.resources.memory`      | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`       |
| `kafka.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `c1.small` |
| `kafka.size`                  | Persistent Volume size for Kafka.                                                                        | `quantity` | `10Gi`     |
| `kafka.storageClass`          | StorageClass used to store the Kafka data.                                                               | `string`   | `""`       |
| `kafka.controllerStorageSize` | Persistent Volume size for KRaft controller metadata (used during ZK-to-KRaft migration).                | `quantity` | `5Gi`      |


## Parameter examples and reference

### resources and resourcesPreset

`resources` sets explicit CPU and memory configurations for each replica.
When left empty, the preset defined in `resourcesPreset` is applied.

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

`resourcesPreset` sets named CPU and memory configurations for each replica.
This setting is ignored if the corresponding `resources` value is set.

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases of their 1:1 instance-type equivalents.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.

### topics

```yaml
topics:
  - name: Results
    partitions: 1
    replicas: 3
    config:
      min.insync.replicas: 2
  - name: Orders
    config:
      cleanup.policy: compact
      segment.ms: 3600000
      max.compaction.lag.ms: 5400000
      min.insync.replicas: 2
    partitions: 1
    replicas: 3
```

## ZooKeeper to KRaft Migration

The chart is pure KRaft — it ships a Kafka CR annotated `strimzi.io/kraft: enabled` with no `spec.zookeeper` block. The broker and controller `KafkaNodePool` resources are **not** regular chart manifests: they are created and reconciled by the migration Job (a `pre-install,pre-upgrade` Helm hook) on every install and upgrade. This is deliberate. The hook has to create the pools and walk Strimzi's migration state machine *before* the main release apply, and a resource a hook creates cannot also be a normal chart manifest without Helm failing the migrating upgrade — its 3-way merge finds the pool in the new manifest and live in the cluster but absent from the previous (ZooKeeper) revision, and aborts with `original object KafkaNodePool ... not found`, leaving the release stuck `failed` even though the cluster migrated fine. Keeping the pools hook-owned avoids that whole class of conflict.

Existing ZooKeeper-based instances are migrated automatically on the next chart upgrade. The Job is idempotent and safe to re-run.

### How it works

1. The Job runs on install and upgrade. It first ensures both `KafkaNodePool`s exist (creating or updating them), then decides what to do from the Kafka CR.
2. It inspects the Kafka CR: a genuine NotFound means a fresh install (the pools are ensured and the main apply creates the KRaft Kafka CR — nothing to migrate); a CR already annotated `strimzi.io/kraft=enabled` or in `status.kafkaMetadataState: KRaft` means done; any other read error aborts (fail closed) rather than risk driving a live ZooKeeper cluster. Otherwise (typically `ZooKeeper` state) it migrates.
3. For a migrating cluster the broker pool is named exactly `kafka` — Strimzi derives broker and PVC names as `<cluster>-<pool>-<id>`, so only that name reuses the existing `<cluster>-kafka-N` brokers and their data in place — while the controller pool pins its node IDs above the broker range (`strimzi.io/next-node-ids`) so the broker pool keeps `[0..replicas-1]` and Strimzi cannot hand the existing broker IDs to the fresh controllers (which would recreate the brokers empty and orphan the data). Fresh clusters use the release-hashed `b-<hash>` broker pool instead.
4. It annotates the Kafka CR with `strimzi.io/node-pools=enabled` and `strimzi.io/kraft=migration`, then polls `status.kafkaMetadataState` and waits for the migration to reach `KRaftPostMigration | PreKRaft | KRaft`.
5. Only once that safe state is reached does it flip the annotation to `strimzi.io/kraft=enabled` and wait until the state reaches `KRaft`; if the wait times out in an intermediate state the Job aborts (`exit 1`) so Helm fails the upgrade and the next reconcile retries from where the migration left off.
6. When the Job succeeds, the main release apply settles the KRaft Kafka CR, which already matches the migrated state.

### Observability and escape hatches

- Tail the Job logs to follow migration progress: `kubectl logs -n <namespace> job/<release>-kafka-migration`
- Monitor `status.kafkaMetadataState` on the Kafka CR directly.
- Until the cluster reaches `PreKRaft`, Strimzi's `rollback` annotation stays available as a manual escape hatch: `kubectl annotate kafka <release> strimzi.io/kraft=rollback --overwrite` (back to `KRaftDualWriting`), then `strimzi.io/kraft=disabled` to return to `ZooKeeper`; then delete the failed Job and retry. Past `PreKRaft` the state machine accepts only `enabled`.

### Multiple Kafka clusters per namespace

Fresh KRaft clusters can coexist in one namespace: each one's node pools get short, release-hashed names (`b-<hash>` for brokers, `c-<hash>` for controllers), unique because `KafkaNodePool` object names are namespace-unique and each release hashes differently, while every pod/PVC/Service is already prefixed by the (unique) cluster name. The hash keeps the pool name a fixed length rather than embedding the release name — which Strimzi already prepends as the cluster name — so the derived `<cluster>-<pool>-<id>` pod hostname stays within Kubernetes' 63-character limit even for long release names. So you can deploy any number of new Kafka instances side by side.

The ZooKeeper→KRaft migration path is more constrained, but still fully automatic. To adopt an existing cluster's `<cluster>-kafka-N` brokers and their data in place, the broker pool must be named exactly `kafka`, and that fixed name is namespace-unique — so co-namespaced ZooKeeper clusters cannot all migrate at once, they must serialize on the `kafka` name (Strimzi's own guidance is one Kafka per namespace for this reason — [strimzi/strimzi-kafka-operator discussions/11120](https://github.com/orgs/strimzi/discussions/11120)). The migration hook handles that serialization automatically: whichever cluster's upgrade wins the `kafka` name (claimed atomically via `kubectl create`) migrates in place; a cluster that loses the race fails its hook and its next reconcile retries. Once a migrated cluster sees other ZooKeeper clusters still waiting, it frees the name — it stands up its own `b-<hash>` broker pool, drains all data off the `kafka`-pool brokers with a Cruise Control `KafkaRebalance` (`mode: remove-brokers`, done online, no downtime), deletes the drained `kafka` pool, and the next waiting cluster's retry adopts the freed name. The last cluster to migrate keeps `kafka`. So N co-namespaced ZooKeeper clusters all reach KRaft with no manual steps — sequentially, and with one full data drain per cluster except the last. Cruise Control is enabled only for the drain and removed after. Fresh KRaft clusters are never involved in any of this.

### Deletion and PVC retention

Both node pools set `deleteClaim: false`, so deleting a Kafka release intentionally leaves its data PVCs (`data-0-<cluster>-<pool>-N`) behind — this protects data across the migration and against accidental deletion. The trade-off is that the PVCs keep consuming tenant quota until removed by hand, and recreating a same-named Kafka rebinds the stale volumes, whose on-disk cluster id will not match the new cluster (`InconsistentClusterIdException`, CrashLoop). To truly start over, delete the leftover PVCs before recreating: `kubectl -n <namespace> delete pvc -l strimzi.io/cluster=<release>`.

### No downgrade across the KRaft boundary

Migration to KRaft is a point of no return once the cluster passes `PreKRaft` (before that, the escape hatch above still works). After it, Strimzi has no KRaft→ZooKeeper rollback, so neither lowering the `version` field on a running cluster nor rolling the whole app back to a pre-KRaft chart is supported: rolling the chart back would re-render the `spec.zookeeper` objects over live KRaft storage and drop the `strimzi.io/kraft` / `strimzi.io/node-pools` annotations — that does not restore ZooKeeper, it **damages** the Kafka cluster. Take a backup first, and never downgrade the kafka app or the platform once a cluster has reached KRaft (`strimzi.io/kraft=enabled`).

**This chart release is a mandatory stop on the upgrade path.** A cluster that skips it arrives at a future Strimzi (0.46+) still on ZooKeeper, and 0.46 removed the ZooKeeper→KRaft migration entirely — there is then no supported path off ZooKeeper. Make sure every Kafka in the platform has reached KRaft on this release before upgrading past it.

Budget a maintenance window of **up to a few hours per instance**: the in-place migration is tens of minutes (broker rolling restarts), and when several Kafkas share a namespace each one but the last also drains its full dataset off the shared `kafka` pool via a Cruise Control rebalance to free the name, which scales with data size.

### Important notes

- **Strimzi 0.45 is the last version supporting ZooKeeper.** Future Strimzi releases only support KRaft.
- The `kafka.controllerStorageSize` parameter controls PV size for the new KRaft controller nodes (default: `5Gi`).
