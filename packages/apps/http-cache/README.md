# Managed Nginx Caching Service

The Nginx Caching Service is designed to optimize web traffic and enhance web application performance. This service combines custom-built Nginx instances with HAproxy for efficient caching and load balancing.

## Deployment infromation

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

VTS module shows wrong upstream resonse time
- https://github.com/vozlt/nginx-module-vts/issues/198

## Parameters

### Common parameters

| Name                      | Description                                                                                                                                                                                                       | Value   |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `external`                | Enable external access from outside the cluster                                                                                                                                                                   | `false` |
| `size`                    | Persistent Volume size                                                                                                                                                                                            | `10Gi`  |
| `storageClass`            | StorageClass used to store the data                                                                                                                                                                               | `""`    |
| `haproxy.replicas`        | Number of HAProxy replicas                                                                                                                                                                                        | `2`     |
| `nginx.replicas`          | Number of Nginx replicas                                                                                                                                                                                          | `2`     |
| `haproxy.resources`       | Resources                                                                                                                                                                                                         | `{}`    |
| `haproxy.resourcesPreset` | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano`  |
| `nginx.resources`         | Resources                                                                                                                                                                                                         | `{}`    |
| `nginx.resourcesPreset`   | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano`  |

### Configuration parameters

| Name        | Description             | Value |
| ----------- | ----------------------- | ----- |
| `endpoints` | Endpoints configuration | `[]`  |
