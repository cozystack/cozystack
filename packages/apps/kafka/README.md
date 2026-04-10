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

| Name                          | Description                                                                                              | Type       | Value   |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `kafka`                       | Kafka configuration.                                                                                     | `object`   | `{}`    |
| `kafka.replicas`              | Number of Kafka replicas.                                                                                | `int`      | `3`     |
| `kafka.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `kafka.resources.cpu`         | CPU available to each replica.                                                                           | `quantity` | `""`    |
| `kafka.resources.memory`      | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`    |
| `kafka.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `small` |
| `kafka.size`                  | Persistent Volume size for Kafka.                                                                        | `quantity` | `10Gi`  |
| `kafka.storageClass`          | StorageClass used to store the Kafka data.                                                               | `string`   | `""`    |
| `kafka.controllerStorageSize` | Persistent Volume size for KRaft controller metadata (used during ZK-to-KRaft migration).                | `quantity` | `5Gi`   |


### ZooKeeper configuration

| Name                         | Description                                                                                              | Type       | Value   |
| ---------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `zookeeper`                  | ZooKeeper configuration (only used for existing instances migrating from ZooKeeper to KRaft).            | `object`   | `{}`    |
| `zookeeper.replicas`         | Number of ZooKeeper replicas.                                                                            | `int`      | `3`     |
| `zookeeper.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `zookeeper.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`    |
| `zookeeper.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`    |
| `zookeeper.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `small` |
| `zookeeper.size`             | Persistent Volume size for ZooKeeper.                                                                    | `quantity` | `5Gi`   |
| `zookeeper.storageClass`     | StorageClass used to store the ZooKeeper data.                                                           | `string`   | `""`    |


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

| Preset name | CPU    | memory  |
|-------------|--------|---------|
| `nano`      | `250m` | `128Mi` |
| `micro`     | `500m` | `256Mi` |
| `small`     | `1`    | `512Mi` |
| `medium`    | `1`    | `1Gi`   |
| `large`     | `2`    | `2Gi`   |
| `xlarge`    | `4`    | `4Gi`   |
| `2xlarge`   | `8`    | `8Gi`   |

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

New Kafka instances deploy directly in KRaft (Kafka Raft) mode. Existing
ZooKeeper-based instances are migrated automatically using Strimzi's built-in
migration state machine.

### How it works

1. On upgrade, existing ZooKeeper instances receive the `strimzi.io/kraft: migration` annotation.
2. Strimzi creates a dedicated KRaft controller pool alongside the existing brokers (separate pool layout).
3. The migration progresses through these states:
   `ZooKeeper` -> `KRaftMigration` -> `KRaftDualWriting` -> `KRaftPostMigration` -> `KRaft`
4. Once the state reaches `KRaft`, ZooKeeper pods are removed automatically.
5. Broker data is preserved throughout the migration — the existing broker pool is kept intact.

### Important notes

- **Strimzi 0.45 is the last version supporting ZooKeeper.** Future Strimzi releases will only support KRaft.
- The migration is fully automated — no manual intervention is required.
- Monitor progress by checking `status.kafkaMetadataState` on the Kafka CR.
- The `kafka.controllerStorageSize` parameter controls PV size for the new KRaft controller nodes (default: 5Gi).
