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
  energy counters on top of the upstream defaults. Required by the
  recording rules in `packages/system/monitoring-agents/alerts/gpu-recording.rules.yaml`
  and by several panels in the `gpu/gpu-performance` dashboard.
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
