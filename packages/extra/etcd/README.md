# Etcd-cluster

## Backups

When `backup.enabled` is set to `true`, the chart renders an `EtcdBackupSchedule` (etcd.aenix.io/v1alpha1) and an S3 credentials `Secret`. The etcd-operator v0.4.3+ reconciles the schedule into a `CronJob` that periodically snapshots the cluster to S3.

Enabling backup requires explicit values for `backup.s3AccessKey`, `backup.s3SecretKey`, `backup.destinationPath` (must start with `s3://` and have no `//` segments), and `backup.endpointURL`. The chart intentionally `fail`s at template time if any of these are left at their placeholder defaults — this avoids silently shipping a Secret containing the literal string `<your-access-key>` to the backup job, which would later fail with an opaque S3 403.

**Restore** (`EtcdCluster.spec.bootstrap`) is not yet exposed through this chart — restoring from a snapshot currently requires hand-applying an `EtcdCluster` with the bootstrap block.

## Parameters

### Common parameters

| Name               | Description                          | Type       | Value   |
| ------------------ | ------------------------------------ | ---------- | ------- |
| `size`             | Persistent Volume size.              | `quantity` | `4Gi`   |
| `storageClass`     | StorageClass used to store the data. | `string`   | `""`    |
| `replicas`         | Number of etcd replicas.             | `int`      | `3`     |
| `resources`        | Resource configuration for etcd.     | `object`   | `{}`    |
| `resources.cpu`    | Number of CPU cores allocated.       | `quantity` | `1000m` |
| `resources.memory` | Amount of memory allocated.          | `quantity` | `512Mi` |


### Backup parameters

| Name                                | Description                                                                   | Type     | Value                               |
| ----------------------------------- | ----------------------------------------------------------------------------- | -------- | ----------------------------------- |
| `backup`                            | Backup configuration.                                                         | `object` | `{}`                                |
| `backup.enabled`                    | Enable scheduled S3 backups.                                                  | `bool`   | `false`                             |
| `backup.schedule`                   | Cron schedule for automated backups.                                          | `string` | `0 2 * * *`                         |
| `backup.destinationPath`            | Destination path for backups (e.g. s3://bucket/path/).                        | `string` | `s3://bucket/path/to/folder/`       |
| `backup.endpointURL`                | S3 endpoint URL for uploads.                                                  | `string` | `http://minio-gateway-service:9000` |
| `backup.region`                     | S3 region.                                                                    | `string` | `""`                                |
| `backup.forcePathStyle`             | Use path-style S3 URLs (required for MinIO and most S3-compatible providers). | `bool`   | `true`                              |
| `backup.s3AccessKey`                | Access key for S3 authentication.                                             | `string` | `<your-access-key>`                 |
| `backup.s3SecretKey`                | Secret key for S3 authentication.                                             | `string` | `<your-secret-key>`                 |
| `backup.successfulJobsHistoryLimit` | Number of successful backup jobs to retain.                                   | `int`    | `3`                                 |
| `backup.failedJobsHistoryLimit`     | Number of failed backup jobs to retain.                                       | `int`    | `1`                                 |

