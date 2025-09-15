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
| `zones[name].replicas` | Number of replicas in the zone                                                                         | `*int`              | `null`   |
| `zones[name].size`     | Zone storage size                                                                                      | `*quantity`         | `null`   |
| `filer`                | Filer service configuration                                                                            | `*object`           | `{}`     |
| `filer.grpcHost`       | The hostname used to expose or access the filer service externally.                                    | `*string`           | `""`     |
| `filer.grpcPort`       | The port used to access the filer service externally.                                                  | `*int`              | `443`    |
| `filer.whitelist`      | A list of IP addresses or CIDR ranges that are allowed to access the filer service.                    | `[]*string`         | `[]`     |


### Vertical Pod Autoscaler parameters

| Name                           | Description                                                         | Type        | Value  |
| ------------------------------ | ------------------------------------------------------------------- | ----------- | ------ |
| `vpa`                          | Vertical Pod Autoscaler configuration for each SeaweedFS component. | `object`    | `{}`   |
| `vpa.master`                   | VPA configuration for the master servers                            | `object`    | `{}`   |
| `vpa.master.minAllowed`        | Minimum resource requests                                           | `*object`   | `null` |
| `vpa.master.minAllowed.cpu`    | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.master.minAllowed.memory` | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.master.maxAllowed`        | Maximum resource requests                                           | `*object`   | `null` |
| `vpa.master.maxAllowed.cpu`    | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.master.maxAllowed.memory` | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.filer`                    | VPA configuration for the filer server                              | `object`    | `{}`   |
| `vpa.filer.minAllowed`         | Minimum resource requests                                           | `*object`   | `null` |
| `vpa.filer.minAllowed.cpu`     | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.filer.minAllowed.memory`  | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.filer.maxAllowed`         | Maximum resource requests                                           | `*object`   | `null` |
| `vpa.filer.maxAllowed.cpu`     | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.filer.maxAllowed.memory`  | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.volume`                   | VPA configuration for the volume servers                            | `object`    | `{}`   |
| `vpa.volume.minAllowed`        | Minimum resource requests                                           | `*object`   | `null` |
| `vpa.volume.minAllowed.cpu`    | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.volume.minAllowed.memory` | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.volume.maxAllowed`        | Maximum resource requests                                           | `*object`   | `null` |
| `vpa.volume.maxAllowed.cpu`    | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.volume.maxAllowed.memory` | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.s3`                       | VPA configuration for the S3 gateway                                | `object`    | `{}`   |
| `vpa.s3.minAllowed`            | Minimum resource requests                                           | `*object`   | `null` |
| `vpa.s3.minAllowed.cpu`        | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.s3.minAllowed.memory`     | Minimum memory request                                              | `*quantity` | `null` |
| `vpa.s3.maxAllowed`            | Maximum resource requests                                           | `*object`   | `null` |
| `vpa.s3.maxAllowed.cpu`        | Minimum CPU request                                                 | `*quantity` | `null` |
| `vpa.s3.maxAllowed.memory`     | Minimum memory request                                              | `*quantity` | `null` |

