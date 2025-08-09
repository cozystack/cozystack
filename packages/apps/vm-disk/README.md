# Virtual Machine Disk

A Virtual Machine Disk

## Parameters

### Common parameters

| Name           | Description                                            | Type       | Value        |
| -------------- | ------------------------------------------------------ | ---------- | ------------ |
| `source`       | The source image location used to create a disk        | `object`   | `{}`         |
| `optical`      | Defines if disk should be considered optical           | `bool`     | `false`      |
| `storage`      | The size of the disk allocated for the virtual machine | `quantity` | `5Gi`        |
| `storageClass` | StorageClass used to store the data                    | `string`   | `replicated` |

