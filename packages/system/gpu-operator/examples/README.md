# GPU operator — native pod workload on Talos (reference)

The files in this directory are **not** templates. They are reference
artifacts that document one working configuration for running GPU
workloads directly in pods on a Talos-based Cozystack cluster, together
with the DCGM metrics needed by the `gpu/gpu-performance` Grafana
dashboard.

The out-of-the-box `values-talos.yaml` for this package targets the
sandbox (VFIO passthrough to KubeVirt VMs) scenario. The files here
illustrate an alternative — running CUDA workloads in regular pods with
the NVIDIA device plugin — and the workarounds it currently requires on
Talos.

## Files

- [`values-native-talos.yaml`](./values-native-talos.yaml) — Cozystack
  `Package` values that disable sandbox workloads, enable the device
  plugin, point `hostPaths.driverInstallDir` at the staging location
  used by the compat DaemonSet, and wire DCGM to the custom metrics
  ConfigMap.
- [`dcgm-custom-metrics.yaml`](./dcgm-custom-metrics.yaml) — `ConfigMap`
  with a DCGM metrics CSV that adds profiling, ECC, throttling and
  energy counters on top of the upstream defaults. The CSV is the
  superset needed for full dashboard coverage; the **recording rules
  themselves** only require the profiling subset
  (`DCGM_FI_PROF_PIPE_TENSOR_ACTIVE`, `DCGM_FI_PROF_GR_ENGINE_ACTIVE`)
  on top of the upstream `default-counters.csv` — every other DCGM
  series the rules consume (utilization, FB used/free, power,
  temperature, energy) is already in the default set. The
  `gpu/gpu-performance` dashboard additionally needs the throttle
  counters (`DCGM_FI_DEV_POWER_VIOLATION`,
  `DCGM_FI_DEV_THERMAL_VIOLATION`), which are not in the default set.
- [`nvidia-driver-compat.yaml`](./nvidia-driver-compat.yaml) — DaemonSet
  that stages `libnvidia-ml.so.1` and `nvidia-smi` from the Talos glibc
  tree into a path where the NVIDIA GPU Operator validator expects
  them. See the "Why the compat DaemonSet exists" section below.

## Why these are reference, not templates

Shipping these as first-class templates would silently impose
assumptions that do not hold for every user:

- Whether the NVIDIA Talos system extension is installed on the nodes.
- Whether GPUs are exposed directly to pods or passed through to VMs.
- The exact path the installed driver ends up at (depends on the
  extension version and Talos release).

The sandbox-oriented `values-talos.yaml` remains the default. Operators
who want native pod GPU workloads can start from this directory and
adapt as needed.

## Why the compat DaemonSet exists

The NVIDIA GPU Operator validator checks for `libnvidia-ml.so.1` and
`bin/nvidia-smi` in the path given by `hostPaths.driverInstallDir`.
Talos installs them under `/usr/local/glibc/usr/lib/` and
`/usr/local/bin/`, which the validator does not look at. Until upstream
addresses [NVIDIA/gpu-operator#1687][1], the DaemonSet copies those
files into a directory the validator does inspect and creates the
`.driver-ctr-ready` flag file so the validator proceeds.

[1]: https://github.com/NVIDIA/gpu-operator/issues/1687

## Verification status

> **Pending verification on an updated GPU Operator release.**
>
> The minimum-CSV claim above (only `DCGM_FI_PROF_*` is needed beyond
> the upstream default counters) is derived by cross-referencing
> `gpu-recording.rules.yaml` against the DCGM Exporter
> [`default-counters.csv`][default-csv] for the version pinned in the
> currently shipped `gpu-operator` package. The package in this branch
> is **not** the latest GPU Operator release; once we move to a newer
> version, the claim must be re-checked because the upstream default
> set occasionally adds or removes counters between releases. Until
> then, treat the CSV in `dcgm-custom-metrics.yaml` as a known-good
> superset rather than a minimal config.

[default-csv]: https://github.com/NVIDIA/dcgm-exporter/blob/main/etc/default-counters.csv

## How the dashboard and recording rules fit in

- `dashboards/gpu/gpu-performance.json` expects `DCGM_FI_*` metrics,
  including profiling series (`DCGM_FI_PROF_PIPE_TENSOR_ACTIVE`,
  `DCGM_FI_PROF_GR_ENGINE_ACTIVE`) and throttling counters
  (`DCGM_FI_DEV_POWER_VIOLATION`, `DCGM_FI_DEV_THERMAL_VIOLATION`).
  These are only emitted when DCGM Exporter is started with the custom
  CSV in `dcgm-custom-metrics.yaml`.
- `packages/system/monitoring-agents/alerts/gpu-recording.rules.yaml`
  precomputes cluster-wide and per-namespace aggregations used by the
  overview panels of the dashboard. The rules are safe to ship on any
  cluster — they evaluate to empty series when DCGM is not scraped.
