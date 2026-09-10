# Proposal: BackupRetentionPolicy — declarative retention for `backups.cozystack.io`

**Status:** Draft / Proposal
**Group:** `backups.cozystack.io/v1alpha1`
**Related:** [`api/backups/v1alpha1/DESIGN.md`](../../api/backups/v1alpha1/DESIGN.md) §4.4 ("Implement higher-level policies (retention) if needed"), [Backup Classes](../operations/backup-classes.md)

## 1. Summary

Add a namespaced CRD `BackupRetentionPolicy` that lets an owner cap how many `Backup`
artifacts survive for an application and for how long, with three knobs — a
minimum-count floor, a maximum-count cap, and a maximum age. A `Backup` is placed
under a policy by a single label; the label is stamped by the core controllers from
`retentionPolicyName` on the `Plan`/`BackupJob` that produced it, with a
platform default carried on the `BackupClass`. Membership is resolved strictly
within one namespace, so identically named policies in different namespaces never
collide.

## 2. Motivation

The core API today schedules and records backups but never removes them.
`DESIGN.md` §4.4 already earmarks "higher-level policies (retention)" as core's job,
and the MariaDB strategy comment states outright that when its driver-native
`maxRetention` is empty, "tenants rely on Cozystack Plan-level retention only" — a
Plan-level retention that does not exist yet. Without it:

* `Backup` objects and their artifacts accumulate without bound.
* The only cleanup available is each driver's native mechanism, which is uneven
  (see §7) and invisible from the core API.
* There is no way to express the operationally common "keep the last N, but never
  fewer than M, and drop anything older than T".

## 3. Prior art

| Dimension | CNPG (barman) | opensearch-operator (SM) | etcd-operator `EtcdDefragPolicy` |
|---|---|---|---|
| Age / TTL | recovery window `30d`/`4w`/`6m` | `maxAge` | `ttlSecondsAfterFinished` |
| Max count | — (not exposed; barman `REDUNDANCY` unused) | `maxCount` | `historyLimit` (runs) |
| Min count (floor) | implicit (~1, via "first valid backup") | `minCount` (default 1) | — |
| Count vs time | time only | **both** | time |
| Where enforced | barman-cloud CLI vs object store | CRD → OpenSearch SM engine | CRD controller |
| Bound to schedule | `ScheduledBackup` separate from retention | creation+deletion in one policy | policy is the schedule |

Takeaways this proposal adopts:

* **Semantics** from OpenSearch Snapshot Management: `minCount` / `maxCount` /
  `maxAge`, with `minCount` overriding `maxAge` (never delete below the floor).
* **Decoupling** from CNPG's lesson: CNPG is time-only and cannot express a
  count cap, and its Backup CRs diverge from object-store contents. A count-based
  policy must be honest about which drivers can physically honor it (§7).
* **Controller mechanics** from `EtcdDefragPolicy`: an idempotent, deterministic
  set-difference sweep; retention is simpler because it needs no schedule anchor.

## 4. Goals and non-goals

### Goals

* A declarative, per-application retention knob covering age, max-count, and a
  min-count safety floor.
* Attach a policy to both scheduled (`Plan`) and ad-hoc (`BackupJob`) backups.
* Let a tenant create their own policy and override a platform default.
* Let a tenant migrate existing artifacts between policies without recreating them.

### Non-goals

* Implementing physical deletion inside any driver (that stays the driver's
  contract; §7 defines the boundary and a follow-up).
* Point-in-time / WAL-range retention for continuous-archiving engines.
* Replacing driver-native retention where the driver owns its archive lifecycle.

## 5. API design

### 5.1 `BackupRetentionPolicy` (new, namespaced)

```go
// BackupRetentionPolicy is a named set of retention rules. Membership is
// determined by the backups.cozystack.io/retention-policy label on a Backup,
// resolved within the policy's own namespace — not by a selector.
type BackupRetentionPolicySpec struct {
	Retention RetentionRule `json:"retention"`
}

type RetentionRule struct {
	// MinCount never deletes below this many newest Ready backups; overrides MaxAge.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	MinCount int32 `json:"minCount"`

	// MaxCount keeps at most this many newest Ready backups. Unset means unbounded.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCount *int32 `json:"maxCount,omitempty"`

	// MaxAge deletes backups whose spec.takenAt is older than this, bounded by MinCount.
	// +optional
	MaxAge *metav1.Duration `json:"maxAge,omitempty"`
}
```

There is intentionally **no selector**: the policy is a set of rules, and a `Backup`
carries exactly one policy name in a label — a scalar — so a backup is never claimed
by two policies at once.

### 5.2 Membership label

`backups.cozystack.io/retention-policy: <policy-name>` on the `Backup`. The policy
controller lists members with:

