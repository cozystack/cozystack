# Managed Kafka Service

## Parameters

### Common parameters

| Name                        | Description                                                                                                                            | Value   |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `external`                  | Enable external access from outside the cluster                                                                                        | `false` |
| `kafka.size`                | Persistent Volume size for Kafka                                                                                                       | `10Gi`  |
| `kafka.replicas`            | Number of Kafka replicas                                                                                                               | `3`     |
| `kafka.storageClass`        | StorageClass used to store the Kafka data                                                                                              | `""`    |
| `zookeeper.size`            | Persistent Volume size for ZooKeeper                                                                                                   | `5Gi`   |
| `zookeeper.replicas`        | Number of ZooKeeper replicas                                                                                                           | `3`     |
| `zookeeper.storageClass`    | StorageClass used to store the ZooKeeper data                                                                                          | `""`    |
| `kafka.resources`           | Explicit CPU and memory configuration for each Kafka replica. When left empty, the preset defined in `resourcesPreset` is applied.     | `{}`    |
| `kafka.resourcesPreset`     | Default sizing preset used when `resources` is omitted. Allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge.      | `small` |
| `zookeeper.resources`       | Explicit CPU and memory configuration for each Zookeeper replica. When left empty, the preset defined in `resourcesPreset` is applied. | `{}`    |
| `zookeeper.resourcesPreset` | Default sizing preset used when `resources` is omitted. Allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge.      | `small` |

Example of `karka.resources` and `zookeeper.resources`:

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

Allowed values for `resourcesPreset` are `none`, `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`.
This value is ignored if the corresponding `resources` value is set.


### Configuration parameters

| Name     | Description          | Value |
| -------- | -------------------- | ----- |
| `topics` | Topics configuration | `[]`  |

Example of `topics`:

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
