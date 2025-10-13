# Managed NATS Service

## Parameters

### Common parameters

| Name                | Description                                                                                            | Type      | Value    |
| ------------------- | ------------------------------------------------------------------------------------------------------ | --------- | -------- |
| `host`              | The hostname used to access the SeaweedFS externally (defaults to 's3' subdomain for the tenant host). | `*string` | `""`     |
| `topology`          | The topology of the SeaweedFS cluster. (allowed values: Simple, MultiZone, Client)                     | `string`  | `Simple` |
| `replicationFactor` | Replication factor: number of replicas for each volume in the SeaweedFS cluster.                       | `int`     | `2`      |


### SeaweedFS Components Configuration

| Name                          | Description                                                                                                                               | Type                | Value   |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `db`                          | Database Configuration                                                                                                                    | `object`            | `{}`    |
| `db.replicas`                 | Number of database replicas                                                                                                               | `*int`              | `2`     |
| `db.size`                     | Persistent Volume size                                                                                                                    | `*quantity`         | `10Gi`  |
| `db.storageClass`             | StorageClass used to store the data                                                                                                       | `*string`           | `""`    |
| `db.resources`                | Explicit CPU and memory configuration for the database. When left empty, the preset defined in `resourcesPreset` is applied.              | `object`            | `{}`    |
| `db.resources.cpu`            | The number of CPU cores allocated                                                                                                         | `*quantity`         | `null`  |
| `db.resources.memory`         | The amount of memory allocated                                                                                                            | `*quantity`         | `null`  |
| `db.resourcesPreset`          | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`            | `small` |
| `master`                      | Master service configuration                                                                                                              | `*object`           | `null`  |
| `master.replicas`             | Number of master replicas                                                                                                                 | `*int`              | `3`     |
| `master.resources`            | Explicit CPU and memory configuration for the master. When left empty, the preset defined in `resourcesPreset` is applied.                | `object`            | `{}`    |
| `master.resources.cpu`        | The number of CPU cores allocated                                                                                                         | `*quantity`         | `null`  |
| `master.resources.memory`     | The amount of memory allocated                                                                                                            | `*quantity`         | `null`  |
| `master.resourcesPreset`      | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`            | `small` |
| `filer`                       | Filer service configuration                                                                                                               | `*object`           | `null`  |
| `filer.replicas`              | Number of filer replicas                                                                                                                  | `*int`              | `2`     |
| `filer.resources`             | Explicit CPU and memory configuration for the filer. When left empty, the preset defined in `resourcesPreset` is applied.                 | `object`            | `{}`    |
| `filer.resources.cpu`         | The number of CPU cores allocated                                                                                                         | `*quantity`         | `null`  |
| `filer.resources.memory`      | The amount of memory allocated                                                                                                            | `*quantity`         | `null`  |
| `filer.resourcesPreset`       | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`            | `small` |
| `filer.grpcHost`              | The hostname used to expose or access the filer service externally.                                                                       | `*string`           | `""`    |
| `filer.grpcPort`              | The port used to access the filer service externally.                                                                                     | `*int`              | `443`   |
| `filer.whitelist`             | A list of IP addresses or CIDR ranges that are allowed to access the filer service.                                                       | `[]*string`         | `[]`    |
| `volume`                      | Volume service configuration                                                                                                              | `*object`           | `null`  |
| `volume.replicas`             | Number of volume replicas                                                                                                                 | `*int`              | `2`     |
| `volume.size`                 | Persistent Volume size                                                                                                                    | `*quantity`         | `10Gi`  |
| `volume.storageClass`         | StorageClass used to store the data                                                                                                       | `*string`           | `""`    |
| `volume.resources`            | Explicit CPU and memory configuration for the volume. When left empty, the preset defined in `resourcesPreset` is applied.                | `object`            | `{}`    |
| `volume.resources.cpu`        | The number of CPU cores allocated                                                                                                         | `*quantity`         | `null`  |
| `volume.resources.memory`     | The amount of memory allocated                                                                                                            | `*quantity`         | `null`  |
| `volume.resourcesPreset`      | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`            | `small` |
| `volume.zones`                | A map of zones for MultiZone topology. Each zone can have its own number of replicas and size.                                            | `map[string]object` | `{}`    |
| `volume.zones[name].replicas` | Number of replicas in the zone                                                                                                            | `*int`              | `null`  |
| `volume.zones[name].size`     | Zone storage size                                                                                                                         | `*quantity`         | `null`  |
| `s3`                          | S3 service configuration                                                                                                                  | `*object`           | `null`  |
| `s3.replicas`                 | Number of s3 replicas                                                                                                                     | `*int`              | `2`     |
| `s3.resources`                | Explicit CPU and memory configuration for the s3. When left empty, the preset defined in `resourcesPreset` is applied.                    | `object`            | `{}`    |
| `s3.resources.cpu`            | The number of CPU cores allocated                                                                                                         | `*quantity`         | `null`  |
| `s3.resources.memory`         | The amount of memory allocated                                                                                                            | `*quantity`         | `null`  |
| `s3.resourcesPreset`          | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`            | `small` |

