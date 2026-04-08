# Virtual Machine Disk

A Virtual Machine Disk

## Parameters

### Common parameters

| Name                | Description                                             | Type       | Value        |
| ------------------- | ------------------------------------------------------- | ---------- | ------------ |
| `source`            | The source image location used to create a disk.        | `object`   | `{}`         |
| `source.image`      | Use image by name from default collection.              | `*object`  | `null`       |
| `source.image.name` | Name of the image to use.                               | `string`   | `""`         |
| `source.upload`     | Upload local image.                                     | `*object`  | `null`       |
| `source.http`       | Download image from an HTTP source.                     | `*object`  | `null`       |
| `source.http.url`   | URL to download the image.                              | `string`   | `""`         |
| `source.disk`       | Clone an existing vm-disk.                              | `*object`  | `null`       |
| `source.disk.name`  | Name of the vm-disk to clone.                           | `string`   | `""`         |
| `optical`           | Defines if disk should be considered optical.           | `bool`     | `false`      |
| `storage`           | The size of the disk allocated for the virtual machine. | `quantity` | `5Gi`        |
| `storageClass`      | StorageClass used to store the data.                    | `string`   | `replicated` |

