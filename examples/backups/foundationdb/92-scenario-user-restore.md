# Scenario: tenant restores from a FoundationDB Backup

This narrative covers both restore variants exercised by the demo's
`05..06` scripts. Both are mechanically the same: the driver materialises
a `FoundationDBRestore` CR with `destinationClusterName` pointed at the
target FoundationDB cluster and the same blob-store coordinates the source
Backup wrote with.

## Variants

### In-place — `05-restore-in-place.sh`

- `RestoreJob.spec.targetApplicationRef` is left empty; the driver
  resolves it to `Backup.spec.applicationRef`.
- The FoundationDB operator pauses the source cluster, clears the
  keyspace, and replays the backup via `fdbrestore`. Anything written
  after the backup point is **lost** — this is exactly what an in-place
  restore is supposed to do.

### To-copy — `06-restore-to-copy.sh`

- `RestoreJob.spec.targetApplicationRef` names a freshly-provisioned
  `apps.cozystack.io/FoundationDB` (the script creates `fdb-dst` for
  exactly this purpose).
- The driver creates the `FoundationDBRestore` against
  `destinationClusterName=foundationdb-fdb-dst` (the operator-side cluster
  carries the `foundationdb-` release prefix; the driver applies it
  automatically).
- The source cluster is **not** touched. The verification step in the
  script reads the sentinel key off the destination cluster as the
  positive proof.

## When to pick which

| Variant | Best for |
|---|---|
| `in-place` | Recover from data corruption / accidental deletion on a live cluster you intend to keep using under the same name. |
| `to-copy`  | Disaster-recovery drills, branch databases, side-by-side validation, or migrating to a new FDB version. The source stays online. |

## Verification

For either variant, watch the RestoreJob and the operator-side restore:

```bash
kubectl -n <ns> get restorejobs.backups.cozystack.io <name> -o yaml
kubectl -n <ns> get foundationdbrestores.apps.foundationdb.org \
  -l backups.cozystack.io/owned-by.BackupJobName=<restorejob-name> \
  -l backups.cozystack.io/owned-by.BackupJobNamespace=<ns>
```

> **Heads-up on the label key.** The driver labels both BackupJob-owned
> and RestoreJob-owned operator CRs with the same key
> `backups.cozystack.io/owned-by.BackupJobName` — the value is the
> **RestoreJob** name for `FoundationDBRestore` CRs and the **BackupJob**
> name for `FoundationDBBackup` CRs. If your BackupJob and RestoreJob
> happen to share a name (uncommon in this demo, where they differ
> deliberately), filter additionally by resource kind to disambiguate.
> The companion `BackupJobNamespace` label scopes the match to the
> tenant namespace and is recommended even when names are unique.

A successful restore reports `status.phase: Succeeded` on the RestoreJob
and `status.state: Completed` on the operator-side
`FoundationDBRestore`.
