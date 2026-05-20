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

    // Parameters is copied into every BackupJob this Plan creates and
    // overrides the matched BackupClassStrategy.Parameters on a per-key
    // basis (see §4.2 "Parameters semantics"). Same value space and the
    // same no-credentials rule as BackupClassStrategy.Parameters.
    // +optional
    Parameters map[string]string `json:"parameters,omitempty"`
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
     * `spec.parameters = plan.spec.parameters` (deep-copied; carries the Plan's per-key overrides forward to every scheduled run)
   * Set `ownerReferences` so the `BackupJob` is owned by the `Plan`.

**Note:** The `BackupJob` controller resolves the `BackupClass` to determine the appropriate strategy and effective parameters, based on the `ApplicationRef`. The strategy template is processed with a context containing the `Application` object and the **effective** `Parameters` — i.e., `BackupClassStrategy.Parameters` overlaid with `BackupJob.Spec.Parameters` (see §4.2 "Parameters semantics").

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

    // Parameters is the DEFAULT parameter set for the strategy — intended
    // for cluster-wide platform configuration (e.g., the platform's
    // default S3 endpoint, bucket, region, and the name of the canonical
    // credentials Secret tenants are expected to provide). Callers
    // (BackupJob, Plan) may override individual keys at run time; see
    // "Parameters semantics" below.
    //
    // Common parameters include:
    // - bucket / endpoint / region: storage coordinates
    // - credentialsSecretName: name of the Secret in the tenant
    //   namespace carrying AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
    //   (and optionally ca.crt)
    // - backupStorageLocationName: name of a pre-provisioned Velero
    //   BackupStorageLocation (escape-hatch for admins who want the
    //   pre-Cozystack Velero flow)
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
  3. Computes the **effective parameters** by merging the caller-supplied overrides over the matched strategy's defaults (see "Parameters semantics" below).
  4. Returns the matched `StrategyRef` and the effective parameter map.
* Strategy templates (e.g., Velero's `backupTemplate.spec`) are processed with a context containing:
  * `Application`: The application object being backed up.
  * `Parameters`: The **effective** parameters (defaults ⊕ overrides).

**Parameters semantics**

`BackupClassStrategy.Parameters` is the **default** parameter set for the strategy, owned by the cluster admin. Per-execution callers (`BackupJob`, `Plan`) may supply their own parameter map via their `spec.parameters` field; the resolver merges those on top of the defaults on a per-key basis:

```
effective[k] = caller.Parameters[k]   if k is present in caller.Parameters
             = strategy.Parameters[k] otherwise
```

Keys present only in defaults are preserved; keys present only in the override are added; keys present in both are taken from the override. The merged map is the `Parameters` context fed to the strategy template.

This is what lets a single platform-shipped `BackupClass` serve both:

* tenants that accept the platform-managed storage (omit `spec.parameters` → defaults apply unchanged), and
* tenants that bring their own bucket / external S3 / Cozystack `Bucket` (set `spec.parameters: { bucket: ..., endpoint: ..., credentialsSecretName: ... }` → those specific keys override the defaults).

See §6.2 / §6.3 for the end-to-end flow.

**Security: no credentials in parameter values**

The no-credentials-in-values rule (`BackupClassStrategy.Parameters` source comment) applies to **both** layers — `BackupClass` defaults AND caller overrides. Parameter values are persisted into `Backup.spec.driverMetadata` / `Backup.status.underlyingResources`, which are tenant-readable and replicated through every derived artifact, so they MUST NOT contain access keys, passwords, tokens, or any other secret material.

Credentials are always referenced by Secret name. The canonical convention is `parameters.credentialsSecretName`, pointing at a Secret in the application's namespace that holds `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` (and optionally `ca.crt` for self-signed endpoints). Drivers consume the Secret directly or materialize a driver-shaped derived Secret from it.

**Future work: override allow-list**

A future revision MAY add `BackupClassStrategy.OverridableParameters []string`. When set, the resolver would reject (or warn-and-drop) caller overrides for keys not in the allow-list — letting admins lock down which knobs tenants can actually touch (e.g., allow `bucket` but pin `endpoint`). Out of scope for the initial implementation; documented here so the field is reserved.

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

    // Parameters overrides values from the matched
    // BackupClassStrategy.Parameters for this run. Merged with the
    // BackupClass defaults at resolve time on a per-key basis (see §4.2
    // "Parameters semantics"). Subject to the same no-credentials rule
    // as BackupClassStrategy.Parameters — credentials live in a Secret
    // referenced by name (e.g. parameters.credentialsSecretName), not
    // inline in this map.
    // Immutable once the BackupJob is created.
    // +optional
    Parameters map[string]string `json:"parameters,omitempty"`
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
     * Match `ApplicationRef` against `BackupClass` strategies to get `StrategyRef` and the strategy's default `Parameters`.
     * Compute **effective parameters** by merging `BackupJob.Spec.Parameters` over the strategy's defaults on a per-key basis (see §4.2 "Parameters semantics"). This is the map exposed to the strategy template as `.Parameters`.
     * Read `Strategy` (driver-owned CRD) from `StrategyRef`.
     * Read `Application` from `ApplicationRef`.
     * Process strategy template with context: `Application` object and the **effective** `Parameters` map.
     * Persist the effective parameters (or the storage coordinates derived from them) into `Backup.spec.driverMetadata` / `Backup.status.underlyingResources` so the restore path can reproduce the same storage target without re-reading the original `BackupClass` / `BackupJob` (see §4.4).
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

**Note:** The driver writes the **effective** storage coordinates into `Backup.spec.driverMetadata` at backup time — that is, the post-merge result of `BackupClassStrategy.Parameters` and the originating `BackupJob.Spec.Parameters` (see §4.2 "Parameters semantics"). The restore path reads from there, so a backup taken with a tenant-supplied override (e.g., a different bucket or endpoint than the BackupClass default) is restored from that same override even if the `BackupClass` defaults change later. The storage location itself is whatever the driver materialised (e.g., a Velero `BackupStorageLocation`); `driverMetadata` holds either the coordinates inline or a reference the driver can resolve back at restore time.

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
    * Implement higher-level policies (retention) if needed.

**Note:** The driver determines where to store backups from the **effective** parameters (BackupClass defaults overlaid with the originating BackupJob's overrides — see §4.2). It persists what restore needs into `Backup.spec.driverMetadata` (the coordinates, plus references to any driver-materialised resources like Velero's `BackupStorageLocation`). When restoring, the driver resolves the storage location from `Backup.spec.driverMetadata` — independent of the current state of the `BackupClass` and without needing the originating `BackupJob` to still exist.

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

## 6. User flows

The parameter-merge model (§4.2) is designed to collapse the user surface to two operational shapes. This section walks both.

### 6.1 Admin: install + default storage

The platform bundle ships, per supported application kind (`Postgres`, `MariaDB`, `ClickHouse`, `FoundationDB`, `Etcd`, plus Velero for VM/disk):

* A cluster-scoped strategy CR (`strategy.backups.cozystack.io/<Kind>`) whose template references `{{ .Parameters.* }}` rather than per-app naming conventions.
* A cluster-scoped `BackupClass` (`postgres`, `mariadb`, `clickhouse`, `foundationdb`, `etcd`) whose `strategies[0].parameters` carries the platform's default storage coordinates and the name of the canonical credentials Secret tenants are expected to provide.

The admin's only configuration is the platform-storage defaults, exposed via Helm values:

```yaml
# values.yaml for the backupstrategy-controller chart
backupClassDefaults:
  bucket:                "cozy-default-backups"
  endpoint:              "http://seaweedfs-s3.cozy-system.svc:8333"
  region:                "us-east-1"
  forcePathStyle:        "true"
  credentialsSecretName: "cozy-default-backup-creds"   # name only; values live in a Secret
  endpointCASecretName:  ""                            # set when the endpoint serves a self-signed cert
```

(Optional) The admin can distribute `cozy-default-backup-creds` into every tenant namespace via cluster-scoped Secret projection (e.g. a `ClusterSecret`/`SecretBinding`) so tenants inherit credentials without per-namespace setup.

That's the entire admin loop. No per-tenant `BackupClass`. No per-application named Secret conventions.

### 6.2 Tenant: default storage path

When the tenant accepts the platform defaults, the entire backup spec is:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: my-postgres-adhoc
  namespace: tenant-foo
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Postgres
    name: my-postgres
  backupClassName: postgres
  # no spec.parameters — defaults from BackupClass `postgres` apply
```

Outcome:

* Effective parameters at resolve time = `BackupClass.strategies[0].parameters` verbatim.
* Backup lands in the platform bucket at the path the strategy template renders.
* `Backup.spec.driverMetadata` records the effective coordinates.
* Restoring (`RestoreJob` referencing the resulting `Backup`) reads those coordinates from `driverMetadata` and reproduces the storage target without any extra tenant input.

A scheduled equivalent (`Plan`) is the same shape with `schedule:` and otherwise identical fields.

### 6.3 Tenant: own storage (Cozystack `Bucket`, external S3, ...)

When the tenant wants a different bucket — e.g. a Cozystack-managed `Bucket` in their own namespace, an external S3-compatible endpoint, or a cloud provider's S3 — they keep the same `BackupClass` and override per-job via `spec.parameters`:

```yaml
# 1. Tenant brings their own bucket (Cozystack-managed example).
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: my-pg-backups
  namespace: tenant-foo
spec:
  users:
    backup:
      readonly: false
---
# 2. Tenant materialises the canonical credentials Secret. Cozystack
#    tenants do not have RBAC on core/v1.Secret directly; the
#    tenant-visible surface is core.cozystack.io/v1alpha1.TenantSecret,
#    which the apiserver proxies to a real Secret carrying the
#    `internal.cozystack.io/tenantresource=true` label. Drivers read the
#    underlying Secret directly (they have core/secrets RBAC).
#
#    For the Cozystack Bucket case (step 1), the chart-emitted
#    BucketInfo Secret is already labelled tenantresource=true by the
#    lineage webhook, so it surfaces as a TenantSecret in read-only and
#    the tenant can compose this Secret from it via jq + apply. For
#    external S3 the tenant supplies their own creds.
apiVersion: core.cozystack.io/v1alpha1
kind: TenantSecret
metadata:
  name: my-s3
  namespace: tenant-foo
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID:     "<access key>"
  AWS_SECRET_ACCESS_KEY: "<secret key>"
  # ca.crt: |
  #   <CA bundle for self-signed endpoints, optional>
---
# 3. BackupJob overrides the platform defaults via spec.parameters.
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: my-postgres-adhoc
  namespace: tenant-foo
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Postgres
    name: my-postgres
  backupClassName: postgres
  parameters:
    bucket:                "my-pg-backups"
    endpoint:              "https://s3.tenant-foo.cozystack.example.com"
    region:                "us-east-1"
    credentialsSecretName: "my-s3"
```

Effective parameters at resolve time: the keys explicitly listed in `spec.parameters` replace the BackupClass defaults; everything else (e.g. `forcePathStyle`, `endpointCASecretName`) inherits unchanged. `Plan` accepts the exact same `spec.parameters` shape, so scheduled runs use the same tenant override on every fire.

Restore (`RestoreJob`) needs no override: it reads the effective coordinates from `Backup.spec.driverMetadata` written at backup time, so an artifact taken against the tenant bucket restores against that same tenant bucket even if the BackupClass defaults change later or the tenant override is removed.

> **Open question — tenant RBAC for `TenantSecret`:** the default
> `cozy:tenant:*` aggregation in `packages/system/cozystack-basics/templates/clusterroles.yaml`
> grants only `get/list/watch` on `core.cozystack.io/v1alpha1.TenantSecret`
> and zero verbs on `core/v1.Secret`. Step 2 above presumes
> `create/update/patch/delete` on `TenantSecret` is granted by
> something — either by extending `cozy:tenant:admin:base` in basics,
> or by shipping a separate aggregated ClusterRole from
> `system/backup-controller/templates/tenant-clusterroles.yaml`, or by
> requiring an admin-supplied Secret projection (and dropping step 2
> from the tenant flow). To be resolved before implementation; see
> §[plan §5 RBAC].

---

## 7. Summary

The Cozystack backups core API:

* Uses a single group, `backups.cozystack.io`, for all core CRDs.
* Cleanly separates:

  * **When** (Plan schedule) – core-owned.
  * **How** (BackupClass strategy + default parameters) – admin-owned, cluster-scoped, shipped with the platform.
  * **Where** (effective parameters: BackupClass defaults overlaid with per-execution overrides on `BackupJob` / `Plan`) – admin sets defaults, tenant overrides per-run when bringing their own storage.
  * **Execution** (BackupJob) – created by Plan when schedule fires or ad-hoc by the tenant, resolves the effective parameters, then delegates to the driver.
  * **What backup artifacts exist** (Backup) – driver-created, cluster-visible, carries the effective storage coordinates in `driverMetadata` so restore is self-contained.
  * **Restore lifecycle** (RestoreJob) – shared contract boundary, reads storage from `Backup.driverMetadata` (no separate restore-side override needed).
* Allows multiple strategy drivers to implement backup/restore logic without entangling their implementation with the core API.

