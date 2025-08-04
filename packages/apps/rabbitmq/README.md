# Managed RabbitMQ Service

RabbitMQ is a robust message broker that plays a crucial role in modern distributed systems. Our Managed RabbitMQ Service simplifies the deployment and management of RabbitMQ clusters, ensuring reliability and scalability for your messaging needs.

## Deployment Details

The service utilizes official RabbitMQ operator. This ensures the reliability and seamless operation of your RabbitMQ instances.

- Github: https://github.com/rabbitmq/cluster-operator/
- Docs: https://www.rabbitmq.com/kubernetes/operator/operator-overview.html

## Parameters

### Common parameters

| Name               | Description                                                                                                                               | Type        | Value   |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `replicas`         | Number of RabbitMQ replicas                                                                                                               | `int`       | `3`     |
| `resources`        | Explicit CPU and memory configuration for each RabbitMQ replica.  When left empty, the preset defined in `resourcesPreset` is applied.    | `*object`   | `{}`    |
| `resources.cpu`    | CPU                                                                                                                                       | `*quantity` | `null`  |
| `resources.memory` | Memory                                                                                                                                    | `*quantity` | `null`  |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `nano`  |
| `size`             | Persistent Volume Claim size, available for application data                                                                              | `quantity`  | `10Gi`  |
| `storageClass`     | StorageClass used to store the data                                                                                                       | `string`    | `""`    |
| `external`         | Enable external access from outside the cluster                                                                                           | `bool`      | `false` |


### Application-specific parameters

| Name                          | Description                 | Type                | Value   |
| ----------------------------- | --------------------------- | ------------------- | ------- |
| `users`                       | Users configuration         | `map[string]object` | `{...}` |
| `users[name].password`        | Password for the user       | `*string`           | `null`  |
| `vhosts`                      | Virtual Hosts configuration | `map[string]object` | `{...}` |
| `vhosts[name].roles`          | Virtual host roles list     | `object`            | `{}`    |
| `vhosts[name].roles.admin`    | List of admin users         | `[]string`          | `[]`    |
| `vhosts[name].roles.readonly` | List of readonly users      | `[]string`          | `[]`    |
| `vhost`                       | Virtual Host                | `object`            | `{}`    |
| `vhost.roles`                 | Virtual host roles list     | `object`            | `{}`    |
| `vhost.roles.admin`           | List of admin users         | `[]string`          | `[]`    |
| `vhost.roles.readonly`        | List of readonly users      | `[]string`          | `[]`    |


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
| `nano`      | `100m` | `128Mi` |
| `micro`     | `250m` | `256Mi` |
| `small`     | `500m` | `512Mi` |
| `medium`    | `500m` | `1Gi`   |
| `large`     | `1`    | `2Gi`   |
| `xlarge`    | `2`    | `4Gi`   |
| `2xlarge`   | `4`    | `8Gi`   |

