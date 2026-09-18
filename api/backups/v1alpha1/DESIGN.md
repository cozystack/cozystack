# Cozystack Backups – Core API & Contracts (Draft)

## 1. Overview

Cozystack’s backup subsystem provides a generic, composable way to back up and restore managed applications:

* Every **application instance** can have one or more **backup plans**.
* Backups are stored in configurable **storage locations**.
* The mechanics of *how* a backup/restore is performed are delegated to **strategy drivers**, each implementing driver-specific **BackupStrategy** CRDs.

The core API:

* Orchestrates **when** backups happen and **where** they’re stored.
* Tracks **what** backups exist and their status.
* Defines contracts with drivers via shared resources (`BackupJob`, `Backup`, `RestoreJob`).

It does **not** implement the backup logic itself.

This document covers only the **core** API and its contracts with drivers, not driver implementations.

### Platform-managed default flow

As of Phase 2, Cozystack also ships **opinionated defaults** for the entire stack:

* A platform-managed S3 bucket `cozy-backups` (provisioned through `apps.cozystack.io/Bucket` in `tenant-root`).
* Pre-rendered `Strategy` CRs `cozy-default-{cnpg,mariadb,etcd,altinity,foundationdb,velero-vminstance,velero-vmdisk}`.
* A single cluster-wide `BackupClass` `cozy-default` whose `spec.strategies[]` binds each supported `apps.cozystack.io/<Kind>` to the matching strategy.
* A `CredentialsProjector` inside `backupstrategy-controller` that copies the bucket-controller Secret into each tenant namespace as `cozy-backups-creds` on demand (per `BackupJob`/`RestoreJob` reconcile) and into a configured list of system namespaces (e.g. `cozy-velero`) on a periodic tick.

Tenants reference `cozy-default` from `BackupJob`/`Plan`/`RestoreJob`; they never see the bucket controller Secret, never write to a Secret in their own namespace (default RBAC denies it), and never share an S3 path with another tenant (every default strategy template prefixes the object key with `<namespace>/<application>`).

The platform-managed flow is layered *on top of* — not in place of — the core API. Custom `BackupClass`/Strategy CRs targeting other buckets continue to work; tenants picking `cozy-default` simply opt into the platform's pre-wired stack.

**Open gap (FoundationDB):** the `cozy-default-foundationdb` Strategy CR is shipped but **not** bound by `cozy-default`. Restore for FoundationDB runs `fdbrestore` from inside the `cozy-foundationdb-operator` deployment, which does not yet mount `cozy-backups-creds` with the blob credentials file. Admins who want platform-default FDB backups today must either wire the operator deployment manually or use a custom BackupClass — see [Backup Classes](../../../docs/operations/backup-classes.md) for the follow-up plan.

**Tenant visibility:** projected Secrets carry no `internal.cozystack.io/tenantresource` label, so the lineage-controller webhook does not promote them to a `TenantSecret` view. Combined with the default RBAC (no core/v1.Secret verbs on tenant ServiceAccounts), the projected credentials are unreadable from a tenant kubeconfig even when they sit in the tenant's own namespace; the operator Pods mount the Secret via kubelet, which bypasses tenant RBAC.

---

## 2. Goals and non-goals

### Goals

* Provide a **stable core API** for:

  * Declaring **backup plans** per application.
  * Configuring **storage targets** (S3, in-cluster bucket, etc.).
  * Tracking **backup artifacts**.
  * Initiating and tracking **restores**.
* Allow multiple **strategy drivers** to plug in, each supporting specific kinds of applications and strategies.
* Let application/product authors implement backup for their kinds by:

  * Creating **Plan** objects referencing a **driver-specific strategy**.
  * Not having to write a backup engine themselves.

### Non-goals

* Implement backup logic for any specific application or storage backend.
* Define the internal structure of driver-specific strategy CRDs.
* Handle tenant-facing UI/UX (that’s built on top of these APIs).

---

## 3. Architecture

High-level components:

* **Core backups controller(s)** (Cozystack-owned):

  * Group: `backups.cozystack.io`
  * Own:

    * `Plan`
    * `BackupJob`
    * `Backup`
    * `RestoreJob`
  * Responsibilities:

    * Schedule backups based on `Plan`.
    * Create `BackupJob` objects when due.
    * Provide stable contracts for drivers to:

      * Perform backups and create `Backup`s.
      * Perform restores based on `Backup`s.

