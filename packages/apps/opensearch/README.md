# Managed OpenSearch Service

## Parameters

### Common parameters

| Name                   | Description                                                                                                                       | Type       | Value   |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `replicas`             | Number of OpenSearch nodes in the cluster.                                                                                        | `int`      | `3`     |
| `resources`            | Explicit CPU and memory configuration for each OpenSearch node. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `resources.cpu`        | CPU available to each node.                                                                                                       | `quantity` | `""`    |
| `resources.memory`     | Memory (RAM) available to each node.                                                                                              | `quantity` | `""`    |
| `resourcesPreset`      | Default sizing preset used when `resources` is omitted. OpenSearch requires minimum 2Gi memory.                                   | `string`   | `large` |
| `size`                 | Persistent Volume Claim size available for application data.                                                                      | `quantity` | `10Gi`  |
| `storageClass`         | StorageClass used to store the data.                                                                                              | `string`   | `""`    |
| `external`             | Enable external access from outside the cluster.                                                                                  | `bool`     | `false` |
| `topologySpreadPolicy` | How strictly to enforce pod distribution across nodes and zones.                                                                  | `string`   | `soft`  |
| `version`              | OpenSearch major version to deploy.                                                                                               | `string`   | `v2`    |


### Image configuration

| Name                | Description                            | Type     | Value |
| ------------------- | -------------------------------------- | -------- | ----- |
| `images`            | Container images used by the operator. | `object` | `{}`  |
| `images.opensearch` | OpenSearch image.                      | `string` | `""`  |


### Node roles configuration

| Name               | Description                   | Type     | Value   |
| ------------------ | ----------------------------- | -------- | ------- |
| `nodeRoles`        | Node roles configuration.     | `object` | `{}`    |
| `nodeRoles.master` | Enable cluster_manager role.  | `bool`   | `true`  |
| `nodeRoles.data`   | Enable data role.             | `bool`   | `true`  |
| `nodeRoles.ingest` | Enable ingest role.           | `bool`   | `true`  |
| `nodeRoles.ml`     | Enable machine learning role. | `bool`   | `false` |


### Users configuration

| Name                   | Description                                        | Type                | Value |
| ---------------------- | -------------------------------------------------- | ------------------- | ----- |
| `users`                | Custom OpenSearch users configuration map.         | `map[string]object` | `{}`  |
| `users[name].password` | Password for the user (auto-generated if omitted). | `string`            | `""`  |
| `users[name].roles`    | List of OpenSearch roles.                          | `[]string`          | `[]`  |


### OpenSearch Dashboards configuration

| Name                          | Description                                           | Type       | Value    |
| ----------------------------- | ----------------------------------------------------- | ---------- | -------- |
| `dashboards`                  | OpenSearch Dashboards configuration.                  | `object`   | `{}`     |
| `dashboards.enabled`          | Enable OpenSearch Dashboards deployment.              | `bool`     | `false`  |
| `dashboards.replicas`         | Number of Dashboards replicas.                        | `int`      | `1`      |
| `dashboards.resources`        | Explicit CPU and memory configuration for Dashboards. | `object`   | `{}`     |
| `dashboards.resources.cpu`    | CPU available to each node.                           | `quantity` | `""`     |
| `dashboards.resources.memory` | Memory (RAM) available to each node.                  | `quantity` | `""`     |
| `dashboards.resourcesPreset`  | Default sizing preset for Dashboards.                 | `string`   | `medium` |

