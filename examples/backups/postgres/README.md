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

## Placeholders and derived Secrets

`05-postgres-src.yaml` and `10-cnpg-strategy.yaml` carry `REPLACE_WITH_COSI_BUCKET_NAME`, `REPLACE_WITH_S3_ENDPOINT` and `REPLACE_WITH_PASSWORD`. They also reference two Secrets, `<app>-cnpg-backup-creds` (the S3 credentials the barman-cloud sidecar reads) and `<app>-cnpg-backup-ca` (the CA it trusts for a self-signed endpoint). `run-all.sh` resolves the placeholders from the provisioned `Bucket`'s `BucketInfo` Secret and materialises both Secrets; editing by hand, you copy the coordinates from `kubectl -n <ns> get secret bucket-<name>-backup -o jsonpath='{.data.BucketInfo}'` and the CA from `seaweedfs-system-ca-cert` in `tenant-root`.

Drop the `endpointCA` block from `05`/`10` (and set `S3_CA_SECRET=""` for `run-all.sh`) when the S3 endpoint is signed by a publicly-trusted CA.

## Run it

```sh
# Defaults to NAMESPACE=tenant-root; override for a tenant namespace.
NAMESPACE=tenant-root examples/backups/postgres/run-all.sh
# Tear everything down afterwards (idempotent).
NAMESPACE=tenant-root examples/backups/postgres/cleanup.sh
```

`run-all.sh` writes a sentinel row into the source, waits for the `BackupJob` to reach `Succeeded`, restores to a copy, and asserts the sentinel round-tripped through S3 into the restored copy. Set `SKIP_RESTORE=1` to stop after a successful backup.

## Automated e2e

The Chainsaw suite at `hack/e2e-chainsaw/postgres/` drives this same `run-all.sh` as a second, opt-in test (`postgres-2-backup-roundtrip`). It is gated out of CI because kind has no reachable, CA-trusted S3 endpoint; run it on a real cluster with `POSTGRES_E2E_S3_ROUNDTRIP=1`.
