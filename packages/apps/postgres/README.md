# Managed PostgreSQL Service

PostgreSQL is currently the leading choice among relational databases, known for its robust features and performance.
The Managed PostgreSQL Service takes advantage of platform-side implementation to provide a self-healing replicated cluster.
This cluster is efficiently managed using the highly acclaimed CloudNativePG operator, which has gained popularity within the community.

## Deployment Details

This managed service is controlled by the CloudNativePG operator, ensuring efficient management and seamless operation.

- Docs: <https://cloudnative-pg.io/docs/>
- Github: <https://github.com/cloudnative-pg/cloudnative-pg>

## Operations

PostgreSQL backups have two layers, and the recommended setup uses both
together rather than picking one:

| Layer                                    | What it does                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Configured via                                                                                                                                |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **Archive plumbing** (chart)             | Renders `spec.backup.barmanObjectStore` on the cnpg.io Cluster from helm-install onwards. CNPG starts postgres with `archive_command=barman-cloud-wal-archive`, every WAL switch ships to object storage, and any backup driver gets a complete WAL chain to start replay from.                                                                                                                                                                                                                                                                                          | `backup.enabled=true` plus `backup.destinationPath`, `backup.endpointURL`, `backup.s3CredentialsSecret`, `backup.endpointCA`                  |
| **Backup orchestration** (recommended)   | Drives ad-hoc and scheduled backups, retention, and restores from `backups.cozystack.io` resources that can span multiple Postgres apps in a tenant. The driver SSA-merges its own `barmanObjectStore` shape over the chart's with `ForceOwnership` (so compression / serverName / target settings flow from the strategy template); chart-managed values cover the same destination so there is no tug-of-war on the live Cluster.                                                                                                                                       | `strategy.backups.cozystack.io/CNPG` + `BackupClass` + `Plan` (recurring) or `BackupJob` (ad-hoc), restores via `RestoreJob`                  |
| Legacy chart-emitted scheduled backup    | The chart can also emit a `cnpg.io/ScheduledBackup` directly. Superseded by the BackupClass + Plan path, kept around for clusters that did not migrate. **Off by default** - rendered only when both `backup.enabled` and `backup.schedule` are non-empty. With `backup.schedule=""` the archive plumbing still runs; only the chart-emitted scheduled backup is silent.                                                                                                                                                                                                  | `backup.enabled=true` plus `backup.schedule` (CNPG 6-field cron, e.g. `"0 2 * * * *"`)                                                        |

The **canonical setup** for tenant-managed backups is therefore:

```yaml
spec:
  backup:
    enabled: true                # archive WAL to object storage
    destinationPath: s3://my-bucket/pg-src/
    endpointURL: https://seaweedfs-s3.tenant-foo:8333
    s3CredentialsSecret:
      name: pg-src-cnpg-backup-creds
    endpointCA:
      name: pg-src-cnpg-backup-ca
    # backup.schedule intentionally left empty - the BackupClass / Plan
    # below drives recurring backups, no chart-emitted ScheduledBackup
    # should compete with that schedule.
```

paired with a `strategy.backups.cozystack.io/CNPG` + `BackupClass` + `Plan`
in the same tenant. The end-to-end e2e fixture under
[`examples/backups/postgres/`](../../../examples/backups/postgres/) is the
canonical reference (`05-postgres-src.yaml` shows the chart side,
`10-cnpg-strategy.yaml` and `15-backupclass.yaml` show the orchestration
side, `25-backupjob-adhoc.yaml` and `40-restorejob-to-copy.yaml` show
ad-hoc backup and restore).

> **Why both layers and not one?** WAL archive is a postmaster setting -
> CNPG can swap `archive_command` at runtime via SIGHUP, but any WAL that
> closed before the swap was "archived" by `archive_command=/bin/true`
> and is gone for good. A backup taken from such a cluster is missing
> the WAL its `begin_wal` points at, and recovery later fails with
> `WAL not found`. Letting the chart bootstrap `archive_command` from
> helm-install removes the race.

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

### How to enable chart-managed scheduled backups (legacy)

The chart can also emit a `cnpg.io/ScheduledBackup` directly, without a
BackupClass. Superseded by the BackupClass + Plan path above and kept
around for clusters that did not migrate. It does not run by default -
`backup.schedule` defaults to an empty string, which gates the chart's
ScheduledBackup template off. To turn it on, fill in a CNPG 6-field cron
expression:

