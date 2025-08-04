# Etcd-cluster

## Parameters

### Common parameters

| Name           | Description                         | Type      | Value                      |
| -------------- | ----------------------------------- | --------- | -------------------------- |
| `size`         | Persistent Volume size              | `*string` | `4Gi`                      |
| `storageClass` | StorageClass used to store the data | `*string` | `""`                       |
| `replicas`     | Number of etcd replicas             | `*int`    | `3`                        |
| `resources`    | Resource configuration for etcd     | `*object` | `{"cpu":4,"memory":"1Gi"}` |

