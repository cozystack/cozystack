# Managed Nginx-based HTTP Cache Service

The Nginx-based HTTP caching service is designed to optimize web traffic and enhance web application performance.
This service combines custom-built Nginx instances with HAProxy for efficient caching and load balancing.

## Deployment information

The Nginx instances include the following modules and features:

- VTS module for statistics
- Integration with ip2location
- Integration with ip2proxy
- Support for 51Degrees
- Cache purge functionality

HAproxy plays a vital role in this setup by directing incoming traffic to specific Nginx instances based on a consistent hash calculated from the URL. Each Nginx instance includes a Persistent Volume Claim (PVC) for storing cached content, ensuring fast and reliable access to frequently used resources.

## Deployment Details

The deployment architecture is illustrated in the diagram below:

```

          ┌─────────┐
          │ metallb │ arp announce
          └────┬────┘
               │
               │
       ┌───────▼───────────────────────────┐
       │  kubernetes service               │  node
       │ (externalTrafficPolicy: Local)    │  level
       └──────────┬────────────────────────┘
                  │
                  │
             ┌────▼────┐  ┌─────────┐
             │ haproxy │  │ haproxy │   loadbalancer
             │ (active)│  │ (backup)│      layer
             └────┬────┘  └─────────┘
                  │
                  │ balance uri whole
                  │ hash-type consistent
           ┌──────┴──────┬──────────────┐
       ┌───▼───┐     ┌───▼───┐      ┌───▼───┐ caching
       │ nginx │     │ nginx │      │ nginx │  layer
       └───┬───┘     └───┬───┘      └───┬───┘
           │             │              │
      ┌────┴───────┬─────┴────┬─────────┴──┐
      │            │          │            │
  ┌───▼────┐  ┌────▼───┐  ┌───▼────┐  ┌────▼───┐
  │ origin │  │ origin │  │ origin │  │ origin │
  └────────┘  └────────┘  └────────┘  └────────┘

```

## Known issues

- VTS module shows wrong upstream response time, [github.com/vozlt/nginx-module-vts#198](https://github.com/vozlt/nginx-module-vts/issues/198)

## Parameters

### Common parameters

| Name                      | Description                                                                                                                          | Value   |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| `external`                | Enable external access from outside the cluster                                                                                      | `false` |
| `size`                    | Persistent Volume size                                                                                                               | `10Gi`  |
| `storageClass`            | StorageClass used to store the data                                                                                                  | `""`    |
| `haproxy.replicas`        | Number of HAProxy replicas                                                                                                           | `2`     |
| `nginx.replicas`          | Number of Nginx replicas                                                                                                             | `2`     |
| `haproxy.resources`       | Explicit CPU and memory configuration for each HAProxy replica. When left empty, the preset defined in `resourcesPreset` is applied. | `{}`    |
| `haproxy.resourcesPreset` | Default sizing preset used when `resources` is omitted. Allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge.    | `nano`  |
| `nginx.resources`         | Explicit CPU and memory configuration for each nginx replica. When left empty, the preset defined in `resourcesPreset` is applied.   | `{}`    |
| `nginx.resourcesPreset`   | Default sizing preset used when `resources` is omitted. Allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge.    | `nano`  |

Example of `resources`:

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

Allowed values for `resourcesPreset` are `none`, `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`.
This value is ignored if the corresponding `resources` value is set.

### Configuration parameters

| Name        | Description             | Value |
| ----------- | ----------------------- | ----- |
| `endpoints` | Endpoints configuration | `[]`  |

Example of `endpoints`:

```yaml
endpoints:
  - 10.100.3.1:80
  - 10.100.3.11:80
  - 10.100.3.2:80
  - 10.100.3.12:80
  - 10.100.3.3:80
  - 10.100.3.13:80
```
