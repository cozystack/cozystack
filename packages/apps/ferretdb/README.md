# Managed FerretDB Service

FerretDB is an open source MongoDB alternative.
It translates MongoDB wire protocol queries to SQL and can be used as a direct replacement for MongoDB 5.0+.
Internally, FerretDB service is backed by Postgres.

## Parameters

### Common parameters

| Name               | Description                                                                                                                               | Type        | Value   |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `replicas`         | Number of replicas                                                                                                                        | `int`       | `2`     |
| `resources`        | Explicit CPU and memory configuration for each FerretDB replica. When left empty, the preset defined in `resourcesPreset` is applied.     | `*object`   | `{}`    |
| `resources.cpu`    | CPU                                                                                                                                       | `*quantity` | `null`  |
| `resources.memory` | Memory                                                                                                                                    | `*quantity` | `null`  |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `micro` |
| `size`             | Persistent Volume Claim size, available for application data                                                                              | `quantity`  | `10Gi`  |
| `storageClass`     | StorageClass used to store the data                                                                                                       | `string`    | `""`    |
| `external`         | Enable external access from outside the cluster                                                                                           | `bool`      | `false` |


### Application-specific parameters

| Name                     | Description                                                                                                                 | Type                | Value   |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `quorum`                 | Configuration for the quorum-based synchronous replication                                                                  | `object`            | `{}`    |
| `quorum.minSyncReplicas` | Minimum number of synchronous replicas that must acknowledge a transaction before it is considered committed                | `int`               | `0`     |
| `quorum.maxSyncReplicas` | Maximum number of synchronous replicas that can acknowledge a transaction (must be lower than the total number of replicas) | `int`               | `0`     |
| `users`                  | Users configuration                                                                                                         | `map[string]object` | `{...}` |
| `users[name].password`   | Password for the user                                                                                                       | `*string`           | `null`  |


### Backup parameters

| Name                     | Description                                                | Type      | Value                               |
| ------------------------ | ---------------------------------------------------------- | --------- | ----------------------------------- |
| `backup`                 | Backup configuration                                       | `object`  | `{}`                                |
| `backup.enabled`         | Enable regular backups, default is `false`.                | `*bool`   | `false`                             |
| `backup.schedule`        | Cron schedule for automated backups                        | `*string` | `0 2 * * * *`                       |
| `backup.retentionPolicy` | Retention policy                                           | `*string` | `30d`                               |
| `backup.endpointURL`     | S3 Endpoint used to upload data to the cloud               | `*string` | `http://minio-gateway-service:9000` |
| `backup.destinationPath` | Path to store the backup (i.e. s3://bucket/path/to/folder) | `*string` | `s3://bucket/path/to/folder/`       |
| `backup.s3AccessKey`     | Access key for S3, used for authentication                 | `*string` | `oobaiRus9pah8PhohL1ThaeTa4UVa7gu`  |
| `backup.s3SecretKey`     | Secret key for S3, used for authentication                 | `*string` | `ju3eum4dekeich9ahM1te8waeGai0oog`  |


### Bootstrap (recovery) parameters

| Name                     | Description                                                                                                           | Type      | Value   |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------- | --------- | ------- |
| `bootstrap`              | Bootstrap (recovery) configuration                                                                                    | `object`  | `{}`    |
| `bootstrap.enabled`      | Restore database cluster from a backup                                                                                | `*bool`   | `false` |
| `bootstrap.recoveryTime` | Timestamp (PITR) up to which recovery will proceed, expressed in RFC 3339 format. If left empty, will restore latest. | `*string` | `""`    |
| `bootstrap.oldName`      | Name of database cluster before deleting                                                                              | `*string` | `""`    |


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
