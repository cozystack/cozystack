# Scenario: tenant restores Etcd in-place from a backup

This narrative describes the destructive in-place restore path: the
running `EtcdCluster` is replaced with a fresh one bootstrapped from a
snapshot in S3. All client traffic to etcd is unavailable for the
duration of the restore.

## 1. Submit the RestoreJob (`05-restore-in-place.sh`)

A `RestoreJob` references the `Backup` artefact produced in step 04.
Crucially, `spec.targetApplicationRef` is left **empty** — that's what
selects the in-place flow. With `targetApplicationRef` set the driver
would reject the RestoreJob with `phase=Failed`, because the chart's
`packages/extra/etcd/templates/check-release-name.yaml` pins the Helm
release name to `etcd` (so two `Etcd` apps cannot coexist in one
namespace) and `targetApplicationRef` is a same-namespace reference.

## 2. What the driver does

1. **Suspend the HelmRelease.** `spec.suspend: true` on
   `helm.toolkit.fluxcd.io/v2 HelmRelease "etcd"` stops Flux from
   re-rendering the chart while the driver mutates the live cluster.
2. **Snapshot the chart-rendered EtcdCluster spec.** The driver reads
   the live `etcd.aenix.io/EtcdCluster "etcd"` spec and stashes it on
   `RestoreJob.status.conditions[EtcdClusterSpecCaptured].message`.
   This makes the destructive flow controller-crash-safe: a restart
   between "snapshot taken" and "cluster recreated" still has the spec
   to rebuild from.
3. **Delete the EtcdCluster.** The operator's finalizers tear down the
   pods + PVCs.
4. **Wait for full deletion.** The driver gates on both the
   `EtcdCluster` being gone AND the per-instance PVCs being gone (a
   half-deleted cluster fights the recreate step's name collision).
5. **Recreate the EtcdCluster with `bootstrap.restore.source.s3`.** The
   driver merges the captured spec with a freshly resolved snapshot
   destination from the `Backup` artefact, then `Create`s the new CR.
   The etcd-operator picks it up and bootstraps from the S3 snapshot.
6. **Wait for `Ready=True`.** The driver polls the new
   `EtcdCluster.status.conditions[Ready]`.
7. **Resume the HelmRelease.** With the cluster Ready, Flux can
   reconcile chart drift safely. Helm's next apply will remove the
   `bootstrap` block on the live CR; the operator only consults
   `bootstrap` at first reconcile, so this is a no-op after the fact.
8. **Mark `RestoreJob.status.phase=Succeeded`.**

## 3. Tenant-visible sentinel

Step 05 puts a `__cozystack_e2e_sentinel` key in etcd before the
backup, mutates it before the restore, and reads it back after the
restore. The read-back value should match the pre-mutation value;
that's the in-cluster witness that the snapshot actually round-tripped
through S3.
