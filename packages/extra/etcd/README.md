# Etcd-cluster

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

