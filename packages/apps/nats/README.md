# Managed NATS Service

NATS is an open-source, simple, secure, and high performance messaging system.
It provides a data layer for cloud native applications, IoT messaging, and microservices architectures.

## Parameters

### Common parameters

| Name               | Description                                                                                                                    | Type       | Value     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------ | ---------- | --------- |
| `replicas`         | Number of replicas.                                                                                                            | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each NATS replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                 | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                        | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                        | `string`   | `u1.nano` |
| `storageClass`     | StorageClass used to store the data.                                                                                           | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                               | `bool`     | `false`   |


### Application-specific parameters

| Name                   | Description                                                   | Type                | Value  |
| ---------------------- | ------------------------------------------------------------- | ------------------- | ------ |
| `users`                | Users configuration map.                                      | `map[string]object` | `{}`   |
| `users[name].password` | Password for the user.                                        | `string`            | `""`   |
| `jetstream`            | Jetstream configuration.                                      | `object`            | `{}`   |
| `jetstream.enabled`    | Enable or disable Jetstream for persistent messaging in NATS. | `bool`              | `true` |
| `jetstream.size`       | Jetstream persistent storage size.                            | `quantity`          | `10Gi` |
| `config`               | NATS configuration.                                           | `object`            | `{}`   |
| `config.merge`         | Additional configuration to merge into NATS config.           | `*object`           | `{}`   |
| `config.resolver`      | Additional resolver configuration to merge into NATS config.  | `*object`           | `{}`   |


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

