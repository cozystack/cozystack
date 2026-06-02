# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the Spotahome Redis Operator for efficient management and orchestration of Redis clusters. 

- Docs: https://redis.io/docs/
- GitHub: https://github.com/spotahome/redis-operator

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                     | Type       | Value     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of Redis replicas.                                                                                                       | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each Redis replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                  | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                         | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                         | `string`   | `t1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                    | `quantity` | `1Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                            | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                | `bool`     | `false`   |


### TLS parameters

| Name              | Description                                                                                                                                                                                                                         | Type     | Value  |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------ |
| `tls`             | TLS configuration. When omitted, TLS is enabled automatically when `external` is true.                                                                                                                                              | `object` | `{}`   |
| `tls.enabled`     | Enable TLS for Redis and Sentinel connections. When omitted, defaults to the value of `external`. Encryption is provided by the redis-operator fork that mounts the certificate Secret into both Redis and Sentinel pods at `/tls`. | `*bool`  | `null` |
| `tls.authClients` | Maps to the Redis `tls-auth-clients` directive. Defaults to `no` — the server certificate is presented but client certificates are not validated.                                                                                 | `string` | `{}`   |
| `version`         | Redis major version to deploy                                                                                                                                                                                                       | `string` | `v8`   |


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

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases of their 1:1 instance-type equivalents.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.
