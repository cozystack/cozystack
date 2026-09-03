# MongoDB backup / restore example

End-to-end example for backing up and restoring a Cozystack-managed MongoDB
application via the `strategy.backups.cozystack.io/v1alpha1` `MongoDB` strategy
driver. The driver delegates execution to the
[Percona Server for MongoDB operator](https://docs.percona.com/percona-operator-for-mongodb/)
(`psmdb.percona.com` CRDs, already shipped with Cozystack): backups materialise
as `PerconaServerMongoDBBackup` CRs and restores as `PerconaServerMongoDBRestore`
CRs in the application's namespace; the operator handles the actual
percona-backup-mongodb (pbm) logical dump / restore.

Files are numbered so that `kubectl apply -f` order matches the dependency graph.

| Step | File | Scope | Purpose |
| --- | --- | --- | --- |
| 0 | `00-bucket.yaml` | namespaced | COSI-backed S3 bucket + `backup` user. |
| 1 | `05-mongodb-src.yaml` | namespaced | Source MongoDB application (backups enabled). |
| 2 | `10-backupjob-adhoc.yaml` | namespaced | One-shot BackupJob for smoke testing. |
| 3 | `15-plan.yaml` | namespaced | Cron schedule (every 6h). |
| 4 | `20-mongodb-target.yaml` | namespaced | Empty MongoDB target for to-copy restore. |
| 5 | `25-restorejob-in-place.yaml` | namespaced | Destructive restore back into the source. |
| 6 | `30-restorejob-to-copy.yaml` | namespaced | Non-destructive restore into the target. |

## How MongoDB backups differ from the other drivers

The psmdb operator only services on-demand `PerconaServerMongoDBBackup` CRs when
the cluster has the pbm agents running and a storage declared — which is what
`backup.enabled: true` on the MongoDB application wires up. Unlike the MariaDB /
CNPG drivers, the strategy does **not** inject the storage into the cluster; it
only names it (`storageName: s3-storage`, the storage the chart declares) via
the platform's `cozy-default-mongodb` Strategy. So the application must opt into
backups (`backup.enabled: true`) with the bucket coordinates in its `backup.*`
values. The driver surfaces a clear `Ready=False` precondition (rather than a
silent hang) when the target cluster has backups disabled.

Restores use `PerconaServerMongoDBRestore.spec.backupSource`, carrying the S3
destination read back from the source backup, so the same artifact restores
in-place **or** into a differently-named instance, and point-in-time recovery is
available via `spec.options.recoveryTime`.

## Placeholders

`05-mongodb-src.yaml` / `20-mongodb-target.yaml` carry `REPLACE_WITH_PASSWORD`,
`REPLACE_WITH_COSI_BUCKET_NAME`, `REPLACE_WITH_S3_ENDPOINT` (a URL *with* scheme,
e.g. `https://seaweedfs-s3.tenant-root:8333`), and `REPLACE_WITH_S3_ACCESS_KEY` /
`REPLACE_WITH_S3_SECRET_KEY`. `run-all.sh` resolves them from the provisioned
`Bucket`'s `BucketInfo` Secret. `insecureSkipTLSVerify: true` lets pbm reach
Cozystack's default self-signed in-cluster seaweedfs — the psmdb s3 storage has
no CA-bundle field (unlike the MariaDB driver's `tls.caSecretKeyRef`), so
skipping verification is the supported way to trust it. Drop it (default
`false`) when the S3 endpoint is signed by a publicly-trusted CA.

## Point-in-time recovery

psmdb records an oplog stream between logical backups (pitr). To restore to a
timestamp instead of the plain snapshot, add `recoveryTime` (RFC3339, the same
option the CNPG/Postgres driver uses) to a RestoreJob:

```yaml
spec:
  options:
    recoveryTime: "2026-08-05T12:34:56Z"   # RFC3339 (UTC)
```

An empty/omitted `recoveryTime` restores the backup snapshot as taken. An
unrecognised key under `spec.options` is ignored but reported as a
`UnknownRestoreOption` Warning event on the RestoreJob.

## Run it

```sh
# Defaults to NAMESPACE=tenant-root; override for a tenant namespace.
NAMESPACE=tenant-root examples/backups/mongodb/run-all.sh
# Tear everything down afterwards (idempotent).
NAMESPACE=tenant-root examples/backups/mongodb/cleanup.sh
```

`run-all.sh` writes a sentinel document into the source, waits for the
`BackupJob` to reach `Succeeded`, restores to a copy with a to-copy `RestoreJob`,
and asserts the sentinel round-tripped through S3 into the restored copy while
the source is left untouched. Set `SKIP_RESTORE=1` to stop after a successful
backup.

The to-copy restore currently ends with the target holding the source cluster's system users, so the target's own credentials stop authenticating and that last assertion fails; #3832 tracks it, and `SKIP_RESTORE=1` stops before that step.

Same-namespace flows are the supported path. Cross-tenant restores (target in
`tenant-test`, source's seaweedfs in `tenant-root`) are blocked by the per-tenant
Cilium egress policy and stay a manual / dev-cluster flow.

## Automated e2e

The Chainsaw suite at `hack/e2e-chainsaw/mongodb/` drives this same `run-all.sh`
as a second test (`mongodb-2-backup-roundtrip`), selected by Test-Impact
Analysis whenever the mongodb app or the suite changes, and on every release
cut. It runs in `tenant-root` against the in-cluster seaweedfs endpoint — the
isolated e2e tenant cannot reach it across the Cilium egress policy, and the
external ingress endpoint is an unroutable placeholder in the sandbox.

It drives `run-all.sh` with `SKIP_RESTORE=1` and stops once the BackupJob succeeds, so the restore steps are not covered by CI while #3832 is open: a to-copy restore replays the source cluster's system users into the target, after which the target's own credentials no longer authenticate and the sentinel cannot be read back.
