# Tenant Flux Sharding — design plan

> Decisions of record are captured inline in the **ADR 0001** section below; the
> rest of this document is the working design that implements them.

## ADR 0001 — Tenant-granular Flux sharding for the management-cluster helm-controller

- Status: Accepted
- Date: 2026-06-03
- Deciders: cozystack maintainers

### Context

The management cluster runs one tenant helm-controller shard
(`sharding.fluxcd.io/key=tenants`, `--concurrent=5`). A noisy tenant HelmRelease
(infinite `remediation.retries`) saturated its workers and lagged its informer
cache, losing a deletion event and hanging an unrelated tenant in `Terminating`.
The damage is per-pod (per-informer). Vertical tuning raises the ceiling but
cannot isolate tenants that share one informer. Horizontal sharding (multiple
helm-controller pods, each scoped to a subset of tenants by label) is the only
thing that isolates both worker starvation and cache-lag blast radius. (Full
problem write-up in *Background* below.)

### Decision

1. **Shard by tenant; weight by HelmRelease count.** The atomic unit of placement
   is the tenant — all of a tenant's HelmReleases (parent `tenant-<id>` in
   `tenant-root` + children in `tenant-<id>`) carry the **same**
   `sharding.fluxcd.io/key=shard<i>` label. Shards are sized/balanced by each
   tenant's HR count, not by raw tenant count (greedy least-loaded; N tenants over
   N shards ⇒ 1 per shard).
2. **Runtime via an in-house operator, not flux-operator.** A new system package
   `flux-shard-operator` provisions `shardCount` helm-controller Deployments
   (`helm-controller-shard<i>`, `--watch-label-selector=sharding.fluxcd.io/key=shard<i>`)
   by cloning the helm-controller container from the existing flux-aio `flux`
   Deployment and sanitising it. flux-aio stays unchanged and keeps
   `!sharding.fluxcd.io/key` (system releases); only the hand-rolled `flux-tenants`
   Deployment is retired.
3. **Labels are the source of truth; assignment stamped on the tenant namespace; a
   CREATE-only webhook stamps HRs at birth.** The placement controller rebuilds its
   in-memory index from HR labels on start (no separate persisted map), records each
   tenant's assignment as a label on its namespace, and a CREATE-only mutating
   webhook (`failurePolicy: Ignore`) copies that label onto every tenant HR so it
   is born on the correct shard regardless of creation path. Unlabeled HRs fall to
   flux-aio (`!key`) as the graceful-degradation net until the controller relabels.
4. **Cap the storm source.** Tenant module templates set a finite
   `remediation.retries` instead of `-1`.

### Why tenant granularity (not per-HelmRelease)

- **Isolation / blast-radius (primary).** Sharding exists to contain a noisy tenant
  so it cannot degrade others. Per-HR placement scatters every tenant's HRs across
  all shards, so a single saturated shard degrades a slice of *every* tenant — the
  opposite of isolation. Per-tenant placement confines a noisy tenant's damage to
  the co-residents of its shard; whales can be pinned to a dedicated shard.
- **Teardown coherence.** Tenant deletion runs a pre-delete hook
  (`packages/apps/tenant/templates/cleanup-job.yaml`) doing
  `kubectl delete helmreleases ... --wait=true` in two waves; each child's
  finalizer is removed by its owning shard. With the whole tenant on one shard,
  teardown depends on one shard's health; per-HR, on the worst of every shard
  hosting a child.
- **Cost:** a whale tenant cannot be split (loads one shard). Accepted — fleet data
  (below) shows even the largest tenant (87 HR) fits one shard; whales are isolated
  via `pinnedTenants`.

### Alternatives considered and rejected

- **flux-operator + FluxInstance sharding.** Rejected: hard singleton FluxInstance
  named `flux` (`cmd/operator/main.go` cache field-selector `metadata.name=flux`) ⇒
  cannot run one FluxInstance per shard; and its main controllers watch
  `!sharding.fluxcd.io/key`, duplicating flux-aio. Would force retiring flux-aio
  entirely or suppressing the FluxInstance main controller — more disruption than
  provisioning shard Deployments directly.
- **weaveworks/flux-shard-controller (FluxShardSet).** Covers the runtime half
  (clone a controller Deployment per shard) but is archived and does no placement.
  We reuse its sanitisation design in-house instead of depending on a dead chart.
