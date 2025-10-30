# Managed NATS Service

## Parameters

### Common parameters

| Name                | Description                                                                                        | Type     | Value    |
| ------------------- | -------------------------------------------------------------------------------------------------- | -------- | -------- |
| `host`              | The hostname used to access SeaweedFS externally (defaults to 's3' subdomain for the tenant host). | `string` | `""`     |
| `topology`          | The topology of the SeaweedFS cluster.                                                             | `string` | `Simple` |
| `replicationFactor` | Replication factor: number of replicas for each volume in the SeaweedFS cluster.                   | `int`    | `2`      |


### SeaweedFS Components Configuration

| Name                          | Description                                                                                              | Type                | Value   |
| ----------------------------- | -------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `db`                          | Database configuration.                                                                                  | `object`            | `{}`    |
| `db.replicas`                 | Number of database replicas.                                                                             | `int`               | `2`     |
| `db.size`                     | Persistent Volume size.                                                                                  | `quantity`          | `10Gi`  |
| `db.storageClass`             | StorageClass used to store the data.                                                                     | `string`            | `""`    |
| `db.resources`                | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`            | `{}`    |
| `db.resources.cpu`            | Number of CPU cores allocated.                                                                           | `quantity`          | `""`    |
| `db.resources.memory`         | Amount of memory allocated.                                                                              | `quantity`          | `""`    |
| `db.resourcesPreset`          | Default sizing preset used when `resources` is omitted.                                                  | `string`            | `small` |
| `master`                      | Master service configuration.                                                                            | `object`            | `{}`    |
| `master.replicas`             | Number of master replicas.                                                                               | `int`               | `3`     |
| `master.resources`            | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`            | `{}`    |
| `master.resources.cpu`        | Number of CPU cores allocated.                                                                           | `quantity`          | `""`    |
| `master.resources.memory`     | Amount of memory allocated.                                                                              | `quantity`          | `""`    |
| `master.resourcesPreset`      | Default sizing preset used when `resources` is omitted.                                                  | `string`            | `small` |
| `filer`                       | Filer service configuration.                                                                             | `object`            | `{}`    |
| `filer.replicas`              | Number of filer replicas.                                                                                | `int`               | `2`     |
| `filer.resources`             | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`            | `{}`    |
| `filer.resources.cpu`         | Number of CPU cores allocated.                                                                           | `quantity`          | `""`    |
| `filer.resources.memory`      | Amount of memory allocated.                                                                              | `quantity`          | `""`    |
| `filer.resourcesPreset`       | Default sizing preset used when `resources` is omitted.                                                  | `string`            | `small` |
| `filer.grpcHost`              | The hostname used to expose or access the filer service externally.                                      | `string`            | `""`    |
| `filer.grpcPort`              | The port used to access the filer service externally.                                                    | `int`               | `443`   |
| `filer.whitelist`             | A list of IP addresses or CIDR ranges that are allowed to access the filer service.                      | `[]string`          | `[]`    |
| `volume`                      | Volume service configuration.                                                                            | `object`            | `{}`    |
| `volume.replicas`             | Number of volume replicas.                                                                               | `int`               | `2`     |
| `volume.size`                 | Persistent Volume size.                                                                                  | `quantity`          | `10Gi`  |
| `volume.storageClass`         | StorageClass used to store the data.                                                                     | `string`            | `""`    |
| `volume.resources`            | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`            | `{}`    |
| `volume.resources.cpu`        | Number of CPU cores allocated.                                                                           | `quantity`          | `""`    |
| `volume.resources.memory`     | Amount of memory allocated.                                                                              | `quantity`          | `""`    |
| `volume.resourcesPreset`      | Default sizing preset used when `resources` is omitted.                                                  | `string`            | `small` |
| `volume.zones`                | A map of zones for MultiZone topology. Each zone can have its own number of replicas and size.           | `map[string]object` | `{}`    |
| `volume.zones[name].replicas` | Number of replicas in the zone.                                                                          | `int`               | `0`     |
| `volume.zones[name].size`     | Zone storage size.                                                                                       | `quantity`          | `""`    |
| `s3`                          | S3 service configuration.                                                                                | `object`            | `{}`    |
| `s3.replicas`                 | Number of S3 replicas.                                                                                   | `int`               | `2`     |
| `s3.resources`                | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`            | `{}`    |
| `s3.resources.cpu`            | Number of CPU cores allocated.                                                                           | `quantity`          | `""`    |
| `s3.resources.memory`         | Amount of memory allocated.                                                                              | `quantity`          | `""`    |
| `s3.resourcesPreset`          | Default sizing preset used when `resources` is omitted.                                                  | `string`            | `small` |

