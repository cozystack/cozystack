# PostgreSQL backup & restore (barman-cloud plugin)

End-to-end demo of backing up a cozystack `Postgres` application to S3 and restoring it, using the CloudNativePG **barman-cloud plugin** (the native `spec.backup.barmanObjectStore` path is deprecated in CNPG 1.27 and removed in 1.29). Backups flow through the platform `BackupClass` + `CNPG` strategy so one strategy CR serves every `Postgres` instance in the tenant.

## What it provisions

The numbered manifests are the documented flow; a human can read and apply them by hand, and `run-all.sh` drives the same files for a smoke test.

- `00-bucket.yaml` — a cozystack-managed S3 `Bucket` for the backups, with a `backup` user.
- `05-postgres-src.yaml` — the source `Postgres`, with `backup.enabled: true` so `archive_command` streams WALs from helm-install onwards.
- `10-cnpg-strategy.yaml` — the `CNPG` strategy templated against the live application.
- `15-backupclass.yaml` — maps `Kind=Postgres` to that strategy.
- `20-plan.yaml` — an optional cron `Plan` for recurring backups.
- `25-backupjob-adhoc.yaml` — an ad-hoc `BackupJob`.
- `30-postgres-target.yaml` + `40-restorejob-to-copy.yaml` — restore into a fresh copy, leaving the source running.
- `35-restorejob-in-place.yaml` — the destructive in-place restore variant.
- `45-restorejob-pitr.yaml` — point-in-time recovery: restore into the copy as of a specific `spec.options.recoveryTime` (RFC3339).

## Placeholders and derived Secrets

`05-postgres-src.yaml` and `10-cnpg-strategy.yaml` carry `REPLACE_WITH_COSI_BUCKET_NAME`, `REPLACE_WITH_S3_ENDPOINT` and `REPLACE_WITH_PASSWORD`. They also reference two Secrets, `<app>-cnpg-backup-creds` (the S3 credentials the barman-cloud sidecar reads) and `<app>-cnpg-backup-ca` (the CA it trusts for a self-signed endpoint). The strategy template renders those names against whichever application it drives, so the restore target needs its own pair too — a restore against `pg-target` looks up `pg-target-cnpg-backup-*`. `run-all.sh` resolves the placeholders from the provisioned `Bucket`'s `BucketInfo` Secret and materialises the pair for both the source and the target; editing by hand, you copy the coordinates from `kubectl -n <ns> get secret bucket-<name>-backup -o jsonpath='{.data.BucketInfo}'` and the CA from the seaweedfs CA secret in `tenant-root` (`seaweedfs-ca-cert` by default; `run-all.sh` auto-discovers it from the cert-manager Certificate if the name differs).

Drop the `endpointCA` block from `05`/`10` (and set `S3_CA_SECRET=""` for `run-all.sh`) when the S3 endpoint is signed by a publicly-trusted CA.

## Run it

```sh
# Defaults to NAMESPACE=tenant-root; override for a tenant namespace.
NAMESPACE=tenant-root examples/backups/postgres/run-all.sh
# Tear everything down afterwards (idempotent).
NAMESPACE=tenant-root examples/backups/postgres/cleanup.sh
```

`run-all.sh` writes a sentinel row into the source, waits for the `BackupJob` to reach `Succeeded`, restores to a copy, and asserts the sentinel round-tripped through S3 into the restored copy. It then runs a **point-in-time recovery**: it writes a `before` marker, captures the server timestamp, writes an `after` marker, and restores the copy to that timestamp (`45-restorejob-pitr.yaml`), asserting the `before` row survived and the `after` row did not. Finally it submits a RestoreJob with a `recoveryTime` an hour in the future and asserts it **fails fast** with `status.phase: Failed` and reason `RecoveryTargetUnreachable` (rather than hanging to the restore deadline). Set `SKIP_RESTORE=1` to stop after a successful backup, or `SKIP_PITR=1` to stop after the latest-point restore.

## Automated e2e

The Chainsaw suite at `hack/e2e-chainsaw/postgres/` drives this same `run-all.sh` as a second test (`postgres-2-backup-roundtrip`), selected by CI whenever the postgres app (or these scripts) change and on every release cut. It runs in `tenant-root` against the in-cluster seaweedfs endpoint — the isolated e2e tenant cannot reach it across the Cilium egress policy, and the external ingress endpoint is an unroutable placeholder in the sandbox.
