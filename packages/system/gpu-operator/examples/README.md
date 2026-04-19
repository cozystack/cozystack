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

The compat DaemonSet runs privileged and bind-mounts host paths, so
the target namespace must allow privileged pods. On clusters that
enforce the Kubernetes Pod Security Standards at `baseline` or
`restricted`, label the namespace with
`pod-security.kubernetes.io/enforce: privileged` (and the matching
`audit`/`warn` labels if the admission webhook is configured to
surface violations) before applying the manifest.

## Dashboards and what DCGM metrics they need

Five GPU dashboards live under `gpu/*` in
`packages/system/monitoring/dashboards-infra.list`. All of them share
`packages/system/monitoring-agents/alerts/gpu-recording.rules.yaml` as
their source of aggregated series. The recording rules are safe to
ship on any cluster — they evaluate to empty series when DCGM is not
scraped, or when optional counters are missing.

What each dashboard needs on top of the upstream DCGM Exporter
[`default-counters.csv`][default-csv]:

| Dashboard         | Scope                              | Needs beyond defaults                                                   |
| ----------------- | ---------------------------------- | ----------------------------------------------------------------------- |
| `gpu-performance` | Per-node, per-GPU deep dive        | `DCGM_FI_DEV_POWER_VIOLATION`, `DCGM_FI_DEV_THERMAL_VIOLATION`          |
| `gpu-efficiency`  | Per-workload util vs tensor active | `DCGM_FI_DEV_POWER_VIOLATION`, `DCGM_FI_DEV_THERMAL_VIOLATION` (via `gpu:*_throttle_fraction:rate5m` recording rules) |
| `gpu-fleet`       | Cluster-wide admin inventory       | `DCGM_FI_DEV_POWER_MGMT_LIMIT` (for the TDP vs draw panel)              |
| `gpu-quotas`      | Kube-quota vs live usage           | nothing (kube-state-metrics + default counters)                         |
| `gpu-tenants`     | Per-namespace tenant view          | nothing (works on default counters)                                     |

`DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` and `DCGM_FI_PROF_GR_ENGINE_ACTIVE`
are already in the upstream default set for the pinned DCGM Exporter
version, so the tensor-saturation and engine-active panels work without
any CSV override. The three counters listed in the table — throttling
violations and the power management limit — are the only extras the
tracked dashboards need. The recording rules in
`gpu-recording.rules.yaml` consume utilization, FB used, power,
temperature and the tensor-active profiling counter from the default
set, plus `DCGM_FI_DEV_POWER_VIOLATION` and
`DCGM_FI_DEV_THERMAL_VIOLATION` — used by the
`gpu.recording.efficiency.1m` group to derive the
`gpu:power_throttle_fraction:rate5m` and
`gpu:thermal_throttle_fraction:rate5m` series consumed by the
throttling panels on the efficiency and fleet dashboards.

The `gpu.recording.throttle.validation.5m` group additionally ships the
`GPUThrottleFractionOverOne` alert (severity `warning`) as a regression
detector: it fires when either throttle-fraction series exceeds 1.0,
which would indicate that DCGM changed the scale/divisor of the
underlying violation counters and the recording rules need to be
re-derived.

## Verification status

The minimum-CSV claims above are verified by
`hack/check-gpu-recording-rules.bats`, which cross-checks every
`DCGM_FI_*` reference in the tracked GPU dashboards and recording rules
against the union of the upstream default set (snapshotted at
`hack/dcgm-default-counters.csv` for the pinned DCGM Exporter version)
and the custom CSV in `dcgm-custom-metrics.yaml`. When the DCGM
Exporter image in `packages/system/gpu-operator/charts/gpu-operator/values.yaml`
is bumped, refresh the snapshot from the matching tag of the
[`NVIDIA/dcgm-exporter`][default-csv] repository and rerun the test.

[default-csv]: https://github.com/NVIDIA/dcgm-exporter/blob/main/etc/default-counters.csv
