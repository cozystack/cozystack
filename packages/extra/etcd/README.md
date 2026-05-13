# Etcd-cluster

## Backups

**DEPRECATED:** the `backup.*` values block (`backup.enabled`,
`backup.schedule`, `backup.destinationPath`, ...) is superseded by the
Cozystack `BackupClass` flow driven by the `Etcd` strategy in
`strategy.backups.cozystack.io/v1alpha1`. New tenants should use a
`BackupJob` / `RestoreJob` against an `Etcd` strategy + `BackupClass`
instead — see `examples/backups/etcd/` for the end-to-end demo and
`internal/backupcontroller/etcdstrategy_controller.go` for the driver.
Existing tenants with `backup.enabled=true` continue to render the
legacy `EtcdBackupSchedule` + S3 credentials Secret unchanged — the
two flows coexist, the chart's scheduled backups have NOT stopped
working, and the `backup.*`-conditional rendered output is identical
to prior releases.

### Upgrade note: `serverTrustedCASecret` is now set unconditionally

This release adds `security.tls.serverTrustedCASecret: etcd-ca-tls` to
the rendered `EtcdCluster`. The field is required so that the
etcd-operator's `backup_agent` / `restore_agent` (v0.4.4+) trust the
server cert when they connect from their own pod via etcdctl —
without it the agent falls back to the system trust store and the
backup/restore handshake fails with "certificate signed by unknown
authority". The chart reuses the existing `etcd-ca-tls` Secret that
already issues every client/server cert today, so the field points at
material that has been present in the namespace since the cluster was
first deployed. On a Helm upgrade the etcd-operator observes the new
field and starts enforcing server-cert verification on its own client
connections; it does NOT roll the etcd member pods (the etcd container's
TLS configuration was already authoritative). Tenants who rely on the
backup-agent today will continue to need this field set, so leaving it
on by default keeps the chart consistent with the documented backup
path.

When `backup.enabled` is set to `true`, the chart renders an `EtcdBackupSchedule` (etcd.aenix.io/v1alpha1) and an S3 credentials `Secret`. The etcd-operator v0.4.4+ reconciles the schedule into a `CronJob` that periodically snapshots the cluster to S3.

Enabling backup requires the following fields to be explicitly set (defaults are empty strings so that missing values fail fast at template render time): `backup.s3AccessKey`, `backup.s3SecretKey`, `backup.destinationPath` (must start with `s3://` and have no `//` segments), and `backup.endpointURL`. S3 credentials passed through plain values end up in the HelmRelease manifest — for production deployments prefer an external secret management tool (ESO, Sealed Secrets, etc.) over committing the keys to Git.

**Restore and ad-hoc backup**: the primary supported path is the
Cozystack `BackupClass` / `BackupJob` / `RestoreJob` flow described
above and demonstrated end-to-end under `examples/backups/etcd/`. The
driver suspends this chart's `HelmRelease` for the duration of an
in-place restore, deletes the live `EtcdCluster`, and re-creates it
with `spec.bootstrap.restore.source.s3` populated from the
`Backup` artefact's coordinates. The upstream `EtcdCluster.spec.bootstrap`
field and the one-shot `EtcdBackup` CR (v0.4.4) are NOT exposed
through this chart's values themselves; tenants who need to bypass
the BackupClass flow (e.g. an out-of-band recovery using a snapshot
that has no `Backup` artefact) can hand-apply the corresponding
custom resource manifest as an escape hatch.

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

| Name                                | Description                                                                                                  | Type     | Value       |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------ | -------- | ----------- |
| `backup`                            | **DEPRECATED**: Backup configuration. Use a BackupClass bound to the Etcd strategy (see Backups section).    | `object` | `{}`        |
| `backup.enabled`                    | **DEPRECATED**: Enable scheduled S3 backups. Use a BackupJob against an Etcd strategy instead.               | `bool`   | `false`     |
| `backup.schedule`                   | **DEPRECATED**: Cron schedule for automated backups. Use a backups.cozystack.io/Plan instead.                | `string` | `0 2 * * *` |
| `backup.destinationPath`            | **DEPRECATED**: Destination path for backups (e.g. s3://bucket/path/).                                       | `string` | `""`        |
| `backup.endpointURL`                | **DEPRECATED**: S3 endpoint URL for uploads.                                                                 | `string` | `""`        |
| `backup.region`                     | **DEPRECATED**: S3 region.                                                                                   | `string` | `""`        |
| `backup.forcePathStyle`             | **DEPRECATED**: Use path-style S3 URLs (required for MinIO and most S3-compatible providers).                | `bool`   | `true`      |
| `backup.s3AccessKey`                | **DEPRECATED**: Access key for S3 authentication.                                                            | `string` | `""`        |
| `backup.s3SecretKey`                | **DEPRECATED**: Secret key for S3 authentication.                                                            | `string` | `""`        |
| `backup.successfulJobsHistoryLimit` | **DEPRECATED**: Number of successful backup jobs to retain.                                                  | `int`    | `3`         |
| `backup.failedJobsHistoryLimit`     | **DEPRECATED**: Number of failed backup jobs to retain.                                                      | `int`    | `1`         |

