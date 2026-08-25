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

The chart itself is now pure KRaft — it ships a Kafka CR with `strimzi.io/kraft: enabled` and separate broker + controller `KafkaNodePool` resources, with no `spec.zookeeper` block.

Existing ZooKeeper-based instances are migrated automatically on the next chart upgrade by a Helm `pre-upgrade` Job, gated by a `<release>-kafka-deployed-version` ConfigMap shipped with the chart.

### How it works

1. The Job renders only when the version ConfigMap is missing or stamped below `"1"`.
2. On upgrade, it inspects the existing Kafka CR's `status.kafkaMetadataState`: a genuine NotFound means fresh install (skip); a CR already in `KRaft` means done (skip); any other read error aborts (fail closed) rather than risk stamping a live ZooKeeper cluster into KRaft. Otherwise (typically `ZooKeeper` state) it creates the controller pool plus a broker pool named exactly `kafka` — Strimzi derives broker and PVC names as `<cluster>-<pool>-<id>`, so only the name `kafka` reuses the existing `<cluster>-kafka-N` brokers and their data in place — and annotates the Kafka CR with `strimzi.io/node-pools=enabled` and `strimzi.io/kraft=migration`.
3. The Job polls `status.kafkaMetadataState` and waits for the migration to reach `KRaftPostMigration | PreKRaft | KRaft`.
4. Only once that safe state is reached does it flip the annotation to `strimzi.io/kraft=enabled` and wait until the state reaches `KRaft`; if the wait times out in an intermediate state the Job aborts (`exit 1`) without finalising, leaving the ConfigMap unstamped.
5. When the Job succeeds, Helm applies the chart's KRaft manifests (which match the post-migration state) and stamps the ConfigMap to `"1"`. Subsequent reconciles see the ConfigMap and skip the Job entirely.
6. If the Job fails or times out, Helm aborts the upgrade — the ConfigMap stays below the threshold, so the next reconcile re-runs the same Job.

### Observability and escape hatches

- Tail the Job logs to follow migration progress: `kubectl logs -n <namespace> job/<release>-kafka-migration`
- Monitor `status.kafkaMetadataState` on the Kafka CR directly.
- If migration gets stuck before `KRaftPostMigration`, Strimzi's `rollback` annotation stays available as a manual escape hatch: `kubectl annotate kafka <release> strimzi.io/kraft=rollback --overwrite`, then delete the failed Job and retry.

### One Kafka per namespace

This chart runs at most one Kafka cluster per namespace, and fails the render (with a clear message) if you deploy a second one into a namespace that already has one. The reason is upstream: the broker `KafkaNodePool` must be named exactly `kafka` for KRaft (and to adopt an existing ZooKeeper cluster's `<cluster>-kafka-N` brokers and data during migration), but `KafkaNodePool` object names are unique within a namespace, so two clusters cannot both own a `kafka` pool there. This matches Strimzi's own guidance to run one Kafka cluster per namespace — see [strimzi/strimzi-kafka-operator discussions/11120](https://github.com/orgs/strimzi/discussions/11120). Deploy each Kafka in its own namespace.

### Deletion and PVC retention

Both node pools set `deleteClaim: false`, so deleting a Kafka release intentionally leaves its data PVCs (`data-0-<cluster>-kafka-N`, `data-0-<cluster>-controller-N`) behind — this protects data across the migration and against accidental deletion. The trade-off is that the PVCs keep consuming tenant quota until removed by hand, and recreating a same-named Kafka rebinds the stale volumes, whose on-disk cluster id will not match the new cluster (`InconsistentClusterIdException`, CrashLoop). To truly start over, delete the leftover PVCs before recreating: `kubectl -n <namespace> delete pvc -l strimzi.io/cluster=<release>`.

### No downgrade across the KRaft boundary

Migration to KRaft is one-way. Strimzi has no supported KRaft→ZooKeeper rollback once the migration completes, so neither lowering the `version` field on a running cluster nor rolling the whole app back to a pre-KRaft chart is supported — the latter would drop the `strimzi.io/kraft` / `strimzi.io/node-pools` annotations and try to restore `spec.zookeeper`, leaving the Strimzi cluster in an invalid state. Treat the migration as a point of no return: take a backup first, and do not downgrade the kafka app once its `<release>-kafka-deployed-version` ConfigMap has been stamped.

### Important notes

- **Strimzi 0.45 is the last version supporting ZooKeeper.** Future Strimzi releases only support KRaft.
- The `kafka.controllerStorageSize` parameter controls PV size for the new KRaft controller nodes (default: `5Gi`).
