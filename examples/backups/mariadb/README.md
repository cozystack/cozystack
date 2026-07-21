# MariaDB backup / restore example

> **Heads up — most clusters do not need this walk-through.** Cozystack
> ships a platform-managed `cozy-default` `BackupClass` together with
> the system bucket `cozy-backups`. Tenants reference `cozy-default`
> directly from BackupJob / Plan / RestoreJob without provisioning a
> Bucket or supplying S3 credentials. See
> [Backup Classes](../../../docs/operations/backup-classes.md).
> The walk-through below covers the **legacy** path that wires a
> per-app Bucket and bespoke BackupClass — useful for tuned non-default
> policies (custom retention, encryption, secondary buckets).

End-to-end example for backing up and restoring a Cozystack-managed
MariaDB application via the `strategy.backups.cozystack.io/v1alpha1`
`MariaDB` strategy driver. Files are numbered so that
`kubectl apply -f` order matches the dependency graph.

| Step | File | Scope | Purpose |
| --- | --- | --- | --- |
| 0 | `00-bucket.yaml` | namespaced | COSI-backed S3 bucket + `backup` user. |
| 1 | `05-mariadb-src.yaml` | namespaced | Source MariaDB application. |
| 2 | `10-mariadb-strategy.yaml` | cluster | Templated `Backup`-CR shape. |
| 3 | `15-backupclass.yaml` | cluster | Map `apps.cozystack.io/MariaDB` to the strategy. |
| 4 | `20-plan.yaml` | namespaced | Cron schedule (every 6h). |
| 5 | `25-backupjob-adhoc.yaml` | namespaced | One-shot BackupJob for smoke testing. |
| 6 | `30-mariadb-target.yaml` | namespaced | Empty MariaDB target for to-copy restore. |
| 7 | `35-restorejob-in-place.yaml` | namespaced | Destructive restore back into the source. |
| 8 | `40-restorejob-to-copy.yaml` | namespaced | Non-destructive restore into the target. |

The MariaDB driver delegates execution to the open-source [mariadb-operator](https://github.com/mariadb-operator/mariadb-operator) (`k8s.mariadb.com` CRDs, already shipped with Cozystack). Backups materialise as `Backup` CRs and restores as `Restore` CRs in the application's namespace; the operator handles the actual `mariadb-dump` / `mariadb-import` invocations.

## Placeholders and derived Secrets

`05-mariadb-src.yaml` / `30-mariadb-target.yaml` carry `REPLACE_WITH_PASSWORD` and `10-mariadb-strategy.yaml` carries `REPLACE_WITH_COSI_BUCKET_NAME` and `REPLACE_WITH_S3_ENDPOINT` (a path-style endpoint without scheme). The strategy also references two Secrets, `<app>-mariadb-backup-creds` (the S3 credentials the mariadb-operator Backup reads) and `<app>-mariadb-backup-ca` (the CA it trusts for a self-signed endpoint), where `<app>` is the Cozystack application name. The strategy template renders those names against whichever application it drives, so the restore target needs its own pair too — a restore into `mariadb-target` looks up `mariadb-target-mariadb-backup-*`. `run-all.sh` resolves the placeholders from the provisioned `Bucket`'s `BucketInfo` Secret and materialises the pair for both the source and the target; editing by hand, you copy the coordinates from `kubectl -n <ns> get secret bucket-<bucket>-backup -o jsonpath='{.data.BucketInfo}'` and the CA from the seaweedfs CA secret in `tenant-root` (`seaweedfs-ca-cert` by default; `run-all.sh` auto-discovers it from the cert-manager Certificate if the name differs).

Drop the `tls` block from `10-mariadb-strategy.yaml` (and set `S3_CA_SECRET=""` for `run-all.sh`) when the S3 endpoint is signed by a publicly-trusted CA.

## Run it

```sh
# Defaults to NAMESPACE=tenant-root; override for a tenant namespace.
NAMESPACE=tenant-root examples/backups/mariadb/run-all.sh
# Tear everything down afterwards (idempotent).
NAMESPACE=tenant-root examples/backups/mariadb/cleanup.sh
```

`S3_CA_NAMESPACE` defaults to `tenant-root` (where the shared seaweedfs and its CA live) independently of `NAMESPACE`. Running against a different tenant whose own seaweedfs CA lives elsewhere means overriding `S3_CA_NAMESPACE` (and `S3_CA_SECRET`) too, alongside `NAMESPACE`.

`run-all.sh` writes a sentinel row into the source, waits for the `BackupJob` to reach `Succeeded`, restores to a copy with a to-copy `RestoreJob`, and asserts the sentinel round-tripped through S3 into the restored copy while the source is left untouched. Set `SKIP_RESTORE=1` to stop after a successful backup.

Same-namespace flows are the supported path. Cross-tenant restores (target in `tenant-test`, source's seaweedfs in `tenant-root`) are blocked by the per-tenant Cilium egress policy and stay a manual / dev-cluster flow.

## Automated e2e

The Chainsaw suite at `hack/e2e-chainsaw/mariadb/` drives this same `run-all.sh` as a second test (`mariadb-2-backup-roundtrip`), selected by Test-Impact Analysis whenever the mariadb app or the suite changes, and on every release cut. It runs in `tenant-root` against the in-cluster seaweedfs endpoint — the isolated e2e tenant cannot reach it across the Cilium egress policy, and the external ingress endpoint is an unroutable placeholder in the sandbox.
