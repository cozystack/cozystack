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
  energy counters on top of the upstream defaults. The CSV is a
  superset needed for full coverage of the `gpu/gpu-performance`
  dashboard. Which parts are actually required depends on which
  dashboards you ship — see the table below.
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

## Dashboards and what DCGM metrics they need

Five GPU dashboards live under `gpu/*` in
`packages/system/monitoring/dashboards-infra.list`. All of them share
`packages/system/monitoring-agents/alerts/gpu-recording.rules.yaml` as
their source of aggregated series. The recording rules are safe to
ship on any cluster — they evaluate to empty series when DCGM is not
scraped, or when optional counters are missing.

What each dashboard needs on top of the upstream DCGM Exporter
[`default-counters.csv`][default-csv]:

| Dashboard         | Scope                              | Needs beyond defaults                                                                        |
| ----------------- | ---------------------------------- | -------------------------------------------------------------------------------------------- |
| `gpu-performance` | Per-node, per-GPU deep dive        | `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE`, `DCGM_FI_PROF_GR_ENGINE_ACTIVE`, `DCGM_FI_DEV_POWER_VIOLATION`, `DCGM_FI_DEV_THERMAL_VIOLATION` |
| `gpu-efficiency`  | Per-workload util vs tensor active | `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE`                                                            |
| `gpu-fleet`       | Cluster-wide admin inventory       | nothing (works on default counters)                                                          |
| `gpu-quotas`      | Kube-quota vs live usage           | nothing (kube-state-metrics + default counters)                                              |
| `gpu-tenants`     | Per-namespace tenant view          | `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` for the tensor-saturation panel; other panels work on defaults |

The throttling counters (`DCGM_FI_DEV_POWER_VIOLATION`,
`DCGM_FI_DEV_THERMAL_VIOLATION`) are only required by `gpu-performance`.
The profiling counters (`DCGM_FI_PROF_*`) are required by
`gpu-performance` and `gpu-efficiency`, and unlock the tensor panel in
`gpu-tenants`. Everything else the recording rules consume —
utilization, FB used/free, power, temperature, energy — is already in
the default counter set.

## Verification status

> **Pending verification on an updated GPU Operator release.**
>
> The minimum-CSV claims above are derived by cross-referencing
> `gpu-recording.rules.yaml` and each dashboard against the DCGM
> Exporter [`default-counters.csv`][default-csv] for the version pinned
> in the currently shipped `gpu-operator` package. The package in this
> branch is **not** the latest GPU Operator release; once we move to a
> newer version, the claims must be re-checked because the upstream
> default set occasionally adds or removes counters between releases.
> Until then, treat the CSV in `dcgm-custom-metrics.yaml` as a
> known-good superset rather than a minimal config.

[default-csv]: https://github.com/NVIDIA/dcgm-exporter/blob/main/etc/default-counters.csv