* **Strategy drivers** (pluggable, possibly third-party):

  * Their own API groups, e.g. `jobdriver.backups.cozystack.io`.
  * Own **strategy CRDs** (e.g. `JobBackupStrategy`).
  * Implement controllers that:

    * Watch `BackupJob` / `RestoreJob`.
    * Match runs whose `strategyRef` GVK they support.
    * Execute backup/restore logic.
    * Create and update `Backup` and run statuses.

Strategy drivers and core communicate entirely via Kubernetes objects; there are no webhook/HTTP calls between them.

* **Storage drivers** (pluggable, possibly third-party):

  * **TBD**

---

## 4. Core API resources

### 4.1 Plan

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=Plan`

**Purpose**
Describe **when**, **how**, and **where** to back up a specific managed application.

**Key fields (spec)**

```go
type PlanSpec struct {
    // Application to back up.
    // If apiGroup is not specified, it defaults to "apps.cozystack.io".
    ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

    // BackupClassName references a BackupClass that contains strategy and other parameters (e.g. storage reference).
    // The BackupClass will be resolved to determine the appropriate strategy and parameters
    // based on the ApplicationRef.
    BackupClassName string `json:"backupClassName"`

    // When backups should run.
    Schedule PlanSchedule `json:"schedule"`
}
```

`PlanSchedule` (initially) supports only cron:

```go
type PlanScheduleType string

const (
    PlanScheduleTypeEmpty PlanScheduleType = ""
    PlanScheduleTypeCron  PlanScheduleType = "cron"
)
```

```go
type PlanSchedule struct {
    // Type is the schedule type. Currently only "cron" is supported.
    // Defaults to "cron".
    Type PlanScheduleType `json:"type,omitempty"`

    // Cron expression (required for cron type).
    Cron string `json:"cron,omitempty"`
}
```

**Plan reconciliation contract**

Core Plan controller:

1. **Read schedule** from `spec.schedule` and compute the next fire time.
2. When due:

   * Create a `BackupJob` in the same namespace:

     * `spec.planRef.name = plan.Name`
     * `spec.applicationRef = plan.spec.applicationRef` (normalized with default apiGroup if not specified)
     * `spec.backupClassName = plan.spec.backupClassName`
   * Set `ownerReferences` so the `BackupJob` is owned by the `Plan`.

**Note:** The `BackupJob` controller resolves the `BackupClass` to determine the appropriate strategy and parameters, based on the `ApplicationRef`. The strategy template is processed with a context containing the `Application` object and `Parameters` from the `BackupClass`.

The Plan controller does **not**:

* Execute backups itself.
* Modify driver resources or `Backup` objects.
* Touch `BackupJob.spec` after creation.

---

### 4.2 BackupClass

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=BackupClass`

**Purpose**
Define a class of backup configurations that encapsulate strategy and parameters per application type. `BackupClass` is a cluster-scoped resource that allows admins to configure backup strategies and parameters in a reusable way.

**Key fields (spec)**

```go
type BackupClassSpec struct {
    // Strategies is a list of backup strategies, each matching a specific application type.
    Strategies []BackupClassStrategy `json:"strategies"`
}

type BackupClassStrategy struct {
    // StrategyRef references the driver-specific BackupStrategy (e.g., Velero).
    StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

    // Application specifies which application types this strategy applies to.
    // If apiGroup is not specified, it defaults to "apps.cozystack.io".
    Application ApplicationSelector `json:"application"`

    // Parameters holds strategy-specific parameters, like storage reference.
    // Common parameters include:
    // - backupStorageLocationName: Name of Velero BackupStorageLocation
    // +optional
    Parameters map[string]string `json:"parameters,omitempty"`
}

type ApplicationSelector struct {
    // APIGroup is the API group of the application.
    // If not specified, defaults to "apps.cozystack.io".
    // +optional
    APIGroup *string `json:"apiGroup,omitempty"`

    // Kind is the kind of the application (e.g., VirtualMachine, MariaDB).
    Kind string `json:"kind"`
}
```

**BackupClass resolution**

* When a `BackupJob` or `Plan` references a `BackupClass` via `backupClassName`, the controller:
  1. Fetches the `BackupClass` by name.
  2. Matches the `ApplicationRef` against strategies in the `BackupClass`:
     * Normalizes `ApplicationRef.apiGroup` (defaults to `"apps.cozystack.io"` if not specified).
     * Finds a strategy where `ApplicationSelector` matches the `ApplicationRef` (apiGroup and kind).
  3. Returns the matched `StrategyRef` and `Parameters`.
