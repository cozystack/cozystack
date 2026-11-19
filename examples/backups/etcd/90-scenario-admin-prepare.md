# Scenario: admin prepares the Etcd backup framework

This narrative describes what a Cozystack admin does once per cluster
to make the `Etcd` backup strategy available to tenants. The
prerequisite hand-off ends at "tenants can author `BackupJob` /
`RestoreJob` CRs against their `Etcd` apps."

## 1. Install the operator + driver

- `packages/system/etcd-operator` ships the cozystack-authored `etcd-operator`
  (`etcd-operator.cozystack.io/v1alpha2`; CRDs: `EtcdCluster`, `EtcdMember`,
  `EtcdSnapshot`).
- `packages/system/backupstrategy-controller` ships the
  `strategy.backups.cozystack.io/v1alpha1` API and the controllers that
  dispatch `BackupJob` / `RestoreJob` to the per-app driver. The
  cluster admin verifies both are running:
  ```sh
  kubectl -n cozy-etcd-operator get deploy
  kubectl -n cozy-backupstrategy-controller get deploy
  ```

## 2. Apply the cluster-scoped Etcd strategy

This is the single-instance `strategy.backups.cozystack.io/v1alpha1 Etcd`
CR that the driver references whenever a `BackupClass` selects
`{Kind: Etcd, name: etcd-strategy-default}`. See
`01-create-strategy.sh` for the canonical YAML; it templates the
destination off `.Application.metadata.name` and `.Parameters` so a
single strategy serves every tenant.

## 3. Hand off to the tenant

Once the strategy is in place, the tenant runs steps 02-05 in their own
namespace. The admin does not need to touch anything per tenant: the
`BackupClass` carries the tenant's bucket coordinates as parameters
that the driver inlines at render time.
