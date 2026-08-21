# Managed RabbitMQ Service

RabbitMQ is a robust message broker that plays a crucial role in modern distributed systems. Our Managed RabbitMQ Service simplifies the deployment and management of RabbitMQ clusters, ensuring reliability and scalability for your messaging needs.

## Deployment Details

The service utilizes official RabbitMQ operator. This ensures the reliability and seamless operation of your RabbitMQ instances.

- Github: https://github.com/rabbitmq/cluster-operator/
- Docs: https://www.rabbitmq.com/kubernetes/operator/operator-overview.html

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                        | Type       | Value     |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of RabbitMQ replicas.                                                                                                       | `int`      | `3`       |
| `resources`        | Explicit CPU and memory configuration for each RabbitMQ replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                     | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                            | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                            | `string`   | `u1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                       | `quantity` | `10Gi`    |
| `storageClass`     | StorageClass used to store the data.                                                                                               | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                   | `bool`     | `false`   |
| `version`          | RabbitMQ major.minor version to deploy                                                                                             | `string`   | `v4.2`    |


### Application-specific parameters

| Name                          | Description                      | Type                | Value |
| ----------------------------- | -------------------------------- | ------------------- | ----- |
| `users`                       | Users configuration map.         | `map[string]object` | `{}`  |
| `users[name].password`        | Password for the user.           | `string`            | `""`  |
| `vhosts`                      | Virtual hosts configuration map. | `map[string]object` | `{}`  |
| `vhosts[name].roles`          | Virtual host roles list.         | `object`            | `{}`  |
| `vhosts[name].roles.admin`    | List of admin users.             | `[]string`          | `[]`  |
| `vhosts[name].roles.readonly` | List of readonly users.          | `[]string`          | `[]`  |


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

The default `u1.nano` preset supplies 250m CPU and 1Gi of memory per replica. See the [resource preset matrix](../../../docs/operations/resource-presets.md) for all instance types and legacy-name compatibility.
