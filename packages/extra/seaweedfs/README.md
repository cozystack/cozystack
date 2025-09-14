# Managed NATS Service

## Parameters

### Common parameters

| Name                   | Description                                                                                            | Type                | Value    |
| ---------------------- | ------------------------------------------------------------------------------------------------------ | ------------------- | -------- |
| `host`                 | The hostname used to access the SeaweedFS externally (defaults to 's3' subdomain for the tenant host). | `*string`           | `""`     |
| `topology`             | The topology of the SeaweedFS cluster. (allowed values: Simple, MultiZone, Client)                     | `string`            | `Simple` |
| `replicationFactor`    | Replication factor: number of replicas for each volume in the SeaweedFS cluster.                       | `int`               | `2`      |
| `replicas`             | Number of replicas                                                                                     | `int`               | `2`      |
| `size`                 | Persistent Volume size                                                                                 | `quantity`          | `10Gi`   |
| `storageClass`         | StorageClass used to store the data                                                                    | `*string`           | `""`     |
| `zones`                | A map of zones for MultiZone topology. Each zone can have its own number of replicas and size.         | `map[string]object` | `{...}`  |
| `zones[name].replicas` | Number of replicas in the zone                                                                         | `int`               | `0`      |
| `zones[name].size`     | Zone storage size                                                                                      | `quantity`          | `""`     |
| `filer`                | Filer service configuration                                                                            | `*object`           | `{}`     |
| `filer.grpcHost`       | The hostname used to expose or access the filer service externally.                                    | `*string`           | `""`     |
| `filer.grpcPort`       | The port used to access the filer service externally.                                                  | `*int`              | `443`    |
| `filer.whitelist`      | A list of IP addresses or CIDR ranges that are allowed to access the filer service.                    | `[]*string`         | `[]`     |


### Vertical Pod Autoscaler parameters

| Name                           | Description                                                         | Type     | Value                                                                                                                               |
| ------------------------------ | ------------------------------------------------------------------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `vpa`                          | Vertical Pod Autoscaler configuration for each SeaweedFS component. | `object` | `{"filer":{"maxAllowed":{},"minAllowed":{}},"master":{"maxAllowed":{},"minAllowed":{}},"volume":{"maxAllowed":{},"minAllowed":{}}}` |
| `vpa.filer.minAllowed.cpu`     | Minimum CPU request for filer pods                                  | `string` | `""`                                                                                                                                |
| `vpa.filer.minAllowed.memory`  | Minimum memory request for filer pods                               | `string` | `""`                                                                                                                                |
| `vpa.filer.maxAllowed.cpu`     | Maximum CPU limit for filer pods                                    | `string` | `""`                                                                                                                                |
| `vpa.filer.maxAllowed.memory`  | Maximum memory limit for filer pods                                 | `string` | `""`                                                                                                                                |
| `vpa.master.minAllowed.cpu`    | Minimum CPU request for master pods                                 | `string` | `""`                                                                                                                                |
| `vpa.master.minAllowed.memory` | Minimum memory request for master pods                              | `string` | `""`                                                                                                                                |
| `vpa.master.maxAllowed.cpu`    | Maximum CPU limit for master pods                                   | `string` | `""`                                                                                                                                |
| `vpa.master.maxAllowed.memory` | Maximum memory limit for master pods                                | `string` | `""`                                                                                                                                |
| `vpa.volume.minAllowed.cpu`    | Minimum CPU request for volume pods                                 | `string` | `""`                                                                                                                                |
| `vpa.volume.minAllowed.memory` | Minimum memory request for volume pods                              | `string` | `""`                                                                                                                                |
| `vpa.volume.maxAllowed.cpu`    | Maximum CPU limit for volume pods                                   | `string` | `""`                                                                                                                                |
| `vpa.volume.maxAllowed.memory` | Maximum memory limit for volume pods                                | `string` | `""`                                                                                                                                |

