# Managed Kafka Service

## Parameters

### Common parameters

| Name       | Description                                      | Type   | Value   |
| ---------- | ------------------------------------------------ | ------ | ------- |
| `external` | Enable external access from outside the cluster. | `bool` | `false` |


### Application-specific parameters

| Name                   | Description           | Type       | Value |
| ---------------------- | --------------------- | ---------- | ----- |
| `topics`               | Topics configuration. | `[]object` | `[]`  |
| `topics[i].name`       | Topic name.           | `string`   | `""`  |
| `topics[i].partitions` | Number of partitions. | `int`      | `0`   |
| `topics[i].replicas`   | Number of replicas.   | `int`      | `0`   |
| `topics[i].config`     | Topic configuration.  | `object`   | `{}`  |


### Kafka configuration

| Name                     | Description                                                                                              | Type       | Value      |
| ------------------------ | -------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `kafka`                  | Kafka configuration.                                                                                     | `object`   | `{}`       |
| `kafka.replicas`         | Number of Kafka replicas.                                                                                | `int`      | `3`        |
| `kafka.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `kafka.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`       |
| `kafka.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`       |
| `kafka.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `u1.micro` |
| `kafka.size`             | Persistent Volume size for Kafka.                                                                        | `quantity` | `10Gi`     |
| `kafka.storageClass`     | StorageClass used to store the Kafka data.                                                               | `string`   | `""`       |


### ZooKeeper configuration

| Name                         | Description                                                                                              | Type       | Value      |
| ---------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `zookeeper`                  | ZooKeeper configuration.                                                                                 | `object`   | `{}`       |
| `zookeeper.replicas`         | Number of ZooKeeper replicas.                                                                            | `int`      | `3`        |
| `zookeeper.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `zookeeper.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`       |
| `zookeeper.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`       |
| `zookeeper.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `s1.small` |
| `zookeeper.size`             | Persistent Volume size for ZooKeeper.                                                                    | `quantity` | `5Gi`      |
| `zookeeper.storageClass`     | StorageClass used to store the ZooKeeper data.                                                           | `string`   | `""`       |


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

#### s1 (Standard) — 1:2 CPU:memory ratio

| Preset name  | CPU    | memory  |
|--------------|--------|---------|
| `s1.nano`    | `250m` | `512Mi` |
| `s1.micro`   | `500m` | `1Gi`   |
| `s1.small`   | `1`    | `2Gi`   |
| `s1.medium`  | `2`    | `4Gi`   |
| `s1.large`   | `4`    | `8Gi`   |
| `s1.xlarge`  | `8`    | `16Gi`  |
| `s1.2xlarge` | `16`   | `32Gi`  |
| `s1.4xlarge` | `32`   | `64Gi`  |

#### u1 (Universal) — 1:4 CPU:memory ratio

| Preset name  | CPU    | memory   |
|--------------|--------|----------|
| `u1.nano`    | `250m` | `1Gi`    |
| `u1.micro`   | `500m` | `2Gi`    |
| `u1.small`   | `1`    | `4Gi`    |
| `u1.medium`  | `2`    | `8Gi`    |
| `u1.large`   | `4`    | `16Gi`   |
| `u1.xlarge`  | `8`    | `32Gi`   |
| `u1.2xlarge` | `16`   | `64Gi`   |
| `u1.4xlarge` | `32`   | `128Gi`  |

#### m1 (Memory) — 1:8 CPU:memory ratio

| Preset name  | CPU    | memory   |
|--------------|--------|----------|
| `m1.nano`    | `250m` | `2Gi`    |
| `m1.micro`   | `500m` | `4Gi`    |
| `m1.small`   | `1`    | `8Gi`    |
| `m1.medium`  | `2`    | `16Gi`   |
| `m1.large`   | `4`    | `32Gi`   |
| `m1.xlarge`  | `8`    | `64Gi`   |
| `m1.2xlarge` | `16`   | `128Gi`  |
| `m1.4xlarge` | `32`   | `256Gi`  |

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
