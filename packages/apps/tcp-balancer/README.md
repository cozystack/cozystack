# Managed TCP Load Balancer Service

The Managed TCP Load Balancer Service simplifies the deployment and management of load balancers. It efficiently distributes incoming TCP traffic across multiple backend servers, ensuring high availability and optimal resource utilization.

## Deployment Details

Managed TCP Load Balancer Service efficiently utilizes HAProxy for load balancing purposes. HAProxy is a well-established and reliable solution for distributing incoming TCP traffic across multiple backend servers, ensuring high availability and efficient resource utilization. This deployment choice guarantees the seamless and dependable operation of your load balancing infrastructure.

- Docs: https://www.haproxy.com/documentation/

## Parameters

### Common parameters

| Name       | Description                                     | Value   |
| ---------- | ----------------------------------------------- | ------- |
| `external` | Enable external access from outside the cluster | `false` |
| `replicas` | Number of HAProxy replicas                      | `2`     |

### Configuration parameters

| Name                             | Description                                                                                                                               | Value   |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `httpAndHttps.mode`              | Mode for balancer. Allowed values: `tcp` and `tcp-with-proxy`                                                                             | `tcp`   |
| `httpAndHttps.targetPorts.http`  | HTTP port number.                                                                                                                         | `80`    |
| `httpAndHttps.targetPorts.https` | HTTPS port number.                                                                                                                        | `443`   |
| `httpAndHttps.endpoints`         | Endpoint addresses list                                                                                                                   | `[]`    |
| `whitelistHTTP`                  | Secure HTTP by enabling  client networks whitelisting                                                                                     | `false` |
| `whitelist`                      | List of client networks                                                                                                                   | `[]`    |
| `resources`                      | Explicit CPU and memory configuration for each TCP Balancer replica. When left empty, the preset defined in `resourcesPreset` is applied. | `{}`    |
| `resourcesPreset`                | Default sizing preset used when `resources` is omitted. Allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge.         | `nano`  |

Example of `resources`:

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

Allowed values for `resourcesPreset` are `none`, `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`.
This value is ignored if the corresponding `resources` value is set.