- **A "dependsOn breaks across shards" argument for tenant granularity.**
  Investigated and **rejected as false**: helm-controller resolves `spec.dependsOn`
  via the uncached `APIReader` ("querying the API server bypassing the cache",
  `helm-controller internal/controller/helmrelease_controller.go` `checkDependencies`;
  `main.go` `APIReader: mgr.GetAPIReader()`), so cross-shard dependsOn works. The
  case for tenant granularity rests on isolation and teardown coherence, not on
  cache visibility.

### Consequences

- A noisy tenant's blast radius is bounded to its shard's co-residents.
- `shardCount` and per-shard `concurrent`/resources become tunable package values
  (autosizing recommendation surfaced in status; enforcement is a later step).
- One label per tenant ⇒ exactly one owning shard, no overlap/gap by construction.
- flux-aio remains the catch-all for `!key` and the degradation net for unlabeled
  tenant HRs.

## Background

The management-cluster Flux that reconciles tenant modules runs as a single
hand-rolled sharded helm-controller (`flux-tenants`,
`--watch-label-selector=sharding.fluxcd.io/key=tenants`, `--concurrent=5`), baked
into the installer as `internal/fluxinstall/manifests/fluxcd-tenants.yaml`. Every
tenant HelmRelease (parent `tenant-<id>` in `tenant-root` plus child modules in
`tenant-<id>`) lands on this one shard. The all-in-one flux-aio `flux` pod runs
the source/kustomize/helm/notification controllers for everything **without** a
shard key (`--watch-label-selector=!sharding.fluxcd.io/key`) — i.e. system/platform
releases.

A production incident showed the failure mode: a few HelmReleases stuck in
infinite remediation (`remediation.retries: -1`) — notably a `harbor` release
looping `upgrade(15m) → rollback(15m)` for days — saturated the 5 worker slots
**and** flooded the shard's single informer with status writes. The informer
cache fell behind the apiserver (`failed to wait for object to sync in-cache
after patching`), a tenant's child `etcd` HelmRelease deletion event was lost,
the object dropped out of the controller's working set, and the tenant hung in
`Terminating`. New tenants queued behind the saturated controller and showed
`Unknown`. The flux-aio pod had **0** such errors throughout — proving the
degradation is per-informer / per-pod, not cluster-wide.

Conclusion: vertical tuning (`--concurrent`, resources) only raises the ceiling;
it cannot isolate a noisy tenant because all tenants share one informer/cache.
Horizontal **sharding** (multiple helm-controller pods, each with its own
informer scoped to a subset of tenants) is the only thing that isolates both
worker starvation and cache-lag blast radius. This document plans a dedicated
package that owns tenant→shard placement **and** provisions the shard
helm-controllers.

## Goals / non-goals

Goals:
- Spread tenants across `shardCount` helm-controller instances so one tenant's
  churn cannot degrade the others.
- **Balanced** distribution (no idle shard while another is doubled), with a
  guarantee that `N` tenants over `N` shards land 1-per-shard.
- Minimal, non-disruptive movement when `shardCount` changes or load drifts.
- Safe online migration from today's single `tenants` shard.

Non-goals:
- Fixing the storm *source* — capping `remediation.retries` to a finite number
  is a separate, complementary change in the tenant module templates. Sharding
  limits blast radius; the retry cap removes the trigger. Ship both.
- Sharding non-tenant (system) HelmReleases — flux-aio keeps owning `!key`.

## Architecture

New cozystack system package `flux-shard-operator` (modelled on
`lineage-controller-webhook`: one binary, one Deployment, `replicas: 2`,
leader-elected controller + webhook served on all replicas, cert-manager webhook
cert). Three parts:

