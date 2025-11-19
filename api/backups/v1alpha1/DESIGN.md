# Cozystack Backups – Core API & Contracts (Draft)

## 1. Overview

Cozystack’s backup subsystem provides a generic, composable way to back up and restore managed applications:

* Every **application instance** can have one or more **backup plans**.
* Backups are stored in configurable **storage locations**.
* The mechanics of *how* a backup/restore is performed are delegated to **strategy drivers**, each implementing driver-specific **BackupStrategy** CRDs.

The core API:

* Orchestrates **when** backups happen and **where** they’re stored.
* Tracks **what** backups exist and their status.
* Defines contracts with drivers via shared resources (`BackupRun`, `Backup`, `RestoreRun`).

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
    * `Storage`
    * `BackupRun`
    * `Backup`
    * `RestoreRun`
  * Responsibilities:

    * Schedule backups based on `Plan`.
    * Create `BackupRun` objects when due.
    * Provide stable contracts for drivers to:

      * Perform backups and create `Backup`s.
      * Perform restores based on `Backup`s.

* **Strategy drivers** (pluggable, possibly third-party):

  * Their own API groups, e.g. `jobdriver.backups.cozystack.io`.
  * Own **strategy CRDs** (e.g. `JobBackupStrategy`).
  * Implement controllers that:

    * Watch `BackupRun` / `RestoreRun`.
    * Match runs whose `strategyRef` GVK they support.
    * Execute backup/restore logic.
    * Create and update `Backup` and run statuses.

Drivers and core communicate entirely via Kubernetes objects; there are no webhook/HTTP calls between them.

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
    ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

    // Where backups should be stored.
    StorageRef corev1.TypedLocalObjectReference `json:"storageRef"`

    // Driver-specific BackupStrategy to use.
    StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

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
    Type PlanScheduleType `json:"type,omitempy"`

    // Cron expression (required for cron type).
    Cron string `json:"cron,omitempty"`
}
```

**Plan reconciliation contract**

Core Plan controller:

1. **Read schedule** from `spec.schedule` and compute the next fire time.
2. When due:

   * Create a `BackupRun` in the same namespace:

     * `spec.planRef.name = plan.Name`
     * `spec.applicationRef = plan.spec.applicationRef`
     * `spec.storageRef = plan.spec.storageRef`
     * `spec.strategyRef = plan.spec.strategyRef`
     * `spec.triggeredBy = "Plan"`
   * Set `ownerReferences` so the `BackupRun` is owned by the `Plan`.

The Plan controller does **not**:

* Execute backups itself.
* Modify driver resources or `Backup` objects.
* Touch `BackupRun.spec` after creation.

---

### 4.2 Storage

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=Storage`

**Purpose**
Describe **where** backups should be stored and (optionally) how to name and organise artifacts.

**Key fields (spec)**

```go
type StorageSpecType string

const (
    StorageSpecTypeEmpty  StorageSpecType = ""
    StorageSpecTypeS3     StorageSpecType = "s3"
    StorageSpecTypeBucket StorageSpecType = "bucket"
)
```

```go
type StorageSpec struct {
    // Type of storage: "s3" or "bucket". Defaults to "bucket".
    Type StorageSpecType `json:"type,omitempty"`

    // Configuration for a Cozystack bucket in the current namespace.
    Bucket StorageSpecBucket `json:"bucket,omitempty"`

    // Configuration for an arbitrary S3-compatible bucket.
    S3 StorageSpecS3 `json:"s3,omitempy"`
}
```

`StorageSpecBucket` includes:

* `name` (bucket name)
* `prefix`
* `format` (go-template style artifact name; default documented in comments)

`StorageSpecS3` is TBD and will hold generic S3 params (endpoint, credentials, bucket, etc.).

**Storage usage**

* `Plan` and `BackupRun` reference `Storage` via `TypedLocalObjectReference`.
* Drivers read `Storage` to know how/where to store or read artifacts.
* Core treats `Storage` spec as opaque; it does not directly talk to S3 or buckets.

---

