# ClickHouse Altinity backup E2E

Chainsaw suite for the ClickHouse Altinity backup/restore flow (`strategy.backups.cozystack.io/v1alpha1` `Altinity` + `backups.cozystack.io/v1alpha1` `BackupJob`/`RestoreJob`). It replaces the never-merged `hack/e2e-apps/backup-clickhouse.bats` from cozystack#2600 with the current Chainsaw approach, and is split into two Tests run in name order — the same shape as the `etcd` suite.

## `clickhouse-backup-1-contracts` (runs in CI)

Deploys a single-shard, single-replica ClickHouse with `backup.enabled: true` and asserts the contract the Altinity strategy depends on, against the running cluster rather than the chart templates:

- the HelmRelease and StatefulSet become Ready;
- the ClickHouseInstallation Pod carries the `clickhouse-backup` sidecar exposing the container port named `ch-backup-api` (`:7171`), the HTTP API the strategy Pod drives;
- the chart-emitted `clickhouse-test-backup-backup-api-auth` Secret exists and is populated (the credentials both the sidecar and the strategy Pod read).

Placeholder S3 coordinates are intentional: `clickhouse-backup server` starts its API without validating S3, so this Test needs no reachable object store. If a chart bump renames the sidecar container, moves the port, or renames the auth Secret, every ClickHouse `BackupJob` breaks silently — this Test catches it on every PR.

## `clickhouse-backup-2-roundtrip` (opt-in)

The full round-trip: `Bucket` → `Altinity` strategy + `BackupClass` → backup-enabled source ClickHouse (with a sentinel write) → `BackupJob` (waits `Succeeded`, emits a `Backup`) → in-place `RestoreJob` → to-copy `RestoreJob` into a second ClickHouse. It drives `examples/backups/clickhouse/run-all.sh` as the harness, so the test and the documented flow cannot drift.

It is **gated out of CI**. The S3 leg cannot complete in the `kind` e2e sandbox: the in-cluster `seaweedfs-s3.<ns>.svc:8333` endpoint serves HTTPS with a self-signed cert, and in CI the `Bucket`'s external endpoint is the `s3.example.org` placeholder — neither routable nor TLS-trustable from the in-Pod `clickhouse-backup` sidecar. Chainsaw v0.2.15 has no conditional Test-level skip, so the gate is an in-script `exit 0` that logs the skip reason.

## Running

The contracts Test runs as part of the normal suite:

```sh
chainsaw test hack/e2e-chainsaw/clickhouse-backup
```

To run the round-trip too, opt in on a cluster with a publicly-trusted S3 endpoint:

```sh
CLICKHOUSE_E2E_S3_ROUNDTRIP=1 chainsaw test hack/e2e-chainsaw/clickhouse-backup
```

Both Tests use the `tenant-test` namespace configured in `hack/e2e-chainsaw/.chainsaw.yaml`.

## Cleanup

The contracts Test lets Chainsaw delete the ClickHouse it applied. The round-trip Test creates its resources through the example scripts (not Chainsaw `apply`) and also produces controller-owned artifacts — the `Backup` CR and the cluster-scoped `Altinity` strategy + `BackupClass` — so it tears them down itself in a `finally` running `examples/backups/clickhouse/cleanup.sh` (idempotent, and a no-op when the round-trip was gated out).