1. **Shard runtime — provisioned by this operator (no flux-operator).**
   flux-operator was evaluated and rejected (it enforces a hard singleton
   FluxInstance named `flux` — `cmd/operator/main.go` cache field-selector
   `metadata.name=flux` — so "one FluxInstance per shard" is impossible, and its
   main controllers watch `!sharding.fluxcd.io/key`, duplicating flux-aio; see
   ADR). Instead the controller reconciles `shardCount` helm-controller
   Deployments `helm-controller-shard<i>` in `cozy-fluxcd`, **cloned from the
   flux-aio `flux` Deployment's helm-controller container** and sanitised:
   - `hostNetwork: false`, `dnsPolicy: ClusterFirst` (flux-aio is
     `hostNetwork:true` / `ClusterFirstWithHostNet`; N pods would collide on host
     ports `:9795/:9796`);
   - extract only the helm-controller container (flux-aio is an all-in-one
     5-container pod);
   - drop/redirect `--events-addr=http://localhost:9690` (localhost is dead in a
     standalone pod — the legacy `flux-tenants` simply omits it; do the same);
   - replace `--watch-label-selector=!sharding.fluxcd.io/key` →
     `=sharding.fluxcd.io/key=shard<i>`; set the Deployment name;
     `--concurrent`/resources from this package's values.
   The helm-controller **image and feature-gates are inherited from flux-aio
   automatically**, so shards stay version-synced with no manual bump (this is the
   weaveworks/flux-shard-controller `sourceDeploymentRef` idea, built in — the
   upstream project is archived, so its design is reused, not depended on). Shard
   names `shard0..shard{K-1}` are defined by us, so the placement controller and
   the Deployment selectors always agree. Deployments beyond `shardCount` are
   pruned (managed-by label).

2. **Placement controller (the core of this package).** Watches `Tenant` and
   `HelmRelease` (predicate: `internal.cozystack.io/tenantmodule=true`). Owns the
   tenant→shard assignment and **records it as a label on the tenant namespace**
   (`tenant-<id>`), so the webhook can resolve it cheaply at admission and the
   assignment lives in the most natural place (no separate ConfigMap/CR). The
   **HR labels are the source of truth**; the controller rebuilds its in-memory
   index from them on start (restart-safe by construction) and self-heals labels
   on all of a tenant's HRs to match the assignment. Handles backfill, rebalance,
   and the runtime swap.
   - **Watch HelmReleases as metadata-only** (`metav1.PartialObjectMetadata` /
     controller-runtime `builder.OnlyMetadata`), **never the typed object** — the
     same pattern `lineage-controller-webhook` already uses. The controller only
     needs labels, namespace/name and `deletionTimestamp`. Payoff is large
     precisely on the clusters that need sharding: the cache holds tiny metadata
     stubs instead of full HRs for 900+ objects, and the controller does **not**
     decode the helm-controller status-patch firehose (the write storm that caused
     the incident). Pair `OnlyMetadata` with a metadata-change predicate so
     status-only `resourceVersion` bumps don't trigger placement reconciles.

3. **Mutating webhook (required, CREATE-only).** Served by the same Deployment
   (≥2 replicas, control-plane, own readiness). On HR CREATE it **looks up the
   tenant's shard via the tenant namespace label and stamps**
   `sharding.fluxcd.io/key`. This makes every creation path uniform — API-created,
   child HRs, and `extra` HRs rendered inside other releases all pass through
   admission, so each HR is **born on the correct shard** (no catch-all install +
   later handoff, no churn). Config: `operations: ["CREATE"]`, `objectSelector`
   `internal.cozystack.io/tenantmodule=true`, `failurePolicy: Ignore`. Never
   intercept UPDATE (helm-controller status patches are a firehose; a Fail webhook
   there would gate all Flux reconciliation).
   - Why a webhook and not the controller's async patch: the controller's informer
     also sees every creation path, but only *after* the object exists — the HR
     would first land unlabeled on flux-aio (`!key`), get reconciled there, then
     migrate. The webhook removes the gap: the right shard label is present at
     persist time.
   - `failurePolicy: Ignore` (not Fail): the webhook is required for steady-state
     correctness/efficiency, but its outage must **degrade gracefully** to the
     catch-all path (HR born unlabeled → flux-aio `!key` → controller relabels),
     not block tenant/HR creation.
   - Map-miss fallback: if the tenant namespace has no assignment yet (rare —
     controller assigns on first sight), the webhook leaves the HR unlabeled and
     flux-aio handles it until the controller assigns.

## Ownership model

- Ownership is encoded as a **single label on the object**:
  `sharding.fluxcd.io/key=shard<i>`. One label ⇒ exactly one owning shard ⇒ no
  overlap (two controllers fighting one release) and no gap, by construction.
