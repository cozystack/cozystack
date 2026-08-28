# Scenario: tenant restores a Redis backup

Both flows drive from the same `Backup` (named after the BackupJob).

## In-place

Restore into the source app — the driver FLUSHALLs the master and replays the RDB:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: redis-restore-inplace
  namespace: tenant-test
spec:
  backupRef:
    name: redis-backup-job
```

## To-copy

Deploy a second, differently-named `Redis` first, then restore into it with `targetApplicationRef`. The Job connects to the target's master but reads the object keyed by the SOURCE app name (`.Backup.ApplicationRef.Name`), so the source keeps running untouched:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: redis-restore-to-copy
  namespace: tenant-test
spec:
  backupRef:
    name: redis-backup-job
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: Redis
    name: redis-restore
```

`03-restore-in-place.sh` and `04-restore-to-copy.sh` automate these and assert the seeded marker round-tripped. There is no PITR: RDB is a discrete snapshot, so `spec.options.recoveryTime` is unsupported.
