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

The psmdb operator runs the pbm agents when the cluster has `backup.enabled: true`, and services an on-demand `PerconaServerMongoDBBackup` only once the storage the CR names is declared on the cluster. Where that storage comes from depends on the flow the application picked. On the default flow (this example) the chart declares it from the application's own `backup.*` values, and the `cozy-default-mongodb` Strategy only names it (`storageName: s3-storage`); the driver injects nothing. With `backup.useSystemBucket: true` the chart leaves the storage out and the driver injects the platform bucket's coordinates onto the cluster before each backup. Either way the driver surfaces a clear `Ready=False` precondition (rather than a silent hang) when the target cluster has backups disabled; see `docs/operations/backup-classes.md` for both flows.

Restores use `PerconaServerMongoDBRestore.spec.backupSource`, carrying the S3
destination read back from the source backup, so the same artifact restores
in-place **or** into a differently-named instance, and point-in-time recovery is
available via `spec.options.recoveryTime`.

A restore into a differently-named instance copies data and keeps the target's own users. The driver restores only the databases the target application declares under `spec.databases` and leaves the target's system users and Secrets as they are, which is why `20-mongodb-target.yaml` declares the `demo` database the sentinel lives in. A target that declares no database is refused before anything is restored. An in-place restore puts the whole backup back, users included. Either way the `RestoreJob` reports `Succeeded` only once the target `PerconaServerMongoDB` reports ready again after the restore.

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
