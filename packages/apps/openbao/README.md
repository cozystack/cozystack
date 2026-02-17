# Managed OpenBAO Service

OpenBAO is an open-source secrets management solution forked from HashiCorp Vault.
It provides identity-based secrets and encryption management for cloud infrastructure.

## Parameters

### Common parameters

| Name               | Description                                                                                                                                                                           | Type       | Value   |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `replicas`         | Number of OpenBAO replicas. HA with Raft is automatically enabled when replicas > 1. Switching between standalone (file storage) and HA (Raft storage) modes requires data migration. | `int`      | `1`     |
| `resources`        | Explicit CPU and memory configuration for each OpenBAO replica. When omitted, the preset defined in `resourcesPreset` is applied.                                                     | `object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica.                                                                                                                                                        | `quantity` | `""`    |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                                                               | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                                                               | `string`   | `small` |
| `size`             | Persistent Volume Claim size for data storage.                                                                                                                                        | `quantity` | `10Gi`  |
| `storageClass`     | StorageClass used to store the data.                                                                                                                                                  | `string`   | `""`    |
| `external`         | Enable external access from outside the cluster.                                                                                                                                      | `bool`     | `false` |


### Application-specific parameters

| Name | Description                | Type   | Value  |
| ---- | -------------------------- | ------ | ------ |
| `ui` | Enable the OpenBAO web UI. | `bool` | `true` |