```yaml
## @param backup.enabled Enable archive_command + render the chart-managed ScheduledBackup
## @param backup.schedule Cron schedule (CNPG 6-field). Empty means no chart-managed ScheduledBackup
## @param backup.retentionPolicy Retention policy
## @param backup.destinationPath Path to store the backup (i.e. s3://bucket/path/to/folder)
## @param backup.endpointURL S3 Endpoint used to upload data to the cloud
## @param backup.s3AccessKey Access key for S3, used for authentication
## @param backup.s3SecretKey Secret key for S3, used for authentication
backup:
  enabled: true
  retentionPolicy: 30d
  destinationPath: s3://bucket/path/to/folder/
  endpointURL: http://minio-gateway-service:9000
  schedule: "0 2 * * * *"  # opt in - empty (the default) means no chart-managed schedule
  s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
  s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
```

### How to recover a backup (preferred: RestoreJob)

For BackupClass-managed backups, create a `backups.cozystack.io/RestoreJob`
that references the desired `Backup`. See
[`examples/backups/postgres/35-restorejob-in-place.yaml`](../../../examples/backups/postgres/35-restorejob-in-place.yaml)
and
[`examples/backups/postgres/40-restorejob-to-copy.yaml`](../../../examples/backups/postgres/40-restorejob-to-copy.yaml).
On a to-copy restore the controller replaces the target app's
`spec.databases` and `spec.users` with the source-spec snapshot persisted
in `Backup.status.underlyingResources`, so the chart's post-install
init-job does not drop the recovered roles or databases.