- **Unit of placement is the tenant, not the HelmRelease.** All HRs of one tenant
  (parent `tenant-<id>` in `tenant-root` + children in `tenant-<id>`) MUST carry
  the same shard label. The reason is **isolation/blast-radius**: per-HR placement
  scatters every tenant's HRs across all shards, so one saturated shard would
  degrade a slice of *every* tenant — the opposite of isolation. Per-tenant
  placement confines a noisy tenant's damage to the co-residents of its shard.
  Secondary: a tenant's teardown (`packages/apps/tenant/templates/cleanup-job.yaml`
  pre-delete hook does `kubectl delete helmreleases ... --wait=true` in two waves;
  each child's finalizer is removed by its owning shard) depends on one shard's
  health instead of the worst of all shards hosting a child. (See ADR for the full
  argument, including why the cross-shard `dependsOn` concern does **not** apply —
  helm-controller reads dependencies via the uncached `APIReader`.)
- Tenant key extraction: children → namespace (`tenant-<id>`); parent → HR name
  (`tenant-<id>`) or `app.kubernetes.io/instance`. Normalise to `<id>`.
- The **labels on HRs are the source of truth**; the tenant namespace label is the
  recorded assignment the webhook reads. On every reconcile the controller
  self-heals HR labels to match the assignment.

## Placement algorithm

### What drives what (HR vs tenants)

HR count and tenant count answer different questions — do not conflate them:

- **Shard count `K` and `concurrent` ← HelmRelease count `H`.** Load on the
  informer/watch/workers is proportional to HRs (each HR is a watched object +
  reconciles + status writes), not to tenants. A tenant with 87 HR loads ~40× one
  with 2. Raw tenant count must **not** size the runtime.
- **Distribution unit ← the tenant; its weight ← its HR count.** A tenant cannot
  be split across shards (isolation; see Ownership model). So we distribute whole
  tenants, weighted by HR.
- **Upper bound on shards ← tenant count `T`.** More shards than tenants is
  useless (nothing left to split), hence `K ≤ T`.

In short: tenants are the buckets, HRs are the weight in each bucket; size by HR,
cap by tenants.

### State

- `T` — tenants; `w(t)` — weight of tenant `t` = number of its HelmReleases
  (parent + children). Default proxy for controller load; tunable.
- `S = {s0 … s_{K-1}}` — shards, `K = shardCount`.
- `A : t → s` — assignment (derived from labels; recorded on the tenant namespace).
- `P : t → s` — pinned overrides (for "whale" tenants on dedicated shards).
- `Load(s) = Σ w(t) for t with A[t] = s`.

### Assign on first sight of an unassigned tenant

```
if t in P:            A[t] = P[t]
else:                 A[t] = argmin_s Load(s)      # least-loaded by weight
                                tie-break: lowest shard index
record A[t] on tenant namespace; stampLabel(all HRs of t, A[t])
```

Greedy least-loaded gives the guarantee a hash cannot: with uniform weights and
tenants created one by one, each new tenant picks a currently-empty/min shard, so
**N tenants across N shards ⇒ exactly 1 per shard**. Deterministic, not
probabilistic. (Hashing 4 tenants into 4 shards yields a perfect 1:1 split only
~9.4% of the time — `4!/4⁴` — and averages ~1.27 idle shards.)

### Rescale: `shardCount` K → K′

Movement is unavoidable (ownership is a per-object label; tenants that change
owner must be relabeled). The goal is **balance + minimal moves**.

- **Scale up (K′ > K).** New shards start empty. Target load `≈ TotalWeight / K′`.
  Pull tenants from the most-loaded existing shards into the new shards until each
  new shard reaches target. Never touch tenants already at/under target. Moves
  ≈ `(K′−K)/K′` of total weight.
- **Scale down (K′ < K).** Shards `s_{K′} … s_{K-1}` are removed; only **their**
  tenants are forced to move. Redistribute them least-loaded-first. Everyone else
  stays. The controller deletes the now-empty `helm-controller-shard<i>`
  Deployments after their tenants have drained.

### Steady-state rebalance (drift from uneven weights)

```
imbalance = (maxLoad − minLoad) / avgLoad
if imbalance > THRESHOLD (e.g. 0.25) sustained:
    repeat:
        pick the move (one tenant from argmax-load shard to argmin-load shard)
        that most reduces (maxLoad − minLoad)
        prefer smaller tenants when several achieve balance (cheaper handoff)
        apply if it strictly improves; stop when within THRESHOLD or no improving move
```

Bounded, converges. Skip pinned tenants and tenants with `deletionTimestamp`.
Per-tenant cooldown to avoid thrashing.

### Move execution (per tenant)

