# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the Spotahome Redis Operator for efficient management and orchestration of Redis clusters. 

- Docs: https://redis.io/docs/
- GitHub: https://github.com/spotahome/redis-operator

## Parameters

### Common parameters

| Name              | Description                                                                                                                                                                                                       | Value   |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `external`        | Enable external access from outside the cluster                                                                                                                                                                   | `false` |
| `size`            | Persistent Volume size                                                                                                                                                                                            | `1Gi`   |
| `replicas`        | Number of Redis replicas                                                                                                                                                                                          | `2`     |
| `storageClass`    | StorageClass used to store the data                                                                                                                                                                               | `""`    |
| `authEnabled`     | Enable password generation                                                                                                                                                                                        | `true`  |
| `resources`       | Resources                                                                                                                                                                                                         | `{}`    |
| `resourcesPreset` | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano`  |