### 4.3 BackupRun

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=BackupRun`

**Purpose**
Represent a single **execution** of a backup operation, typically created when a `Plan` fires or when a user triggers an ad-hoc backup.

**Key fields (spec)**

```go
type BackupRunSpec struct {
    // Plan that triggered this run, if any.
    PlanRef *corev1.LocalObjectReference `json:"planRef,omitempty"`

    // Application to back up.
    ApplicationRef corev1.TypedLocalObjectReference `json:"applicationRef"`

    // Storage to use.
    StorageRef corev1.TypedLocalObjectReference `json:"storageRef"`

    // Driver-specific BackupStrategy to use.
    StrategyRef corev1.TypedLocalObjectReference `json:"strategyRef"`

    // Informational: what triggered this run ("Plan", "Manual", etc.).
    TriggeredBy string `json:"triggeredBy,omitempty"`
}
```

**Key fields (status)**

```go
type BackupRunStatus struct {
    Phase       BackupRunPhase         `json:"phase,omitempty"`
    BackupRef   *corev1.LocalObjectReference `json:"backupRef,omitempty"`
    StartedAt   *metav1.Time           `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time           `json:"completedAt,omitempty"`
    Message     string                 `json:"message,omitempty"`
    Conditions  []metav1.Condition     `json:"conditions,omitempty"`
}
```

`BackupRunPhase` is one of: `Pending`, `Running`, `Succeeded`, `Failed`.

**BackupRun contract with drivers**

* Core **creates** `BackupRun` and must treat `spec` as immutable afterwards.
* Each driver controller:

  * Watches `BackupRun`.
  * Reconciles runs where `spec.strategyRef.apiGroup/kind` matches its **strategy type(s)**.
* Driver responsibilities:

  1. On first reconcile:

     * Set `status.startedAt` if unset.
     * Set `status.phase = Running`.
  2. Resolve inputs:

     * Read `Strategy` (driver-owned CRD), `Storage`, `Application`, optionally `Plan`.
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

Drivers must **not** modify `BackupRun.spec` or delete `BackupRun` themselves.

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
    StorageRef     corev1.TypedLocalObjectReference `json:"storageRef"`
    StrategyRef    corev1.TypedLocalObjectReference `json:"strategyRef"`
    TakenAt        metav1.Time                      `json:"takenAt"`
    DriverMetadata map[string]string                `json:"driverMetadata,omitempty"`
}
```

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

* On successful completion of a `BackupRun`, the **driver**:

  * Creates a `Backup` in the same namespace (typically owned by the `BackupRun`).
  * Populates `spec` fields with:

    * The application, storage, strategy references.
    * `takenAt`.
    * Optional `driverMetadata`.
  * Sets `status` with:

    * `phase = Ready` (or equivalent when fully usable).
    * `artifact` describing the stored object.
* Core:

  * Treats `Backup` spec as mostly immutable and opaque.
  * Uses it to:

    * List backups for a given application/plan.
    * Anchor `RestoreRun` operations.
    * Implement higher-level policies (retention) if needed.

---

### 4.5 RestoreRun

**Group/Kind**
`backups.cozystack.io/v1alpha1, Kind=RestoreRun`

**Purpose**
Represent a single **restore operation** from a `Backup`, either back into the same application or into a new target application.

**Key fields (spec)**

```go
type RestoreRunSpec struct {
    // Backup to restore from.
    BackupRef corev1.LocalObjectReference `json:"backupRef"`

    // Target application; if omitted, drivers SHOULD restore into
    // backup.spec.applicationRef.
    TargetApplicationRef *corev1.TypedLocalObjectReference `json:"targetApplicationRef,omitempty"`

    // Optional override of the Storage; if omitted, drivers SHOULD
    // use backup.spec.storageRef.
    StorageRefOverride *corev1.TypedLocalObjectReference `json:"storageRefOverride,omitempty"`

    // Optional override of the strategy; if omitted, drivers SHOULD
    // use backup.spec.strategyRef.
    StrategyRefOverride *corev1.TypedLocalObjectReference `json:"strategyRefOverride,omitempty"`

    // Informational only.
    TriggeredBy string `json:"triggeredBy,omitempty"`
}
```

**Key fields (status)**

```go
type RestoreRunStatus struct {
    Phase       RestoreRunPhase   `json:"phase,omitempty"` // Pending, Running, Succeeded, Failed
    StartedAt   *metav1.Time      `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time      `json:"completedAt,omitempty"`
    Message     string            `json:"message,omitempty"`
    Conditions  []metav1.Condition `json:"conditions,omitempty"`
}
```

**RestoreRun contract with drivers**

* RestoreRun is created either manually or by core.
* Driver controller:

  1. Watches `RestoreRun`.
  2. On reconcile:

     * Fetches the referenced `Backup`.
     * Determines effective:

       * **Strategy**: `spec.strategyRefOverride` or `backup.spec.strategyRef`.
       * **Storage**: `spec.storageRefOverride` or `backup.spec.storageRef`.
       * **Target application**: `spec.targetApplicationRef` or `backup.spec.applicationRef`.
     * If effective strategy’s GVK is one of its supported strategy types → driver is responsible.
  3. Behaviour:

     * On first reconcile, set `status.startedAt` and `phase = Running`.
     * Resolve `Backup`, `Storage`, `Strategy`, target application.
     * Execute restore logic (implementation-specific).
     * On success:

       * Set `status.completedAt`.
       * Set `status.phase = Succeeded`.
     * On failure:

       * Set `status.completedAt`.
       * Set `status.phase = Failed`.
       * Set `status.message` and conditions.

Drivers must not modify `RestoreRun.spec` or delete `RestoreRun`.

---

## 5. Strategy drivers (high-level)

Strategy drivers are separate controllers that:

* Define their own **strategy CRDs** (e.g. `JobBackupStrategy`) in their own API groups:

  * e.g. `jobdriver.backups.cozystack.io/v1alpha1, Kind=JobBackupStrategy`
* Implement the **BackupRun contract**:

  * Watch `BackupRun`.
  * Filter by `spec.strategyRef.apiGroup/kind`.
  * Execute backup logic.
  * Create/update `Backup`.
* Implement the **RestoreRun contract**:

  * Watch `RestoreRun`.
  * Resolve `Backup`, then effective `strategyRef`.
  * Filter by effective strategy GVK.
  * Execute restore logic.

The core backups API **does not** dictate:

* The fields and structure of driver strategy specs.
* How drivers implement backup/restore internally (Jobs, snapshots, native operator CRDs, etc.).

Drivers are interchangeable as long as they respect:

* The `BackupRun` and `RestoreRun` contracts.
* The shapes and semantics of `Backup` objects.

---

## 6. Summary

The Cozystack backups core API:

* Uses a single group, `backups.cozystack.io`, for all core CRDs.
* Cleanly separates:

  * **When & where** (Plan + Storage) – core-owned.
  * **What backup artifacts exist** (Backup) – driver-created but cluster-visible.
  * **Execution lifecycle** (BackupRun, RestoreRun) – shared contract boundary.
* Allows multiple strategy drivers to implement backup/restore logic without entangling their implementation with the core API.

