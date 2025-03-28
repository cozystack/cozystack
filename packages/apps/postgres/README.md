# Managed PostgreSQL Service

PostgreSQL is currently the leading choice among relational databases, known for its robust features and performance. Our Managed PostgreSQL Service takes advantage of platform-side implementation to provide a self-healing replicated cluster. This cluster is efficiently managed using the highly acclaimed CloudNativePG operator, which has gained popularity within the community.

## Deployment Details

This managed service is controlled by the CloudNativePG operator, ensuring efficient management and seamless operation.

- Docs: <https://cloudnative-pg.io/docs/>
- Github: <https://github.com/cloudnative-pg/cloudnative-pg>

## HowTos

### How to switch master/slave replica

See:

- <https://cloudnative-pg.io/documentation/1.15/rolling_update/#manual-updates-supervised>

### How to restore backup

find snapshot:

```bash
restic -r s3:s3.example.org/postgres-backups/database_name snapshots
```

restore:

```bash
restic -r s3:s3.example.org/postgres-backups/database_name restore latest --target /tmp/
```

more details:

- <https://itnext.io/restic-effective-backup-from-stdin-4bc1e8f083c1>

## Parameters

### Common parameters

| Name                                    | Description                                                                                                              | Value   |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ | ------- |
| `external`                              | Enable external access from outside the cluster                                                                          | `false` |
| `size`                                  | Persistent Volume size                                                                                                   | `10Gi`  |
| `replicas`                              | Number of Postgres replicas                                                                                              | `2`     |
| `storageClass`                          | StorageClass used to store the data                                                                                      | `""`    |
| `postgresql.parameters.max_connections` | Determines the maximum number of concurrent connections to the database server. The default is typically 100 connections | `100`   |
| `quorum.minSyncReplicas`                | Minimum number of synchronous replicas that must acknowledge a transaction before it is considered committed.            | `0`     |
| `quorum.maxSyncReplicas`                | Maximum number of synchronous replicas that can acknowledge a transaction (must be lower than the number of instances).  | `0`     |

### Configuration parameters

| Name        | Description             | Value |
| ----------- | ----------------------- | ----- |
| `users`     | Users configuration     | `{}`  |
| `databases` | Databases configuration | `{}`  |

### Backup parameters

| Name                     | Description                                                                                                                                                                                                       | Value                                                  |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `backup.enabled`         | Enable pereiodic backups                                                                                                                                                                                          | `false`                                                |
| `backup.s3Region`        | The AWS S3 region where backups are stored                                                                                                                                                                        | `us-east-1`                                            |
| `backup.s3Bucket`        | The S3 bucket used for storing backups                                                                                                                                                                            | `s3.example.org/postgres-backups`                      |
| `backup.schedule`        | Cron schedule for automated backups                                                                                                                                                                               | `0 2 * * *`                                            |
| `backup.cleanupStrategy` | The strategy for cleaning up old backups                                                                                                                                                                          | `--keep-last=3 --keep-daily=3 --keep-within-weekly=1m` |
| `backup.s3AccessKey`     | The access key for S3, used for authentication                                                                                                                                                                    | `oobaiRus9pah8PhohL1ThaeTa4UVa7gu`                     |
| `backup.s3SecretKey`     | The secret key for S3, used for authentication                                                                                                                                                                    | `ju3eum4dekeich9ahM1te8waeGai0oog`                     |
| `backup.resticPassword`  | The password for Restic backup encryption                                                                                                                                                                         | `ChaXoveekoh6eigh4siesheeda2quai0`                     |
| `resources`              | Resources                                                                                                                                                                                                         | `{}`                                                   |
| `resourcesPreset`        | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano`                                                 |
