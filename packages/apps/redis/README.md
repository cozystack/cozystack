# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the Spotahome Redis Operator for efficient management and orchestration of Redis clusters. 

- Docs: https://redis.io/docs/
- GitHub: https://github.com/spotahome/redis-operator

## Parameters

### Common parameters

| Name               | Description                                                                                                                     | Type       | Value   |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `replicas`         | Number of Redis replicas.                                                                                                       | `int`      | `2`     |
| `resources`        | Explicit CPU and memory configuration for each Redis replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica.                                                                                                  | `quantity` | `""`    |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                         | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                         | `string`   | `nano`  |
| `size`             | Persistent Volume Claim size available for application data.                                                                    | `quantity` | `1Gi`   |
| `storageClass`     | StorageClass used to store the data.                                                                                            | `string`   | `""`    |
| `external`         | Enable external access from outside the cluster.                                                                                | `bool`     | `false` |


### Application-specific parameters

| Name          | Description                 | Type   | Value  |
| ------------- | --------------------------- | ------ | ------ |
| `authEnabled` | Enable password generation. | `bool` | `true` |


## Parameter examples and reference

### resources and resourcesPreset

`resources` sets explicit CPU and memory configurations for each replica.
When left empty, the preset defined in `resourcesPreset` is applied.

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

`resourcesPreset` sets named CPU and memory configurations for each replica.
This setting is ignored if the corresponding `resources` value is set.

| Preset name | CPU    | memory  |
|-------------|--------|---------|
| `nano`      | `250m` | `128Mi` |
| `micro`     | `500m` | `256Mi` |
| `small`     | `1`    | `512Mi` |
| `medium`    | `1`    | `1Gi`   |
| `large`     | `2`    | `2Gi`   |
| `xlarge`    | `4`    | `4Gi`   |
| `2xlarge`   | `8`    | `8Gi`   |
