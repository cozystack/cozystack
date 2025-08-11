# Managed NATS Service

NATS is an open-source, simple, secure, and high performance messaging system.
It provides a data layer for cloud native applications, IoT messaging, and microservices architectures.

## Parameters

### Common parameters

| Name               | Description                                                                                                                               | Type        | Value   |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `replicas`         | Number of replicas                                                                                                                        | `int`       | `2`     |
| `resources`        | Explicit CPU and memory configuration for each NATS replica. When left empty, the preset defined in `resourcesPreset` is applied.         | `*object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica                                                                                                             | `*quantity` | `null`  |
| `resources.memory` | Memory (RAM) available to each replica                                                                                                    | `*quantity` | `null`  |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `nano`  |
| `storageClass`     | StorageClass used to store the data                                                                                                       | `string`    | `""`    |
| `external`         | Enable external access from outside the cluster                                                                                           | `bool`      | `false` |


### Application-specific parameters

| Name                   | Description                                                                                                    | Type                | Value   |
| ---------------------- | -------------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `users`                | Users configuration                                                                                            | `map[string]object` | `{...}` |
| `users[name].password` | Password for the user                                                                                          | `*string`           | `null`  |
| `jetstream`            | Jetstream configuration                                                                                        | `object`            | `{}`    |
| `jetstream.enabled`    | Enable or disable Jetstream. Set to `true` (default) to enable Jetstream for persistent messaging in NATS.     | `bool`              | `true`  |
| `jetstream.size`       | Jetstream persistent storage size. Specifies the size of the persistent storage for Jetstream (message store). | `quantity`          | `10Gi`  |
| `config`               | NATS configuration                                                                                             | `object`            | `{}`    |
| `config.merge`         | Additional configuration to merge into NATS config (see example)                                               | `*object`           | `{}`    |
| `config.resolver`      | Additional resolver configuration to merge into NATS config (see example)                                      | `*object`           | `{}`    |


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

