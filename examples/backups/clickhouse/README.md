# ClickHouse backup/restore example

> **Heads up — most clusters do not need this walk-through.** Cozystack
> ships a platform-managed `cozy-default` `BackupClass` together with
> the system bucket `cozy-backups`. To use the default flow, set
> `backup.enabled: true` and `backup.useSystemBucket: true` on the
> ClickHouse release; tenants do not provision a Bucket or supply S3
> credentials. See
> [Backup Classes](../../../docs/operations/backup-classes.md)
> for the supported BackupJob / Plan flow against `cozy-default`. The
> walk-through below covers the **legacy** path that wires a per-app
> Bucket, custom strategy CR, and bespoke BackupClass — useful when an
> admin needs a tuned non-default policy.

This directory shows how to back up and restore a Cozystack-managed
`ClickHouse` application using the cluster's `Altinity` backup strategy
driver. The chart materialises an [Altinity `clickhouse-backup`][altinity]
sidecar inside every chi-* Pod (when `backup.enabled=true`) that exposes
the tool's HTTP API on port 7171 and shares `/var/lib/clickhouse` with
clickhouse-server. The strategy Pod itself is a tiny `curl + jq` client
that POSTs to the sidecar and polls the action log.

## Step order

| File | Role | Triggered by |
|---|---|---|
| `00-helpers.sh` | Shared bash helpers and env defaults; sourced by every step. | n/a |
| `01-create-strategy.sh` | Creates the cluster-scoped `Altinity` strategy that wraps `clickhouse-backup`. | admin |
| `02-create-backupclass.sh` | Maps `apps.cozystack.io/ClickHouse` to that strategy. | admin |
| `03-create-bucket.sh` | Provisions a `Bucket`, caches its S3 coordinates into `.bucket-info.env` (chmod 600; raw access keys), and copies the S3 endpoint CA into a per-release Secret so the sidecar can verify a self-signed endpoint. `cleanup.sh` removes both. | tenant, **plus admin** for the CA copy — see below |
| `04-create-clickhouse.sh` | Provisions a `ClickHouse` instance with `backup.enabled=true` (chart emits the backup-s3 Secret + sidecar) and writes a sentinel row. | tenant |
| `05-create-backupjob.sh` | Submits a `BackupJob` and waits for Succeeded. | tenant |
| `06-restore-in-place.sh` | Drops the sentinel and restores into the same instance via `RestoreJob`. | tenant |
| `07-restore-to-copy.sh` | Provisions a second `ClickHouse` and restores into it via `RestoreJob.spec.targetApplicationRef`. | tenant |
| `cleanup.sh` | Removes everything created by the demo. | admin or tenant |
| `run-all.sh` | Convenience runner that executes 01..07 in order. | demo |
| `90-scenario-admin-prepare.md` | Narrative for the admin preparation steps. | docs |
| `91-scenario-user-backup.md` | Narrative for the user backup flow. | docs |
| `92-scenario-user-restore.md` | Narrative for the user restore flow (in-place and to-copy). | docs |

## Environment variables (overrides)

