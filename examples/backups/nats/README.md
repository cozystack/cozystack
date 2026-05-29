# NATS JetStream backup/restore example

This directory shows how to back up and restore a Cozystack-managed `NATS`
application using the **generic `Job` backup strategy**. Unlike the
app-specific drivers (Altinity for ClickHouse, CNPG for Postgres, ...), the
`Job` strategy has no built-in knowledge of the application: it runs a Pod the
operator supplies. Here that Pod is a stock [`natsio/nats-box`][natsbox] image
(which carries the `nats` CLI plus `curl` and `tar`) running one shell script:

- **backup**: `nats stream backup` → `tar` → `PUT` the tarball to S3.
- **restore**: `GET` the tarball from S3 → `tar -x` → `nats stream restore`.

The same `PodTemplateSpec` serves both directions. The strategy engine
templates each string field independently — it cannot add or remove containers
per mode — so the single image branches at runtime on `{{ .Mode }}` (rendered
into the `MODE` env var). Backup pushes to a key scoped by the source app
(`{{ .Release.Name }}`); restore reads the key scoped by the backup's source
(`{{ .Backup.ApplicationRef.Name }}`), so a to-copy restore lands the source's
data into a differently-named target.

The upload uses `curl --aws-sigv4`, so no purpose-built backup image is needed
— a stock client image plus a shell one-liner is the whole driver. This is the
point of the generic `Job` strategy: back up an app that has no dedicated
driver, with tools you already have.

## Step order

| File | Role | Triggered by |
|---|---|---|
| `00-helpers.sh` | Shared bash helpers, env defaults, and the `nats`/S3-secret helpers; sourced by every step. | n/a |
| `01-create-strategy.sh` | Creates the cluster-scoped `Job` strategy (the nats-box `PodTemplateSpec`). | admin |
| `02-create-backupclass.sh` | Maps `apps.cozystack.io/NATS` to that strategy, with `stream`/`natsUser` parameters. | admin |
| `03-create-bucket.sh` | Provisions a `Bucket` and caches its S3 coordinates into `.bucket-info.env` (chmod 600; raw access keys). `cleanup.sh` removes this file. | tenant |
| `04-create-nats.sh` | Provisions a `NATS` instance, creates the `<app>-backup-s3` Secret, seeds a JetStream stream with sentinel messages. | tenant |
| `05-create-backupjob.sh` | Submits a `BackupJob` and waits for Succeeded. | tenant |
| `06-restore-in-place.sh` | Deletes the stream and restores it into the same instance via `RestoreJob`. | tenant |
| `07-restore-to-copy.sh` | Provisions a second `NATS` and restores into it via `RestoreJob.spec.targetApplicationRef`. | tenant |
| `cleanup.sh` | Removes everything created by the demo. | admin or tenant |
| `run-all.sh` | Convenience runner that executes 01..07 in order. | demo |
| `90-scenario-admin-prepare.md` | Narrative for the admin preparation steps. | docs |
| `91-scenario-user-backup.md` | Narrative for the user backup flow. | docs |
| `92-scenario-user-restore.md` | Narrative for the user restore flow (in-place and to-copy). | docs |

## Environment variables (overrides)

All variables come from `00-helpers.sh`:

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `tenant-test` | Tenant namespace for the demo. |
| `NATS_NAME` | `nats-test` | Source NATS application name. |
| `NATS_RESTORE_NAME` | `nats-restore` | Target NATS for the to-copy restore. |
| `NATS_USER` | `backup` | NATS user the strategy authenticates as. |
| `NATS_PASSWORD` | `jetstream-demo-pw` | Password set on that user (kept fixed for a deterministic demo). |
| `STREAM_NAME` | `ORDERS` | JetStream stream to back up. |
| `MESSAGE_COUNT` | `10` | Sentinel messages published before backup. |
| `BUCKET_NAME` | `nats-backups` | Cozystack `Bucket` to provision. |
| `BACKUPCLASS_NAME` | `nats-backup` | BackupClass name (cluster-scoped). |
| `STRATEGY_NAME` | `nats-job` | Strategy name (cluster-scoped). |
| `BACKUPJOB_NAME` | `nats-backup-job` | BackupJob name. |
| `RESTOREJOB_INPLACE_NAME` | `nats-restore-inplace` | In-place RestoreJob name. |
| `RESTOREJOB_TOCOPY_NAME` | `nats-restore-to-copy` | To-copy RestoreJob name. |
| `NATS_BOX_IMAGE` | `natsio/nats-box:0.14.5` | Image with the `nats` CLI used by the strategy and the seed/verify helpers. |

## Prerequisites

- Cozystack cluster with the backup-controller and backupstrategy-controller installed.
- `kubectl` and `jq` locally.
- Egress to pull `natsio/nats-box` (used both by the strategy Pod and by the demo's seed/verify helpers).

## Notes / caveats

- **Not run in CI.** Like every other directory under `examples/backups/`, this
  demo is a runnable reference, not an automated regression test — the e2e
  harness (`hack/e2e-apps/*.bats`) does not exercise backup examples. Run it by
  hand against a cluster.
- **`nats` CLI specifics.** The strategy invokes `nats stream backup`/`nats
  stream restore`; flag names can vary across `nats` CLI versions. Pin
  `NATS_BOX_IMAGE` to a version whose CLI matches if the defaults drift.
- **Self-signed S3.** The COSI/seaweedfs S3 endpoint uses an internal CA, so
  the strategy passes `-k` to `curl`. A production strategy should mount the
  tenant CA and drop `-k`.
- **Definitions vs. data.** This backs up a JetStream stream's messages and
  configuration via `nats stream backup`; it does not capture NATS account or
  KV/object-store state beyond the named stream.

[natsbox]: https://github.com/nats-io/nats-box
