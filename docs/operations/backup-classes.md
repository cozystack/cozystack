# Backup Classes

Cozystack ships a single platform-managed `BackupClass` named `cozy-default`. It is provisioned automatically when the `backupstrategy-controller` package is installed and references the system-managed S3 bucket `cozy-backups` in the `tenant-root` namespace.

Tenants reference `cozy-default` from `BackupJob`, `Plan`, and `RestoreJob` resources — they do **not** supply S3 credentials, endpoints, or paths. The platform projects the system-managed credentials Secret into the tenant namespace per BackupJob (or, for long-lived references like Velero's `BackupStorageLocation`, into a fixed list of system namespaces on a periodic tick), and the default strategy templates encode `<namespace>/<application>` into every S3 path so two tenants with the same application name never collide.

## Supported applications

### Bound by `cozy-default` (work out-of-the-box)

| Application Kind                 | Driver                               | Strategy CR                                                                |
|----------------------------------|--------------------------------------|----------------------------------------------------------------------------|
| `apps.cozystack.io/Postgres`     | CloudNativePG (barman)               | `strategy.backups.cozystack.io/CNPG` `cozy-default-cnpg`                   |
| `apps.cozystack.io/MariaDB`      | mariadb-operator dump                | `strategy.backups.cozystack.io/MariaDB` `cozy-default-mariadb`             |
| `apps.cozystack.io/ClickHouse`   | Altinity `clickhouse-backup` sidecar | `strategy.backups.cozystack.io/Altinity` `cozy-default-altinity`           |
| `apps.cozystack.io/Etcd`         | etcd-operator snapshot               | `strategy.backups.cozystack.io/Etcd` `cozy-default-etcd`                   |
| `apps.cozystack.io/VMInstance`   | Velero + kubevirt-velero-plugin      | `strategy.backups.cozystack.io/Velero` `cozy-default-velero-vminstance`    |
| `apps.cozystack.io/VMDisk`       | Velero                               | `strategy.backups.cozystack.io/Velero` `cozy-default-velero-vmdisk`        |

### Shipped but NOT bound (admin opt-in required)

| Application Kind                 | Driver                               | Strategy CR                                                                |
|----------------------------------|--------------------------------------|----------------------------------------------------------------------------|
| `apps.cozystack.io/FoundationDB` | FoundationDB operator backup_agent   | `strategy.backups.cozystack.io/FoundationDB` `cozy-default-foundationdb`   |

The FoundationDB strategy CR is rendered by the chart so admins can reference it from a custom BackupClass once the operator-side plumbing (mounting `cozy-backups-creds` into the `cozy-foundationdb-operator` Deployment) is wired manually. See "FoundationDB caveat" below.

### Endpoint format per driver

Different operators expect different endpoint shapes; the strategy templates rendered by `backupstrategy-controller` resolve one S3 endpoint (via the `backupstrategy-controller.endpoint` helper) and adapt it to each consumer's contract. For a provisioned bucket (`provisionBucket: true`, the default) the endpoint is **derived from the COSI bucket's system-credentials Secret** (`backupStorage.systemSecretName`) and forced to `https://` — the external S3 ingress with an ACME cert, the only endpoint the backup operators can verify. SeaweedFS's in-cluster S3 serves TLS on `:8333` behind the self-signed "SeaweedFS CA", and the Etcd Strategy S3 schema has no `caCert` field, so the in-cluster endpoint cannot be targeted directly. The `backupStorage.endpoint` chart value (a full URL like `http://seaweedfs-s3.tenant-root.svc:8333`) is the **fallback**, used for external S3 (`provisionBucket: false`) and for offline `helm template`/pre-reconcile renders where the Secret lookup returns nothing. The resolved endpoint is adapted per consumer:

| Driver | Strategy template field | Form |
|--------|-------------------------|------|
| CNPG (Postgres) | `barmanObjectStore.endpointURL` | full URL (scheme preserved) |
| Etcd            | `destination.s3.endpoint`       | full URL (scheme preserved) |
| MariaDB         | `storage.s3.endpoint`           | bare host:port (scheme stripped); `tls.enabled` derived from the scheme |
| FoundationDB    | `blobStoreConfiguration.accountName` + `urlParameters.secure_connection` | bare host:port + derived secure flag |
| Velero          | `BackupStorageLocation.spec.config.s3Url` | full URL (scheme preserved) |
| ClickHouse sidecar | `S3_ENDPOINT` env | bare host:port (from projected Secret) |

The projected `cozy-backups-creds.endpoint` key is **stripped of scheme** so chart-emitted sidecars (ClickHouse) consume it directly. Drivers that need the full URL receive the resolved endpoint described above — derived from the COSI system Secret (forced `https://`) for a provisioned bucket, or the `backupStorage.endpoint` fallback for external S3.

VM-driven (Velero) backups land in the same `cozy-backups` bucket under the `velero/` prefix. A `BackupStorageLocation` named `cozy-default` is shipped by the `backupstrategy-controller` chart (`packages/system/backupstrategy-controller/templates/velero-bsl.yaml`) so endpoint/bucket/region come from the same `backupStorage` values block used by Strategy CRs and the projector.

### FoundationDB caveat

The strategy CR `cozy-default-foundationdb` is shipped, but it is **not** bound by `cozy-default` yet. Restore runs `fdbrestore` from inside the `cozy-foundationdb-operator` Deployment, which does not yet mount `cozy-backups-creds`. Until the operator deployment is updated to mount the projected Secret, FDB platform-default restore silently fails — admins who need it today should keep using a per-app `Bucket` plus a custom `BackupClass`, or wire the credentials file into the operator deployment themselves.

**Cleanup gotcha (zombie backup_agent).** Unlike CNPG/MariaDB/Altinity (one-shot operator-side Backup CRs), the FoundationDB driver creates a `foundationdb.org/FoundationDBBackup` CR that drives a **long-lived** `backup_agent` Deployment streaming continuously to S3. Deleting a Cozystack `Backup` (e.g. via retention sweeping) does NOT stop that Deployment — the agent keeps writing until the next BackupJob's `stopOtherFoundationDBBackups` call swaps it out, until an admin invokes `examples/backups/foundationdb/cleanup.sh`, or until the operator-side CR is deleted by hand. If a tenant deletes their last Cozystack Backup and never submits another BackupJob, the agent pods will continue running indefinitely and accumulate S3 PUTs. This is intentional today (the driver has no RBAC verb to stop the operator-side CR on Cozystack-Backup deletion) but admins should be aware of it.

## ClickHouse: opt-in to the system bucket

The `clickhouse-backup` sidecar runs inside the ClickHouse Pod itself, so the Helm chart is what wires its S3 credentials. Existing tenants on the legacy `backup.s3*` values continue to work unchanged. To switch a release onto the platform bucket, set:

```yaml
backup:
  enabled: true
  useSystemBucket: true
```

When `useSystemBucket: true`:

- The chart-emitted `<release>-backup-s3` Secret is no longer rendered.
- The sidecar consumes `cozy-backups-creds` (projected by the platform).
- `S3_PATH` is set to `<namespace>/<release>` so two tenants with the same ClickHouse release name never share a prefix.

`s3Region`, `s3Bucket`, `endpoint`, `s3AccessKey`, `s3SecretKey`, and `s3CredentialsSecret` are ignored in this mode.

## Inspecting the defaults

```bash
kubectl get backupclasses
kubectl get backupclass cozy-default -o yaml
kubectl -n tenant-root get bucket cozy-backups
kubectl -n tenant-root get secret bucket-cozy-backups-system-credentials
kubectl -n cozy-velero get backupstoragelocation cozy-default
```

The bucket lives in `tenant-root` and is provisioned through the `apps.cozystack.io/Bucket` CR. The system-managed credentials Secret never leaves that namespace. The backupstrategy-controller projects a copy under the name `cozy-backups-creds` into a tenant namespace right before each BackupJob runs, and refreshes the same Secret in `cozy-velero` (and any other namespace listed in `backupStorage.systemNamespaces`) on a 1-minute tick. The projected Secret carries multiple key formats so each driver finds what it needs in one place:

| Key                                           | Consumer                                  |
|-----------------------------------------------|-------------------------------------------|
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | CNPG, MariaDB, Etcd                       |
| `accessKey` / `secretKey` (plus `bucketName`, `endpoint`, `region`) | ClickHouse sidecar  |
| `cloud`                                       | Velero (AWS credentials file format)      |
| `blob_credentials.json`                       | FoundationDB backup_agent                 |

### Bootstrap window

On a fresh-cluster install, the Velero `BackupStorageLocation` `cozy-default` is rendered before the credentials projector has had a chance to copy `cozy-backups-creds` into `cozy-velero`. The BSL reports `Unavailable` until the projector's first synchronous round completes (which happens immediately when the `backupstrategy-controller` Pod becomes Ready — typically tens of seconds after `helm install` returns, not minutes). Velero rejects new `Backup` AND `Restore` requests against `storageLocation: cozy-default` during that window. Plan VM backup automation accordingly, or wait for `kubectl -n cozy-velero get bsl cozy-default -o jsonpath='{.status.phase}' = Available` before submitting backups.

**Note on controller restarts.** The BSL flickers `Unavailable` on every `backupstrategy-controller` pod restart while the projector replays its first synchronous round. The window is short (single-digit seconds) but operators who alert on BSL availability should suppress alerts during the controller's `kube_pod_container_status_restarts_total{container=backupstrategy-controller}` events or use a longer evaluation window than the projector tick (60s).

### Cozy-default Bucket bootstrap

`cozy-default` ships an `apps.cozystack.io/Bucket cozy-backups` CR in `tenant-root`, which the bucket-application chart turns into a `BucketClaim`; the COSI driver then assigns the real S3 bucket name and writes it to the BucketClaim's `.status.bucketName`. The strategy templates and the Velero BSL all read that real bucket name (Helm `lookup` against the BucketClaim). On a fresh install the BucketClaim takes a short reconcile cycle to populate its status — until it does, the strategy templates render empty and only the `Bucket` CR + `BackupClass` are present in the cluster. Flux re-renders the HelmRelease on its standard interval (default 10 minutes), at which point the populated BucketClaim status causes the missing strategy templates to materialise.

If you need the BackupClass functional immediately (e.g. an e2e), trigger a Flux reconcile (`flux reconcile helmrelease backupstrategy-controller`) once you see `kubectl get bucketclaim -n tenant-root bucket-cozy-backups -o jsonpath='{.status.bucketName}'` non-empty.

### Observability

The credentials projector emits two Prometheus counters labelled by `namespace` (and `reason` for failures):

- `cozystack_backup_credentials_projection_successes_total`
- `cozystack_backup_credentials_projection_failures_total`

Alert on `rate(failures_total) > 0` or `absent_over_time(successes_total[10m])` to catch a stale BSL credential or a malformed source Secret without log scraping.

## Admin overrides for `cozy-default`

`cozy-default` is rendered by the `backupstrategy-controller` chart and owned by Flux's helm-controller. **Direct `kubectl edit backupclass cozy-default` is overwritten on the next helm reconcile** — the same applies to its companion `strategy.backups.cozystack.io/*` CRs (`cozy-default-cnpg`, `cozy-default-etcd`, `cozy-default-mariadb`, `cozy-default-altinity`, `cozy-default-foundationdb`, the two `cozy-default-velero-*`). The supported override path is the `backupStorage` block on the **`platform` component** of the `cozystack.cozystack-platform` Package CR:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  components:
    platform:
      values:
        backupStorage:
          provisionBucket: true                    # default; set false for external S3
          bucketName: cozy-backups                  # apps.cozystack.io/Bucket release name
          endpoint: http://seaweedfs-s3.tenant-root.svc.cozy.local:8333
          region: us-east-1
          forcePathStyle: true
          systemSecretName: bucket-cozy-backups-system-credentials
          systemNamespaces:
            - cozy-velero
```

The platform chart forwards this block into the child `Package cozystack.backupstrategy-controller` as `components.backupstrategy-controller.values.backupStorage` (`packages/core/platform/templates/bundles/system.yaml`), from where the cozystack operator merges it into the `backupstrategy-controller` HelmRelease over the chart defaults. Two paths that look plausible do **not** work: `spec.components.backupstrategy-controller` on the `cozystack.cozystack-platform` Package is silently ignored (the only component under that PackageSource is `platform`), and patching the child `Package cozystack.backupstrategy-controller` directly is reverted whenever the platform helm-reconcile re-renders it.

| Knob | Effect |
|---|---|
| `provisionBucket` | Toggle creation of the in-cluster `apps.cozystack.io/Bucket` CR. Set `false` for external S3 (see [Disabling the platform-managed bucket](#disabling-the-platform-managed-bucket)). |
| `bucketName` | K8s name of the Bucket CR + lookup key for the COSI BucketClaim. The actual S3 bucket name is the COSI-assigned UUID, surfaced through `BucketClaim.status.bucketName`. |
| `bucketNameOverride` | Escape hatch for offline `helm template` renders — bypasses the live-cluster BucketClaim lookup. Leave empty in production. |
| `endpoint` | **Fallback** S3 endpoint. For a provisioned bucket the strategy CRs + Velero BSL derive the endpoint from the COSI system Secret (external ACME ingress, forced `https://`) instead; this value is used only for external S3 (`provisionBucket: false`) and offline renders. For external S3, switching it to `https://` enables TLS in the MariaDB/FoundationDB strategies — ensure the CA bundle is reachable to the relevant operator/driver Pods first. |
| `region` | Re-projected into `cozy-backups-creds` on the next reconcile. Pod-restart required for chart-emitted clients consuming the region via env (ClickHouse sidecar today). |
| `forcePathStyle` | Path-style addressing; SeaweedFS S3 requires it, AWS S3 typically doesn't. |
| `systemSecretName` | Name of the human-friendly Secret produced by the Bucket app (or pre-created manually for external S3). The projector also accepts the raw COSI Secret format. |
| `systemNamespaces` | Namespaces where the controller eagerly projects `cozy-backups-creds` (Velero BSL, FDB operator). Tenants are projected lazily during BackupJob reconcile. |

When the override needs to go beyond storage coordinates — different retention, different driver→Kind binding, multi-region split — create a **sibling BackupClass** with a unique name (anything but `cozy-default`). Sibling BackupClasses live outside the chart, are admin-owned, and Flux will not touch them. Tenants opt in by setting `backupClassName: <your-class>` on their `BackupJob`s.

## Tuning via a custom BackupClass

The defaults aim at a reasonable middle (30-day retention, gzip compression where applicable). To override for a specific tenant or workload, create your own `BackupClass` pointing at the same strategy CRs but with tweaked `parameters`, or a fresh strategy CR. Common knobs:

- **CNPG strategy**: `barmanObjectStore.retentionPolicy`, `data.compression`, `wal.compression`.
- **MariaDB strategy**: `compression`, `maxRetention`, `databases[]`.
- **Altinity strategy**: tune the `clickhouse-backup` sidecar via `backup.*` values on the ClickHouse release; the strategy Pod is a thin HTTP client.
- **FoundationDB strategy**: `snapshotPeriodSeconds`, `agentCount`, `urlParameters[]`.
- **Velero strategy (VMInstance / VMDisk)**: `ttl`, `includedResources[]`, `excludedResources[]`.
- **Etcd strategy**: today the strategy is path-only; combine with `Plan.spec.retentionPolicy` for trim cadence.

The system-managed credentials Secret is the **only** way for in-cluster strategies to reach `cozy-backups`. Do not embed access keys in `BackupClass.parameters` — the security model relies on Secret references, and `parameters` end up in `Backup.status.underlyingResources`, which tenants can read.

## Disabling the platform-managed bucket

If a deployment runs against an external S3 (no SeaweedFS), set `backupStorage.provisionBucket: false` in the `backupstrategy-controller` values and create the source credentials Secret in `tenant-root` manually (flat-key format: `accessKey` / `secretKey` / `endpoint` / `bucketName`; or the raw COSI `BucketInfo` JSON). Update `backupStorage.endpoint`, `backupStorage.region`, and (for VM backups) the chart's Velero BSL settings to point at the external S3.

## Upgrade notes from chart-managed backups

> **Postgres backups now use the CloudNativePG Barman Cloud plugin, not native `spec.backup.barmanObjectStore`.**
>
> Postgres backups were migrated off the deprecated native `barmanObjectStore` (removed in CNPG 1.29) onto the [Barman Cloud plugin](https://github.com/cloudnative-pg/plugin-barman-cloud). The `barman-cloud-*` binaries the native path needs are absent from the `standard` image variant `keycloak-db` pins; `apps/postgres` pins bare (system-flavor) tags that still ship them — its breakage was the operator/CRD skew below, not missing binaries — but the system flavor is deprecated upstream, so both move to the plugin. The chart now renders `spec.plugins` on the `cnpg.io/Cluster` plus a `barmancloud.cnpg.io/ObjectStore` CR that carries the S3 configuration, and the CNPG backup driver SSA-applies an `ObjectStore` and patches `spec.plugins` (instead of `spec.backup.barmanObjectStore`) for the platform flow; `Backup`/`ScheduledBackup` use `method: plugin`. On `helm upgrade` a legacy chart-managed Cluster's `spec.backup.barmanObjectStore` is replaced by `spec.plugins` + an `ObjectStore`. This only works once the `plugin-barman-cloud` operator is installed and co-located with the CNPG operator in `cozy-postgres-operator` (shipped by the `postgres-operator` chart) and its cert-manager dependency is present — otherwise the Cluster references a plugin that is not registered and backups / WAL archiving stall.
>
> **Mixed-version window: BackupJobs fail until the app re-renders with the new chart.** A `BackupJob` that fires while a `Cluster` still carries the pre-upgrade chart's native `spec.backup.barmanObjectStore` is rejected by the CNPG admission webhook (`Cannot enable a WAL archiver plugin when barmanObjectStore is configured`) — the driver cannot attach `spec.plugins` alongside the native config. This resolves itself once Flux re-renders the Postgres app with the new chart (which replaces `barmanObjectStore` with `spec.plugins`); re-run the BackupJob afterward. Upgrading only the operators/driver without the app charts leaves every `backup.enabled: true` Postgres in this state permanently.
>
> **Postgres `backup.enabled: true` with placeholder credentials no longer wires backups on upgrade.**
>
> The pre-PR defaults for `backup.s3AccessKey` / `backup.s3SecretKey` in `packages/apps/postgres/values.yaml` were the literal `"<your-access-key>"` / `"<your-secret-key>"` placeholders, so the Postgres chart still rendered a backup block on the `cnpg.io/Cluster` (with junk credentials, WAL archiving failing at runtime). After this PR those defaults are empty strings and the chart NO LONGER renders any backup wiring when the placeholders are unmodified. Tenants on the legacy chart-managed flow who relied on those placeholders see `spec.plugins` and the `ObjectStore` disappear from the live `Cluster` on `helm upgrade`. Action — pick one:
>
> - **Move to the platform flow (recommended).** Set `backup.useSystemBucket: true`; the chart leaves `spec.plugins` unset and the CNPG backup driver SSA-applies the `ObjectStore` and patches `spec.plugins` onto the live `Cluster` at first BackupJob time. No tenant-side keys required.
> - **Stay on the legacy chart-managed flow.** Supply real `backup.s3AccessKey` / `backup.s3SecretKey` (or a pre-existing `backup.s3CredentialsSecret.name`); the chart renders `spec.plugins` + the `ObjectStore` from those coordinates.
>
> **Two platform-level changes ride along with this migration (they are required for the plugin to run):**
>
> - **CloudNativePG operator bumped to 1.28.1** (`postgres-operator` chart). A minor operator upgrade for the whole fleet; the CNPG CRDs move with it. This also fixes a latent bug where the operator image was newer than its bundled CRDs (`status.instanceID.sessionID` pruned), which made the operator believe the instance manager restarted and fail **every** backup cluster-wide with `instance manager was restarted during backup`. The vendored chart ships the CNPG CRDs as ordinary release manifests (gated by `crds.create`), so a normal `helm upgrade` updates them together with the operator — the `helm.sh/resource-policy: keep` annotation only prevents their deletion, not updates. The historical skew came from the `image.tag` pin outrunning the vendored chart's CRDs, which this PR removes; no out-of-band CRD apply is needed unless the HelmRelease itself is held back.
> - **LINSTOR scheduler admission webhook must not strip native-sidecar fields.** Older `linstor-scheduler` extender builds (≤ v0.3.5's predecessors) decoded every pod through a stale vendored Kubernetes API and re-encoded it, stripping `initContainers[].restartPolicy` cluster-wide — which breaks the plugin's restartable barman sidecar ("startupProbe forbidden without restartPolicy=Always"). The platform already ships the fixed webhook (`linstor-scheduler` re-vendored to upstream chart 0.3.1 / extender v0.3.6, which stopped stripping unknown pod fields), so no action is needed on an up-to-date platform; just do not hold the `linstor-scheduler` package back on an older version when rolling out the plugin.
>
> The same `useSystemBucket` opt-in applies to ClickHouse — see [ClickHouse: opt-in to the system bucket](#clickhouse-opt-in-to-the-system-bucket). When `useSystemBucket: true` is set on ClickHouse, the legacy `<release>-backup` CronJob, credential Secret, and backup script are no longer rendered (they are mutually exclusive with the platform flow); migrate scheduled backups to a `backups.cozystack.io/Plan` against `cozy-default`.

## Tenant workflow

Tenants only ever see the BackupClass name. Typical apply:

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ad-hoc
  namespace: tenant-acme
spec:
  backupClassName: cozy-default
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Postgres
    name: orders-db
```
