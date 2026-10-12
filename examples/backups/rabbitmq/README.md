# RabbitMQ backup/restore demo

This demo backs up and restores a Cozystack-managed RabbitMQ instance, proving data integrity by round-tripping a sentinel definition through object storage. It provisions its own `Bucket`, `Rabbitmq` strategy and `BackupClass` so the round-trip is self-contained; the platform ships an equivalent `cozy-default-rabbitmq` strategy that writes to the shared `cozy-backups` system bucket, which you can route to from your own `BackupClass` instead.

## What is backed up

RabbitMQ's cluster operator ships no backup CRD, and RabbitMQ has no consistent online export of message payloads. This strategy therefore backs up broker **definitions** — vhosts, users, permissions, queues, exchanges, bindings, policies and parameters — via the management HTTP API (`GET /api/definitions`), and restores them with `POST /api/definitions`. **Message payloads are out of scope**, and there is **no point-in-time recovery**: a full-data backup would be a Velero volume snapshot, which is a different strategy.

Definitions import is additive: an in-place restore re-creates dropped definitions but does not delete objects that exist live but not in the backup.

## Prerequisites

The `backupstrategy-controller` and the backup CRDs must be installed (the platform default backups stack). `run-all.sh` provisions its own `Bucket`, reads the S3 coordinates from its `BucketInfo`, projects them into a `rabbitmq-backup-creds` Secret, copies the endpoint's CA into `rabbitmq-backup-ca`, and reuses the container image the shipped `cozy-default-rabbitmq` strategy runs — so that strategy must be present, but this demo does not write to the system bucket.

To-copy restore imports the source's definitions into a separate broker via `POST /api/definitions`, which merges the source's users, permissions and vhosts into the target. It does not clobber the target's own operator-generated default user, because the cluster-operator gives each RabbitmqCluster a distinct random default username. It does, however, import the source's users (with their password hashes), so after a to-copy restore those source credentials are valid logins on the target — grant access accordingly.

Each backup is stored under a per-run key (`<namespace>/<application>/<backup-name>/definitions.json`), so distinct backups of one application do not overwrite each other and a restore reads the exact object its Backup recorded.

## Flow

- `00-helpers.sh` — shared bash helpers (waiters, sentinel seed/check via `rabbitmqctl`).
- `00-bucket.yaml` — the demo `Bucket` backing the round-trip.
- `03-rabbitmq-strategy.yaml` — the `Rabbitmq` strategy (S3 coordinates filled from the Bucket by `run-all.sh`).
- `04-backupclass.yaml` — the `rabbitmq-default` `BackupClass` mapping `RabbitMQ` to that strategy.
- `05-rabbitmq-src.yaml` — the source application (no `backup:` block; the strategy carries the S3 coordinates).
- `10-backupjob-adhoc.yaml` — an ad-hoc `BackupJob` routed to `rabbitmq-default`.
- `15-plan.yaml` — a cron `Plan` for scheduled backups.
- `20-rabbitmq-target.yaml` — a separate application for restore-to-copy.
- `25-restorejob-in-place.yaml` / `30-restorejob-to-copy.yaml` — the two restore flows.

Run the whole round-trip:

```bash
./run-all.sh          # backup, drop the sentinel, restore in place, then restore to a copy
./cleanup.sh          # remove this demo's objects (idempotent)
```

`SKIP_RESTORE=1 ./run-all.sh` stops after a successful backup (backup-only smoke). Override `NAMESPACE` (default `tenant-root`) to run elsewhere. `BucketInfo` advertises the external S3 ingress endpoint, which in-cluster Pods cannot always reach or TLS-validate; set `S3_ENDPOINT=https://seaweedfs-s3.<ns>.svc:8333` to target the in-cluster SeaweedFS instead — the `.svc` FQDN is the name the SeaweedFS serving cert's SAN covers, so `curl` can verify TLS against the copied CA (the e2e harness does this).
