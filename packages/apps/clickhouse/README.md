# Managed ClickHouse Service

ClickHouse is an open source high-performance and column-oriented SQL database management system (DBMS).
It is used for online analytical processing (OLAP).

## Backup orchestration

Two backup paths coexist on the chart; pick one per release.

### Recommended: BackupClass + Plan via the Altinity strategy

The cluster-scoped `Altinity` strategy
(`strategy.backups.cozystack.io/v1alpha1`) wraps
[Altinity's `clickhouse-backup`][altinity-clickhouse-backup] in a one-shot
`batch/v1.Job` per `BackupJob` / `RestoreJob`. It is engine-aware (FREEZE +
upload), supports both in-place and `targetApplicationRef` (to-copy) restore,
and does not require any in-chart `CronJob`.

Wiring per release:

1. Set `backup.enabled: true` plus the `s3*` (or `s3CredentialsSecret.name`)
   fields on the chart values. The chart materialises a Secret named
   `<release>-backup-s3` carrying bucket coordinates and credentials. That
   Secret is consumed by the chart-emitted `clickhouse-backup` sidecar
   (rendered into the ClickHouseInstallation Pod by
   `templates/clickhouse.yaml`); the strategy Pod itself is a curl/jq
   client that reaches the sidecar's HTTP API on port 7171 and never
   reads the Secret directly.
2. Cluster admin one-time installs an `Altinity` strategy and a `BackupClass`
   that maps `apps.cozystack.io/ClickHouse` to it (see
   `examples/backups/clickhouse/01-create-strategy.sh` and `02-create-backupclass.sh`).
3. Tenant creates a `Plan` (cron schedule) or submits an ad-hoc `BackupJob`
   referencing the BackupClass. Restoring is a `RestoreJob` referencing the
   resulting `Backup`; omit `targetApplicationRef` for in-place, set it to a
   second ClickHouse instance for to-copy.

#### Bringing your own S3 credentials Secret

Setting `backup.s3CredentialsSecret.name` makes the chart skip the
materialisation of `<release>-backup-s3` and points the sidecar at the
referenced Secret instead. The Secret must hold the five S3 fields as
**separate string keys**, not a JSON blob; the per-key field names default
to `bucketName` / `endpoint` / `region` / `accessKey` / `secretKey` and
can be remapped via `backup.s3CredentialsSecret.{bucketKey,endpointKey,…}`.

Example:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-s3-creds
  namespace: tenant-test
type: Opaque
stringData:
  bucketName: my-clickhouse-archive
  endpoint:   https://s3.example.org
  region:     us-east-1
  accessKey:  AKIAIOSFODNN7EXAMPLE
  secretKey:  wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
---
# In ClickHouse values.yaml:
backup:
  enabled: true
  s3CredentialsSecret:
    name: my-s3-creds
```

> The Cozystack `Bucket` app's BucketInfo Secret (`bucket-<name>-<user>`,
> single `BucketInfo` key holding a JSON blob) is **not directly
> consumable** as `s3CredentialsSecret`. Either let the chart read the raw
> values via `backup.s3*` (as
> `examples/backups/clickhouse/03-create-bucket.sh` does — extract
> coordinates from BucketInfo and pass them to the chart values), or
> materialise an intermediate Secret with the five string keys above.

### Legacy: chart-emitted Restic CronJob

The chart still ships a CronJob that streams per-table `SHOW CREATE TABLE`
+ `SELECT * FORMAT TabSeparated` into a Restic repository. This path is
kept for backward compatibility; new installations should prefer the
BackupClass flow above.

To opt in:

1. Set `backup.enabled: true`, `backup.schedule: "0 2 * * *"` (or another
   non-empty cron), `backup.s3*` and `backup.resticPassword`.
2. To restore manually:

   ```bash
   restic -r s3:s3.example.org/clickhouse-backups/table_name snapshots
   restic -r s3:s3.example.org/clickhouse-backups/table_name restore latest --target /tmp/
   ```

   For more details, read
   [Restic: Effective Backup from Stdin](https://blog.aenix.io/restic-effective-backup-from-stdin-4bc1e8f083c1).

The legacy `backup.schedule`, `backup.cleanupStrategy`, and
`backup.resticPassword` values are deprecated and will be removed once the
Altinity strategy is the default in all reference deployments. The
chart-emitted CronJob renders only when `backup.schedule` is non-empty.

[altinity-clickhouse-backup]: https://github.com/Altinity/clickhouse-backup

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                          | Type       | Value      |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ---------- |
| `replicas`         | Number of ClickHouse replicas.                                                                                                       | `int`      | `2`        |
| `shards`           | Number of ClickHouse shards.                                                                                                         | `int`      | `1`        |
| `resources`        | Explicit CPU and memory configuration for each ClickHouse replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `resources.cpu`    | CPU available to each replica.                                                                                                       | `quantity` | `""`       |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                              | `quantity` | `""`       |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                              | `string`   | `t1.small` |
| `size`             | Persistent Volume Claim size available for application data.                                                                         | `quantity` | `10Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                                 | `string`   | `""`       |


### Application-specific parameters

| Name                   | Description                                                   | Type                | Value   |
| ---------------------- | ------------------------------------------------------------- | ------------------- | ------- |
| `logStorageSize`       | Size of Persistent Volume for logs.                           | `quantity`          | `2Gi`   |
| `logTTL`               | TTL (expiration time) for `query_log` and `query_thread_log`. | `int`               | `15`    |
| `users`                | Users configuration map.                                      | `map[string]object` | `{}`    |
| `users[name].password` | Password for the user.                                        | `string`            | `""`    |
| `users[name].readonly` | User is readonly (default: false).                            | `bool`              | `false` |


### Backup parameters

| Name                                            | Description                                                                                                                                                                                                                                                                                                                                                                                                  | Type     | Value                                                  |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------- | ------------------------------------------------------ |
| `backup`                                        | Backup configuration.                                                                                                                                                                                                                                                                                                                                                                                        | `object` | `{}`                                                   |
| `backup.enabled`                                | Enable backup integration. Materialises the chart-emitted `<release>-backup-s3` Secret consumed by the Altinity backup strategy and, when `schedule` is non-empty, also renders the legacy chart-managed CronJob.                                                                                                                                                                                            | `bool`   | `false`                                                |
| `backup.useSystemBucket`                        | Opt-in: when true, the chart-emitted `<release>-backup-s3` Secret is skipped and the `clickhouse-backup` sidecar reads bucket coordinates + S3 credentials from the platform-projected `cozy-backups-creds` Secret. `S3_PATH` is set to `<namespace>/<release>` for cross-tenant isolation. Use together with the platform `cozy-default` BackupClass — tenants do not need to fill any `s3*` field below. | `bool`   | `false`                                                |
| `backup.s3Region`                               | DEPRECATED. Per-tenant S3 configuration is being phased out in favour of the platform-managed `cozy-default` BackupClass. Optional now so tenants on `useSystemBucket: true` can omit it.                                                                                                                                                                                                                    | `string` | `us-east-1`                                            |
| `backup.s3Bucket`                               | DEPRECATED. Optional; see `s3Region`.                                                                                                                                                                                                                                                                                                                                                                        | `string` | `s3.example.org/clickhouse-backups`                    |
| `backup.endpoint`                               | DEPRECATED. S3 endpoint URL for the legacy chart-emitted sidecar.                                                                                                                                                                                                                                                                                                                                            | `string` | `""`                                                   |
| `backup.s3PathOverride`                         | DEPRECATED. Object-key prefix inside the legacy `s3Bucket`; the new platform flow scopes by namespace automatically.                                                                                                                                                                                                                                                                                         | `string` | `""`                                                   |
| `backup.schedule`                               | Legacy. Cron schedule for the chart-emitted CronJob that runs the dump+restic backup. Empty (default) skips the legacy CronJob; recommended when a `BackupClass` + `Plan` from `backups.cozystack.io` already drives backup orchestration via the Altinity strategy.                                                                                                                                         | `string` | `""`                                                   |
| `backup.cleanupStrategy`                        | Legacy. Restic retention policy passed to the legacy CronJob (`restic forget …`). Unused by the Altinity strategy.                                                                                                                                                                                                                                                                                         | `string` | `--keep-last=3 --keep-daily=3 --keep-within-weekly=1m` |
| `backup.s3AccessKey`                            | DEPRECATED. Tenants no longer supply S3 keys; the platform-managed Bucket Secret is the source of truth. Optional now so `useSystemBucket: true` tenants can omit it.                                                                                                                                                                                                                                        | `string` | `<your-access-key>`                                    |
| `backup.s3SecretKey`                            | DEPRECATED. Optional; see `s3AccessKey`.                                                                                                                                                                                                                                                                                                                                                                     | `string` | `<your-secret-key>`                                    |
| `backup.resticPassword`                         | Legacy. Password for Restic backup encryption used by the legacy CronJob. Unused by the Altinity strategy.                                                                                                                                                                                                                                                                                                   | `string` | `<password>`                                           |
| `backup.s3CredentialsSecret`                    | DEPRECATED. Pre-existing Secret reference for the legacy chart-emitted sidecar. The platform flow projects `cozy-backups-creds` instead.                                                                                                                                                                                                                                                                     | `object` | `{}`                                                   |
| `backup.s3CredentialsSecret.name`               | Name of the Secret in the application namespace. Empty means the chart materialises `<release>-backup-s3` from the legacy `s3*` fields.                                                                                                                                                                                                                                                                      | `string` | `""`                                                   |
| `backup.s3CredentialsSecret.bucketKey`          | Key in the Secret holding the bucket name. Defaults to `bucketName`.                                                                                                                                                                                                                                                                                                                                         | `string` | `""`                                                   |
| `backup.s3CredentialsSecret.endpointKey`        | Key in the Secret holding the S3 endpoint URL. Defaults to `endpoint`.                                                                                                                                                                                                                                                                                                                                       | `string` | `""`                                                   |
| `backup.s3CredentialsSecret.regionKey`          | Key in the Secret holding the S3 region. Defaults to `region`.                                                                                                                                                                                                                                                                                                                                               | `string` | `""`                                                   |
| `backup.s3CredentialsSecret.accessKeyIDKey`     | Key in the Secret holding the access key ID. Defaults to `accessKey`.                                                                                                                                                                                                                                                                                                                                        | `string` | `""`                                                   |
| `backup.s3CredentialsSecret.secretAccessKeyKey` | Key in the Secret holding the secret access key. Defaults to `secretKey`.                                                                                                                                                                                                                                                                                                                                    | `string` | `""`                                                   |


### ClickHouse Keeper parameters

| Name                               | Description                                                  | Type       | Value      |
| ---------------------------------- | ------------------------------------------------------------ | ---------- | ---------- |
| `clickhouseKeeper`                 | ClickHouse Keeper configuration.                             | `object`   | `{}`       |
| `clickhouseKeeper.enabled`         | Deploy ClickHouse Keeper for cluster coordination.           | `bool`     | `true`     |
| `clickhouseKeeper.size`            | Persistent Volume Claim size available for application data. | `quantity` | `1Gi`      |
| `clickhouseKeeper.resourcesPreset` | Default sizing preset.                                       | `string`   | `t1.micro` |
| `clickhouseKeeper.replicas`        | Number of Keeper replicas.                                   | `int`      | `3`        |


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
