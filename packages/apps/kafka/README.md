# Managed Kafka Service

> Both `kafka.storageClass` and `zookeeper.storageClass` are annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name          | Description                                                                                                                                                                                                                                                                                                                                                                                         | Type     | Value   |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `external`    | Enable external access from outside the cluster.                                                                                                                                                                                                                                                                                                                                                    | `bool`   | `false` |
| `tls`         | TLS configuration. Strimzi manages the cluster PKI automatically (no cert-manager is involved for this chart): the operator auto-creates `<release>-cluster-ca-cert` and `<release>-clients-ca-cert` secrets, both exposed for client trust setup. The internal TLS listener on 9093 is always on; this toggle only controls the external listener on 9094.                                         | `object` | `{}`    |
| `tls.enabled` | Enable TLS on the external listener. When unset, inherits the value of `external` (TLS is on when external access is enabled). Warning: setting this to false while external is true exposes Kafka over plaintext on a public IP via LoadBalancer. Strimzi does not provide authentication on this listener unless SCRAM, mTLS, or OAuth is separately configured. Use only in controlled networks. | `bool`   | `null` |


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
| `kafka.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `c1.small` |
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
| `zookeeper.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `c1.small` |
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

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases of their 1:1 instance-type equivalents.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.

### Authentication

This chart does not configure listener authentication. When TLS is enabled on the external listener, clients can connect without credentials. To require authentication, use Strimzi's `KafkaUser` resource with an appropriate `authentication` type (`tls`, `scram-sha-512`, or `oauth`) outside this chart. See the [Strimzi documentation on KafkaUser](https://strimzi.io/docs/operators/latest/overview.html#security-options_str) for details.

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