```go
c.List(ctx, &backups,
	client.InNamespace(policy.Namespace),
	client.MatchingLabels{"backups.cozystack.io/retention-policy": policy.Name})
```

### 5.3 Binding fields (new, optional)

The label value travels into a DNS-1123 label, so all three are capped at 63 chars.

```go
// Plan.spec / BackupJob.spec
// +kubebuilder:validation:MaxLength=63
RetentionPolicyName string `json:"retentionPolicyName,omitempty"`

// BackupClass.spec — platform default, resolved in the Backup's namespace
// +kubebuilder:validation:MaxLength=63
DefaultRetentionPolicyName string `json:"defaultRetentionPolicyName,omitempty"`
```

Resolution when a `Backup` is created (highest precedence first), stamped into the
label by the core `BackupJob`/`Backup` controller:

1. `BackupJob.spec.retentionPolicyName` (ad-hoc) or `Plan.spec.retentionPolicyName`
   (scheduled, inherited onto the `BackupJob`).
2. `BackupClass.spec.defaultRetentionPolicyName` (platform default).
3. none → no label → the `Backup` is never swept (fail-safe).

## 6. Semantics

### Selection algorithm

For a policy `P` in namespace `N`:

1. List `Backup` in `N` labeled `retention-policy=P.name`; keep only
   `status.phase == Ready`. `Failed`/`Pending` backups never consume the floor and
   are pruned only by `MaxAge`.
2. Sort by `spec.takenAt` descending.
3. Protect the newest `MinCount` unconditionally.
4. From the rest, delete a backup if its index `>= MaxCount` **or**
   `now - takenAt > MaxAge`. `MinCount` always wins over `MaxAge`.
5. Delete by `client.Delete` on the `Backup`; the existing
   `backups.cozystack.io/cleanup` finalizer runs the driver's cleanup path (§7).

The sweep is an idempotent set difference — safe to run on every reconcile and on
`Backup` create/delete/label events.

### Namespace-local invariant (no name collisions)

Because both `Backup` and `BackupRetentionPolicy` are namespaced and matching is
`InNamespace(policy.Namespace)`, a policy named `default` in `tenant-root` and a
policy named `default` in `tenant-acme` are distinct objects governing disjoint
backup sets. Names are identifiers only within a namespace, which is sufficient
because a backup and its policy always share one. Splitting backups and policies
across namespaces (e.g. central policies elsewhere) would reintroduce ambiguity and
break tenant isolation — explicitly out of scope.

### Migration by relabel

Moving artifacts to another policy is a label change, no Plan edit:

```bash
kubectl label backup -n tenant-acme \
  -l backups.cozystack.io/retention-policy=cozy-default \
  backups.cozystack.io/retention-policy=pg-src-90d --overwrite
```

Changing `retentionPolicyName` on a `Plan` moves only *future* backups; existing
ones stay until relabeled. Relabeling onto a stricter policy may delete artifacts on
the next sweep; onto a laxer one, it spares them.

## 7. Physical reclamation and the driver contract

Deleting a `Backup` fires the `backups.cozystack.io/cleanup` finalizer, but what it
reclaims is **driver-dependent** — this is the sharpest design constraint:

| Driver | `delete Backup` reclaims | Count cap physically enforceable? |
|---|---|---|
| Velero | `DeleteBackupRequest` purges archive on the BSL | yes |
| CNPG (Postgres) | deletes `cnpg.io/Backup` CR only; barman archive governed by driver retention | **no** (time-only, see below) |
| MariaDB | no-op (RBAC has no delete verb); `maxRetention` owns the archive | no |
| Altinity (ClickHouse) | no-op; `clickhouse-backup` owns S3 | no |
| FoundationDB | no-op; long-lived `backup_agent` keeps writing (see Backup Classes "Cleanup gotcha") | no |
| Job | nothing to reclaim | n/a |

Consequences for the policy:

* `maxCount` is physically authoritative only where Cozystack owns the archive
  (Velero today). For CNPG/MariaDB/Altinity it prunes the *record* while the driver
  keeps the data — a divergence users must not be surprised by.
* Where the driver is **time-only** (CNPG barman, MariaDB), the controller should
  translate `maxAge` into the driver-native retention (e.g. CNPG
  `strategy.retentionPolicy` `"30d"`) rather than pretend the Backup-CR delete
  reclaimed storage, and surface a `DriverCountUnsupported` condition when `maxCount`
  is set on such an application.

### Postgres: toward driver-independent ownership (open, for follow-up)

