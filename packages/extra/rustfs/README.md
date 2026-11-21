# Managed RustFS Service

## Parameters

### Common parameters

| Name               | Description                                                                                                             | Type       | Value   |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `host`             | The hostname used to access the S3 endpoint externally (defaults to 's3' subdomain for the tenant host).                | `string`   | `""`    |
| `replicas`         | Number of RustFS nodes. Distributed mode needs at least 2; the node count cannot be changed after the first deployment. | `int`      | `4`     |
| `size`             | Persistent Volume size of each node.                                                                                    | `quantity` | `10Gi`  |
| `storageClass`     | StorageClass used to store the data.                                                                                    | `string`   | `""`    |
| `resources`        | Explicit CPU and memory configuration of each node. When omitted, the preset defined in `resourcesPreset` is applied.   | `object`   | `{}`    |
| `resources.cpu`    | Number of CPU cores allocated.                                                                                          | `quantity` | `""`    |
| `resources.memory` | Amount of memory allocated.                                                                                             | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                 | `string`   | `small` |

