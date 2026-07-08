# Etcd backup/restore example

> **Heads up — most clusters do not need this walk-through.** Cozystack
> ships a platform-managed `cozy-default` `BackupClass` together with
> the system bucket `cozy-backups`. Tenants reference `cozy-default`
> directly from BackupJob / Plan / RestoreJob without provisioning a
> Bucket or supplying S3 credentials. See
> [Backup Classes](../../../docs/operations/backup-classes.md).
> The walk-through below covers the **legacy** path that wires a
> per-app Bucket and bespoke BackupClass — useful for tuned non-default
> policies.

This directory shows how to back up and restore a Cozystack-managed `Etcd`
application using the cluster's `Etcd` backup strategy driver. The driver
delegates to the [etcd-operator][etcd-operator]: each Cozystack `BackupJob`
materialises an `etcd-operator.cozystack.io/v1alpha2 EtcdSnapshot` CR (a one-shot Job that
streams `etcdctl snapshot save` output to S3), and each `RestoreJob`
performs an in-place restore by suspending the Etcd HelmRelease,
deleting the chart-rendered `EtcdCluster`, and recreating it with
`spec.bootstrap.restore.source.s3` populated from the artefact.

## Supported destinations

This driver supports **S3 destinations only**. The strategy CR's
`spec.template.destination` schema rejects `pvc` at admission time. The
upstream etcd-operator's PVC backup-write path
(`<mount>/<subPath>/<backupName>.db`, plus
`BACKUP_INCLUDE_REVISION` / `BACKUP_TIMESTAMP` suffixes the agent adds at
write time) disagrees with its PVC restore-read path (opens
`<mount>/<subPath>` as a file, or `<mount>/snapshot.db` when subPath is
empty), so a PVC backup taken through this strategy would be
unrestoreable. The S3 path uses the operator's `BackupSnapshot.URI`
status field to recover the full final S3 object key including all
agent-injected suffixes — see
`s3KeyFromArtifactURI` in the driver.

PVC support will land once upstream gains symmetric semantics
(equivalent of `BackupSnapshot.URI` for PVC) or makes the restore-side
read the same path the backup-write side produced.

## Restore semantics

This driver supports **in-place restore only**. To-copy restore is
rejected by the driver with a terminal `phase=Failed`. The PRIMARY
reason is API-level: `RestoreJob.spec.targetApplicationRef` is a
`TypedLocalObjectReference`, which has no namespace field — the
restore target is always the SAME namespace as the source `Backup`,
so there is no API surface today for a "fresh cluster in a different
namespace" flow. A SECONDARY chart-level constraint compounds this:
`packages/extra/etcd/templates/check-release-name.yaml` pins the
Helm release name to `etcd`, so two `apps.cozystack.io/Etcd`
applications cannot coexist in the same namespace regardless of
whether `targetApplicationRef` were extended to cross namespaces.

To get "a fresh cluster with the snapshot's data" today, deploy a
new `Etcd` app in a different namespace and hand-author an
`EtcdCluster` with `spec.bootstrap.restore.source.s3` pointing at
the Backup's S3 coordinates from `Backup.spec.driverMetadata`.
Enabling a managed to-copy flow in this driver would require both
extending `RestoreJob.spec.targetApplicationRef` to a namespaced
reference upstream AND lifting the chart's release-name pin — the
chart change alone is not sufficient.

## Step order

| File | Role | Triggered by |
|---|---|---|
| `00-helpers.sh` | Shared bash helpers and env defaults; sourced by every `01..05` step and by `cleanup.sh`. | n/a |
| `01-create-strategy.sh` | Creates the cluster-scoped `Etcd` strategy. | admin |
| `02-create-bucket.sh` | Provisions a `Bucket`, mints an `<app>-etcd-backup-creds` Secret in the source namespace, and creates the `BackupClass` bound to the Etcd strategy with resolved S3 coordinates. Caches raw creds in `.bucket-info.env` (chmod 600); `cleanup.sh` removes it. | tenant |
| `03-create-etcd-src.sh` | Provisions the source `Etcd` application, waits for `EtcdCluster.status.conditions[Available]=True`, and writes a sentinel key under `etcdctl`. | tenant |
| `04-create-backupjob.sh` | Submits a `BackupJob` and waits for `phase=Succeeded`. | tenant |
| `05-restore-in-place.sh` | Mutates the sentinel, submits a `RestoreJob` (no `targetApplicationRef`), and verifies the sentinel is round-tripped back to its pre-mutation value. | tenant |
| `cleanup.sh` | Best-effort teardown. | admin or tenant |
| `run-all.sh` | Convenience runner: 01..05 in order. | demo |

## Environment variables (overrides)

All variables come from `00-helpers.sh`:

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `tenant-root` | Tenant namespace for the demo. |
| `ETCD_NAME` | `etcd` | Source Etcd application name. Must be `etcd` because the chart pins the release name. |
| `BUCKET_NAME` | `etcd-backups` | Cozystack `Bucket` to provision. |
| `BACKUPCLASS_NAME` | `etcd-default` | BackupClass name (cluster-scoped). |
| `STRATEGY_NAME` | `etcd-strategy-default` | Strategy name (cluster-scoped). |
| `BACKUPJOB_NAME` | `etcd-backup-job` | BackupJob name. |
| `RESTOREJOB_INPLACE_NAME` | `etcd-restore-inplace` | In-place RestoreJob name. |

## Prerequisites

- Cozystack cluster with `backup-controller` and `backupstrategy-controller`
  running, with the Etcd dispatch wired (see
  `internal/backupcontroller/etcdstrategy_controller.go`).
- `kubectl`, `jq`.
- The `etcd-operator.cozystack.io` CRDs installed by
  `packages/system/etcd-operator-crds`. The Cozystack flow specifically depends
  on `EtcdCluster` (the driver re-creates it with
  `spec.bootstrap.restore.source.s3` during in-place restore) and `EtcdSnapshot`
  (one materialised per `BackupJob`). There is no separate `EtcdRestore` CRD —
  restore goes through `EtcdCluster.spec.bootstrap`.
- The Cozystack `etcd-rd` ApplicationDefinition installed so
  `apps.cozystack.io/Etcd` renders a HelmRelease.

## Notes

- The chart's `backup.*` values block (`backup.enabled`, `backup.schedule`,
  `backup.destinationPath`, ...) is marked **DEPRECATED**. It continues to
  render unchanged for existing tenants; new tenants should use this
  `BackupClass` flow instead. See `packages/extra/etcd/README.md`.
- `02-create-bucket.sh` reads the bucket's `region` from the
  `BucketInfo` Secret. SeaweedFS (the default object-storage backend
  for Cozystack `Bucket` resources) does not populate
  `.spec.secretS3.region`, so the script falls back to `us-east-1`. The
  fallback only matters for S3 SDKs that require a non-empty region;
  override it by setting `ETCD_REGION` in your shell before sourcing
  `00-helpers.sh` if you point the demo at a non-SeaweedFS endpoint
  whose region differs.
- Step 05 only verifies the happy path. Edge cases (stuck HR resume, failed bootstrap, mid-restore controller crash) belong in the etcd Chainsaw suite at `hack/e2e-chainsaw/etcd/`.
- The destructive in-place flow deletes the `EtcdCluster` (and the
  operator drops the member PVCs alongside it). All client traffic to
  etcd is unavailable for the duration of the restore. Plan a window.

[etcd-operator]: https://github.com/cozystack/etcd-operator
