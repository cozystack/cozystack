# Managed ClickHouse Service

ClickHouse is an open source high-performance and column-oriented SQL database management system (DBMS).
It is used for online analytical processing (OLAP).

### How to restore backup from S3

1.  Find the snapshot:

    ```bash
    restic -r s3:s3.example.org/clickhouse-backups/table_name snapshots
    ```

2.  Restore it:

    ```bash
    restic -r s3:s3.example.org/clickhouse-backups/table_name restore latest --target /tmp/
    ```

For more details, read [Restic: Effective Backup from Stdin](https://blog.aenix.io/restic-effective-backup-from-stdin-4bc1e8f083c1).

## Parameters

### Common parameters

| Name               | Description                                                                                                                               | Type        | Value   |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `replicas`         | Number of Clickhouse replicas                                                                                                             | `int`       | `2`     |
| `shards`           | Number of Clickhouse shards                                                                                                               | `int`       | `1`     |
| `resources`        | Explicit CPU and memory configuration for each Clickhouse replica. When left empty, the preset defined in `resourcesPreset` is applied.   | `*object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica                                                                                                             | `*quantity` | `null`  |
| `resources.memory` | Memory (RAM) available to each replica                                                                                                    | `*quantity` | `null`  |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `small` |
| `size`             | Persistent Volume Claim size, available for application data                                                                              | `quantity`  | `10Gi`  |
| `storageClass`     | StorageClass used to store the data                                                                                                       | `string`    | `""`    |


### Application-specific parameters

| Name                   | Description                                                  | Type                | Value   |
| ---------------------- | ------------------------------------------------------------ | ------------------- | ------- |
| `logStorageSize`       | Size of Persistent Volume for logs                           | `quantity`          | `2Gi`   |
| `logTTL`               | TTL (expiration time) for `query_log` and `query_thread_log` | `int`               | `15`    |
| `users`                | Users configuration                                          | `map[string]object` | `{...}` |
| `users[name].password` | Password for the user                                        | `*string`           | `null`  |
| `users[name].readonly` | User is `readonly`, default is `false`.                      | `*bool`             | `null`  |


### Backup parameters

| Name                     | Description                                    | Type     | Value                                                  |
| ------------------------ | ---------------------------------------------- | -------- | ------------------------------------------------------ |
| `backup`                 | Backup configuration                           | `object` | `{}`                                                   |
| `backup.enabled`         | Enable regular backups, default is `false`     | `bool`   | `false`                                                |
| `backup.s3Region`        | AWS S3 region where backups are stored         | `string` | `us-east-1`                                            |
| `backup.s3Bucket`        | S3 bucket used for storing backups             | `string` | `s3.example.org/clickhouse-backups`                    |
| `backup.schedule`        | Cron schedule for automated backups            | `string` | `0 2 * * *`                                            |
| `backup.cleanupStrategy` | Retention strategy for cleaning up old backups | `string` | `--keep-last=3 --keep-daily=3 --keep-within-weekly=1m` |
| `backup.s3AccessKey`     | Access key for S3, used for authentication     | `string` | `<your-access-key>`                                    |
| `backup.s3SecretKey`     | Secret key for S3, used for authentication     | `string` | `<your-secret-key>`                                    |
| `backup.resticPassword`  | Password for Restic backup encryption          | `string` | `<password>`                                           |


### Clickhouse Keeper parameters

| Name                               | Description                                                                                                                               | Type        | Value   |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `clickhouseKeeper`                 | Clickhouse Keeper configuration                                                                                                           | `*object`   | `{}`    |
| `clickhouseKeeper.enabled`         | Deploy ClickHouse Keeper for cluster coordination                                                                                         | `*bool`     | `true`  |
| `clickhouseKeeper.size`            | Persistent Volume Claim size, available for application data                                                                              | `*quantity` | `1Gi`   |
| `clickhouseKeeper.resourcesPreset` | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `micro` |
| `clickhouseKeeper.replicas`        | Number of Keeper replicas                                                                                                                 | `*int`      | `3`     |


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
