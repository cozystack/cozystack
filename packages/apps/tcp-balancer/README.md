# Managed TCP Load Balancer Service

The Managed TCP Load Balancer Service simplifies the deployment and management of load balancers. It efficiently distributes incoming TCP traffic across multiple backend servers, ensuring high availability and optimal resource utilization.

## Deployment Details

Managed TCP Load Balancer Service efficiently utilizes HAProxy for load balancing purposes. HAProxy is a well-established and reliable solution for distributing incoming TCP traffic across multiple backend servers, ensuring high availability and efficient resource utilization. This deployment choice guarantees the seamless and dependable operation of your load balancing infrastructure.

- Docs: https://www.haproxy.com/documentation/

## Parameters

### Common parameters

| Name               | Description                                                                                                                            | Type       | Value     |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of HAProxy replicas.                                                                                                            | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each TCP Balancer replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                         | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                | `string`   | `t1.nano` |
| `external`         | Enable external access from outside the cluster.                                                                                       | `bool`     | `false`   |


### Application-specific parameters

| Name                             | Description                                                                                                                                                                                                       | Type       | Value   |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `httpAndHttps`                   | HTTP and HTTPS configuration.                                                                                                                                                                                     | `object`   | `{}`    |
| `httpAndHttps.mode`              | Mode for balancer.                                                                                                                                                                                                | `string`   | `tcp`   |
| `httpAndHttps.targetPorts`       | Target ports configuration.                                                                                                                                                                                       | `object`   | `{}`    |
| `httpAndHttps.targetPorts.http`  | HTTP port number.                                                                                                                                                                                                 | `int`      | `80`    |
| `httpAndHttps.targetPorts.https` | HTTPS port number.                                                                                                                                                                                                | `int`      | `443`   |
| `httpAndHttps.endpoints`         | Endpoint addresses list.                                                                                                                                                                                          | `[]string` | `[]`    |
| `whitelistHTTP`                  | Secure HTTP by whitelisting client networks (default: false).                                                                                                                                                     | `bool`     | `false` |
| `whitelist`                      | List of allowed client networks.                                                                                                                                                                                  | `[]string` | `[]`    |
| `schedulingClass`                | Name of a SchedulingClass CR (cluster-scoped, group cozystack.io) applied to this application's workloads. When set, takes precedence over any tenant-level schedulingClass. Empty means inherit from the tenant. | `string`   | `""`    |


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
