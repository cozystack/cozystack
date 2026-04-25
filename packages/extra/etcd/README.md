# Etcd-cluster

## Parameters

### Common parameters

| Name               | Description                                                          | Type       | Value                         |
| ------------------ | -------------------------------------------------------------------- | ---------- | ----------------------------- |
| `size`             | Persistent Volume size.                                              | `quantity` | `4Gi`                         |
| `storageClass`     | StorageClass used to store the data.                                 | `string`   | `""`                          |
| `replicas`         | Number of etcd replicas.                                             | `int`      | `3`                           |
| `resources`        | Resource configuration for etcd.                                     | `object`   | `{}`                          |
| `resources.cpu`    | Number of CPU cores allocated.                                       | `quantity` | `1000m`                       |
| `resources.memory` | Amount of memory allocated.                                          | `quantity` | `512Mi`                       |
| `certWaitTimeout`  | Timeout in seconds to wait for cert-manager to populate TLS Secrets. | `int`      | `300`                         |
| `kubectlImage`     | Container image used for DataStore creation hook Job.                | `string`   | `docker.io/alpine/k8s:1.33.4` |

