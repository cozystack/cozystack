# cozy-db-autoscaler

Optional platform operator that automatically scales the number of **read replicas** of a managed database in response to load, driven by the `DatabaseHorizontalAutoscaler` (DHA) custom resource (group `autoscaling.cozystack.io/v1alpha1`).

It is the implementation of the [Database Horizontal Autoscaler design proposal](https://github.com/cozystack/community/tree/main/design-proposals/database-horizontal-autoscaling).

## Supported engines

Horizontal autoscaling applies to primary-replica engines (read replicas are scaled; the primary is never touched):

| Kind | Topology | Status (validated on a live cozystack cluster) |
|---|---|---|
| `Postgres` | CloudNativePG, 1 primary + standbys | **Full scale up/down validated.** Quorum floor = `quorum.maxSyncReplicas + 1`; PromQL joins CNPG metrics against `kube_pod_labels` on `label_cnpg_io_instance_role`. |
| `Redis` | spotahome RedisFailover (Sentinel) | **Full scale up/down validated.** 1 master + read-serving slaves; PromQL selects slaves via `label_redisfailovers_role="slave"`; `redis_exporter` sidecar. No sync-replica quorum (floor = 1). |
| `MariaDB` | mariadb-operator async replication | Metric read + patch + ownership validated; PromQL scopes by the `<release>-metrics` exporter job and selects replicas by the `target`s exposing `mysql_slave_status_*`. **Caveat:** the cozystack mariadb chart does not currently set `replication.replica.bootstrapFrom`, so the operator rejects on-the-fly scale-out (`MariaDBScaleOutError`); the autoscaler patches correctly but the engine cannot add a replica until the chart supports scale-out — the StuckScaling guardrail then rolls back. |
| `MongoDB` | Percona replica set (`rs0`) | `Scalable=true` for replica-set / `ScalingActive=False` for `sharding: true` validated. **Caveat:** cozystack ships the Percona PMM / mongodb_exporter **disabled**, so no MongoDB metrics are scraped; the loop correctly fail-safe freezes (`AbleToScale=False(MetricUnavailable)`) rather than scaling blind. Enable the exporter to use MongoDB autoscaling. |

Engines that require data rebalancing — `ClickHouse`, `Kafka`, and sharded `MongoDB` — are intentionally **not** scalable and report `ScalingActive=False` with a reason.

All queries constrain to the tenant namespace — a tenant can never read another tenant's series. The `postgres`, `redis` and `mariadb` expressions are calibrated against real metrics on a live cluster; `mongodb`'s are best-effort pending the exporter being enabled.

## How it works

The autoscaler watches a DHA, reads the driver metric from VictoriaMetrics and the linked `WorkloadMonitor`, computes the desired total instance count under the design's guardrails (quorum floor, replication-lag brake, stabilization windows, single-flight with a convergence deadline and rollback, tenant-quota ceiling, dry-run), and applies its decision by patching the target `Application`'s `replicas` value — the same Flux-compatible field a human would edit. Scale-down is handed to the engine operator's graceful instance removal.

## Enabling

The package is off by default. Enable it by adding it to `bundles.enabledPackages`:

```yaml
bundles:
  enabledPackages:
    - cozystack.db-autoscaler
```

It declares a `dependsOn` on the monitoring stack (VictoriaMetrics / `WorkloadMonitor`), which the decision loop requires.

## Ownership enforcement

While a DHA is active the autoscaler is the single owner of the target's `replicas` value: it stamps the marker annotation `autoscaling.cozystack.io/managed-by: <dha-name>` and writes via the `db-autoscaler` field manager.

Server-side-apply field-level ownership does **not** hold on the aggregated `apps.cozystack.io` API (its `spec` is an opaque JSON blob and managed-fields are not round-tripped). A validating admission webhook provides deterministic enforcement, but it is registered against the backing Flux **HelmRelease**, not the aggregated apps API: kube-apiserver does not run admission webhooks for aggregated APIServices (it proxies those requests to the extension server), whereas the HelmRelease is a Flux CRD served by kube-apiserver — and it is the object a force-applying GitOps writer targets directly. The webhook reads the projected marker (`apps.cozystack.io-autoscaling.cozystack.io/managed-by`) and `spec.values.replicas`, and rejects a `replicas` change from any identity other than the autoscaler or the apps-API extension server (through which the autoscaler's own patch and a tenant's `kubectl edit` legitimately flow). This closes the design's named competing-writer case — a tenant GitOps `Kustomization` with `spec.force: true` writing the HelmRelease directly.

The webhook's `failurePolicy` is `Ignore` so an outage never blocks Flux reconciliation of unrelated HelmReleases; when it is unavailable, enforcement degrades to the controller's advisory marker plus convergence-based conflict detection (surfaced as a `ScalingLimited` condition). The full deterministic guarantee should be confirmed by a live-cluster spike (per the design proposal's Testing section) before relying on it.

Deleting the DHA stops all autoscaling immediately and clears the marker, leaving the application at its current `replicas`.

## Example

```yaml
apiVersion: autoscaling.cozystack.io/v1alpha1
kind: DatabaseHorizontalAutoscaler
metadata:
  name: db
  namespace: tenant-acme
spec:
  targetRef: { kind: Postgres, name: db }
  minReplicas: 2   # TOTAL instances (primary + standbys); >= 2 to serve reads
  maxReplicas: 6
  metrics:
    - type: ReadConnections
      target: { averageValue: "150" }   # per read-serving replica
  behavior:
    scaleUp:   { stabilizationWindowSeconds: 300,  step: 1 }
    scaleDown: { stabilizationWindowSeconds: 1800, step: 1 }
    convergenceDeadlineSeconds: 900
  constraints:
    respectQuorum: true
    maxReplicationLagSeconds: 30
    gracefulScaleDown: true
  dryRun: false
```
