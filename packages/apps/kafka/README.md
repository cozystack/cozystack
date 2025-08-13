# Managed Kafka Service

## Parameters

### Common parameters

| Name       | Description                                     | Type   | Value   |
| ---------- | ----------------------------------------------- | ------ | ------- |
| `external` | Enable external access from outside the cluster | `bool` | `false` |


### Application-specific parameters

| Name                   | Description          | Type       | Value |
| ---------------------- | -------------------- | ---------- | ----- |
| `topics`               | Topics configuration | `[]object` | `[]`  |
| `topics[i].name`       | Topic name           | `string`   | `""`  |
| `topics[i].partitions` | Number of partitions | `int`      | `0`   |
| `topics[i].replicas`   | Number of replicas   | `int`      | `0`   |
| `topics[i].config`     | Topic configuration  | `object`   | `{}`  |


### Kafka configuration

| Name                     | Description                                                                                                                               | Type        | Value   |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `kafka`                  | Kafka configuration                                                                                                                       | `object`    | `{}`    |
| `kafka.replicas`         | Number of Kafka replicas                                                                                                                  | `int`       | `3`     |
| `kafka.resources`        | Explicit CPU and memory configuration for each replica. When left empty, the preset defined in `resourcesPreset` is applied.              | `*object`   | `null`  |
| `kafka.resources.cpu`    | CPU available to each replica                                                                                                             | `*quantity` | `null`  |
| `kafka.resources.memory` | Memory (RAM) available to each replica                                                                                                    | `*quantity` | `null`  |
| `kafka.resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `small` |
| `kafka.size`             | Persistent Volume size for Kafka                                                                                                          | `quantity`  | `10Gi`  |
| `kafka.storageClass`     | StorageClass used to store the Kafka data                                                                                                 | `string`    | `""`    |


### Zookeeper configuration

| Name                         | Description                                                                                                                               | Type        | Value   |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `zookeeper`                  | Zookeeper configuration                                                                                                                   | `object`    | `{}`    |
| `zookeeper.replicas`         | Number of ZooKeeper replicas                                                                                                              | `int`       | `3`     |
| `zookeeper.resources`        | Explicit CPU and memory configuration for each replica. When left empty, the preset defined in `resourcesPreset` is applied.              | `*object`   | `null`  |
| `zookeeper.resources.cpu`    | CPU available to each replica                                                                                                             | `*quantity` | `null`  |
| `zookeeper.resources.memory` | Memory (RAM) available to each replica                                                                                                    | `*quantity` | `null`  |
| `zookeeper.resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `small` |
| `zookeeper.size`             | Persistent Volume size for ZooKeeper                                                                                                      | `quantity`  | `5Gi`   |
| `zookeeper.storageClass`     | StorageClass used to store the ZooKeeper data                                                                                             | `string`    | `""`    |


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
