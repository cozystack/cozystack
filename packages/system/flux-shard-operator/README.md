# flux-shard-operator

Spreads tenant HelmReleases across multiple helm-controller shards so a noisy tenant (e.g. a HelmRelease stuck in remediation) cannot degrade the others. The damage of a saturated helm-controller is per-pod / per-informer, so horizontal sharding with per-shard label-scoped informers is what isolates both worker starvation and cache-lag blast radius.

## What it does

The operator has three parts, all served by one Deployment (leader-elected controllers, webhook on every replica):

1. **Shard runtime.** Reconciles `shardCount` helm-controller Deployments (`helm-controller-shard<i>`, `--watch-label-selector=sharding.fluxcd.io/key=shard<i>`) in the flux namespace, cloned from the flux-aio `flux` Deployment's helm-controller container and sanitised (no host networking, no localhost cross-container wiring). The helm-controller image and feature-gates are inherited from flux-aio automatically. Deployments beyond `shardCount` are pruned once they drain, and the legacy hand-rolled `flux-tenants` Deployment is retired once no HelmRelease carries `sharding.fluxcd.io/key=tenants`.

2. **Placement controller.** Owns the tenant→shard assignment. The unit of placement is the tenant: all HelmReleases of one tenant (parent `tenant-<id>` plus everything in namespace `tenant-<id>`) carry the same shard label, so a noisy tenant's blast radius is bounded to its shard's co-residents. Tenants are distributed greedy least-loaded, weighted by HelmRelease count (N tenants over N shards land exactly 1 per shard). The assignment is recorded as the `internal.cozystack.io/flux-shard` label on the tenant namespace; HelmRelease labels remain the source of truth on restarts. Moves are paced and deleting tenants are never moved. Watches are metadata-only, so the controller does not decode the helm-controller status-patch firehose.

3. **Mutating webhook (CREATE-only, `failurePolicy: Ignore`).** Stamps the tenant's shard onto every HelmRelease at admission, so each one is born on the correct shard regardless of creation path. On webhook outage or before the first assignment, HelmReleases keep their legacy `tenants` key until the placement controller relabels them — graceful degradation, never blocked creation.

System (non-tenant) HelmReleases are untouched: flux-aio keeps reconciling everything without a shard key (`!sharding.fluxcd.io/key`).

## Parameters

### Common parameters

| Name                                  | Description                                                                              | Value                                                |
| ------------------------------------- | ---------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| `fluxShardOperator.image`             | Container image                                                                          | `ghcr.io/cozystack/cozystack/flux-shard-operator:*`  |
| `fluxShardOperator.debug`             | Enable debug logging                                                                     | `false`                                              |
| `fluxShardOperator.replicas`          | Operator replica count                                                                   | `2`                                                  |
| `fluxShardOperator.shardCount`        | Number of helm-controller shards                                                         | `1`                                                  |
| `fluxShardOperator.rebalanceThreshold`| Load spread ratio above which tenants are rebalanced                                     | `0.25`                                               |
| `fluxShardOperator.pinnedTenants`     | Map of tenant namespace to shard, pins heavy tenants to dedicated shards                 | `{}`                                                 |
| `fluxShardOperator.shard.concurrent`  | `--concurrent` of each shard helm-controller                                             | `5`                                                  |
| `fluxShardOperator.shard.resources`   | Resources of each shard helm-controller (empty values inherit flux-aio)                  | `{requests: {cpu: 100m, memory: 64Mi}, limits: {memory: 1Gi}}` |

## Telemetry

The operator exports `cozy_flux_shard_load`, `cozy_flux_shard_tenants`, `cozy_flux_shard_helmreleases`, `cozy_flux_shard_pending_moves`, `cozy_flux_shard_moves_total` and `cozy_flux_shard_recommended_count` (the autosizing recommendation `K = clamp(ceil(H/150), 1, min(16, T))`; v1 surfaces it only, raising `shardCount` stays an operator decision).
