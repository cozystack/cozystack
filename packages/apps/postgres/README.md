# Managed PostgreSQL Service

PostgreSQL is currently the leading choice among relational databases, known for its robust features and performance.
The Managed PostgreSQL Service takes advantage of platform-side implementation to provide a self-healing replicated cluster.
This cluster is efficiently managed using the highly acclaimed CloudNativePG operator, which has gained popularity within the community.

## Deployment Details

This managed service is controlled by the CloudNativePG operator, ensuring efficient management and seamless operation.

- Docs: <https://cloudnative-pg.io/docs/>
- Github: <https://github.com/cloudnative-pg/cloudnative-pg>

## Operations

PostgreSQL backups can be configured in two ways. **Pick one - mixing them
on the same application produces a continuous SSA tug-of-war on the
underlying cnpg.io Cluster.spec.backup field, since the chart and the CNPG
backup driver both write to it.**

| Path                                       | When to use                                                                                                             | Configured via                                |
| ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| **BackupClass / Plan / BackupJob (preferred)** | Tenant-managed backup schedules, ad-hoc backups, restores, cross-app copy restores                                      | `BackupClass` + `Plan` (or `BackupJob` ad-hoc) |
| Chart-managed (legacy)                     | Single application, no tenant-level backup orchestration, credentials acceptable in chart values                        | `backup.enabled=true` plus `backup.*` knobs   |

If a `BackupClass` referencing the CNPG strategy applies to this Postgres
app, **leave `backup.enabled=false`** in the chart values. The CNPG strategy
controller writes `spec.backup.barmanObjectStore` on the cnpg.io Cluster
itself; setting `backup.enabled=true` would make the chart emit the same
field and the two writers would fight on every reconcile.

### How to enable backups (preferred: BackupClass + Plan)

End-to-end manifests live under [`examples/backups/postgres/`](../../../examples/backups/postgres/).
Briefly, the moving parts are:

1. A `strategy.backups.cozystack.io/CNPG` describing the destination bucket
   and templating the `barmanObjectStore` (including a Secret reference to
   S3 credentials - the credentials never appear on the Postgres CR
   `.spec`; see Security note below).
2. A `backups.cozystack.io/BackupClass` that names the strategy and is
   selected by an `applicationRef` matching the Postgres app's `Kind`/`Name`.
3. A `backups.cozystack.io/Plan` (recurring) or `BackupJob` (ad-hoc) that
   references the BackupClass. The controller materialises a
   `Backup` artifact when the cnpg.io Backup completes; restores then
   reference that Backup via `RestoreJob`.

