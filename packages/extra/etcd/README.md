# Etcd-cluster

## Backups

When `backup.enabled` is set to `true`, the chart renders an `EtcdBackupSchedule` (etcd.aenix.io/v1alpha1) and an S3 credentials `Secret`. The etcd-operator v0.4.3+ reconciles the schedule into a `CronJob` that periodically snapshots the cluster to S3.

Enabling backup requires the following fields to be explicitly set (defaults are empty strings so that missing values fail fast at template render time): `backup.s3AccessKey`, `backup.s3SecretKey`, `backup.destinationPath` (must start with `s3://` and have no `//` segments), and `backup.endpointURL`. S3 credentials passed through plain values end up in the HelmRelease manifest — for production deployments prefer an external secret management tool (ESO, Sealed Secrets, etc.) over committing the keys to Git.

**Restore** (`EtcdCluster.spec.bootstrap`) and the one-shot `EtcdBackup` CRD shipped upstream in v0.4.3 are not yet exposed through this chart. Restoring from a snapshot or taking an ad-hoc backup currently requires hand-applying the corresponding CRD.

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

| Name                                | Description                                                                   | Type     | Value       |
| ----------------------------------- | ----------------------------------------------------------------------------- | -------- | ----------- |
| `backup`                            | Backup configuration.                                                         | `object` | `{}`        |
| `backup.enabled`                    | Enable scheduled S3 backups.                                                  | `bool`   | `false`     |
| `backup.schedule`                   | Cron schedule for automated backups.                                          | `string` | `0 2 * * *` |
| `backup.destinationPath`            | Destination path for backups (e.g. s3://bucket/path/).                        | `string` | `""`        |
| `backup.endpointURL`                | S3 endpoint URL for uploads.                                                  | `string` | `""`        |
| `backup.region`                     | S3 region.                                                                    | `string` | `""`        |
| `backup.forcePathStyle`             | Use path-style S3 URLs (required for MinIO and most S3-compatible providers). | `bool`   | `true`      |
| `backup.s3AccessKey`                | Access key for S3 authentication.                                             | `string` | `""`        |
| `backup.s3SecretKey`                | Secret key for S3 authentication.                                             | `string` | `""`        |
| `backup.successfulJobsHistoryLimit` | Number of successful backup jobs to retain.                                   | `int`    | `3`         |
| `backup.failedJobsHistoryLimit`     | Number of failed backup jobs to retain.                                       | `int`    | `1`         |

