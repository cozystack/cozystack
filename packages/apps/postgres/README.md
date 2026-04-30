# Managed PostgreSQL Service

PostgreSQL is currently the leading choice among relational databases, known for its robust features and performance.
The Managed PostgreSQL Service takes advantage of platform-side implementation to provide a self-healing replicated cluster.
This cluster is efficiently managed using the highly acclaimed CloudNativePG operator, which has gained popularity within the community.

## Deployment Details

This managed service is controlled by the CloudNativePG operator, ensuring efficient management and seamless operation.

- Docs: <https://cloudnative-pg.io/docs/>
- Github: <https://github.com/cloudnative-pg/cloudnative-pg>

## Operations

### How to enable backups

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

### How to recover a backup

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

| Name               | Description                                                                                                                          | Type       | Value     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------ | ---------- | --------- |
| `replicas`         | Number of Postgres replicas.                                                                                                         | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each PostgreSQL replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                       | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                              | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                              | `string`   | `u1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                         | `quantity` | `10Gi`    |
| `storageClass`     | StorageClass used to store the data.                                                                                                 | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                     | `bool`     | `false`   |
| `version`          | PostgreSQL major version to deploy                                                                                                   | `string`   | `v18`     |


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

| Name                     | Description                                            | Type     | Value                               |
| ------------------------ | ------------------------------------------------------ | -------- | ----------------------------------- |
| `backup`                 | Backup configuration.                                  | `object` | `{}`                                |
| `backup.enabled`         | Enable regular backups.                                | `bool`   | `false`                             |
| `backup.schedule`        | Cron schedule for automated backups.                   | `string` | `0 2 * * * *`                       |
| `backup.retentionPolicy` | Retention policy (e.g. "30d").                         | `string` | `30d`                               |
| `backup.destinationPath` | Destination path for backups (e.g. s3://bucket/path/). | `string` | `s3://bucket/path/to/folder/`       |
| `backup.endpointURL`     | S3 endpoint URL for uploads.                           | `string` | `http://minio-gateway-service:9000` |
| `backup.s3AccessKey`     | Access key for S3 authentication.                      | `string` | `<your-access-key>`                 |
| `backup.s3SecretKey`     | Secret key for S3 authentication.                      | `string` | `<your-secret-key>`                 |


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

#### s1 (Standard) — 1:2 CPU:memory ratio

| Preset name  | CPU    | memory  |
|--------------|--------|---------|
| `s1.nano`    | `250m` | `512Mi` |
| `s1.micro`   | `500m` | `1Gi`   |
| `s1.small`   | `1`    | `2Gi`   |
| `s1.medium`  | `2`    | `4Gi`   |
| `s1.large`   | `4`    | `8Gi`   |
| `s1.xlarge`  | `8`    | `16Gi`  |
| `s1.2xlarge` | `16`   | `32Gi`  |
| `s1.4xlarge` | `32`   | `64Gi`  |

#### u1 (Universal) — 1:4 CPU:memory ratio

| Preset name  | CPU    | memory   |
|--------------|--------|----------|
| `u1.nano`    | `250m` | `1Gi`    |
| `u1.micro`   | `500m` | `2Gi`    |
| `u1.small`   | `1`    | `4Gi`    |
| `u1.medium`  | `2`    | `8Gi`    |
| `u1.large`   | `4`    | `16Gi`   |
| `u1.xlarge`  | `8`    | `32Gi`   |
| `u1.2xlarge` | `16`   | `64Gi`   |
| `u1.4xlarge` | `32`   | `128Gi`  |

#### m1 (Memory) — 1:8 CPU:memory ratio

| Preset name  | CPU    | memory   |
|--------------|--------|----------|
| `m1.nano`    | `250m` | `2Gi`    |
| `m1.micro`   | `500m` | `4Gi`    |
| `m1.small`   | `1`    | `8Gi`    |
| `m1.medium`  | `2`    | `16Gi`   |
| `m1.large`   | `4`    | `32Gi`   |
| `m1.xlarge`  | `8`    | `64Gi`   |
| `m1.2xlarge` | `16`   | `128Gi`  |
| `m1.4xlarge` | `32`   | `256Gi`  |

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