- Patch `sharding.fluxcd.io/key=shard<new>` on **all** HRs of the tenant (and
  update the namespace label), close together, so the tenant is never split for
  long.
- Old shard's informer sees the object leave its label selector → synthetic watch
  DELETE **without** `deletionTimestamp` ⇒ helm-controller does **not** uninstall,
  it simply stops reconciling. New shard sees ADD → reconciles. No pod restart, no
  full relist — only a burst of reconciles on the target shard.
- Rate-limit moves (a few tenants at a time) to avoid an ADD burst overwhelming a
  target shard.
- TODO: e2e asserting that relabel (leave-selector) never triggers a Helm
  uninstall, before relying on this in production.

### Why label-on-object, not selector-on-controller

Ownership must live somewhere; changing `shardCount` rewrites it for some fraction
either way:
- **Label on HR** (this design): relabel ~`1/N` of tenants via watch-handoff, no
  restarts → cheap.
- **Bucket label + per-shard selector ranges**: HRs immutable, but rescale changes
  shard `--watch-label-selector`s → shard **restarts + relist** → the exact load
  class the incident was about → worse.

So ownership stays in the per-HR label; the controller minimises and paces the
relabels.

## Migration / backfill

- Today every tenant HR has `sharding.fluxcd.io/key=tenants` (single bucket),
  reconciled by the hand-rolled `flux-tenants`. flux-aio (`!key`) never touches
  them.
- On install / shardCount change, the controller backfills: compute a balanced
  assignment for all existing tenants and relabel gradually (paced) from `tenants`
  onto `shard<i>`. As each tenant is relabeled, `flux-tenants` (which watches
  `key=tenants`) stops seeing it and the target shard picks it up.
- **flux-aio is the catch-all** for unlabeled / legacy / in-flight tenant HRs
  (anything with no shard key falls to its `!key` controllers) — the
  graceful-degradation net, not the primary path.

## Safety / invariants

- **Single label ⇒ single owner.** No overlap, no gap.
- **Whole-tenant atomicity.** All HRs of a tenant share one shard.
- **Webhook stamps the shard at admission** so every HR is born on the correct
  shard regardless of creation path (API / child / `extra`). Required component.
- **flux-aio (`!key`) is the graceful-degradation net** (webhook outage or
  map-miss): an unlabeled HR is reconciled by flux-aio until the placement
  controller relabels it.
- **Never move a deleting tenant** (`deletionTimestamp` present) — let it finish.
- **Webhook**: CREATE-only, `tenantmodule` scope, `failurePolicy: Ignore`.
- Pace relabels; per-tenant cooldown; hysteresis on rebalance threshold.

## Config surface (package values)

```yaml
fluxShardOperator:
  shardCount: 1                    # ship at 1 (≈ today), raise gradually
  placementStrategy: LeastLoaded   # LeastLoaded (default) | ConsistentHash
  weightBy: helmReleaseCount       # helmReleaseCount (default) | tenantCount
  rebalanceThreshold: 0.25
  pinnedTenants: {}                # e.g. { bigtenant: shard3 }
  shard:
    concurrent: 5
    resources: { requests: { cpu: 100m, memory: 64Mi }, limits: { memory: 1Gi } }
```

`ConsistentHash` (rendezvous/HRW) remains available as a stateless option that
minimises rescale movement but gives only statistical balance (poor at small N).
`LeastLoaded` is the default because balance + the N-over-N guarantee matter more.

## Auto-sizing `shardCount` and `concurrent`

A fixed size does not fit the fleet. Today every cluster ships one tenant shard at
`--concurrent=5`, `cpu req=100m / mem limit=1Gi`, regardless of load.

### Fleet sample (anonymised)

The "system" shard (HRs with no shard label — platform releases, flux-aio) is
roughly constant (~80–100 HR) on every cluster. The **tenant shard** is what
varies, by ~60×. Load is driven by HR count, not tenant count.

| cluster | env   | total HR | tenant-shard HR | tenants | HR/tenant avg / max |
|---------|-------|----------|-----------------|---------|---------------------|
| C1      | prod  | 1049     | 946             | 128     | 7.3 / 87            |
| C2      | dev   | 163      | 73              | 6       | 12                  |
| C3      | prod  | 157      | 71              | 6       | 12                  |
| C4      | stage | 151      | 61              | 10      | 6                   |
| C5      | prod  | ~170     | ~81             | 13      | ~6                  |
| C6      | dev   | 112      | 30              | 2       | 15                  |
| C7      | prod  | 100      | 15              | 3       | 5                   |