All variables come from `00-helpers.sh`:

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `tenant-root` | Tenant namespace for the demo. `tenant-root` because the S3 client is the `clickhouse-backup` sidecar inside the tenant and cozystack's shared SeaweedFS lives there; an isolated tenant's Cilium egress allowlist blocks its Pods from reaching `tenant-root`'s SeaweedFS. |
| `CLICKHOUSE_NAME` | `clickhouse-test` | Source ClickHouse application name. |
| `CLICKHOUSE_RESTORE_NAME` | `clickhouse-restore` | Target ClickHouse for the to-copy restore. |
| `BUCKET_NAME` | `clickhouse-backups` | Cozystack `Bucket` to provision. |
| `BACKUPCLASS_NAME` | `clickhouse-backup` | BackupClass name (cluster-scoped). |
| `STRATEGY_NAME` | `altinity` | Strategy name (cluster-scoped). |
| `BACKUPJOB_NAME` | `clickhouse-backup-job` | BackupJob name. |
| `RESTOREJOB_INPLACE_NAME` | `clickhouse-restore-inplace` | In-place RestoreJob name. |
| `RESTOREJOB_TOCOPY_NAME` | `clickhouse-restore-to-copy` | To-copy RestoreJob name. |
| `S3_ENDPOINT` | *(from BucketInfo)* | Overrides the S3 endpoint the sidecar uses. `BucketInfo` advertises the **external** ingress URL, which in-cluster Pods cannot always reach or TLS-validate; set `https://seaweedfs-s3.tenant-root:8333` to use the in-cluster endpoint. |
| `S3_CA_SECRET` | `seaweedfs-ca-cert` | Secret holding the S3 endpoint's CA. Step 03 copies its CA into a per-release Secret wired to `backup.endpointCA`, so the sidecar can verify a self-signed endpoint. Auto-discovered from the seaweedfs cert-manager `Certificate` if this name is absent. Set to `""` on a publicly-trusted endpoint to skip the copy and leave `backup.endpointCA` unset. |
| `S3_CA_NAMESPACE` | `tenant-root` | Namespace of `S3_CA_SECRET`. Independent of `NAMESPACE`: the shared SeaweedFS and its CA live in `tenant-root` even when the demo runs elsewhere. |
| `S3_CA_KEY` | `ca.crt` | Key inside `S3_CA_SECRET` holding the PEM bundle. |
| `CH_CA_SECRET_NAME` | `<CLICKHOUSE_NAME>-backup-s3-ca` | Per-release Secret the CA is copied into; referenced by `backup.endpointCA`. Scoped to the instance name so two instances in one namespace don't collide, and stamped with a `cozystack.io/owned-by-demo` label so step 03 refuses to overwrite a Secret it doesn't own and `cleanup.sh` deletes only one this demo created. |
| `CH_BACKUP_CA_SECRET` | *(set by step 03)* | Secret name steps 04 and 07 wire into `backup.endpointCA`. Step 03 sets it to `CH_CA_SECRET_NAME` after copying the CA and leaves it empty when `S3_CA_SECRET=""`. On the admin-copy path, export it yourself to the pre-copied Secret before step 04, since step 03 does no copy and leaves it empty. |

## Prerequisites

- Cozystack cluster with the backup-controller and backupstrategy-controller installed.
- `kubectl`, `jq`, and (for `04`+) the ClickHouse operator deployed by the chart.

### Who can run step 03

Step 03 is otherwise a tenant action, but copying the S3 endpoint CA reads a Secret in `S3_CA_NAMESPACE` (`tenant-root` by default), which is a platform/admin capability rather than a tenant one. Three ways out, in order of preference:

- run step 03 with credentials that can read that namespace (the admin path this demo assumes);
- have an admin copy the CA into `<NAMESPACE>/<CH_CA_SECRET_NAME>` once, then run the demo with `S3_CA_SECRET=""` and `export CH_BACKUP_CA_SECRET="<CH_CA_SECRET_NAME>"` before steps 04 and 07, so both ClickHouse flows wire `backup.endpointCA` to that pre-copied Secret;
- skip it entirely with `S3_CA_SECRET=""` when the S3 endpoint's certificate is signed by a publicly-trusted CA — nothing needs a CA bundle then.

The script distinguishes the three outcomes so a permissions problem does not present as a missing Secret: it stops with an explicit permissions message on `Forbidden`, and on a genuinely absent default (no `seaweedfs-ca-cert`, no seaweedfs `Certificate` to discover, and `S3_CA_SECRET` not set explicitly) it warns and continues with `backup.endpointCA` unset instead of failing — which is the right outcome on a cluster whose endpoint is publicly trusted. Naming a Secret that does not exist is still an error.

[altinity]: https://github.com/Altinity/clickhouse-backup
