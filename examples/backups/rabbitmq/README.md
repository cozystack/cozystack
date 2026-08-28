# RabbitMQ backup/restore demo

This demo backs up and restores a Cozystack-managed RabbitMQ instance through the platform **cozy-default** flow, and proves data integrity by round-tripping a sentinel definition through object storage.

## What is backed up

RabbitMQ's cluster operator ships no backup CRD, and RabbitMQ has no consistent online export of message payloads. This strategy therefore backs up broker **definitions** — vhosts, users, permissions, queues, exchanges, bindings, policies and parameters — via the management HTTP API (`GET /api/definitions`), and restores them with `POST /api/definitions`. **Message payloads are out of scope**, and there is **no point-in-time recovery**: a full-data backup would be a Velero volume snapshot, which is a different strategy.

Definitions import is additive: an in-place restore re-creates dropped definitions but does not delete objects that exist live but not in the backup.

## Prerequisites

The platform default backups stack must be installed: the `backupstrategy-controller`, the `cozy-backups` bucket it provisions, and the `cozy-default-rabbitmq` strategy it ships. No demo `Bucket` is created here — the strategy carries the system-bucket coordinates and the controller projects `cozy-backups-creds` into the namespace before each run.

To-copy restore imports the source's definitions into a separate broker via `POST /api/definitions`, which merges the source's users, permissions and vhosts into the target. It does not clobber the target's own operator-generated default user, because the cluster-operator gives each RabbitmqCluster a distinct random default username. It does, however, import the source's users (with their password hashes), so after a to-copy restore those source credentials are valid logins on the target — grant access accordingly.

Each backup is stored under a per-run key (`<namespace>/<application>/<backup-name>/definitions.json`), so distinct backups of one application do not overwrite each other and a restore reads the exact object its Backup recorded.

## Flow

- `00-helpers.sh` — shared bash helpers (waiters, sentinel seed/check via `rabbitmqctl`).
- `05-rabbitmq-src.yaml` — the source application (no `backup:` block; cozy-default carries it).
- `10-backupjob-adhoc.yaml` — an ad-hoc `BackupJob` routed to `cozy-default`.
- `15-plan.yaml` — a cron `Plan` for scheduled backups.
- `20-rabbitmq-target.yaml` — a separate application for restore-to-copy.
- `25-restorejob-in-place.yaml` / `30-restorejob-to-copy.yaml` — the two restore flows.

Run the whole round-trip:

```bash
./run-all.sh          # backup, drop the sentinel, restore in place, then restore to a copy
./cleanup.sh          # remove this demo's objects (idempotent)
```

`SKIP_RESTORE=1 ./run-all.sh` stops after a successful backup (backup-only smoke). Override `NAMESPACE` (default `tenant-root`) to run elsewhere.
