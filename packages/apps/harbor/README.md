# Managed Harbor Container Registry

Harbor is an open-source trusted cloud-native registry project that stores, signs, and scans content.

## Prerequisites

The Cozystack Harbor app stores its registry data exclusively in S3-compatible object storage: the chart pins the registry backend to S3 and exposes no filesystem option. That bucket is provisioned through COSI (`objectstorage.k8s.io`) from a SeaweedFS deployment, so before deploying Harbor the tenant must have SeaweedFS available — enabled on the same tenant or inherited from a parent tenant (the resolved class is propagated down the tenant tree, surfaced as the `namespace.cozystack.io/seaweedfs` namespace annotation).

Enable it by setting `seaweedfs: true` on the tenant (or a parent tenant):

```yaml
seaweedfs: true
```

Without object storage in the tenant chain, Harbor cannot provision its registry bucket: the `<release>-registry` `BucketClaim`/`BucketAccess` never produces the `<release>-registry-bucket` credentials secret, so the Harbor `HelmRelease` stays unreconciled, waiting on `BucketInfo`.

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name           | Description                                                                                  | Type     | Value |
| -------------- | -------------------------------------------------------------------------------------------- | -------- | ----- |
| `host`         | Hostname for external access to Harbor (defaults to 'harbor' subdomain for the tenant host). | `string` | `""`  |
| `storageClass` | StorageClass used to store the data.                                                         | `string` | `""`  |


### Component configuration

| Name                          | Description                                                                                                                                                                                                       | Type       | Value      |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `core`                        | Core API server configuration.                                                                                                                                                                                    | `object`   | `{}`       |
| `core.resources`              | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied.                                                                                                          | `object`   | `{}`       |
| `core.resources.cpu`          | Number of CPU cores allocated.                                                                                                                                                                                    | `quantity` | `""`       |
| `core.resources.memory`       | Amount of memory allocated.                                                                                                                                                                                       | `quantity` | `""`       |
| `core.resourcesPreset`        | Default sizing preset used when `resources` is omitted.                                                                                                                                                           | `string`   | `t1.small` |
| `registry`                    | Container image registry configuration.                                                                                                                                                                           | `object`   | `{}`       |
| `registry.resources`          | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied.                                                                                                          | `object`   | `{}`       |
| `registry.resources.cpu`      | Number of CPU cores allocated.                                                                                                                                                                                    | `quantity` | `""`       |
| `registry.resources.memory`   | Amount of memory allocated.                                                                                                                                                                                       | `quantity` | `""`       |
| `registry.resourcesPreset`    | Default sizing preset used when `resources` is omitted.                                                                                                                                                           | `string`   | `t1.small` |
| `jobservice`                  | Background job service configuration.                                                                                                                                                                             | `object`   | `{}`       |
| `jobservice.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied.                                                                                                          | `object`   | `{}`       |
| `jobservice.resources.cpu`    | Number of CPU cores allocated.                                                                                                                                                                                    | `quantity` | `""`       |
| `jobservice.resources.memory` | Amount of memory allocated.                                                                                                                                                                                       | `quantity` | `""`       |
| `jobservice.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                                                                                           | `string`   | `t1.nano`  |
| `trivy`                       | Trivy vulnerability scanner configuration.                                                                                                                                                                        | `object`   | `{}`       |
| `trivy.enabled`               | Enable or disable the vulnerability scanner.                                                                                                                                                                      | `bool`     | `true`     |
| `trivy.size`                  | Persistent Volume size for vulnerability database cache.                                                                                                                                                          | `quantity` | `5Gi`      |
| `trivy.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied.                                                                                                          | `object`   | `{}`       |
| `trivy.resources.cpu`         | Number of CPU cores allocated.                                                                                                                                                                                    | `quantity` | `""`       |
| `trivy.resources.memory`      | Amount of memory allocated.                                                                                                                                                                                       | `quantity` | `""`       |
| `trivy.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                                                                                                                           | `string`   | `t1.nano`  |
| `database`                    | PostgreSQL database configuration.                                                                                                                                                                                | `object`   | `{}`       |
| `database.size`               | Persistent Volume size for database storage.                                                                                                                                                                      | `quantity` | `5Gi`      |
| `database.replicas`           | Number of database instances.                                                                                                                                                                                     | `int`      | `2`        |
| `redis`                       | Redis cache configuration.                                                                                                                                                                                        | `object`   | `{}`       |
| `redis.size`                  | Persistent Volume size for cache storage.                                                                                                                                                                         | `quantity` | `1Gi`      |
| `redis.replicas`              | Number of Redis replicas.                                                                                                                                                                                         | `int`      | `2`        |
| `schedulingClass`             | Name of a SchedulingClass CR (cluster-scoped, group cozystack.io) applied to this application's workloads. When set, takes precedence over any tenant-level schedulingClass. Empty means inherit from the tenant. | `string`   | `""`       |

