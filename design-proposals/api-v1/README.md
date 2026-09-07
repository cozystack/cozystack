# Cozystack API v1: what `v1` promises, and the removals that gate it

- **Title:** `Cozystack API v1 — promoting apps.cozystack.io to v1, and the removals that gate it`
- **Author(s):** `@kvaps`
- **Date:** `2026-09-07`
- **Status:** Draft

## Overview

Every tenant-facing Cozystack API is served at `v1alpha1` and has been for the platform's whole life. The label is now actively misleading: customers run production databases, virtual machines and Kubernetes clusters through `apps.cozystack.io`, integrators write against it, and the `api/apps/v1alpha1` Go module is tagged in lockstep with every platform release. We do not in practice break these shapes, but we also do not promise not to, and we have no mechanism that would let us keep such a promise if we made one.

This proposal defines what a Cozystack `v1` API is, and proposes promoting **`apps.cozystack.io`** to `v1` — kind by kind, not as a flag day. It makes three claims. First, that `v1` must be the point at which the API surface stops being a superset of whatever a chart happens to accept: today `spec` is an open object and nothing server-side validates it, so a field "removed" from the schema is still accepted, still stored, and silently does nothing. Second, that promotion is only worth doing after the surface has been cleaned, and the two things that must go are the legacy backup options — superseded by `backups.cozystack.io` — and the `external: true` family, superseded by `EndpointAttachment` (community #45) drawing addresses from `IPAddressClaim` (community #35). Third, that the removal itself needs a mechanism we do not have yet: `ApplicationDefinition` multi-version conversion (community #6) is a hard prerequisite, not an adjacent nicety.

What this proposal is *not* is a request to freeze everything at once. `cozystack.io` — the platform-author contract — stays at `v1alpha1`, because four separate proposals are currently adding fields to `ApplicationDefinition`. `core.cozystack.io` and `sdn.cozystack.io` stay at `v1alpha1` for reasons given below. `v1` is a promise about the surface tenants build against, made where we can actually keep it.

## Scope and related proposals

- **Hard prerequisite: [community#6](https://github.com/cozystack/community/pull/6) — ApplicationDefinition multi-version conversion (`@kvaps`).** A second served version with `to`/`from` conversion and a background storage migration is the only way to remove a field without breaking every stored release. Nothing in this proposal is implementable before #6. This proposal also names one gap in #6 that only a removal exposes — see *The conversion round-trip check cannot survive a removal*.
- **Supplies the `external` replacement: [community#45](https://github.com/cozystack/community/pull/45) — `EndpointAttachment` (`@lllamnyp`) and [community#35](https://github.com/cozystack/community/pull/35) — `IPAddress` / `IPAddressClaim` / `IPAddressClass` (`@lllamnyp`).** #45 already states that `external`'s sunset is gated on an address-preserving migration; this proposal is the other half of that sentence — it says what the sunset *is* (removal from a served API version) and when it happens (at each kind's `v1`).
- **Frames the version model: [community#46](https://github.com/cozystack/community/pull/46) — Cozystack as a distribution (`@myasnikovdaniil`).** #46 versions the *package*; #6 versions the *API surface*; this proposal spends #6's mechanism on one specific transition. It is deliberately consistent with #46 rather than against it: #46 defines a package MAJOR as including "a change of storage version under #6", which is per-package by construction, and that is exactly why *v1 here is per-kind and not a flag day*.
- **Blocks `kind: Tenant` and the module kinds: [community#39](https://github.com/cozystack/community/pull/39) — fold `extra` into `apps` (`@myasnikovdaniil`), with [community#25](https://github.com/cozystack/community/pull/25) and [community#33](https://github.com/cozystack/community/pull/33).** #39 replaces the `Tenant` spec's module booleans (`etcd`, `monitoring`, `ingress`, `gateway`, `seaweedfs`, `computeplane`) with declarative capabilities on the `ApplicationDefinition`, and moves the module packages themselves out of `packages/extra`. Freezing `Tenant` at `v1` before #39 settles would turn #39 into a `v1` → `v2` change for the platform's single most-used kind, and the same holds for the kinds those booleans switch on — `Etcd`, `Monitoring`, `Ingress`, `SeaweedFS`, `ComputePlane`, plus `BootBox` and `ExternalDNS`, which #39 shows are already miscategorised. All of them promote *after* #39, not before.
- **Delivers the mechanics: [community#58](https://github.com/cozystack/community/pull/58) — platform migration engine (`@myasnikovdaniil`, Accepted).** The imperative half of every removal — creating the replacement objects before the field disappears — is a numbered platform migration, not a conversion template. See *Two migrations, and why they cannot be one*.
- **Out of scope, deliberately: `core.cozystack.io`.** `TenantSecret` is being redesigned right now (tenant user-secret API), and `Tap` shipped with the marketplace only weeks ago. Promoting either would freeze a shape whose author is still holding the pen.
- **Out of scope, deliberately: `sdn.cozystack.io`.** #35 argues for a `local.sdn.cozystack.io` group and for one coherent `sdn` family; `SecurityGroup`'s group membership is an open question inside that argument, and a group whose membership is contested cannot be promoted.
- **Out of scope, deliberately: `cozystack.io`.** #46 §9 counts four proposals adding fields to `ApplicationDefinition` (#39, #6, #46 itself, and cozystack#3448) and recommends a single consolidating pass before any of them ships. That pass has to happen before this group can be stabilised; #46 already owns the recommendation.

## Context

### What exists today

The aggregated apiserver serves three groups, all at `v1alpha1`, and the version string is a Go constant in three `register.go` files wired up at `pkg/cmd/server/start.go:80-82`:

| Group | Kinds | Served by |
|---|---|---|
| `apps.cozystack.io` | 32 kinds, registered dynamically from the `ApplicationDefinition` objects under `packages/system/*/cozyrds/` at boot | `pkg/registry/apps/application` |
| `core.cozystack.io` | `TenantNamespace`, `TenantSecret`, `TenantModule`, `Option`, `Tap` | `pkg/registry/core/*` |
| `sdn.cozystack.io` | `SecurityGroup` | `pkg/registry/sdn` |

Alongside them, CRD-served groups: `cozystack.io/v1alpha1` (`ApplicationDefinition`, `Package`, `PackageSource`, `Workload`, `WorkloadMonitor`), `backups.cozystack.io/v1alpha1` (`BackupClass`, `Plan`, `Backup`, `BackupJob`, `RestoreJob`), and `gateway.cozystack.io/v1alpha1` (`TenantGateway`).

An `apps.cozystack.io` kind's schema is not compiled in: it is a string in `ApplicationDefinition.spec.application.openAPISchema`, generated from the chart's annotated `values.yaml` by `cozyvalues-gen`, and the apiserver rolls itself when the object changes. A package can therefore change its own API surface without a core rebuild, which is what makes any of this feasible.

### Three facts about `apps.cozystack.io/v1alpha1` that `v1` has to change

**1. The spec is an open object, so nothing can be removed from it.** `pkg/cmd/server/openapi.go:169-171` marks the published `spec` schema open whenever the chart's schema does not itself declare `additionalProperties`, which is every chart. The comment is explicit about why: it preserves "the historical *spec is a superset of the documented values* behavior". Deleting a key from a chart's schema therefore does not reject it, does not prune it, and does not warn — the value is accepted and stored and simply stops rendering anything.

**2. There is no server-side validation of `spec` at all.** The structural schema built at `pkg/registry/apps/application/rest.go:107-127` is used in exactly one place: `applySpecDefaults` (`rest_defaulting.go:30-42`). Nothing validates, nothing prunes. `kubectl` will complain client-side against the published OpenAPI; a direct API write of arbitrary JSON will not.

**3. Deprecation is consequently a hand-written Go list.** When Phase 2 moved worker pools out of `kind: Kubernetes`, the fields could not be removed, so `rest.go:1927-1952` carries `removedKubernetesFields = []string{"nodeGroups", "nodeHealthCheck", "maxNodeProvisionTime"}` and emits an admission warning saying the field "is stored but has no effect". The legacy `resourcesPreset` names get the same treatment one function up (`rest.go:1892-1925`), as a `klog` line nobody sees. Both are correct responses to a missing mechanism, and neither scales: every future removal adds another hardcoded list to the apiserver, and every one of them leaves a live field that lies.

### The problem

> "Is this API stable? Can I write an operator against it?"

Today the honest answer is "in practice yes, formally no, and we cannot tell you which fields we are about to stop honouring, because we have no way to stop honouring one." Three concrete consequences:

- **A tenant cannot tell a live field from a dead one.** `spec.backup.enabled` on a Postgres and `spec.nodeGroups` on a Kubernetes look identical through the API. One works; the other has been inert since Phase 2.
- **The platform cannot finish a migration.** `backups.cozystack.io` shipped; the chart-level backup options are marked `DEPRECATED` in `api/apps/v1alpha1/*/types.go` and are still the first thing a tenant finds. Two ways to configure backups, one of them wrong, indefinitely.
- **Every new exposure design inherits the old one.** #45 cannot retire `external` because there is no version boundary to retire it at, so it correctly proposes coexistence — and coexistence with 15 charts' worth of five different render mechanisms is the permanent state unless something ends it.

## Goals

- A written, testable definition of what `v1` promises, and a per-kind list of what blocks each kind from reaching it.
- `apps.cozystack.io/v1` rejects fields it does not declare, so that removal is a real event rather than a documentation change.
- The legacy backup options and the `external: true` family are absent from `v1`, with an upgrade path that changes nothing observable about a running cluster at the moment of the flip.
- A repeatable removal ladder, so the next removal is a process step and not another hardcoded list in the apiserver.
- `v1alpha1` keeps its exact current behaviour, including its openness, for the whole deprecation window.
- No tenant action is required by the promotion itself.

### Non-goals

- **Promoting `core.cozystack.io`, `sdn.cozystack.io` or `cozystack.io`.** Reasons per group in *Scope*.
- **Designing the replacements.** `backups.cozystack.io` exists; `EndpointAttachment` and `IPAddressClaim` are #45 and #35. This proposal consumes them and does not re-decide them.
- **Designing the conversion mechanism.** That is #6. This proposal specifies what `v1` needs *from* #6 and names one gap, nothing more.
- **A `v1` for kinds whose replacement does not exist yet.** Kafka, NATS and MongoDB are discovery engines: #45 defers their external-address write-back and states plainly that it has no owner. Those kinds do not promote until it does, and pretending otherwise would make `v1` a lie in three places.
- **Changing any chart's runtime behaviour.** Every step here is an API-surface change plus a migration; a cluster that upgrades and touches nothing must render byte-identically.

## Design

### 1. What `v1` promises

Four promises, each one testable:

1. **Declared fields are honoured.** A field present in a `v1` schema does something. There is no `v1` equivalent of `removedKubernetesFields`: if a field stops having an effect, it leaves the version.
2. **Undeclared fields are rejected.** Writing an unknown key to a `v1` resource fails with a validation error naming the key. This is the promise that gives the other three teeth.
3. **No field is removed or retyped within `v1`.** Additive change only: new optional fields with defaults, new kinds, new enum members on fields documented as open-ended. Anything else is `v2`.
4. **A superseded version is served for a stated window.** When `v1alpha1` is superseded for a kind, it keeps being served, and conversion is transparent, for at least two minor releases and no less than six months (see *Support window*).

Promise 3 is deliberately narrower than "the API never changes". Defaults may change in a minor release when the change is safe for a resource that does not set the field, and status is not covered — status is observation, and observations get more precise.

### 2. Per-kind promotion, not a flag day

`apps.cozystack.io/v1` serves the kinds that are ready, and grows. It is legal for a group-version to serve a subset of a group's kinds, and discovery is the source of truth about which.

This is the load-bearing structural decision, and there are three independent reasons for it.

**The blocking argument.** A flag day makes every kind hostage to the slowest. `kind: Kafka`'s `external` cannot be removed until an allocated address can be written back into the broker's advertised listeners, and #45 states that this loop "is exactly the work item #29's review identified as ownerless". A flag-day `v1` is therefore blocked on an unowned work item with no date. Postgres does not have that problem and should not wait for it.

**The consistency argument.** #6's conversion is written between two storage versions *of one application* and lives with that application. #46 makes the package the version unit and defines a storage-version change as a package MAJOR. Both mechanisms are already per-package; a group-wide flag day would be the only thing in the system that is not, and it would have nowhere to attach its conversion.

**The honesty argument.** A `v1` that ships when the last kind is ready tells a tenant nothing for the year it takes to get there. A `v1` that grows tells them, per kind, today, whether the shape they depend on is frozen.

The cost is a discovery surface where `apps.cozystack.io/v1` and `apps.cozystack.io/v1alpha1` serve overlapping but unequal kind sets. That is exactly what upstream Kubernetes looked like for years, it is what `kubectl api-resources` is for, and the platform additionally publishes the per-kind state in the dashboard and in `ApplicationDefinition`.

### 3. `v1` closes the object

At `v1`, and only at `v1`:

- The published `spec` schema is **not** marked open. `markOpenObject` is applied only when the requested version is `v1alpha1`, preserving the historical superset behaviour exactly where it exists today.
- The structural schema already built for defaulting is additionally used to **validate and prune** on `Create` and `Update`. An unknown key is a `422` naming the field path.
- Chart values that the platform writes and the tenant must not — the restore driver's channel discussed below, `_cluster` and `_namespace`, `_version` from #6 — are **service fields**: stripped from the `v1` projection on read, preserved in storage on write. `_cluster` and `_namespace` already work this way by construction, since Flux merges them from a Secret via `valuesFrom` and they never appear in the HelmRelease's inline values.

This is the smallest change that makes `v1` mean anything, and it is close to free: the structural schema exists, it is built at every `NewREST`, and it is currently used for one thing.

There is one migration hazard and it must be handled rather than discovered: a cluster upgraded from before `v1` may hold stored values containing keys `v1` does not declare, put there by an older platform or by the open `v1alpha1` surface. Pruning them on read would silently change what the chart renders. So the rule is asymmetric — **`v1` rejects unknown keys on write, and tolerates them in storage**, reporting them through a `Warning` on read (`spec.<key> is not part of v1 and is ignored`) until a migration removes them. Only `v1alpha1` can still write them.

### 4. Two migrations, and why they cannot be one

Removing a field is two operations with a strict order, and they belong to two different mechanisms.

**The imperative half — create the replacement.** Before `spec.external` may disappear, an `EndpointAttachment` bound to an adopted `IPAddressClaim` carrying the *same address* must exist, or the tenant's customers lose an allow-listed IP. Before `spec.backup` may disappear, an equivalent `Plan` against a `BackupClass` must exist, or backups silently stop. This is object creation with cluster-specific inputs — it is a numbered platform migration under the community#58 engine (`packages/core/platform/templates/migration-hook.yaml`, `targetVersion` currently `57`), run once per cluster at upgrade.

**The declarative half — drop the key.** #6's `to` template converts `v1alpha1` values into `v1` storage values. It is a pure go-template over data: it cannot create an object, cannot read the cluster, and must not try. All it does is omit the key.

Collapsing these into one is the tempting mistake. A conversion template that "creates the attachment" would need cluster access from inside a data transformation, would run on every read as well as every write, and would have no way to be idempotent. Keeping them separate also gives the safety property that makes the whole thing shippable:

> **The storage-version flip is gated on the imperative migration having completed.** The migration that flips a kind's storage version to `v1` refuses to run while any release of that kind still carries a to-be-removed field without its replacement object present. A cluster in that state stays on `v1alpha1` storage, keeps working, and reports why.

### 5. The removal ladder

Every field removed at `v1` walks the same five steps. The ladder is the reusable output of this proposal; the two removals below are its first two users.

1. **The replacement ships and is documented.** Not "is designed" — a tenant can do the thing the field did, by another route, on a released version.
2. **The field is marked deprecated in the `v1alpha1` schema, and the API says so.** Today this means adding to a Go list in `rest.go`. It should instead be a schema annotation — `x-cozystack-deprecated: "<message>"` on the property, emitted by `cozyvalues-gen` from the existing `DEPRECATED` prose already sitting in `api/apps/v1alpha1/*/types.go` — with the apiserver walking the schema and emitting one `warning.AddWarning` per deprecated key present. That replaces two hardcoded lists with one generic mechanism and makes every existing `DEPRECATED` comment reach a user for the first time.
3. **The platform migration creates replacement objects** for every affected release, idempotently, preserving identity (the address; the backup schedule and destination).
4. **`v1` omits the field.** `to` drops it; `from` reconstructs a best-effort projection where one exists and omits it where none does.
5. **`v1alpha1` stops being served** at the end of the support window. Only then may the key leave `values.yaml` and the chart stop rendering from it.

Step 5 is worth stating loudly because it inverts the intuitive order: **the chart value outlives the API field.** The API is the contract, `values.yaml` is storage; the field can vanish from the contract while storage still carries it, and that gap is precisely what makes the flip non-disruptive.

#### The conversion round-trip check cannot survive a removal

#6 specifies a CI check: "round-trip identity on golden samples: `from(to(values_X))` equals `values_X` for every served X". For a removal that check is false by construction — `to` drops `external`, `from` cannot invent it back, and the round trip loses the key.

This is not an argument against the check; it is the check working. The fix is to make the loss *declared* rather than incidental: each version's source file gains a `dropped: [<key>, ...]` list, and the round-trip check excludes exactly the declared keys and nothing else. A removal that forgets to declare itself fails CI, which is the behaviour we want. This is a small addition to #6 and should land there, not here.

### 6. Removal one — the legacy backup options

**What goes.** The chart-level backup configuration on the five kinds that carry it: `Postgres`, `MariaDB`, `MongoDB`, `ClickHouse`, `FoundationDB` — `spec.backup.*` in each — plus the restore blocks `spec.bootstrap.*` on `Postgres` and `MongoDB`. Roughly forty properties, most already carrying `DEPRECATED` in `api/apps/v1alpha1/*/types.go`, several of them S3 credentials in a tenant-writable spec.

**What replaces it.** `backups.cozystack.io`: a `BackupClass` selecting a strategy per application type, a `Plan` for the schedule, `BackupJob` / `RestoreJob` for runs, and `Backup` as the artifact. The platform ships the `cozy-default` class over the `cozy-backups` system bucket, which is what the `useSystemBucket: true` flow on the charts already routes to.

**The prerequisite nobody expects.** `spec.backup` and `spec.bootstrap` are not purely tenant inputs. `internal/backupcontroller/cnpgstrategy_controller.go:1078-1140` (`patchPostgresAppForRestore`, via `buildPostgresAppRestorePatch`) **patches the `apps.cozystack.io` Postgres resource's own `spec.backup.s3CredentialsSecret`, `spec.backup.endpointCA` and `spec.bootstrap.*`** to drive a restore — the field comments in `api/apps/v1alpha1/postgresql/types.go` say so outright ("The CNPG backup driver writes this field on restore so credentials never land in the CR `.spec`"). The restore path actuates *through* the fields we are removing.

So the removal is blocked on re-homing that channel, and there are two viable shapes:

- **Service fields (cheaper).** The keys stay in `values.yaml` and stay driver-written, but leave the `v1` schema and join `_cluster` / `_namespace` in the stripped-on-read set. The tenant loses the ability to set them; the driver keeps its channel; nothing about the render changes. This is the recommended shape, because at the flip *nothing happens to a running cluster at all*.
- **SSA onto the CNPG `Cluster` (cleaner).** The driver already SSA-applies the `ObjectStore` and patches `spec.plugins` on the live Cluster (`cnpgstrategy_controller.go:490-496`); restore actuation could join it there and stop touching the Application entirely. Better end state, more work, and it changes the restore path's failure modes — which argues for doing it *after* the flip, not as part of it.

**Migration.** For each release with `backup.enabled: true`, synthesise a `Plan` referencing the applicable `BackupClass` with the same schedule and retention, and verify it reconciles before the storage flip is allowed. Releases with `backup.enabled: false` need nothing. Tenant-supplied S3 credentials in `s3AccessKey` / `s3SecretKey` are moved into a Secret and referenced, never copied into a new spec.

**What the `from` projection does.** Nothing clever, and that is the point: it reports the stored values, which the migration deliberately left in place. `from` is a pure function of storage, storage still carries `backup.*`, so a client reading `v1alpha1` sees the same values after the upgrade as before it. The projection does not consult the `Plan` — that would be a cluster read from inside a data transformation, which §4 rules out.

### 7. Removal two — `external` and its family

**What goes.** `spec.external` on 15 kinds (`HTTPCache`, `Kafka`, `MariaDB`, `MongoDB`, `NATS`, `OpenBao`, `OpenSearch`, `Postgres`, `Qdrant`, `RabbitMQ`, `Redis`, `TCPBalancer`, `Valkey`, `VMInstance`, `VPN`), plus `externalMethod`, `externalPorts` and `externalAllowICMP` on `VMInstance` and `externalIPs` on `VPN`.

**What replaces it.** `EndpointAttachment` (#45), attaching an address to a named tenant-facing Service of an application, drawing that address from an `IPAddressClaim` (#35) that the tenant can hold across the attachment's lifetime. #45 documents at length why this is strictly more expressive than the boolean; the relevant point here is that it is *additive*, so the two coexist for the whole window.

**The trap: `external` is four things.** It is not only the LoadBalancer gate. Across the tree it also:

- **decides certificate SANs** — `packages/apps/{mariadb,qdrant,rabbitmq,redis,nats}/templates/certmanager.yaml` all branch on it, and `packages/apps/postgres` has an explicit `injectExternalHostnameSAN` tri-state whose default *is* `external`;
- **defaults TLS on** — `packages/apps/kafka/templates/kafka.yaml` documents that "if `tls.enabled` is unset, TLS inherits from `.Values.external`", and `Qdrant` and `NATS` say the same in their field docs;
- **gates dashboard visibility** — `packages/apps/kafka/templates/dashboard-resourcemap.yaml` keys `$showExternal` off it;
- **sets `externalTrafficPolicy`** — `packages/apps/vpn/templates/service.yaml`.

Dropping the key without decoupling these would silently change certificates and TLS posture on upgrade, which is a far worse outcome than the exposure change itself.

Fortunately this is exactly what a conversion template is for, and it is the clearest illustration of why #6 is the prerequisite. The `v1alpha1` → `v1` `to` template resolves each inherited default explicitly before dropping the source:

```gotemplate
# to: v1alpha1 values -> v1 storage values (kafka, abridged)
tls:
  enabled: {{ if hasKey .tls "enabled" }}{{ .tls.enabled }}{{ else }}{{ .external | default false }}{{ end }}
# ... every other key passes through unchanged; `external` is not emitted.
```

The tri-state and inheritance disappear into a resolved boolean at migration time, once, per release. After the flip, TLS is configured by the field that configures TLS. That is a real improvement independent of exposure, and it is the strongest argument for doing the two removals in the same version boundary rather than separately.

**Migration.** Per release with `external: true`: adopt the existing LoadBalancer Service's address into an `IPAddress` with a bound `IPAddressClaim` (#35's adoption path), create an equivalent `EndpointAttachment` referencing that claim, wait for it to report `Attached` with the same address, and only then allow the storage flip. #45 already names this the gate on `external`'s sunset; this is the same gate, given a version boundary to sit at.

**Kinds that cannot do this yet.** `Kafka`, `NATS` and `MongoDB` are #45's deferred discovery engines: the address must be written back into engine configuration, and that loop has no owner. They stay at `v1alpha1`. `VMInstance` needs #45's phase-2 datapath modes (`WholeIP` / `PortList`). The remaining eleven need only #45 phase 1.

### 8. The rest of the `v1` cleanup list

`v1` is the garbage-collection point for everything the platform has already deprecated and cannot remove. Beyond the two headline removals:

| Kind | Field | State today |
|---|---|---|
| `Kubernetes` | `nodeGroups`, `nodeHealthCheck`, `maxNodeProvisionTime` | Inert since Phase 2; stored, warned about from a hardcoded Go list (`rest.go:1932`). Replaced by `KubernetesNodes`. |
| `VMInstance` | `subnets` | `Deprecated: use networks instead` (`api/apps/v1alpha1/vminstance/types.go`). |
| all | legacy flat `resourcesPreset` names (`nano` … `2xlarge`) | Aliased in `cozy-lib`, deprecation reported only to `klog` (`rest.go:1915-1925`). `v1` accepts the `<class>.<size>` form only. |
| `ClickHouse`, `Postgres` | the per-tenant `s3*` legacy backup keys | Already `DEPRECATED`; removed together with §6. |

Nothing on this list needs a new replacement — all four already have one. They are on the list because a version boundary is the only place they can leave.

### 9. Support window

`v1alpha1` remains served for a kind, with transparent conversion, for **at least two minor releases and no less than six months** after that kind reaches `v1`. Removing it is a breaking change to the platform's contract and, under #46, a core MAJOR.

Two dates are published per kind, in the `ApplicationDefinition` and in the docs: the release in which it reached `v1`, and the earliest release in which `v1alpha1` may be withdrawn. #6 already refuses to unserve a version while the controller still observes stored resources at it, which makes the window's end a check rather than a promise.

## User-facing changes

- Tenants gain `apps.cozystack.io/v1` for the kinds that have promoted. Existing manifests keep working unchanged against `v1alpha1`, and `kubectl get postgres` continues to return whichever version the client asks for.
- Writing an undeclared field to a `v1` resource now fails instead of being silently stored. This is the intended behaviour change and the only one a tenant can observe at the flip.
- Backups are configured through `backups.cozystack.io` only, and external exposure through `EndpointAttachment` only, once a kind is at `v1`. Both routes are already the documented ones.
- The dashboard shows a kind's API version and its deprecation state, sourced from the same `ApplicationDefinition` fields.
- Integrators consuming the `api/apps/v1alpha1` Go module get an `api/apps/v1` module alongside it. The old module keeps being tagged for the support window; #46 separately asks whether that module tracks apps or core, and this proposal does not pre-empt that.

## Upgrade and rollback compatibility

- **Upgrade.** The platform migration creates replacement objects; the storage flip runs only for kinds where it succeeded for every release. A cluster where it did not stays on `v1alpha1` storage and reports which releases blocked it. Nothing about a running workload changes at either step.
- **Rollback within the window.** `v1alpha1` is still served and #6's `from` templates still convert, so a rolled-back client keeps working. Rolling the *platform* back to a release predating `v1` restores the previous `ApplicationDefinition` and its storage version, and #6's conversion converts stored values back — a property its round-trip CI check enforces, extended by the `dropped:` declaration of §5.
- **Rollback after the window.** Once `v1alpha1` is withdrawn, this is a downgrade across a core MAJOR and is not supported, which is what a MAJOR means.
- **Irreversible steps, flagged.** The migration's *object creations* are not undone by a platform rollback: an `EndpointAttachment` and its adopted `IPAddressClaim` survive, holding the same address, and a rolled-back chart rendering `external: true` would then publish the same address twice. The migration must therefore leave the legacy Service in place and let the attachment be additive — which is #45's model anyway — and remove the legacy Service only at step 5, past the point of return.

## Security

- **The main effect is a reduction.** Closing the object removes a path where arbitrary tenant-supplied JSON is stored verbatim in a HelmRelease, and §6 removes S3 access keys from tenant-writable spec fields on five kinds.
- **New surface: none.** No new kind, no new controller, no new RBAC. The conversion templates run inside the apiserver under #6's sprig denylist (no `env`, `readFile`, `lookup`, `getHostByName`), which is #6's decision and is not relaxed here.
- **The migration runs with platform privilege** and writes objects in tenant namespaces. It creates only `EndpointAttachment`, `IPAddressClaim`, `Plan` and Secrets derived from values that were already in the tenant's own release, and copies no credential across a namespace boundary.
- **One honest caveat.** `v1`'s validation is a correctness boundary, not a security boundary: `v1alpha1` remains open for the whole window, so anything a tenant could write before, they can still write through the older version. The security benefit lands at step 5, not at the flip.

## Failure and edge cases

- **A release carries `external: true` and its kind's attachment path is not ready** → the kind does not promote; no migration runs.
- **The address adoption fails** (the address is outside every `IPAddressClass` pool) → migration reports the release and does not flip; the cluster stays on `v1alpha1` with the legacy Service intact.
- **A stored value contains a key `v1` does not declare** → tolerated in storage, reported as a `Warning` on `v1` reads, rejected only on `v1` writes (§3).
- **A tenant writes an undeclared key to `v1`** → `422` naming the field path. Writing it through `v1alpha1` still succeeds during the window.
- **Round-trip identity fails in CI for a removed key** → expected; passes only if the key is listed in that version's `dropped:` declaration (§5).
- **The CNPG restore driver patches `spec.backup` on a `v1`-storage Postgres** → it writes service fields, which are accepted from the platform's field manager and stripped from the tenant projection. If the driver has not been re-homed, the kind does not promote — this is the §6 prerequisite as a hard gate, not a warning.
- **Two consecutive storage-version bumps in one release** → refused. #6 states it does not support a transitive conversion path, so a kind reaching `v1` may not also move again in the same release.
- **A conversion template renders invalid YAML at runtime** → #6's behaviour: `500`, and the write or read is refused. Caught in CI on golden samples first.
- **A kind is promoted while a proposal is scheduled to change its spec** (the `Tenant` / #39 case) → prevented by process, not by code: the promotion checklist requires no open proposal targeting the kind's spec shape.

## Testing

- **Unit (apiserver):** validation and pruning at `v1` versus openness at `v1alpha1` over a matrix of declared, undeclared and service fields; the tolerate-in-storage / reject-on-write asymmetry; deprecation warnings emitted from `x-cozystack-deprecated` rather than a Go list, with the two existing hardcoded lists deleted and their tests re-pointed.
- **Unit (conversion):** for every promoted kind, `to` / `from` on golden samples, including the declared-`dropped:` exclusions; the kafka TLS-inheritance resolution of §7 asserted explicitly, since it is the case where a silent regression would be invisible.
- **e2e (postgres, the flagship for both removals):** create at `v1alpha1` with `backup.enabled: true` and `external: true`; run the migration; assert a `Plan` reconciles against `cozy-default`, an `EndpointAttachment` reports `Attached` on **the same address**, the storage flip happens, the resource reads correctly as both versions, and an external client is uninterrupted throughout.
- **e2e (negative):** the same cluster with the attachment path disabled — assert the flip refuses, the release is named, and `v1alpha1` keeps working.
- **e2e (upgrade):** a cluster carrying stored `nodeGroups` on a `Kubernetes` — assert the value survives the flip, reads with a warning, and is rejected on a `v1` write.
- **Manual, once per kind:** the promotion checklist (§ *Rollout* step 2) is a human gate; it is not automatable and should not pretend to be.

## Rollout

1. **#6 lands**, with the `dropped:` declaration of §5 folded in. Nothing promotes before this.
2. **The `v1` machinery lands, unused:** version-aware openness, validation and pruning, service-field stripping, schema-driven deprecation warnings replacing the two Go lists, and the per-kind promotion checklist as a documented gate.
3. **Cohort 0 — kinds with neither removal:** `Bucket`, `Harbor`, `VMDisk`, `VPC`, `KubernetesNodes`. Nothing to migrate; these exercise the machinery on real kinds and are the proof it is non-disruptive.
4. **`Kubernetes` promotes**, taking the Phase 2 `nodeGroups` family with it — the first real removal, and one whose replacement shipped long ago.
5. **Backups removal**, once the CNPG restore channel is re-homed (§6): `Postgres`, `MariaDB`, `ClickHouse`, `FoundationDB`.
6. **`external` removal**, once #45 phase 1 and #35's adoption path exist: the eleven passthrough kinds. `VMInstance` follows at #45 phase 2.
7. **`Tenant` and the module kinds promote** after #39 and #25 settle the module fields and the packages they live in.
8. **Deferred indefinitely:** `Kafka`, `NATS`, `MongoDB` — until discovery-engine address write-back has an owner. Stated as a known gap in the docs rather than quietly omitted.
9. **Window ends per kind:** `v1alpha1` withdrawn, chart values deleted, legacy Services released.

## Open questions

1. **Does `v1` mean `apps.cozystack.io` only, or the whole tenant-facing API?** This proposal argues for `apps` alone and gives per-group reasons, but a customer asking "is the Cozystack API stable" means all of it. The alternative is a slower, wider `v1` that waits for `TenantSecret`, `Tap`, `SecurityGroup`'s group placement and the `ApplicationDefinition` consolidation pass. That is a scope call for the API owners.
2. **Should the `Warning` on undeclared stored keys be a `Condition` instead?** A warning is per-request and easy to miss; a condition on the resource would be visible in the dashboard and greppable across a cluster. Against it: applications' status is a projection over HelmReleases, and #45 notes that every cross-object lookup added there makes reads and Lists worse.
3. **Do `v1alpha1` and `v1` need different defaults for the same field?** §7's TLS inheritance is resolved at conversion time, but a client that keeps writing `v1alpha1` after the flip re-creates the inherited default on every write. Either `v1alpha1`'s defaulting is frozen at its pre-flip behaviour (simple, slightly wrong) or it inherits from the `v1` resolution (correct, and a behaviour change inside a version we promised not to change).
4. **What is the promotion checklist's approving authority?** The `cmd/api-gate` CI check already routes sizeable API changes to a designated API owner. Promotion is the largest such change there is; it should plausibly need both owners, and that is a governance decision, not a design one.

## Alternatives considered

**Promote everything to `v1` as it stands, and remove nothing.** The cheapest option, and the one that gets asked for. Rejected because it freezes `spec.backup` and `spec.external` into a version we promise not to break, permanently entrenching the two migrations we are in the middle of. A `v1` containing fields we already know are wrong costs more than staying at `v1alpha1`.

**Go to `v1beta1` first.** The upstream ladder, and defensible. Rejected because it buys a naming convention and no mechanism: `v1beta1` needs the same conversion machinery, the same closed object and the same removals, and arriving there without them changes nothing except the string. If #6 slips badly, a `v1beta1` cohort 0 is a reasonable fallback — but it is a fallback, not the plan.

**Remove the fields at `v1alpha1`, without a new version.** Precedent exists — Phase 2 did it for `nodeGroups`. Rejected because it is not removal: the field stays acceptable, stays stored, and stays inert, and the platform accumulates another hardcoded warning list. The mechanism the precedent had to invent is the evidence against repeating it.

**A group-wide flag day for `apps.cozystack.io`.** Simpler to explain and better for docs. Rejected on the blocking, consistency and honesty arguments of §2, of which the first is decisive: it makes `v1` wait on an unowned work item.

**Version by package rather than by API group (#46's unit, taken literally).** Tempting, since #46 argues the package is the right version unit and it is right about packages. Rejected as the *user-facing* version: a tenant writes `apiVersion: apps.cozystack.io/v1`, not a package version, and Kubernetes has no way to express "this kind is at package 2.3.0". The two coexist without conflict — the package version says what shipped, the API version says what is promised — and §2's per-kind promotion is what keeps them aligned.

**Keep `external` and make it a shorthand for an `EndpointAttachment`.** A compatibility layer instead of a removal: the chart would create the attachment on the tenant's behalf. Rejected because it re-creates the coupling #45 exists to break — the attachment's lifecycle would again be a Helm upgrade of the release, the address would again be un-choosable, and `external`'s four other meanings would still be tangled. It also leaves the field in `v1`, which is the thing we are trying not to do.

**Do nothing.** `v1alpha1` forever; the deprecated fields stay; each removal adds another list to `rest.go`; #45 coexists with 15 charts' five render mechanisms permanently; and the honest answer to "is this API stable" stays "formally, no."

---

<!--
Inspired by KubeVirt enhancement proposals
(https://github.com/kubevirt/enhancements) and Kubernetes Enhancement
Proposals (KEPs).
-->