> **e2e coverage:** the CI e2e (`hack/e2e-apps/backup-postgres.bats`)
> exercises the same-namespace to-copy restore (Steps 0-7), which leaves
> the source running and is therefore the deterministic end-to-end signal.
> The cross-tenant variant (target Postgres in a different tenant from
> the source's seaweedfs) stays a manual / dev-cluster exercise —
> reachability is blocked by the per-tenant Cilium egress policy. The
> in-place restore code path is shipped and is covered at the unit level
> by `TestClusterHasRecoveryBootstrap_TerminatingCluster`,
> `TestCNPGBackupWALArchived`, `TestCNPGPurgeNeeded`, and the rest of the
> `internal/backupcontroller/cnpgstrategy_controller_test.go` suite.

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

### How to enable PostGIS

Set `flavor: postgis` to bootstrap the cluster from the CloudNativePG
PostGIS image (`ghcr.io/cloudnative-pg/postgis`) instead of the standard
PostgreSQL image. The variant ships PostGIS and its companion extensions
(`postgis_topology`, `postgis_raster`, `pgrouting`,
`address_standardizer`) precompiled, so they can be activated per
database via `databases.<name>.extensions`:

```yaml
spec:
  flavor: postgis
  version: v17
  databases:
    gis:
      extensions:
        - postgis
        - postgis_topology
```

`flavor` is a bootstrap-time choice. Switching it on an existing release
swaps the cluster image and CNPG will fail to restart the existing pods
if the on-disk format does not match — plan a fresh release (with
`bootstrap.recovery` from a backup if needed) when changing `flavor`.
PostgreSQL v18 is not yet covered by the PostGIS image; pick a version
in `[v13, v17]` when `flavor: postgis`.

## Parameters

### Common parameters

| Name               | Description                                                                                                                                                                                                                                                                                                                                                             | Type       | Value        |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------------ |
| `replicas`         | Number of Postgres replicas.                                                                                                                                                                                                                                                                                                                                            | `int`      | `2`          |
| `resources`        | Explicit CPU and memory configuration for each PostgreSQL replica. When omitted, the preset defined in `resourcesPreset` is applied.                                                                                                                                                                                                                                    | `object`   | `{}`         |
| `resources.cpu`    | CPU available to each replica.                                                                                                                                                                                                                                                                                                                                          | `quantity` | `""`         |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                                                                                                                                                                                                                                                 | `quantity` | `""`         |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                                                                                                                                                                                                                                                 | `string`   | `t1.micro`   |
| `size`             | Persistent Volume Claim size available for application data.                                                                                                                                                                                                                                                                                                            | `quantity` | `10Gi`       |
| `storageClass`     | StorageClass used to store the data.                                                                                                                                                                                                                                                                                                                                    | `string`   | `""`         |
| `external`         | Enable external access from outside the cluster.                                                                                                                                                                                                                                                                                                                        | `bool`     | `false`      |
| `version`          | PostgreSQL major version to deploy                                                                                                                                                                                                                                                                                                                                      | `string`   | `v18`        |
| `flavor`           | PostgreSQL image flavor. `postgresql` (default) uses the standard CloudNativePG image. `postgis` switches to the CloudNativePG PostGIS image, which ships PostGIS and companion extensions (postgis_topology, postgis_raster, pgrouting, address_standardizer) precompiled. Declare the needed extensions in `databases.<name>.extensions` to enable them per database. | `string`   | `postgresql` |


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

| Name                                            | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                 | Type     | Value                               |
| ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ----------------------------------- |
| `backup`                                        | Backup configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                       | `object` | `{}`                                |
| `backup.enabled`                                | Enable regular backups.                                                                                                                                                                                                                                                                                                                                                                                                                                     | `bool`   | `false`                             |
| `backup.schedule`                               | Legacy. Cron schedule (CNPG 6-field format) for the chart-emitted ScheduledBackup. Empty means no chart-managed schedule, which is the recommended setup when a `BackupClass` from `backups.cozystack.io` already drives backup orchestration. The chart still emits `spec.backup.barmanObjectStore` whenever `backup.enabled=true`, so `archive_command` runs and the BackupClass driver can take ad-hoc / Plan-driven backups against a live WAL archive. | `string` | `""`                                |
| `backup.retentionPolicy`                        | Retention policy (e.g. "30d").                                                                                                                                                                                                                                                                                                                                                                                                                              | `string` | `30d`                               |
| `backup.destinationPath`                        | Destination path for backups (e.g. s3://bucket/path/).                                                                                                                                                                                                                                                                                                                                                                                                      | `string` | `s3://bucket/path/to/folder/`       |
| `backup.endpointURL`                            | S3 endpoint URL for uploads.                                                                                                                                                                                                                                                                                                                                                                                                                                | `string` | `http://minio-gateway-service:9000` |
| `backup.s3AccessKey`                            | Access key for S3 authentication. Ignored when `s3CredentialsSecret.name` is set.                                                                                                                                                                                                                                                                                                                                                                           | `string` | `<your-access-key>`                 |
| `backup.s3SecretKey`                            | Secret key for S3 authentication. Ignored when `s3CredentialsSecret.name` is set.                                                                                                                                                                                                                                                                                                                                                                           | `string` | `<your-secret-key>`                 |
| `backup.s3CredentialsSecret`                    | Pre-existing Secret with S3 credentials. When set, the chart references this Secret directly instead of materialising one from `s3AccessKey`/`s3SecretKey`. The CNPG backup driver writes this field on restore so credentials never land in the CR `.spec`.                                                                                                                                                                                                | `object` | `{}`                                |
| `backup.s3CredentialsSecret.name`               | Name of the Secret in the application namespace. Empty means the chart materialises `<release>-s3-creds` from `s3AccessKey`/`s3SecretKey`.                                                                                                                                                                                                                                                                                                                  | `string` | `""`                                |
| `backup.s3CredentialsSecret.accessKeyIDKey`     | Key in the Secret holding the access key ID. Defaults to `AWS_ACCESS_KEY_ID`.                                                                                                                                                                                                                                                                                                                                                                               | `string` | `""`                                |
| `backup.s3CredentialsSecret.secretAccessKeyKey` | Key in the Secret holding the secret access key. Defaults to `AWS_SECRET_ACCESS_KEY`.                                                                                                                                                                                                                                                                                                                                                                       | `string` | `""`                                |
| `backup.endpointCA`                             | Pre-existing Secret with the CA bundle Barman should trust when reaching a self-signed S3 endpoint. Used for both backup and bootstrap recovery. The CNPG backup driver writes this field on restore.                                                                                                                                                                                                                                                       | `object` | `{}`                                |
| `backup.endpointCA.name`                        | Name of the Secret in the application namespace. Empty means no endpointCA is emitted (Barman uses the system trust store).                                                                                                                                                                                                                                                                                                                                 | `string` | `""`                                |
| `backup.endpointCA.key`                         | Key within the Secret containing the CA bundle. Defaults to `ca.crt`.                                                                                                                                                                                                                                                                                                                                                                                       | `string` | `""`                                |


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

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases of their 1:1 instance-type equivalents.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.

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