The follow-up question — can Cozystack physically reclaim a specific Postgres base
backup on `Backup`-CR deletion, independent of CNPG's own retention? — is tractable
because the CNPG driver already records enough to address the object:
`driverMetadata` carries `cnpg.io/server-name`, the barman destination path and the
endpoint (artifact URI `cnpg://<serverName>/<backup-name>`), and the strategy holds
the S3 credentials. A cleanup step could run `barman-cloud-backup-delete` against
that object store for the specific backup id. The blocker is **not** addressing —
it is the WAL chain: a base backup that a still-in-window PITR target depends on
cannot be removed without breaking recoverability, so any such deletion must respect
the recovery window and the "first valid backup" rule. This proposal scopes the
policy/record layer; physical per-backup reclamation for CNPG is called out as the
next design increment.

## 8. Controller design

A new core controller `BackupRetentionPolicyReconciler`:

* Watches `BackupRetentionPolicy` and, via a label-keyed mapping, `Backup`
  create/delete/label-change events.
* Runs the §6 sweep, deleting excess `Backup` objects.
* Writes status: `matchedBackups`, `protectedByFloor`, `lastEvaluationTime`,
  `lastDeletedCount`, and conditions (`Ready`, `DriverCountUnsupported`).

It stays disjoint from the `Plan` controller, which per `DESIGN.md` §4.1 must not
touch `Backup` objects. Retention is the only core path that deletes `Backup`s.

```yaml
status:
  matchedBackups: 27
  protectedByFloor: 5
  lastEvaluationTime: "2026-09-10T10:40:00Z"
  lastDeletedCount: 4
  conditions:
    - { type: Ready, status: "True", reason: Enforced }
```

## 9. Examples

### Platform default (shipped per tenant namespace)

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupRetentionPolicy
metadata:
  name: cozy-default
  namespace: tenant-acme
spec:
  retention:
    minCount: 3
    maxAge: 168h        # one week
---
# cluster-scoped platform BackupClass points scheduled/ad-hoc backups at it
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: cozy-default
spec:
  defaultRetentionPolicyName: cozy-default
  strategies:
    - application: { apiGroup: apps.cozystack.io, kind: Postgres }
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: CNPG
        name: cozy-default-cnpg
```

### Tenant overrides the default on a Plan

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupRetentionPolicy
metadata: { name: pg-src-90d, namespace: tenant-acme }
spec:
  retention: { minCount: 5, maxCount: 60, maxAge: 2160h }   # 90 days
---
apiVersion: backups.cozystack.io/v1alpha1
kind: Plan
metadata: { name: pg-src-daily, namespace: tenant-acme }
spec:
  applicationRef: { apiGroup: apps.cozystack.io, kind: Postgres, name: pg-src }
  backupClassName: cozy-default
  schedule: { type: cron, cron: "0 */6 * * *" }
  retentionPolicyName: pg-src-90d          # overrides BackupClass default
```

### Ad-hoc backup kept longer than the app default

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupRetentionPolicy
metadata: { name: manual-keep-180d, namespace: tenant-acme }
spec:
  retention: { minCount: 1, maxAge: 4320h }
---
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata: { name: pg-src-premigration, namespace: tenant-acme }
spec:
  applicationRef: { apiGroup: apps.cozystack.io, kind: Postgres, name: pg-src }
  backupClassName: cozy-default
  retentionPolicyName: manual-keep-180d
```

### Resulting Backup (label stamped by the controller)

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: Backup
metadata:
  name: pg-src-20260910-1040
  namespace: tenant-acme
  labels:
    backups.cozystack.io/retention-policy: pg-src-90d
spec:
  applicationRef: { apiGroup: apps.cozystack.io, kind: Postgres, name: pg-src }
  planRef: { name: pg-src-daily }
  strategyRef: { apiGroup: strategy.backups.cozystack.io, kind: CNPG, name: cozy-default-cnpg }
  takenAt: "2026-09-10T10:40:00Z"
```

## 10. Alternatives considered

* **Embed `retention` in `Plan.spec`.** Simpler, mirrors OpenSearch's single-policy
  coupling, but ad-hoc `BackupJob`s (no `planRef`) fall outside it and multiple Plans
  per app fragment the rule. Rejected in favor of a standalone, reusable object.
* **Selector-based membership** (policy carries an `applicationRef`/label selector).
  Flexible, but two selectors can claim one backup, forcing a conflict rule and
  fail-safe no-op. The scalar label removes the whole class of conflicts and enables
  relabel-migration. Rejected.
* **Cluster-scoped policy (StorageClass-style).** Global name uniqueness by design,
  but tenants could not own a `default`, and a cluster-scoped CRD cannot be granted
  to tenants for write in a multi-tenant cluster. Rejected.

## 11. Open questions

* Validate existence of the referenced policy? Recommended soft: always stamp the
  label and back up regardless, surface `RetentionPolicyNotFound` on the
  `Plan`/`BackupJob` — never block a backup on a missing retention policy.
* `maxAge` → driver-native retention translation: which drivers, and how to keep the
  two authorities from diverging (§7).
* Physical per-backup reclamation for CNPG within the recovery window (§7).