* Strategy templates (e.g., Velero's `backupTemplate.spec`) are processed with a context containing:
  * `Application`: The application object being backed up.
  * `Parameters`: The parameters from the matched `BackupClassStrategy`.

**Parameters**

* Parameters are passed via `Parameters` in the `BackupClass` (e.g., `backupStorageLocationName` for Velero).
* The driver uses these parameters to resolve the actual resources (e.g., Velero's `BackupStorageLocation` CRD).

---

### 4.3 BackupJob

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=BackupJob`

**Purpose**
Represent a single **execution** of a backup operation, typically created when a `Plan` fires or when a user triggers an ad-hoc backup.

**Key fields (spec)**

```go
type BackupJobSpec struct {
    // Plan that triggered this run, if any.
    PlanRef *corev1.LocalObjectReference `json:"planRef,omitempty"`

    // Application to back up.
    // If apiGroup is not specified, it defaults to "apps.cozystack.io".
    ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

    // BackupClassName references a BackupClass that contains strategy and related parameters
    // The BackupClass will be resolved to determine the appropriate strategy and parameters
    // based on the ApplicationRef.
    BackupClassName string `json:"backupClassName"`
}
```

**Key fields (status)**

```go
type BackupJobStatus struct {
    Phase       BackupJobPhase         `json:"phase,omitempty"`
    BackupRef   *corev1.LocalObjectReference `json:"backupRef,omitempty"`
    StartedAt   *metav1.Time           `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time           `json:"completedAt,omitempty"`
    Message     string                 `json:"message,omitempty"`
    Conditions  []metav1.Condition     `json:"conditions,omitempty"`
}
```

`BackupJobPhase` is one of: `Pending`, `Running`, `Succeeded`, `Failed`.

**BackupJob contract with drivers**

* Core **creates** `BackupJob` and must treat `spec` as immutable afterwards.
* Each driver controller:

  * Watches `BackupJob`.
  * Resolves the `BackupClass` referenced by `spec.backupClassName`.
  * Matches the `ApplicationRef` against strategies in the `BackupClass` to find the appropriate strategy.
  * Reconciles runs where the resolved strategy's `apiGroup/kind` matches its **strategy type(s)**.
* Driver responsibilities:

  1. On first reconcile:

     * Set `status.startedAt` if unset.
     * Set `status.phase = Running`.
  2. Resolve inputs:

     * Resolve `BackupClass` from `spec.backupClassName`.
     * Match `ApplicationRef` against `BackupClass` strategies to get `StrategyRef` and `Parameters`.
     * Read `Strategy` (driver-owned CRD) from `StrategyRef`.
     * Read `Application` from `ApplicationRef`.
     * Extract parameters from `Parameters` (e.g., `backupStorageLocationName` for Velero).
     * Process strategy template with context: `Application` object and `Parameters` from `BackupClass`.
  3. Execute backup logic (implementation-specific).
  4. On success:

     * Create a `Backup` resource (see below).
     * Set `status.backupRef` to the created `Backup`.
     * Set `status.completedAt`.
     * Set `status.phase = Succeeded`.
  5. On failure:

     * Set `status.completedAt`.
     * Set `status.phase = Failed`.
     * Set `status.message` and conditions.

Drivers must **not** modify `BackupJob.spec` or delete `BackupJob` themselves.

---

### 4.4 Backup

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=Backup`

**Purpose**
Represent a single **backup artifact** for a given application, decoupled from a particular run. usable as a stable, listable “thing you can restore from”.

**Key fields (spec)**

```go
type BackupSpec struct {
    ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`
    PlanRef        *corev1.LocalObjectReference     `json:"planRef,omitempty"`
    StrategyRef    corev1.TypedLocalObjectReference `json:"strategyRef"`
    TakenAt        metav1.Time                      `json:"takenAt"`
    DriverMetadata map[string]string                `json:"driverMetadata,omitempty"`
}
```

**Note:** Parameters are not stored directly in `Backup`. Instead, they are resolved from `BackupClass` parameters when the backup was created. The storage location is managed by the driver (e.g., Velero's `BackupStorageLocation`) and referenced via parameters in the `BackupClass`.

**Key fields (status)**

```go
type BackupStatus struct {
    Phase      BackupPhase       `json:"phase,omitempty"` // Pending, Ready, Failed, etc.
    Artifact   *BackupArtifact   `json:"artifact,omitempty"`
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

`BackupArtifact` describes the artifact (URI, size, checksum).

**Backup contract with drivers**

* On successful completion of a `BackupJob`, the **driver**:

  * Creates a `Backup` in the same namespace (typically owned by the `BackupJob`).
  * Populates `spec` fields with:

    * The application reference.
    * The strategy reference (resolved from `BackupClass` during `BackupJob` execution).
    * `takenAt`.
    * Optional `driverMetadata`.
  * Sets `status` with:

    * `phase = Ready` (or equivalent when fully usable).
    * `artifact` describing the stored object.
* Core:

  * Treats `Backup` spec as mostly immutable and opaque.
  * Uses it to:

    * List backups for a given application/plan.
    * Anchor `RestoreJob` operations.
    * Implement higher-level retention policies — see §4.6.

**Note:** Parameters are resolved from `BackupClass` when the `BackupJob` is created. The driver uses these parameters to determine where to store backups. The storage location itself is managed by the driver (e.g., Velero's `BackupStorageLocation` CRD) and is not directly referenced in the `Backup` resource. When restoring, the driver resolves the storage location from the original `BackupClass` parameters or from the driver's own metadata.

**Backup cleanup**

Cozystack owns the lifecycle of a `Backup` artifact end to end. The platform is the single authority that decides when a backup is removed, and it MUST NOT delegate that decision to any mechanism controlled by the application's own operator — not a TTL, not a retention window, not an object-store lifecycle rule (CNPG `Cluster.spec.backup.retentionPolicy`, MariaDB `maxRetention`, and the like are left unset). Retention is expressed only through `BackupRetentionPolicy` (§4.6) and enforced only by the core sweep, so an operator upgrade or a changed operator default can never silently expire, keep, or resurrect a backup the platform still accounts for.

Deletion is driven through the `Backup` object: removing it fires the `backups.cozystack.io/cleanup` finalizer, and the strategy driver reclaims both the record and the physical archive on the platform's behalf.

Every strategy driver MUST honor this contract:

1. **Platform-owned, single authority.** The application operator's native retention MUST be unset; the core retention sweep is the only mechanism that deletes backup artifacts, so the operator and the platform never race over the same archive.
2. **Idempotency and finalizer-hold.** The `backups.cozystack.io/cleanup` finalizer holds the `Backup` until the driver confirms the archive is gone; a retried or duplicated deletion of the same backup is a no-op.
3. **Serialization.** Physical deletion for a server MUST NOT run concurrently with an in-flight backup or WAL archiving for that same server.

---

### 4.5 RestoreJob

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=RestoreJob`

**Purpose**
Represent a single **restore operation** from a `Backup`, either back into the same application or into a new target application.

**Key fields (spec)**

```go
type RestoreJobSpec struct {
    // Backup to restore from.
    BackupRef corev1.LocalObjectReference `json:"backupRef"`

    // Target application; if omitted, drivers SHOULD restore into
    // backup.spec.applicationRef.
    TargetApplicationRef *corev1.TypedLocalObjectReference `json:"targetApplicationRef,omitempty"`
}
```

**Key fields (status)**

```go
type RestoreJobStatus struct {
    Phase       RestoreJobPhase   `json:"phase,omitempty"` // Pending, Running, Succeeded, Failed
    StartedAt   *metav1.Time      `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time      `json:"completedAt,omitempty"`
    Message     string            `json:"message,omitempty"`
    Conditions  []metav1.Condition `json:"conditions,omitempty"`
}
```

**RestoreJob contract with drivers**

* RestoreJob is created either manually or by core.
* Driver controller:

  1. Watches `RestoreJob`.
  2. On reconcile:

     * Fetches the referenced `Backup`.
     * Determines effective:

       * **Strategy**: `backup.spec.strategyRef`.
       * **Storage**: Resolved from driver metadata or `BackupClass` parameters (e.g., `backupStorageLocationName` stored in `driverMetadata` or resolved from the original `BackupClass`).
       * **Target application**: `spec.targetApplicationRef` or `backup.spec.applicationRef`.
     * If effective strategy’s GVK is one of its supported strategy types → driver is responsible.
  3. Behaviour:

     * On first reconcile, set `status.startedAt` and `phase = Running`.
     * Resolve `Backup`, storage location (from driver metadata or `BackupClass`), `Strategy`, target application.
     * Execute restore logic (implementation-specific).
     * On success:

       * Set `status.completedAt`.
       * Set `status.phase = Succeeded`.
     * On failure:

       * Set `status.completedAt`.
       * Set `status.phase = Failed`.
       * Set `status.message` and conditions.

Drivers must not modify `RestoreJob.spec` or delete `RestoreJob`.

---

### 4.6 BackupRetentionPolicy

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=BackupRetentionPolicy` (namespaced)

**Purpose**
Cap how many `Backup` artifacts survive for an application and for how long — the higher-level retention earmarked in §4.4. A policy is a named set of rules; membership is decided by a label on `Backup`, not by a selector.

**Key fields (spec)**

```go
type BackupRetentionPolicySpec struct {
    // Never delete below this many newest Ready backups; overrides MaxAge. Defaults to 1.
    MinCount int32 `json:"minCount"`
    // Keep at most this many newest Ready backups. Unset means unbounded.
    MaxCount *int32 `json:"maxCount,omitempty"`
    // Delete backups older than this, by spec.takenAt, bounded by MinCount.
    // Suffix h/d/w (e.g. "7d", "90d"); metav1.Duration is not used because it stops at hours.
    MaxAge string `json:"maxAge,omitempty"`
}
```

There is intentionally no selector: a `Backup` carries exactly one policy name in a label — a scalar — so it is never claimed by two policies at once.

**Use cases**

The three knobs are orthogonal, and picking the right one starts from the intent:

* `minCount` is a floor — "keep at least N newest, whatever their age". It guarantees a minimum but sets no ceiling: with many young backups it keeps them all. Example: a fresh backup plus a six-month-old manual one under `minCount: 2, maxAge: 30d` — the old one survives because it is among the two newest, even though it is past `maxAge`.
* `maxAge` is a TTL — the natural knob for regular scheduled backups, expressing a recovery window in time.
* `maxCount` is a ceiling — "keep at most N". It is the only knob that bounds the object count (and therefore storage cost) independently of schedule frequency, which matters in three situations `minCount`/`maxAge` cannot cover: an unbounded S3 footprint under a frequent cron, a burst of ad-hoc backups before a migration, and a misconfigured "every minute" schedule. In all three the backups are still young, so `maxAge` does not fire and only a count ceiling trims them.

`minCount` cannot substitute for `maxCount`: a floor never trims a large set of young backups.

| State | `minCount=2, maxAge=30d` | `maxCount=5` |
|---|---|---|
| 2 backups (fresh + 180d) | both kept | both kept |
| 20 young backups (<30d, burst or frequent cron) | all 20 kept | 5 kept |

**Recovery-window caveat.** On a continuous-archiving driver `maxCount` indirectly bounds the recovery window: under an hourly `Plan`, `maxCount: 3` collapses PITR to roughly three hours, because dropping the oldest base backups drops the WALs they anchored. This is intuitive for `maxAge` but surprising for `maxCount` at high frequency — surface the effective window rather than only the count.

**Binding fields**

A `Backup` joins a policy through the `backups.cozystack.io/retention-policy` label, stamped by the core controller when the `Backup` is created. One new optional field, `retentionPolicyName`, feeds it on `Plan`, `BackupJob`, and `BackupClass`, capped at 63 characters because the value lands in a DNS-1123 label:

```go
// Plan.spec, BackupJob.spec, and BackupClass.spec (BackupClass as the namespace default).
RetentionPolicyName string `json:"retentionPolicyName,omitempty"`
```

Resolution when a `Backup` is created, highest precedence first: `BackupJob.spec.retentionPolicyName` (or `Plan.spec.retentionPolicyName`, inherited onto the BackupJob), then `BackupClass.spec.retentionPolicyName`, then none — a `Backup` with no label is never swept.

**Retention semantics**

For a policy `P` in namespace `N`:

1. List `Backup` in `N` labeled `retention-policy=P.name`; keep only `status.phase == Ready`. `Failed`/`Pending` backups never consume the floor and are pruned only by `MaxAge`.
2. Sort by `spec.takenAt` descending.
3. Protect the newest `MinCount` unconditionally.
4. From the rest, delete a backup when its index `>= MaxCount` **or** `now - takenAt > MaxAge`. `MinCount` always wins over `MaxAge`.
5. Delete via the `Backup` object; the `backups.cozystack.io/cleanup` finalizer runs the driver's cleanup path.

The sweep is an idempotent set difference — safe to run on every reconcile and on `Backup` create/delete/label events.

**Namespace-local matching**

Both `Backup` and `BackupRetentionPolicy` are namespaced, and matching is scoped to the policy's own namespace. A policy named `default` in one namespace and one so named in another are distinct objects governing disjoint backup sets, so identical names never collide. A name identifies a policy only within a namespace, which suffices because a `Backup` and its policy always share one.

**Migration between policies**

Moving artifacts to another policy is a label change, with no `Plan` edit:

```bash
kubectl label backup -n tenant-acme \
  -l backups.cozystack.io/retention-policy=default \
  backups.cozystack.io/retention-policy=keep-90d --overwrite
```

Changing `retentionPolicyName` on a `Plan` moves only future backups; existing ones stay until relabeled. Relabeling onto a stricter policy may delete artifacts on the next sweep; onto a laxer one, it spares them.

**Examples**

Platform default, shipped per tenant namespace and wired as the BackupClass default:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupRetentionPolicy
metadata:
  name: default
  namespace: tenant-acme
spec:
  minCount: 3
  maxAge: 7d
---
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: cozy-default        # cluster-scoped platform BackupClass
spec:
  retentionPolicyName: default
  strategies:
    - application: { apiGroup: apps.cozystack.io, kind: Postgres }
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: CNPG
        name: cozy-default-cnpg
```

A tenant creating one policy and using it from both a scheduled `Plan` and an ad-hoc `BackupJob`:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupRetentionPolicy
metadata: { name: keep-90d, namespace: tenant-acme }
spec:
  minCount: 5
  maxCount: 60
  maxAge: 90d
---
apiVersion: backups.cozystack.io/v1alpha1
kind: Plan
metadata: { name: pg-src-daily, namespace: tenant-acme }
spec:
  applicationRef: { apiGroup: apps.cozystack.io, kind: Postgres, name: pg-src }
  backupClassName: cozy-default
  schedule: { type: cron, cron: "0 */6 * * *" }
  retentionPolicyName: keep-90d          # overrides BackupClass default
---
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata: { name: pg-src-adhoc, namespace: tenant-acme }
spec:
  applicationRef: { apiGroup: apps.cozystack.io, kind: Postgres, name: pg-src }
  backupClassName: cozy-default
  retentionPolicyName: keep-90d          # same policy for an ad-hoc run
```

The resulting `Backup`, with the label stamped by the controller:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: Backup
metadata:
  name: pg-src-20260910-1040
  namespace: tenant-acme
  labels:
    backups.cozystack.io/retention-policy: keep-90d
spec:
  applicationRef: { apiGroup: apps.cozystack.io, kind: Postgres, name: pg-src }
  planRef: { name: pg-src-daily }
  strategyRef: { apiGroup: strategy.backups.cozystack.io, kind: CNPG, name: cozy-default-cnpg }
  takenAt: "2026-09-10T10:40:00Z"
```

---

## 5. Strategy drivers (high-level)

Strategy drivers are separate controllers that:

* Define their own **strategy CRDs** (e.g. `JobBackupStrategy`) in their own API groups:

  * e.g. `jobdriver.backups.cozystack.io/v1alpha1, Kind=JobBackupStrategy`
* Implement the **BackupJob contract**:

  * Watch `BackupJob`.
  * Filter by `spec.strategyRef.apiGroup/kind`.
  * Execute backup logic.
  * Create/update `Backup`.
* Implement the **RestoreJob contract**:

  * Watch `RestoreJob`.
  * Resolve `Backup`, then effective `strategyRef`.
  * Filter by effective strategy GVK.
  * Execute restore logic.

The core backups API **does not** dictate:

* The fields and structure of driver strategy specs.
* How drivers implement backup/restore internally (Jobs, snapshots, native operator CRDs, etc.).

Drivers are interchangeable as long as they respect:

* The `BackupJob` and `RestoreJob` contracts.
* The shapes and semantics of `Backup` objects.

---

## 6. Summary

The Cozystack backups core API:

* Uses a single group, `backups.cozystack.io`, for all core CRDs.
* Cleanly separates:

  * **When** (Plan schedule) – core-owned.
  * **How & where** (BackupClass) – central configuration unit that encapsulates strategy and parameters (e.g., storage reference) per application type, resolved per BackupJob/Plan.
  * **Execution** (BackupJob) – created by Plan when schedule fires, resolves BackupClass to get strategy and parameters, then delegates to driver.
  * **What backup artifacts exist** (Backup) – driver-created but cluster-visible.
  * **Restore lifecycle** (RestoreJob) – shared contract boundary.
  * **Retention** (BackupRetentionPolicy) – label-scoped, namespaced pruning of Backup artifacts by min/max count and age.
* Allows multiple strategy drivers to implement backup/restore logic without entangling their implementation with the core API.

