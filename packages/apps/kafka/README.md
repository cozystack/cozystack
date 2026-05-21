# Managed Kafka Service

## Parameters

### Common parameters

| Name       | Description                                      | Type     | Value   |
| ---------- | ------------------------------------------------ | -------- | ------- |
| `external` | Enable external access from outside the cluster. | `bool`   | `false` |
| `version`  | Kafka version to deploy.                         | `string` | `v3.9`  |


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

The chart itself is now pure KRaft — it ships a Kafka CR with
`strimzi.io/kraft: enabled` and separate broker + controller `KafkaNodePool`
resources, with no `spec.zookeeper` block.

Existing ZooKeeper-based instances are migrated automatically on the next chart
upgrade by a Helm `pre-upgrade` Job, gated by a `<release>-kafka-deployed-version`
ConfigMap shipped with the chart.

### How it works

1. The Job renders only when the version ConfigMap is missing or stamped below `"1"`.
2. On upgrade, it inspects the existing Kafka CR's `status.kafkaMetadataState`:
   - If the CR is absent (fresh install) or already in `KRaft`, it exits immediately.
   - Otherwise (typically `ZooKeeper` state), it creates the broker + controller
     `KafkaNodePool` resources matching the chart's values and annotates the
     Kafka CR with `strimzi.io/node-pools=enabled` and `strimzi.io/kraft=migration`.
3. The Job polls `status.kafkaMetadataState` and waits for the migration to
   reach `KRaftPostMigration | PreKRaft | KRaft`.
4. It then flips the annotation to `strimzi.io/kraft=enabled` and waits until
   the state reaches `KRaft`.
5. When the Job succeeds, Helm applies the chart's KRaft manifests (which match
   the post-migration state) and stamps the ConfigMap to `"1"`. Subsequent
   reconciles see the ConfigMap and skip the Job entirely.
6. If the Job fails or times out, Helm aborts the upgrade — the ConfigMap stays
   below the threshold, so the next reconcile re-runs the same Job.

### Observability and escape hatches

- Tail the Job logs to follow migration progress:
  `kubectl logs -n <namespace> job/<release>-kafka-migration`
- Monitor `status.kafkaMetadataState` on the Kafka CR directly.
- If migration gets stuck before `KRaftPostMigration`, Strimzi's `rollback`
  annotation stays available as a manual escape hatch:
  `kubectl annotate kafka <release> strimzi.io/kraft=rollback --overwrite`,
  then delete the failed Job and retry.

### Important notes

- **Strimzi 0.45 is the last version supporting ZooKeeper.** Future Strimzi
  releases only support KRaft.
- The `kafka.controllerStorageSize` parameter controls PV size for the new
  KRaft controller nodes (default: `5Gi`).
