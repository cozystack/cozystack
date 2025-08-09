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
| `size`         | Persistent Volume Claim size, available for application data | `quantity` | `10Gi`  |
| `storageClass` | StorageClass used to store the data                          | `string`   | `""`    |
| `external`     | Enable external access from outside the cluster              | `bool`     | `false` |


### Application-specific parameters

| Name        | Description                                     | Type       | Value |
| ----------- | ----------------------------------------------- | ---------- | ----- |
| `endpoints` | Endpoints configuration, as a list of <ip:port> | `[]string` | `[]`  |


### HAProxy parameters

| Name                       | Description                                                                                                                               | Type        | Value  |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------ |
| `haproxy`                  | HAProxy configuration                                                                                                                     | `object`    | `{}`   |
| `haproxy.replicas`         | Number of HAProxy replicas                                                                                                                | `int`       | `2`    |
| `haproxy.resources`        | Explicit CPU and memory configuration for each replica. When left empty, the preset defined in `resourcesPreset` is applied.              | `object`    | `{}`   |
| `haproxy.resources.cpu`    | CPU                                                                                                                                       | `*quantity` | `null` |
| `haproxy.resources.memory` | Memory                                                                                                                                    | `*quantity` | `null` |
| `haproxy.resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `nano` |


### Nginx parameters

| Name                     | Description                                                                                                                               | Type        | Value  |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------ |
| `nginx`                  | Nginx configuration                                                                                                                       | `object`    | `{}`   |
| `nginx.replicas`         | Number of Nginx replicas                                                                                                                  | `int`       | `2`    |
| `nginx.resources`        | Explicit CPU and memory configuration for each replica. When left empty, the preset defined in `resourcesPreset` is applied.              | `*object`   | `null` |
| `nginx.resources.cpu`    | CPU                                                                                                                                       | `*quantity` | `null` |
| `nginx.resources.memory` | Memory                                                                                                                                    | `*quantity` | `null` |
| `nginx.resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `nano` |


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
