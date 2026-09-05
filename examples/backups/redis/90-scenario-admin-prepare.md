# Scenario: platform admin prepares Redis backups

The `Redis` → `cozy-default-redis` binding ships in the `cozy-default` BackupClass, so an admin does not wire anything per-tenant. What must be in place cluster-wide:

1. `backupstrategy-controller` is running and its system bucket (`cozy-backups`) is provisioned, so the `bucketName` helper resolves and the gated `cozy-default-redis` Strategy CR materialises.
2. The controller projects `cozy-backups-creds` into a tenant namespace on the first `Redis` BackupJob there — no manual Secret.
3. For an `https://` S3 endpoint with a self-signed cert, set `backupStorage.endpointCASecretName` to a Secret (key `ca.crt`) the strategy Pod can mount in the app namespace; the platform default endpoint is `http://` and needs none.

Once that holds, tenants use the flow in `91-scenario-user-backup.md` and `92-scenario-user-restore.md`.
