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
    * Implement higher-level policies (retention) if needed.

**Note:** Parameters are resolved from `BackupClass` when the `BackupJob` is created. The driver uses these parameters to determine where to store backups. The storage location itself is managed by the driver (e.g., Velero's `BackupStorageLocation` CRD) and is not directly referenced in the `Backup` resource. When restoring, the driver resolves the storage location from the original `BackupClass` parameters or from the driver's own metadata.

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

## 6. Summary

The Cozystack backups core API:

* Uses a single group, `backups.cozystack.io`, for all core CRDs.
* Cleanly separates:

  * **When** (Plan schedule) – core-owned.
  * **How & where** (BackupClass) – central configuration unit that encapsulates strategy and parameters (e.g., storage reference) per application type, resolved per BackupJob/Plan.
  * **Execution** (BackupJob) – created by Plan when schedule fires, resolves BackupClass to get strategy and parameters, then delegates to driver.
  * **What backup artifacts exist** (Backup) – driver-created but cluster-visible.
  * **Restore lifecycle** (RestoreJob) – shared contract boundary.
* Allows multiple strategy drivers to implement backup/restore logic without entangling their implementation with the core API.