Calibration anchors: C5 runs 81 HR on `concurrent=5` comfortably; C1 at 946 HR on
a single `concurrent=5` shard is at the edge (intermittent `failed to wait for
object to sync in-cache after patching`, CPU 283m vs 100m request).

### Sizing function

Scale by tenant-shard HR count `H`, capped by tenant count `T`:

```
shardCount K = clamp( ceil(H / H_target), 1, min(maxShards, T) )
concurrent C = clamp( round( (H/K) / hrPerWorker ), C_min, C_max )
```

Defaults: `H_target ≈ 150` HR/shard, `maxShards = 16`, `hrPerWorker ≈ 15`,
`C_min = 5`, `C_max = 20`. Applied to the sample, only C1 needs more than one shard
(946/150 → **7** shards, ~135 HR/shard, `C≈9`); every other cluster → `K=1, C=5`.

Per-shard resources track `C`: `cpu req ≈ 100m + C·25m`, `mem limit ≈ 256Mi + C·64Mi`.

### Auto-mode rules

- **v1: compute and surface** the recommended `K` (and per-shard load) in status /
  telemetry next to `cozy_application_count`; `shardCount` stays operator-set.
- **v2: enforce** — the controller drives `K` from `H` with **hysteresis** (scale
  up at `H/K > H_target·1.2`, down at `< H_target·0.6`, with a cooldown; up
  eagerly, down lazily) and later from observed per-shard cache-lag / reconcile
  latency / `in-cache` error rate.
- `maxShards` and per-tenant pins always stay in values.

## Runtime swap: retire the hand-rolled `flux-tenants`

Today's runtime stays almost entirely as-is — **flux-aio is not retired**:
- flux-aio (`packages/core/flux-aio/flux-aio.cue` → `internal/fluxinstall/manifests/fluxcd.yaml`)
  keeps running all controllers for `!sharding.fluxcd.io/key` (system releases).
- The only thing removed is the hand-rolled tenant shard:
  `internal/fluxinstall/manifests/fluxcd-tenants.yaml` and the two `yq` lines in
  `packages/core/flux-aio/Makefile` that synced its image/version from flux-aio
  (`install.go` globs `manifests/*.yaml`, so deleting the file is enough — no code
  change). The runtime image sync now happens at runtime via the clone.

Swap sequence (controller-owned, no reconciliation gap):
1. Deploy `flux-shard-operator` (system package) with `shardCount: 1`. The
   controller provisions `helm-controller-shard0` (cloned from flux-aio).
2. The controller backfills existing tenant HRs `tenants → shard0` (paced). As they
   move, `flux-tenants` drains.
3. Once **no `sharding.fluxcd.io/key=tenants` HR remains**, the controller deletes
   the orphaned `flux-tenants` Deployment.
4. Migration `44` (44→45) stamps `cozystack-version=45` (labeled ConfigMap, mirror
   migration 43) and defensively removes a leftover `flux-tenants` Deployment only
   if the `key=tenants` guard is satisfied. `targetVersion` bumps `44 → 45`.

Net: single `flux-tenants` (`concurrent=5`) → operator-managed `shard0`
(≈ today) → balanced shards as `shardCount` is raised.

## Open questions / TODO

- Decide weight function evolution: start with HR count; evaluate adding
  reconcile-activity weighting later.
- e2e: relabel does not trigger uninstall (leave-selector semantics).
- Telemetry: per-shard HR count / load, assignment map, rebalance/move events;
  wire into existing `cozy_*` metrics.
- Autosizing enforcement (v2) — drive `K`/`concurrent` from observed load.

## Rollout

1. Land the `remediation.retries` cap in tenant module templates (kills the storm
   source) — independent of, and prerequisite to, sharding.
2. Ship `flux-shard-operator` with `shardCount: 1`: provisions one operator-managed
   shard, backfills `tenants → shard0`, drains and removes `flux-tenants`
   (≈ today's behaviour) to de-risk the runtime swap first.
3. Raise `shardCount` gradually; watch per-shard reconcile bursts as the controller
   backfills `tenants → shard<i>`.
4. Pin known heavy tenants to dedicated shards.
5. Enable autosizing enforcement (v2) once telemetry is trusted.
