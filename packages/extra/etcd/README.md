# Etcd-cluster

## Parameters

### Common parameters

| Name               | Description                         | Type        | Value |
| ------------------ | ----------------------------------- | ----------- | ----- |
| `size`             | Persistent Volume size              | `*quantity` | `4Gi` |
| `storageClass`     | StorageClass used to store the data | `*string`   | `""`  |
| `replicas`         | Number of etcd replicas             | `*int`      | `3`   |
| `resources`        | Resource configuration for etcd     | `*object`   | `{}`  |
| `resources.cpu`    | The number of CPU cores allocated   | `*quantity` | `4`   |
| `resources.memory` | The amount of memory allocated      | `*quantity` | `1Gi` |

