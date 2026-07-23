# cozy-db-autoscaler

Optional platform operator that automatically scales the number of **read replicas** of a managed database in response to load, driven by the `DatabaseHorizontalAutoscaler` (DHA) custom resource (group `autoscaling.cozystack.io/v1alpha1`).

It is the implementation of the [Database Horizontal Autoscaler design proposal](https://github.com/cozystack/community/tree/main/design-proposals/database-horizontal-autoscaling).

## Supported engines

Horizontal autoscaling applies to primary-replica engines (read replicas are scaled; the primary is never touched):

| Kind | Topology | Status |
|---|---|---|
| `Postgres` | CloudNativePG, 1 primary + standbys | **Enabled — full scale up/down validated on a live cluster.** Quorum floor = `quorum.maxSyncReplicas + 1`; PromQL joins CNPG metrics against `kube_pod_labels` on `label_cnpg_io_instance_role`. |
| `Redis` | spotahome RedisFailover (Sentinel) | **Enabled — full scale up/down validated on a live cluster.** 1 master + read-serving slaves; PromQL selects slaves via `label_redisfailovers_role="slave"`; `redis_exporter` sidecar. No sync-replica quorum (floor = 1). |
| `MariaDB` | mariadb-operator async replication | **Not enabled yet** (`Scalable=false`). The adapter is implemented and its PromQL is calibrated, but the cozystack mariadb chart does not set `replication.replica.bootstrapFrom`, so the mariadb-operator rejects on-the-fly scale-out (`MariaDBScaleOutError`). Reported as not-scalable to avoid a patch→stuck→rollback loop; unblocked once the chart supports scale-out (follow-up in the mariadb package). |
| `MongoDB` | Percona replica set (`rs0`) | **Not enabled yet** (`Scalable=false`). Sharded clusters are inherently non-scalable (data rebalancing); replica sets are topologically scalable but cozystack ships the Percona PMM/mongodb_exporter disabled, so there is no metric source and the queries are uncalibrated. Enabled once the exporter ships on and the PromQL is calibrated (follow-up). |

Engines that require data rebalancing — `ClickHouse`, `Kafka`, and sharded `MongoDB` — are intentionally **not** scalable and report `ScalingActive=False` with a reason.

All queries constrain to the tenant namespace — a tenant can never read another tenant's series. The enabled adapters (`postgres`, `redis`) are calibrated against real metrics on a live cluster.

## How it works

The autoscaler watches a DHA, reads the driver metric from VictoriaMetrics and the linked `WorkloadMonitor`, computes the desired total instance count under the design's guardrails (quorum floor, replication-lag brake, stabilization windows, single-flight with a convergence deadline and rollback, tenant-quota ceiling, dry-run), and applies its decision by patching the target `Application`'s `replicas` value — the same Flux-compatible field a human would edit. Scale-down is handed to the engine operator's graceful instance removal.

## Enabling

The package is off by default. Enable it by adding it to `bundles.enabledPackages`:

```yaml
bundles:
  enabledPackages:
    - cozystack.db-autoscaler
```

It declares a `dependsOn` on the monitoring stack (VictoriaMetrics / `WorkloadMonitor`), which the decision loop requires, and on cert-manager, which issues the webhook serving certificate.

## Monitoring requirements

The autoscaler resolves the target's vmselect from the `namespace.cozystack.io/monitoring` label on the target's namespace, then runs its PromQL there. For the Postgres and MariaDB adapters that vmselect must serve **both** the engine's own per-pod metrics (e.g. `cnpg_backends_total`) **and** `kube_pod_labels` from kube-state-metrics, because the read-replica role is not carried on the engine metrics and is recovered by joining against `kube_pod_labels` on `label_cnpg_io_instance_role`. kube-state-metrics is cluster-scoped and is scraped only by the root/cluster monitoring, so this holds for the common tenant layout — a tenant with `monitoring: false`, whose namespace label points at the parent (root) monitoring where both series coexist (verified on a live cluster). A tenant that runs its **own** isolated stack (`monitoring: true`) scrapes the engine metrics but not cluster-scoped `kube_pod_labels`; the role join then returns no series, the metric reads as unavailable, and the DHA fail-safe-freezes (`AbleToScale=False`, reason `MetricUnavailable`) rather than scaling blind. Point such targets at a monitoring stack that also scrapes kube-state-metrics before enabling autoscaling.

## Ownership enforcement

While a DHA is active the autoscaler is the single owner of the target's `replicas` value: it stamps the marker annotation `autoscaling.cozystack.io/managed-by: <dha-name>` and writes via the `db-autoscaler` field manager.

Server-side-apply field-level ownership does **not** hold on the aggregated `apps.cozystack.io` API (its `spec` is an opaque JSON blob and managed-fields are not round-tripped). A validating admission webhook provides deterministic enforcement, but it is registered against the backing Flux **HelmRelease**, not the aggregated apps API: kube-apiserver does not run admission webhooks for aggregated APIServices (it proxies those requests to the extension server), whereas the HelmRelease is a Flux CRD served by kube-apiserver — and it is the object a force-applying GitOps writer targets directly. The webhook reads the projected marker (`apps.cozystack.io-autoscaling.cozystack.io/managed-by`) and `spec.values.replicas`, and rejects a `replicas` change from any identity other than the autoscaler or the apps-API extension server (through which the autoscaler's own patch and a tenant's `kubectl edit` legitimately flow). This closes the design's named competing-writer case — a tenant GitOps `Kustomization` with `spec.force: true` writing the HelmRelease directly.

The webhook's `failurePolicy` is `Ignore` so an outage never blocks Flux reconciliation of unrelated HelmReleases; when it is unavailable, enforcement degrades to the controller's advisory marker plus convergence-based conflict detection (surfaced as a `ScalingLimited` condition). The full deterministic guarantee should be confirmed by a live-cluster spike (per the design proposal's Testing section) before relying on it.

Operational note: the webhook matches every `helmreleases` UPDATE cluster-wide (annotations cannot be used in an admission `objectSelector`, so it cannot be narrowed to DHA-managed releases). Once the package is enabled, the webhook pods become a soft latency dependency for all Flux HelmRelease reconciliation — during a full operator outage each HelmRelease update waits up to `timeoutSeconds` (5s) before `failurePolicy: Ignore` lets it through. This is mitigated by the 2-replica deployment + PodDisruptionBudget.

Because the webhook allows writes that flow through the apps-API extension server, a tenant's manual `kubectl edit <kind> … replicas` is not rejected — but the controller detects it (observed `replicas` differs from the value the autoscaler last wrote), surfaces `ScalingLimited=True(OwnershipConflict)`, and backs off rather than fighting. This back-off is **terminal**: a single manual replicas edit disables autoscaling for that target until the DHA is deleted and recreated. This is intentional ("do not enter a write war"), but worth knowing — recreate the DHA to resume autoscaling after a manual override.

Deleting the DHA stops all autoscaling immediately: a finalizer removes the managed-by marker annotation from the target Application (so the ownership webhook stops guarding its HelmRelease and manual/GitOps control is regained), leaving the application at its current `replicas`.

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
    - type: ReadConnections           # for ReadCPUUtilization the target is a CPU quantity in millicores, e.g. "250m" or "1"
      target: { averageValue: "150" } # per read-serving replica
  behavior:
    scaleUp:   { stabilizationWindowSeconds: 300,  step: 1 }
    scaleDown: { stabilizationWindowSeconds: 1800, step: 1 }
    convergenceDeadlineSeconds: 900
  constraints:
    respectQuorum: true               # false lets the count fall to minReplicas below the sync floor
    maxReplicationLagSeconds: 30
  dryRun: false
```

Scale-down is always graceful — the autoscaler only patches `replicas` and never terminates backends; the engine operator removes the highest-ordinal standby.
