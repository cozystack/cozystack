# Scenario: admin prepares the FoundationDB backup strategy

This narrative covers the one-time admin setup that lets tenants in the
cluster back up `apps.cozystack.io/FoundationDB` applications without
authoring strategy CRDs themselves.

## Goal

- Make a cluster-scoped `strategy.backups.cozystack.io/FoundationDB`
  available.
- Hand off to tenants so they can provision a `Bucket`, materialise the
  matching `BackupClass`, and `kubectl apply` a `BackupJob` against any
  FoundationDB instance they own.

## Steps

1. **Apply the strategy** — `01-create-strategy.sh`.
   - Renders a `FoundationDB` strategy CR that templates blob-store
     coordinates from BackupClass parameters and references a per-app
     `{{ .Application.metadata.name }}-fdb-backup-creds` Secret in the
     application namespace.

The matching `BackupClass` is *not* applied by the admin step. Tenants
apply it in `02-create-bucket.sh` with the bucket coordinates already
resolved from the Bucket's BucketInfo Secret. An earlier draft of this
demo split BackupClass creation into a separate admin step that wrote
`accountName: "REPLACE_ME"` first and patched the real value later —
the placeholder passed the operator's `MinLength=1` validation but
failed at backup_agent runtime, so a tenant who skipped the patch step
burned the 45-minute backup deadline. The current flow avoids that
trap by creating the BackupClass exactly once, with the real
coordinates baked in.

## Verification

```bash
kubectl get foundationdbs.strategy.backups.cozystack.io
kubectl get backupclasses.backups.cozystack.io foundationdb-default
```

The strategy CR should report no error conditions, and the BackupClass
should list one strategy entry pointing at it.