Both in-place restores (overwrite the source app's data) and to-copy
restores (restore into a separate target Postgres app in the same
namespace) are supported via the `RestoreJob.spec.targetApplicationRef`
field.

> **Security:** With the BackupClass path, S3 credentials live in a
> tenant-readable Secret referenced from the strategy template. The CNPG
> driver forwards that Secret reference into the Postgres app's
> `spec.backup.s3CredentialsSecret` on restore, so access keys never land in
> the Postgres CR `.spec`, etcd object store, or `kubectl get -o yaml`
> output. Prefer this over the chart-managed path whenever possible.

### How to enable chart-managed backups (legacy)

To back up a PostgreSQL application, an external S3-compatible storage is required.

To start regular backups, update the application, setting `backup.enabled` to `true`, and fill in the path and credentials to an  `backup.*`:

```yaml
## @param backup.enabled Enable regular backups
## @param backup.schedule Cron schedule for automated backups
## @param backup.retentionPolicy Retention policy
## @param backup.destinationPath Path to store the backup (i.e. s3://bucket/path/to/folder)
## @param backup.endpointURL S3 Endpoint used to upload data to the cloud
## @param backup.s3AccessKey Access key for S3, used for authentication
## @param backup.s3SecretKey Secret key for S3, used for authentication
backup:
  enabled: false
  retentionPolicy: 30d
  destinationPath: s3://bucket/path/to/folder/
  endpointURL: http://minio-gateway-service:9000
  schedule: "0 2 * * * *"
  s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
  s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
```

### How to recover a backup (preferred: RestoreJob)

For BackupClass-managed backups, create a `backups.cozystack.io/RestoreJob`
that references the desired `Backup`. See
[`examples/backups/postgres/35-restorejob-in-place.yaml`](../../../examples/backups/postgres/35-restorejob-in-place.yaml)
and
[`examples/backups/postgres/40-restorejob-to-copy.yaml`](../../../examples/backups/postgres/40-restorejob-to-copy.yaml).
On a to-copy restore, the controller mirrors the source app's
`spec.databases` and `spec.users` onto the target so the post-install
init-job does not drop the recovered data; target-only databases/users that
predate the restore are preserved.

### How to recover a backup (chart-managed bootstrap)

CloudNativePG supports point-in-time-recovery.
Recovering a backup is done by creating a new database instance and restoring the data in it.

Create a new PostgreSQL application with a different name, but identical configuration.
Set `bootstrap.enabled` to `true` and fill in the name of the database instance to recover from and the recovery time:

```yaml
## @param bootstrap.enabled Restore database cluster from a backup
## @param bootstrap.recoveryTime Timestamp (PITR) up to which recovery will proceed, expressed in RFC 3339 format. If left empty, will restore latest
## @param bootstrap.oldName Name of database cluster before deleting
##
bootstrap:
  enabled: false
  recoveryTime: ""  # leave empty for latest or exact timestamp; example: 2020-11-26 15:22:00.00000+00
  oldName: "<previous-postgres-instance>"
```

### How to switch primary/secondary replica

See:

- <https://cloudnative-pg.io/documentation/1.15/rolling_update/#manual-updates-supervised>

## Parameters

### Common parameters

| Name               | Description                                                                                                                          | Type       | Value   |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ------- |
| `replicas`         | Number of Postgres replicas.                                                                                                         | `int`      | `2`     |
| `resources`        | Explicit CPU and memory configuration for each PostgreSQL replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica.                                                                                                       | `quantity` | `""`    |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                              | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                              | `string`   | `micro` |
| `size`             | Persistent Volume Claim size available for application data.                                                                         | `quantity` | `10Gi`  |
| `storageClass`     | StorageClass used to store the data.                                                                                                 | `string`   | `""`    |
| `external`         | Enable external access from outside the cluster.                                                                                     | `bool`     | `false` |
| `version`          | PostgreSQL major version to deploy                                                                                                   | `string`   | `v18`   |


### Application-specific parameters

| Name                    | Description                                                                                                                                                                                                                                                                                                                                                                                                   | Type                | Value |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ----- |
| `postgresql`            | PostgreSQL server configuration.                                                                                                                                                                                                                                                                                                                                                                              | `object`            | `{}`  |
| `postgresql.parameters` | PostgreSQL server parameters. All values must be strings (quote numbers: "100"). BLOCKED (enable arbitrary code execution): archive_command, restore_command, ssl_passphrase_command, dynamic_library_path, local_preload_libraries, session_preload_libraries, shared_preload_libraries. Do NOT override CloudNativePG-managed parameters: archive_mode, primary_conninfo, wal_level, max_replication_slots. | `map[string]string` | `{}`  |


### Quorum-based synchronous replication

| Name                     | Description                                                                        | Type     | Value |
| ------------------------ | ---------------------------------------------------------------------------------- | -------- | ----- |
| `quorum`                 | Quorum configuration for synchronous replication.                                  | `object` | `{}`  |
| `quorum.minSyncReplicas` | Minimum number of synchronous replicas required for commit.                        | `int`    | `0`   |
| `quorum.maxSyncReplicas` | Maximum number of synchronous replicas allowed (must be less than total replicas). | `int`    | `0`   |


### Users configuration

| Name                      | Description                                  | Type                | Value   |
| ------------------------- | -------------------------------------------- | ------------------- | ------- |
| `users`                   | Users configuration map.                     | `map[string]object` | `{}`    |
| `users[name].password`    | Password for the user.                       | `string`            | `""`    |
| `users[name].replication` | Whether the user has replication privileges. | `bool`              | `false` |


### Databases configuration

| Name                             | Description                              | Type                | Value |
| -------------------------------- | ---------------------------------------- | ------------------- | ----- |
| `databases`                      | Databases configuration map.             | `map[string]object` | `{}`  |
| `databases[name].roles`          | Roles assigned to users.                 | `object`            | `{}`  |
| `databases[name].roles.admin`    | List of users with admin privileges.     | `[]string`          | `[]`  |
| `databases[name].roles.readonly` | List of users with read-only privileges. | `[]string`          | `[]`  |
| `databases[name].extensions`     | List of enabled PostgreSQL extensions.   | `[]string`          | `[]`  |


### Backup parameters

| Name                                            | Description                                                                                                                                                                                                                                                  | Type     | Value                               |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------- | ----------------------------------- |
| `backup`                                        | Backup configuration.                                                                                                                                                                                                                                        | `object` | `{}`                                |
| `backup.enabled`                                | Enable regular backups.                                                                                                                                                                                                                                      | `bool`   | `false`                             |
| `backup.schedule`                               | Cron schedule for automated backups.                                                                                                                                                                                                                         | `string` | `0 2 * * * *`                       |
| `backup.retentionPolicy`                        | Retention policy (e.g. "30d").                                                                                                                                                                                                                               | `string` | `30d`                               |
| `backup.destinationPath`                        | Destination path for backups (e.g. s3://bucket/path/).                                                                                                                                                                                                       | `string` | `s3://bucket/path/to/folder/`       |
| `backup.endpointURL`                            | S3 endpoint URL for uploads.                                                                                                                                                                                                                                 | `string` | `http://minio-gateway-service:9000` |
| `backup.s3AccessKey`                            | Access key for S3 authentication. Ignored when `s3CredentialsSecret.name` is set.                                                                                                                                                                            | `string` | `<your-access-key>`                 |
| `backup.s3SecretKey`                            | Secret key for S3 authentication. Ignored when `s3CredentialsSecret.name` is set.                                                                                                                                                                            | `string` | `<your-secret-key>`                 |
| `backup.s3CredentialsSecret`                    | Pre-existing Secret with S3 credentials. When set, the chart references this Secret directly instead of materialising one from `s3AccessKey`/`s3SecretKey`. The CNPG backup driver writes this field on restore so credentials never land in the CR `.spec`. | `object` | `{}`                                |
| `backup.s3CredentialsSecret.name`               | Name of the Secret in the application namespace. Empty means the chart materialises `<release>-s3-creds` from `s3AccessKey`/`s3SecretKey`.                                                                                                                   | `string` | `""`                                |
| `backup.s3CredentialsSecret.accessKeyIDKey`     | Key in the Secret holding the access key ID. Defaults to `AWS_ACCESS_KEY_ID`.                                                                                                                                                                                | `string` | `""`                                |
| `backup.s3CredentialsSecret.secretAccessKeyKey` | Key in the Secret holding the secret access key. Defaults to `AWS_SECRET_ACCESS_KEY`.                                                                                                                                                                        | `string` | `""`                                |


### Bootstrap (recovery) parameters

| Name                     | Description                                                                                                                                                                                                                  | Type     | Value   |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `bootstrap`              | Bootstrap configuration.                                                                                                                                                                                                     | `object` | `{}`    |
| `bootstrap.enabled`      | Whether to restore from a backup.                                                                                                                                                                                            | `bool`   | `false` |
| `bootstrap.recoveryTime` | Timestamp (RFC3339) for point-in-time recovery; empty means latest.                                                                                                                                                          | `string` | `""`    |
| `bootstrap.oldName`      | Previous cluster name before deletion.                                                                                                                                                                                       | `string` | `""`    |
| `bootstrap.serverName`   | Barman server name (S3 path prefix) used by the original cluster when writing backups. Set this only when the original cluster had an explicit barmanObjectStore.serverName that differed from its Kubernetes resource name. | `string` | `""`    |


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

### users

```yaml
users:
  user1:
    password: strongpassword
  user2:
    password: hackme
  airflow:
    password: qwerty123
  debezium:
    replication: true
```

### databases

```yaml
databases:          
  myapp:            
    roles:          
      admin:        
      - user1       
      - debezium    
      readonly:     
      - user2       
  airflow:          
    roles:          
      admin:        
      - airflow     
    extensions:     
    - hstore        
```
