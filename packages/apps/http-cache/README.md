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

| Name           | Description                                                  | Type       | Value   |
| -------------- | ------------------------------------------------------------ | ---------- | ------- |
| `size`         | Persistent Volume Claim size available for application data. | `quantity` | `10Gi`  |
| `storageClass` | StorageClass used to store the data.                         | `string`   | `""`    |
| `external`     | Enable external access from outside the cluster.             | `bool`     | `false` |


### Application-specific parameters

| Name        | Description                                      | Type       | Value |
| ----------- | ------------------------------------------------ | ---------- | ----- |
| `endpoints` | Endpoints configuration, as a list of <ip:port>. | `[]string` | `[]`  |


### HAProxy parameters

| Name                       | Description                                                                                              | Type       | Value     |
| -------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `haproxy`                  | HAProxy configuration.                                                                                   | `object`   | `{}`      |
| `haproxy.replicas`         | Number of HAProxy replicas.                                                                              | `int`      | `2`       |
| `haproxy.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `haproxy.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`      |
| `haproxy.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`      |
| `haproxy.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `s1.nano` |


### Nginx parameters

| Name                     | Description                                                                                              | Type       | Value     |
| ------------------------ | -------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `nginx`                  | Nginx configuration.                                                                                     | `object`   | `{}`      |
| `nginx.replicas`         | Number of Nginx replicas.                                                                                | `int`      | `2`       |
| `nginx.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `nginx.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`      |
| `nginx.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`      |
| `nginx.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `s1.nano` |


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


### endpoints

`endpoints` is a flat list of IP addresses:

```yaml
endpoints:
  - 10.100.3.1:80
  - 10.100.3.11:80
  - 10.100.3.2:80
  - 10.100.3.12:80
  - 10.100.3.3:80
  - 10.100.3.13:80
```
