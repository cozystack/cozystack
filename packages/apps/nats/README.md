# Managed NATS Service

NATS is an open-source, simple, secure, and high performance messaging system.
It provides a data layer for cloud native applications, IoT messaging, and microservices architectures.

## Parameters

### Common parameters

| Name                | Description                                                                                                                       | Value   |
| ------------------- | --------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `external`          | Enable external access from outside the cluster                                                                                   | `false` |
| `replicas`          | Persistent Volume size for NATS                                                                                                   | `2`     |
| `storageClass`      | StorageClass used to store the data                                                                                               | `""`    |
| `users`             | Users configuration                                                                                                               | `{}`    |
| `jetstream.size`    | Jetstream persistent storage size                                                                                                 | `10Gi`  |
| `jetstream.enabled` | Enable or disable Jetstream                                                                                                       | `true`  |
| `config.merge`      | Additional configuration to merge into NATS config                                                                                | `{}`    |
| `config.resolver`   | Additional configuration to merge into NATS config                                                                                | `{}`    |
| `resources`         | Explicit CPU and memory configuration for each NATS replica. When left empty, the preset defined in `resourcesPreset` is applied. | `{}`    |
| `resourcesPreset`   | Default sizing preset used when `resources` is omitted. Allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge. | `nano`  |

Example of `resources`:

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

Allowed values for `resourcesPreset` are `none`, `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`.
This value is ignored if the corresponding `resources` value is set.