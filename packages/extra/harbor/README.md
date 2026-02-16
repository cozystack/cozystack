# Managed Harbor Container Registry

Harbor is an open source trusted cloud native registry project that stores, signs, and scans content.

## Parameters

### Common parameters

| Name           | Description                                                                                  | Type     | Value |
| -------------- | -------------------------------------------------------------------------------------------- | -------- | ----- |
| `host`         | Hostname for external access to Harbor (defaults to 'harbor' subdomain for the tenant host). | `string` | `""`  |
| `storageClass` | StorageClass used to store the data.                                                         | `string` | `""`  |


### Component configuration

| Name                          | Description                                                                                              | Type       | Value   |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `core`                        | Core API server configuration.                                                                           | `object`   | `{}`    |
| `core.resources`              | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `core.resources.cpu`          | Number of CPU cores allocated.                                                                           | `quantity` | `""`    |
| `core.resources.memory`       | Amount of memory allocated.                                                                              | `quantity` | `""`    |
| `core.resourcesPreset`        | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `small` |
| `registry`                    | Container image registry configuration.                                                                  | `object`   | `{}`    |
| `registry.size`               | Persistent Volume size for container image storage.                                                      | `quantity` | `50Gi`  |
| `registry.resources`          | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `registry.resources.cpu`      | Number of CPU cores allocated.                                                                           | `quantity` | `""`    |
| `registry.resources.memory`   | Amount of memory allocated.                                                                              | `quantity` | `""`    |
| `registry.resourcesPreset`    | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `small` |
| `jobservice`                  | Background job service configuration.                                                                    | `object`   | `{}`    |
| `jobservice.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `jobservice.resources.cpu`    | Number of CPU cores allocated.                                                                           | `quantity` | `""`    |
| `jobservice.resources.memory` | Amount of memory allocated.                                                                              | `quantity` | `""`    |
| `jobservice.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `nano`  |
| `trivy`                       | Trivy vulnerability scanner configuration.                                                               | `object`   | `{}`    |
| `trivy.enabled`               | Enable or disable the vulnerability scanner.                                                             | `bool`     | `true`  |
| `trivy.size`                  | Persistent Volume size for vulnerability database cache.                                                 | `quantity` | `5Gi`   |
| `trivy.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `trivy.resources.cpu`         | Number of CPU cores allocated.                                                                           | `quantity` | `""`    |
| `trivy.resources.memory`      | Amount of memory allocated.                                                                              | `quantity` | `""`    |
| `trivy.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `nano`  |
| `database`                    | Internal PostgreSQL database configuration.                                                              | `object`   | `{}`    |
| `database.size`               | Persistent Volume size for database storage.                                                             | `quantity` | `5Gi`   |
| `database.resources`          | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `database.resources.cpu`      | Number of CPU cores allocated.                                                                           | `quantity` | `""`    |
| `database.resources.memory`   | Amount of memory allocated.                                                                              | `quantity` | `""`    |
| `database.resourcesPreset`    | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `nano`  |
| `redis`                       | Internal Redis cache configuration.                                                                      | `object`   | `{}`    |
| `redis.size`                  | Persistent Volume size for cache storage.                                                                | `quantity` | `1Gi`   |
| `redis.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `redis.resources.cpu`         | Number of CPU cores allocated.                                                                           | `quantity` | `""`    |
| `redis.resources.memory`      | Amount of memory allocated.                                                                              | `quantity` | `""`    |
| `redis.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `nano`  |

