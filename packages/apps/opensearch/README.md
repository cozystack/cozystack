# Managed OpenSearch Service

## Parameters

### Common parameters

| Name       | Description                                      | Type   | Value   |
| ---------- | ------------------------------------------------ | ------ | ------- |
| `external` | Enable external access from outside the cluster. | `bool` | `false` |


### OpenSearch configuration

| Name                          | Description                                                                                              | Type       | Value    |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | -------- |
| `opensearch`                  | OpenSearch configuration.                                                                                | `object`   | `{}`     |
| `opensearch.replicas`         | Number of OpenSearch replicas.                                                                           | `int`      | `3`      |
| `opensearch.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`     |
| `opensearch.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`     |
| `opensearch.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`     |
| `opensearch.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `medium` |
| `opensearch.size`             | Persistent Volume size for OpenSearch data.                                                              | `quantity` | `10Gi`   |
| `opensearch.storageClass`     | StorageClass used to store the OpenSearch data.                                                          | `string`   | `""`     |


### Dashboards configuration

| Name                          | Description                                                                                              | Type       | Value   |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `dashboards`                  | OpenSearch Dashboards configuration.                                                                     | `object`   | `{}`    |
| `dashboards.enabled`          | Enable OpenSearch Dashboards.                                                                            | `bool`     | `true`  |
| `dashboards.replicas`         | Number of Dashboards replicas.                                                                           | `int`      | `1`     |
| `dashboards.resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `dashboards.resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`    |
| `dashboards.resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`    |
| `dashboards.resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `small` |

