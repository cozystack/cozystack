# Resource presets

Every cozystack app exposes a `resourcesPreset` field whose value selects a CPU and memory reservation. Presets follow a cloud-style `<series>.<size>` naming convention. Setting `resources:` explicitly overrides the preset.

## Series

| Series | CPU:Memory ratio | When to use                                       |
| ------ | ---------------- | ------------------------------------------------- |
| `t1`   | 1:0.5            | Tiny / burstable, low memory                      |
| `c1`   | 1:1              | Compute-balanced, CPU-bound workloads             |
| `s1`   | 1:2              | Standard — proxies, caches, lightweight services  |
| `u1`   | 1:4              | Universal — databases and messaging               |
| `m1`   | 1:8              | Memory-heavy — search, analytics, large caches    |

## Watch out: legacy and instance-type `medium` differ

Legacy `medium` had **1 CPU / 1Gi**. The new `*.medium` sizes have **2 CPU** (in any series). The names overlap but the resources do not. The migration table below stays correct — `medium → c1.small (1 CPU / 1Gi)` — but if you read the instance-type sizing matrix first and pick `c1.medium` "to keep things the same", you will double your CPU. When in doubt, consult the legacy-to-instance-type mapping at the bottom of this page.

## Sizes

| Size       | CPU  | t1 mem | c1 mem | s1 mem | u1 mem | m1 mem |
| ---------- | ---- | ------ | ------ | ------ | ------ | ------ |
| `nano`     | 250m | 128Mi  | 256Mi  | 512Mi  | 1Gi    | 2Gi    |
| `micro`    | 500m | 256Mi  | 512Mi  | 1Gi    | 2Gi    | 4Gi    |
| `small`    | 1    | 512Mi  | 1Gi    | 2Gi    | 4Gi    | 8Gi    |
| `medium`   | 2    | 1Gi    | 2Gi    | 4Gi    | 8Gi    | 16Gi   |
| `large`    | 4    | 2Gi    | 4Gi    | 8Gi    | 16Gi   | 32Gi   |
| `xlarge`   | 8    | 4Gi    | 8Gi    | 16Gi   | 32Gi   | 64Gi   |
| `2xlarge`  | 16   | 8Gi    | 16Gi   | 32Gi   | 64Gi   | 128Gi  |
| `4xlarge`  | 32   | 16Gi   | 32Gi   | 64Gi   | 128Gi  | 256Gi  |

Ephemeral storage is 2Gi for every preset.

## Legacy flat names (deprecated)

The following short names existed before the instance-type rename and remain accepted for backward compatibility. They render exactly the CPU and memory they did before — the legacy block in `cozy-lib` returns the original values verbatim. The 1:1 mapping is:

| Legacy   | CPU  | Memory | Instance-type equivalent |
| -------- | ---- | ------ | ------------------------ |
| `nano`   | 250m | 128Mi  | `t1.nano`                |
| `micro`  | 500m | 256Mi  | `t1.micro`               |
| `small`  | 1    | 512Mi  | `t1.small`               |
| `medium` | 1    | 1Gi    | `c1.small`               |
| `large`  | 2    | 2Gi    | `c1.medium`              |
| `xlarge` | 4    | 4Gi    | `c1.large`               |
| `2xlarge`| 8    | 8Gi    | `c1.xlarge`              |

Legacy names are scheduled for removal in a future cozystack release. New manifests should use the instance-type form. `cozystack-api` emits a `klog` warning whenever an app CR carries a legacy value, naming the suggested replacement.

## Automatic migration

Platform upgrades run `migration 39` as a pre-upgrade hook. It walks every `HelmRelease` `spec.values` and every app CR under `apps.cozystack.io/v1alpha1`, rewriting legacy `resourcesPreset` values in place using the table above. The conversion is idempotent and best-effort: a failure on one object logs a warning and leaves the rest of the migration to complete. Set `MIGRATION_DRY_RUN=1` to preview the patches without applying them.
