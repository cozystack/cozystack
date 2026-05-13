# Scenario: Cluster admin prepares ClickHouse backups

A cluster administrator performs the cluster-level preparation once. Tenants
will then be able to back up and restore their ClickHouse applications by
referencing the BackupClass created here, with no further admin action.

## Prerequisites

- A running Cozystack cluster with the backup-controller and
  backupstrategy-controller installed.
- `kubectl` access with permissions to create cluster-scoped
  `Altinity.strategy.backups.cozystack.io` and `BackupClass.backups.cozystack.io`.
- Reachable S3-compatible storage. The demo uses the in-cluster `Bucket` app
  (step 03), but any S3-compatible coordinates work — the chart picks them up
  from `spec.backup.*` on the tenant ClickHouse and materialises a
  `<release>-backup-s3` Secret consumed by the in-pod `clickhouse-backup`
  sidecar.

## Steps

```bash
./01-create-strategy.sh        # Altinity strategy: alpine + curl driving the sidecar HTTP API
./02-create-backupclass.sh     # Maps Kind=ClickHouse -> the strategy
```

## What gets created

| Resource | Scope | Purpose |
|---|---|---|
| `Altinity.strategy.backups.cozystack.io/altinity` | Cluster | PodTemplateSpec for the small client Pod that POSTs `create_remote`/`restore_remote` to the sidecar's HTTP API and polls the action log |
| `BackupClass/clickhouse-backup` | Cluster | Maps `apps.cozystack.io/ClickHouse` to the strategy. No parameters are required: the strategy template addresses the in-pod sidecar by deterministic Pod DNS, and S3 credentials live next to the application as a chart-emitted Secret |

## How the strategy template gets rendered

The Altinity driver renders the `Altinity.spec.template` PodTemplateSpec
with this context for every BackupJob/RestoreJob run:

| Variable | Source |
|---|---|
| `.Application` | The `ClickHouse` (`apps.cozystack.io`) object referenced by the BackupJob (or `targetApplicationRef` on restore) |
| `.Release.Name` / `.Release.Namespace` | `metadata.name` / `metadata.namespace` of `.Application` |
| `.Parameters` | `BackupClass.spec.strategies[].parameters` (kept available for future strategy variants — this template does not use it) |
| `.Mode` | `"backup"` or `"restore"` |
| `.Backup` | `{ Name, Namespace, ApplicationRef.{APIGroup,Kind,Name} }` (only for restore runs; `.ApplicationRef` points at the *source* release so to-copy restores can derive its name and namespace prefix) |

The same template renders both backup and restore Pods; the
`if [ "{{ .Mode }}" = "backup" ]; then ... else ... fi` branch in `args`
chooses the HTTP endpoint (`/backup/create_remote` vs.
`/backup/restore_remote/<name>`).
