# HAMi — GPU Virtualization Middleware

[HAMi](https://github.com/Project-HAMi/HAMi) (Heterogeneous AI Computing Virtualization Middleware) is a CNCF Sandbox project that enables fractional GPU sharing in Kubernetes. It allows workloads to request specific amounts of GPU memory and compute cores instead of claiming entire GPUs.

## Architecture

HAMi consists of four components:

- **MutatingWebhook** — intercepts pod creation, injects `schedulerName: hami-scheduler`
- **Scheduler Extender** — extends kube-scheduler with GPU-aware Filter and Bind logic
- **Device Plugin** (DaemonSet) — registers vGPU resources via the Kubernetes Device Plugin API
- **HAMi-core** (`libvgpu.so`) — `LD_PRELOAD` library injected into workload containers, intercepts CUDA API calls to enforce memory and compute isolation

## Prerequisites

- GPU Operator must be enabled (`addons.gpuOperator.enabled: true`)
- NVIDIA driver >= 440 on host nodes
- nvidia-container-toolkit configured as the default container runtime
- GPU nodes labeled with `gpu=on`

## Known Limitations

### glibc < 2.34 requirement for workload containers

HAMi-core uses `LD_PRELOAD` to intercept `dlsym()` for CUDA symbol resolution. The fallback code path relies on `_dl_sym`, a private glibc internal symbol that was removed in glibc 2.34 when libdl and libpthread were merged into libc.so.

**This limitation affects workload containers only**, not the host OS or HAMi's own components.

| Distribution    | glibc | Result                                       |
| --------------- | ----- | -------------------------------------------- |
| Ubuntu 18.04    | 2.27  | Full isolation (memory + compute)            |
| Ubuntu 20.04    | 2.31  | Full isolation (memory + compute)            |
| Ubuntu 22.04    | 2.35  | Memory isolation works, compute breaks       |
| Ubuntu 24.04    | 2.39  | Both memory and compute isolation break      |
| Alpine (musl)   | N/A   | Completely incompatible (`dlvsym` absent)    |

Most modern ML/AI base images (CUDA 12.x, PyTorch 2.x, TensorFlow 2.x) use Ubuntu 22.04+ with glibc >= 2.35, which means compute isolation will not work with these images until the upstream fix is merged.

**Upstream tracking issues:**

- [HAMi-core#174](https://github.com/Project-HAMi/HAMi-core/issues/174) — `_dl_sym` removal in glibc 2.34 breaks HAMi-core's CUDA symbol resolution at the symbol level
- [HAMi#1190](https://github.com/Project-HAMi/HAMi/issues/1190) — maintainer thread confirming the empirical per-glibc-version isolation behavior shown in the table above

### musl libc (Alpine) incompatibility

HAMi-core is completely incompatible with musl libc. The `dlvsym()` function used by HAMi-core is a glibc extension not available in musl. Only glibc-based container images (Debian, Ubuntu, RHEL, etc.) can use HAMi GPU isolation.

## Usage

Enable HAMi in your tenant Kubernetes cluster values:

```yaml
addons:
  gpuOperator:
    enabled: true
  hami:
    enabled: true
```

When HAMi is enabled, GPU Operator's built-in device plugin is automatically disabled to avoid conflicts.

### Requesting fractional GPU resources

```yaml
resources:
  limits:
    nvidia.com/gpu: 1
    nvidia.com/gpumem: 3000     # 3000 MB of GPU memory
    nvidia.com/gpucores: 30     # 30% of GPU compute cores
```

## Parameters

Default values shown below are inherited from the upstream HAMi chart and may change with upstream updates.

| Name | Description | Default |
| --- | --- | --- |
| `hami.devicePlugin.runtimeClassName` | RuntimeClass for device plugin pods | `nvidia` |
| `hami.devicePlugin.deviceSplitCount` | Max virtual GPUs per physical GPU | `10` |
| `hami.devicePlugin.deviceMemoryScaling` | Memory overcommit factor (> 1.0 enables overcommit) | `1` |
| `hami.scheduler.defaultSchedulerPolicy.nodeSchedulerPolicy` | Node packing strategy (`binpack` or `spread`) | `binpack` |
| `hami.scheduler.defaultSchedulerPolicy.gpuSchedulerPolicy` | GPU packing strategy (`binpack` or `spread`) | `spread` |
