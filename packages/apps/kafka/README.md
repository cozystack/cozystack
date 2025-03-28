# Managed Kafka Service

## Parameters

### Common parameters

| Name                        | Description                                                                                                                                                                                                       | Value   |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `external`                  | Enable external access from outside the cluster                                                                                                                                                                   | `false` |
| `kafka.size`                | Persistent Volume size for Kafka                                                                                                                                                                                  | `10Gi`  |
| `kafka.replicas`            | Number of Kafka replicas                                                                                                                                                                                          | `3`     |
| `kafka.storageClass`        | StorageClass used to store the Kafka data                                                                                                                                                                         | `""`    |
| `zookeeper.size`            | Persistent Volume size for ZooKeeper                                                                                                                                                                              | `5Gi`   |
| `zookeeper.replicas`        | Number of ZooKeeper replicas                                                                                                                                                                                      | `3`     |
| `zookeeper.storageClass`    | StorageClass used to store the ZooKeeper data                                                                                                                                                                     | `""`    |
| `kafka.resources`           | Resources                                                                                                                                                                                                         | `{}`    |
| `kafka.resourcesPreset`     | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano`  |
| `zookeeper.resources`       | Resources                                                                                                                                                                                                         | `{}`    |
| `zookeeper.resourcesPreset` | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano`  |

### Configuration parameters

| Name     | Description          | Value |
| -------- | -------------------- | ----- |
| `topics` | Topics configuration | `[]`  |
