# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the Spotahome Redis Operator for efficient management and orchestration of Redis clusters. 

- Docs: https://redis.io/docs/
- GitHub: https://github.com/spotahome/redis-operator

## Parameters

### Common parameters

| Name               | Description                                                                                                                     | Type       | Value     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of Redis replicas.                                                                                                       | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each Redis replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                  | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                         | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                         | `string`   | `m1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                    | `quantity` | `1Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                            | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                | `bool`     | `false`   |
| `version`          | Redis major version to deploy                                                                                                   | `string`   | `v8`      |


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

#### s1 (Standard) — 1:2 CPU:memory ratio

| Preset name  | CPU    | memory  |
|--------------|--------|---------|
| `s1.nano`    | `250m` | `512Mi` |
| `s1.micro`   | `500m` | `1Gi`   |
| `s1.small`   | `1`    | `2Gi`   |
| `s1.medium`  | `2`    | `4Gi`   |
| `s1.large`   | `4`    | `8Gi`   |
| `s1.xlarge`  | `8`    | `16Gi`  |
| `s1.2xlarge` | `16`   | `32Gi`  |
| `s1.4xlarge` | `32`   | `64Gi`  |

#### u1 (Universal) — 1:4 CPU:memory ratio

| Preset name  | CPU    | memory   |
|--------------|--------|----------|
| `u1.nano`    | `250m` | `1Gi`    |
| `u1.micro`   | `500m` | `2Gi`    |
| `u1.small`   | `1`    | `4Gi`    |
| `u1.medium`  | `2`    | `8Gi`    |
| `u1.large`   | `4`    | `16Gi`   |
| `u1.xlarge`  | `8`    | `32Gi`   |
| `u1.2xlarge` | `16`   | `64Gi`   |
| `u1.4xlarge` | `32`   | `128Gi`  |

#### m1 (Memory) — 1:8 CPU:memory ratio

| Preset name  | CPU    | memory   |
|--------------|--------|----------|
| `m1.nano`    | `250m` | `2Gi`    |
| `m1.micro`   | `500m` | `4Gi`    |
| `m1.small`   | `1`    | `8Gi`    |
| `m1.medium`  | `2`    | `16Gi`   |
| `m1.large`   | `4`    | `32Gi`   |
| `m1.xlarge`  | `8`    | `64Gi`   |
| `m1.2xlarge` | `16`   | `128Gi`  |
| `m1.4xlarge` | `32`   | `256Gi`  |
