# Scenario: tenant runs a FoundationDB backup

This narrative covers the per-tenant backup flow that the demo's
`03..05` scripts encode.

## Goal

Take a Cozystack `Backup` artefact of a running FoundationDB application,
backed by a discrete blob-store directory the operator's `backup_agent`
streamed into.

## Steps

1. **Provision a Bucket and project credentials** — `02-create-bucket.sh`.
   - Creates the Cozystack `Bucket` CR.
   - Reads the resulting `bucket-<name>-backup` BucketInfo Secret to
     extract S3 endpoint + access keys + bucket name.
   - Materialises per-app `<app>-fdb-backup-creds` Secrets in the
     application namespace. The Secret carries a `blob_credentials.json`
     payload in the FoundationDB operator's expected shape:
     ```json
     {
       "accounts": {
         "<api_key>@<endpoint-host>:<port>": {
           "api_key": "<access_key>",
           "secret":  "<secret_key>"
         }
       }
     }
     ```
   - Creates the `BackupClass` bound to the FoundationDB strategy with
     the resolved `accountName`, `bucket`, `region`, and
     `secureConnection` parameters. (Earlier drafts of this demo
     applied a `REPLACE_ME` placeholder version of the BackupClass in
     a separate admin step and patched it here; the placeholder passed
     `MinLength=1` validation but failed at backup_agent runtime, so a
     tenant who skipped the patch step burned the 45-minute backup
     deadline. The fold avoids that trap.)
2. **Provision the FoundationDB and write some data** —
   `03-create-foundationdb-src.sh`.
   - Renders the chart with `backup.enabled=false`: the new BackupClass
     flow is out-of-chart.
   - Writes `/backup-demo/sentinel` so the restore flows can witness the
     value land on a restored cluster.
3. **Submit the BackupJob** — `04-create-backupjob.sh`.
   - The driver creates a `FoundationDBBackup` CR labelled by the
     BackupJob, sets `backupState=Running`, and waits for the operator to
     reconcile + the `backup_agent` to land a full snapshot
     (`status.backupDetails.snapshotTime > 0`).
   - Stamps a Cozystack `Backup` artefact (same name as the BackupJob).
     `Backup.spec.driverMetadata` carries the per-run blob path so the
     RestoreJob path can rebuild the blob-store config from it.

## Verification

```bash
kubectl -n <ns> get backupjobs.backups.cozystack.io <name> -o yaml
kubectl -n <ns> get backups.backups.cozystack.io      <name> -o yaml
kubectl -n <ns> get foundationdbbackups.apps.foundationdb.org \
  -l backups.cozystack.io/owned-by.BackupJobName=<name> \
  -l backups.cozystack.io/owned-by.BackupJobNamespace=<ns>
```

> **Heads-up on the label key.** The driver labels both BackupJob-owned
> and RestoreJob-owned operator CRs with the same key
> `backups.cozystack.io/owned-by.BackupJobName` — the value is the
> **BackupJob** name for `FoundationDBBackup` CRs and the **RestoreJob**
> name for `FoundationDBRestore` CRs. If your BackupJob and RestoreJob
> happen to share a name, filter additionally by resource kind to
> disambiguate. The companion `BackupJobNamespace` label scopes the
> match to the tenant namespace and is recommended even when names are
> unique.

The BackupJob should report `phase: Succeeded`. The operator-side
FoundationDBBackup carries
`status.backupDetails.{running: true, snapshotTime: <int>, url: <s3-url>}`.
