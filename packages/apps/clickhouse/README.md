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

| Name              | Description                                                                                                                             | Value   |
| ----------------- | --------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| `replicas`        | Number of Clickhouse replicas                                                                                                           | `2`     |
| `shards`          | Number of Clickhouse shards                                                                                                             | `1`     |
| `resources`       | Explicit CPU and memory configuration for each ClickHouse replica. When left empty, the preset defined in `resourcesPreset` is applied. | `{}`    |
| `resourcesPreset` | Default sizing preset used when `resources` is omitted. Allowed values: nano, micro, small, medium, large, xlarge, 2xlarge.             | `small` |
| `size`            | Persistent Volume Claim size, available for application data                                                                            | `10Gi`  |
| `storageClass`    | StorageClass used to store the application data                                                                                         | `""`    |

### Application-specific parameters

| Name             | Description                                              | Value |
| ---------------- | -------------------------------------------------------- | ----- |
| `logStorageSize` | Size of Persistent Volume for logs                       | `2Gi` |
| `logTTL`         | TTL (expiration time) for query_log and query_thread_log | `15`  |
| `users`          | Users configuration                                      | `{}`  |

### Backup parameters

| Name                     | Description                                    | Value                                                  |
| ------------------------ | ---------------------------------------------- | ------------------------------------------------------ |
| `backup.enabled`         | Enable periodic backups                        | `false`                                                |
| `backup.s3Region`        | AWS S3 region where backups are stored         | `us-east-1`                                            |
| `backup.s3Bucket`        | S3 bucket used for storing backups             | `s3.example.org/clickhouse-backups`                    |
| `backup.schedule`        | Cron schedule for automated backups            | `0 2 * * *`                                            |
| `backup.cleanupStrategy` | Retention strategy for cleaning up old backups | `--keep-last=3 --keep-daily=3 --keep-within-weekly=1m` |
| `backup.s3AccessKey`     | Access key for S3, used for authentication     | `oobaiRus9pah8PhohL1ThaeTa4UVa7gu`                     |
| `backup.s3SecretKey`     | Secret key for S3, used for authentication     | `ju3eum4dekeich9ahM1te8waeGai0oog`                     |
| `backup.resticPassword`  | Password for Restic backup encryption          | `ChaXoveekoh6eigh4siesheeda2quai0`                     |

### clickhouseKeeper parameters

| Name                               | Description                                                                                                                 | Value   |
| ---------------------------------- | --------------------------------------------------------------------------------------------------------------------------- | ------- |
| `clickhouseKeeper.enabled`         | Deploy ClickHouse Keeper for cluster coordination                                                                           | `true`  |
| `clickhouseKeeper.size`            | Persistent Volume Claim size, available for application data                                                                | `1Gi`   |
| `clickhouseKeeper.resourcesPreset` | Default sizing preset used when `resources` is omitted. Allowed values: nano, micro, small, medium, large, xlarge, 2xlarge. | `micro` |
| `clickhouseKeeper.replicas`        | Number of keeper replicas                                                                                                   | `3`     |

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
