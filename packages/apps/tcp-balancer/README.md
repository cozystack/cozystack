# Managed TCP Load Balancer Service

The Managed TCP Load Balancer Service simplifies the deployment and management of load balancers. It efficiently distributes incoming TCP traffic across multiple backend servers, ensuring high availability and optimal resource utilization.

## Deployment Details

Managed TCP Load Balancer Service efficiently utilizes HAProxy for load balancing purposes. HAProxy is a well-established and reliable solution for distributing incoming TCP traffic across multiple backend servers, ensuring high availability and efficient resource utilization. This deployment choice guarantees the seamless and dependable operation of your load balancing infrastructure.

- Docs: https://www.haproxy.com/documentation/

## Parameters

### Common parameters

| Name               | Description                                                                                                                            | Type       | Value   |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `replicas`         | Number of HAProxy replicas.                                                                                                            | `int`      | `2`     |
| `resources`        | Explicit CPU and memory configuration for each TCP Balancer replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica.                                                                                                         | `quantity` | `""`    |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                | `string`   | `nano`  |
| `external`         | Enable external access from outside the cluster.                                                                                       | `bool`     | `false` |


### Application-specific parameters

| Name                             | Description                                                   | Type       | Value   |
| -------------------------------- | ------------------------------------------------------------- | ---------- | ------- |
| `httpAndHttps`                   | HTTP and HTTPS configuration.                                 | `object`   | `{}`    |
| `httpAndHttps.mode`              | Mode for balancer.                                            | `string`   | `tcp`   |
| `httpAndHttps.targetPorts`       | Target ports configuration.                                   | `object`   | `{}`    |
| `httpAndHttps.targetPorts.http`  | HTTP port number.                                             | `int`      | `80`    |
| `httpAndHttps.targetPorts.https` | HTTPS port number.                                            | `int`      | `443`   |
| `httpAndHttps.endpoints`         | Endpoint addresses list.                                      | `[]string` | `[]`    |
| `whitelistHTTP`                  | Secure HTTP by whitelisting client networks (default: false). | `bool`     | `false` |
| `whitelist`                      | List of allowed client networks.                              | `[]string` | `[]`    |


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
