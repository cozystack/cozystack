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
| **Archive plumbing** (chart, legacy)     | Renders `spec.plugins` (referencing a barman-cloud `ObjectStore`) on the cnpg.io Cluster from helm-install onwards. CNPG runs the barman-cloud plugin as a WAL-archiver sidecar, every WAL switch ships to object storage, and any backup driver gets a complete WAL chain to start replay from. Renders only when `backup.enabled=true`, `useSystemBucket=false`, `destinationPath` is non-empty, AND inline-or-external creds are supplied. **Skipped in the platform `useSystemBucket=true` mode** — the CNPG backup driver SSA-applies the `ObjectStore` and patches `spec.plugins` onto the live Cluster at first BackupJob time; until that first BackupJob fires, plugin WAL archiving is not active and WAL accumulates on the PVC. Fire an ad-hoc BackupJob immediately after enabling the flag on existing releases. | Legacy: `backup.enabled=true` plus `backup.destinationPath`, `backup.endpointURL`, and either `backup.s3AccessKey`+`backup.s3SecretKey` or `backup.s3CredentialsSecret`. Platform: `backup.enabled=true` plus `backup.useSystemBucket=true` — no S3 fields. |
| **Backup orchestration** (recommended)   | Drives ad-hoc and scheduled backups, retention, and restores from `backups.cozystack.io` resources that can span multiple Postgres apps in a tenant. The driver SSA-applies its own barman-cloud `ObjectStore` and patches `spec.plugins` with `ForceOwnership` (so compression / target settings flow from the strategy template, while the live Cluster's `serverName` — its WAL-archive S3 prefix — is always preserved); chart-managed values cover the same destination so there is no tug-of-war on the live Cluster.                                                                                                                                       | `strategy.backups.cozystack.io/CNPG` + `BackupClass` + `Plan` (recurring) or `BackupJob` (ad-hoc), restores via `RestoreJob`                  |
| Legacy chart-emitted scheduled backup    | The chart can also emit a `cnpg.io/ScheduledBackup` directly. Superseded by the BackupClass + Plan path, kept around for clusters that did not migrate. **Off by default** - rendered only when both `backup.enabled` and `backup.schedule` are non-empty. With `backup.schedule=""` plugin WAL archiving still runs; only the chart-emitted scheduled backup is silent.                                                                                                                                                                                                  | `backup.enabled=true` plus `backup.schedule` (CNPG 6-field cron, e.g. `"0 2 * * * *"`)                                                        |

The **canonical setup** depends on whether the cluster ships the platform `cozy-default` BackupClass.

**Platform-managed flow (recommended for new clusters)** — opt in via `backup.useSystemBucket: true` and reference `cozy-default` from BackupJob/Plan/RestoreJob. The chart leaves S3 coordinates blank; the CNPG driver SSA-applies a barman-cloud `ObjectStore` and patches `spec.plugins` from the platform-managed bucket coordinates at first BackupJob time.

```yaml
spec:
  backup:
    enabled: true
    useSystemBucket: true        # platform projects cozy-backups-creds + driver SSA-applies ObjectStore + patches spec.plugins
```

**Legacy chart-managed flow** — for clusters that pre-date the platform BackupClass or use a tuned non-default bucket. Provide all S3 coordinates inline (or via `s3CredentialsSecret.name`); `spec.plugins` and the barman-cloud `ObjectStore` render from helm-install onwards.

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

> **Why both layers and not one?** WAL archiving is handled by the barman-cloud plugin's WAL-archiver sidecar - CNPG can attach or detach the plugin at runtime, but any WAL that closed before the plugin began archiving is gone for good. A backup taken from such a cluster is missing the WAL its `begin_wal` points at, and recovery later fails with `WAL not found`. Letting the chart wire the plugin from helm-install removes the race.

### How to enable backups (preferred: BackupClass + Plan)

End-to-end manifests live under [`examples/backups/postgres/`](../../../examples/backups/postgres/).
Briefly, the moving parts are:

1. A `strategy.backups.cozystack.io/CNPG` describing the destination bucket and templating the barman-cloud `ObjectStore` (including a Secret reference to S3 credentials - the credentials never appear on the Postgres CR `.spec`; see Security note below).
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
## @param backup.enabled Enable plugin WAL archiving + render the chart-managed ScheduledBackup
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

> **e2e coverage:** the same-namespace to-copy restore (Steps 0-7) leaves
> the source running and is the deterministic end-to-end signal; it stays
> a manual / dev-cluster reference flow, not part of the automated e2e suite.
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

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

### TLS for server connections

CNPG manages the cert chain end-to-end. The operator auto-generates a self-signed CA, signs server, client, and replication leaf certs from it, and rotates them as needed. The chart does not render any cert-manager `Issuer`/`Certificate` objects — that path is mutually exclusive with the operator-managed chain on the CNPG admission webhook.

What the chart contributes: when TLS is on and `external: true`, the chart sets `spec.certificates.serverAltDNSNames` on the CNPG Cluster CR to inject the external hostname `<release>.<_namespace.host>` into the auto-generated server certificate's SAN list. CNPG's default SAN coverage already includes the three built-in ClusterIP services (`-rw`, `-r`, `-ro`) across the four DNS forms (`<svc>`, `<svc>.<ns>`, `<svc>.<ns>.svc`, `<svc>.<ns>.svc.<cluster-domain>`); only the external hostname needs to be added.

The tri-state `tls.enabled` controls whether the chart injects `serverAltDNSNames`:

- `tls.enabled: null` (the default) — TLS posture inherits from `external`. When `external: true`, the chart injects the external hostname into the operator-managed cert.
- `tls.enabled: true` with `external: true` — same effect as the default.
- `tls.enabled: true` with `external: false` — no `serverAltDNSNames` injection is needed (there is no external hostname to add); CNPG's auto-generated cert covers internal services.
- `tls.enabled: false` — the chart skips `serverAltDNSNames` injection. **Note:** CNPG keeps its built-in TLS on the wire regardless of this flag; this toggle only controls whether the external hostname is added to the cert. To force PostgreSQL to drop TLS entirely you would need to set `postgresql.parameters.ssl = "off"` at the CNPG layer, which is out of scope for this flag.

**Retrieving the CA bundle** for client verification:

CNPG bundles the CA certificate in every user-credentials Secret it creates under the key `ca.crt`. Retrieve it from the `<release>-credentials` Secret, which is already accessible to tenants via the dashboard RBAC:

```bash
kubectl --context <ctx> --namespace <tenant> \
  get secret <release>-credentials \
  --output jsonpath='{.data.ca\.crt}' | base64 --decode
```

**Connecting with full verification** (psql example):

```bash
psql "host=<host> port=5432 dbname=app user=app \
  sslmode=verify-full sslrootcert=ca.crt"
```

For `sslmode=verify-full` to work, the CA bundle retrieved above must be saved to `ca.crt`. Without it, use `sslmode=require` (encrypts but does not verify the server certificate).

## Parameters

### Common parameters

| Name               | Description                                                                                                                          | Type       | Value      |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ---------- |
| `replicas`         | Number of Postgres replicas.                                                                                                         | `int`      | `2`        |
| `resources`        | Explicit CPU and memory configuration for each PostgreSQL replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `resources.cpu`    | CPU available to each replica.                                                                                                       | `quantity` | `""`       |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                              | `quantity` | `""`       |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                              | `string`   | `t1.micro` |
| `size`             | Persistent Volume Claim size available for application data.                                                                         | `quantity` | `10Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                                 | `string`   | `""`       |
| `external`         | Enable external access from outside the cluster.                                                                                     | `bool`     | `false`    |
| `version`          | PostgreSQL major version to deploy                                                                                                   | `string`   | `v18`      |


### TLS configuration

| Name          | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Type     | Value  |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------ |
| `tls`         | TLS configuration for server connections.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | `object` | `{}`   |
| `tls.enabled` | Tri-state switch controlling whether the chart injects the external hostname into the operator-managed CNPG cert via spec.certificates.serverAltDNSNames. When omitted, the chart injects the SAN if `external: true` and skips it otherwise. Set explicitly to `true` to inject regardless of `external` (no-op when `external: false` since there is no external hostname to add). Set to `false` to skip injection. Note that CNPG keeps its built-in TLS on the wire regardless of this flag — this toggle only controls the chart-side SAN injection; to disable PostgreSQL TLS entirely set `postgresql.parameters.ssl = "off"` at the CNPG layer. | `*bool`  | `null` |


### Application-specific parameters

| Name                    | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Type                     | Value |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ | ----- |
| `postgresql`            | PostgreSQL server configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `object`                 | `{}`  |
| `postgresql.parameters` | PostgreSQL server parameters. Values may be strings or integers; integers are coerced to strings by the template (e.g. both `max_connections: 100` and `max_connections: "100"` are accepted). BLOCKED (enable arbitrary code execution): archive_command, restore_command, ssl_passphrase_command, archive_cleanup_command, recovery_end_command, dynamic_library_path, local_preload_libraries, session_preload_libraries, shared_preload_libraries. Do NOT override CloudNativePG-managed parameters: archive_mode, primary_conninfo, wal_level, max_replication_slots. | `map[string]intOrString` | `{}`  |


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

| Name                                            | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Type     | Value                               |
| ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ----------------------------------- |
| `backup`                                        | Backup configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | `object` | `{}`                                |
| `backup.enabled`                                | Enable regular backups.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `bool`   | `false`                             |
| `backup.useSystemBucket`                        | Opt-in: when true, the chart-emitted `<release>-s3-creds` Secret is skipped AND `spec.plugins` (plus the barman-cloud ObjectStore) is left UNSET in the chart-rendered Cluster — the cozy-default BackupClass driver SSA-applies an ObjectStore (carrying destinationPath/endpointURL/credentials) and patches `spec.plugins` on the live Cluster when the first BackupJob runs. Consequence: plugin WAL archiving is NOT active until that first BackupJob fires; WAL accumulates on the PVC in the meantime, so fire an ad-hoc BackupJob immediately after enabling the flag on existing releases. Use together with the platform `cozy-default` BackupClass — tenants do not need to fill `s3AccessKey`/`s3SecretKey` or `destinationPath`/`endpointURL`. The destination path automatically scopes to `s3://cozy-backups/<namespace>/<release>/`. | `bool`   | `false`                             |
| `backup.schedule`                               | Legacy. Cron schedule (CNPG 6-field format) for the chart-emitted ScheduledBackup. Empty means no chart-managed schedule, which is the recommended setup when a `BackupClass` from `backups.cozystack.io` already drives backup orchestration. In the legacy chart-managed flow `spec.plugins` plus the barman-cloud ObjectStore is rendered when `backup.enabled=true` AND `useSystemBucket=false` AND `destinationPath` is non-empty AND inline-or-external creds are supplied; in the platform `useSystemBucket=true` flow the chart skips emitting `spec.plugins` and the CNPG driver SSA-applies the ObjectStore and patches `spec.plugins` onto the live Cluster at first BackupJob time.                                                                                                                                                           | `string` | `""`                                |
| `backup.retentionPolicy`                        | Retention policy (e.g. "30d").                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `string` | `30d`                               |
| `backup.destinationPath`                        | DEPRECATED. Per-tenant S3 configuration is superseded by the platform-managed `cozy-default` BackupClass and the `cozy-backups` system bucket. Leave empty for new installations; the BackupClass driver picks up the system-managed coordinates. Kept for in-place upgrade compatibility.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `string` | `s3://bucket/path/to/folder/`       |
| `backup.endpointURL`                            | DEPRECATED. See `destinationPath`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | `string` | `http://minio-gateway-service:9000` |
| `backup.s3AccessKey`                            | DEPRECATED. Tenants no longer supply S3 keys; the system Bucket Secret is projected into the tenant namespace by the backup controller. Ignored when `s3CredentialsSecret.name` is set or `useSystemBucket` is true. The chart skips materialising `<release>-s3-creds` whenever this field is empty so a default install does not leak placeholder credentials into the tenant namespace.                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `string` | `""`                                |
| `backup.s3SecretKey`                            | DEPRECATED. See `s3AccessKey`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `string` | `""`                                |
| `backup.s3CredentialsSecret`                    | DEPRECATED. Pre-existing Secret with S3 credentials. Use the platform-managed `cozy-default` BackupClass instead. When set, the chart references this Secret directly (legacy chart-managed flow). The CNPG backup driver writes this field on restore so credentials never land in the CR `.spec`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `object` | `{}`                                |
| `backup.s3CredentialsSecret.name`               | Name of the Secret in the application namespace. Empty means the chart materialises `<release>-s3-creds` from `s3AccessKey`/`s3SecretKey`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `string` | `""`                                |
| `backup.s3CredentialsSecret.accessKeyIDKey`     | Key in the Secret holding the access key ID. Defaults to `AWS_ACCESS_KEY_ID`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | `string` | `""`                                |
| `backup.s3CredentialsSecret.secretAccessKeyKey` | Key in the Secret holding the secret access key. Defaults to `AWS_SECRET_ACCESS_KEY`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | `string` | `""`                                |
| `backup.endpointCA`                             | DEPRECATED. Pre-existing Secret with the CA bundle the barman-cloud plugin should trust when reaching a self-signed S3 endpoint. Used for both backup and bootstrap recovery in the legacy chart-managed flow.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `object` | `{}`                                |
| `backup.endpointCA.name`                        | Name of the Secret in the application namespace. Empty means no endpointCA is emitted (the plugin uses the system trust store).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `string` | `""`                                |
| `backup.endpointCA.key`                         | Key within the Secret containing the CA bundle. Defaults to `ca.crt`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | `string` | `""`                                |


### Bootstrap (recovery) parameters

| Name                     | Description                                                                                                                                                                                                                                                                                                   | Type     | Value   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `bootstrap`              | Bootstrap configuration.                                                                                                                                                                                                                                                                                      | `object` | `{}`    |
| `bootstrap.enabled`      | Whether to restore from a backup.                                                                                                                                                                                                                                                                             | `bool`   | `false` |
| `bootstrap.recoveryTime` | Timestamp (RFC3339) for point-in-time recovery; empty means latest.                                                                                                                                                                                                                                           | `string` | `""`    |
| `bootstrap.oldName`      | Previous cluster name before deletion.                                                                                                                                                                                                                                                                        | `string` | `""`    |
| `bootstrap.serverName`   | Server name (S3 path prefix) used by the original cluster when writing backups; passed to the barman-cloud plugin via `externalClusters[].plugin.parameters.serverName`. Defaults to `bootstrap.oldName`. Set this only when the original cluster wrote backups under an explicit server name that differed from its Kubernetes resource name. | `string` | `""`    |


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
