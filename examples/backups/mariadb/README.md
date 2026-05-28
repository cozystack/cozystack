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

Replace `REPLACE_WITH_*` placeholders in `10-mariadb-strategy.yaml`
(bucket, S3 endpoint) and `05-mariadb-src.yaml` (password) before
applying.

The MariaDB driver delegates execution to the open-source
[mariadb-operator](https://github.com/mariadb-operator/mariadb-operator)
(`k8s.mariadb.com` CRDs, already shipped with Cozystack). Backups
materialise as `Backup` CRs and restores as `Restore` CRs in the
application's namespace; the operator handles the actual `mariadb-dump`
/ `mariadb-import` invocations.

Same-namespace flows are smoke-testable manually with `kubectl apply -f`.
Cross-tenant restores are blocked by the per-tenant Cilium egress policy
and stay a manual flow. A dedicated bats e2e harness will land in a
follow-up PR.
