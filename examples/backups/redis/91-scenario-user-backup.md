# Scenario: tenant takes a Redis backup

A tenant with a `Redis` app named `redis-test` backs it up by creating one `BackupJob` that points at the platform `cozy-default` BackupClass:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: redis-backup-job
  namespace: tenant-test
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Redis
    name: redis-test
  backupClassName: cozy-default
```

The controller resolves it to `cozy-default-redis`, projects `cozy-backups-creds`, and runs a Job that discovers the current master via sentinel, dumps its RDB, and uploads it to `s3://<bucket>/<namespace>/redis-test/dump.rdb`. When `status.phase` is `Succeeded`, `status.backupRef.name` names the `Backup` artifact the restore flow consumes. `02-create-backupjob.sh` automates this.
