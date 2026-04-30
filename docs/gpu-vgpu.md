# GPU Operator: vGPU Support

This document describes how to configure the GPU Operator package with NVIDIA vGPU support so that a single physical GPU can be sliced and shared across multiple virtual machines.

## Two driver models

NVIDIA's vGPU driver uses two different host-side models depending on GPU generation:

- **Mediated devices (mdev)** — Pascal / Volta / Turing / Ampere up to A100 / A30. The driver creates `mdev` parent devices under `/sys/class/mdev_bus/`; KubeVirt advertises them via `permittedHostDevices.mediatedDevices`.
- **SR-IOV with per-VF sysfs** — Ada Lovelace (L4, L40, L40S, …) and Blackwell (B100, …) on the vGPU 17/20 driver branch. The driver creates SR-IOV virtual functions; profile selection happens via `/sys/bus/pci/devices/<VF>/nvidia/current_vgpu_type`. KubeVirt advertises VFs via `permittedHostDevices.pciHostDevices` after [kubevirt/kubevirt#16890](https://github.com/kubevirt/kubevirt/pull/16890).

This guide focuses on the **SR-IOV path**, which is the only model NVIDIA supports for current data-centre GPUs. Mdev is mentioned for completeness; for Pascal–Ampere refer to the upstream NVIDIA GPU Operator docs.

## Prerequisites

- An Ada Lovelace (or newer) NVIDIA GPU that supports SR-IOV vGPU (L4, L40, L40S, etc.).
- Ubuntu 24.04 host OS. Older Ubuntu releases also work if the upstream `gpu-driver-container` repo has a matching `vgpu-manager/` Dockerfile. **Talos Linux is not recommended** for vGPU. NVIDIA does not publicly distribute the vGPU guest driver — it requires NVIDIA Enterprise Portal access — and Sidero [closed siderolabs/extensions#461](https://github.com/siderolabs/extensions/issues/461) noting that they cannot support vGPU "unless NVIDIA changes their licensing terms or provides us a way to obtain, test, and distribute the software". Building a Talos system extension that includes the driver in-tree is therefore not feasible without a private fork that violates the EULA.
- KubeVirt with [kubevirt/kubevirt#16890](https://github.com/kubevirt/kubevirt/pull/16890) ("vGPU: SRIOV support", merged to `main` 2026-04-10). Targeted at the next minor release (v1.9.0); track the PR for the actual release tag. Released tags up to and including v1.8.x do not include the patch and backports are not planned. If you need vGPU before v1.9.0 lands you have to run a `main`-based nightly build of `virt-handler`; the rest of the operator can stay on the latest released tag.
- An NVIDIA vGPU Software / NVIDIA AI Enterprise subscription (the `.run` is not redistributable).
- A reachable NVIDIA Delegated License Service (DLS) instance and a matching `client_configuration_token.tok` file.

## Variants

The `gpu-operator` package exposes two variants:

- **`default`** — passthrough mode (`vfio-pci`). Whole GPU goes to a single VM. Talos is supported here; the kernel module is the open-source `vfio-pci`, no proprietary driver is needed on the host.
- **`vgpu`** — SR-IOV vGPU mode. One physical GPU is sliced into multiple VFs, each VF bound to a vGPU profile that the guest sees as its own GPU.

## Building the vGPU Manager image

The proprietary vGPU Manager driver must be obtained from NVIDIA and packaged into a container image. The canonical build path is the upstream NVIDIA repository (the older `gitlab.com/nvidia/container-images/driver` is archived):

```bash
git clone https://github.com/NVIDIA/gpu-driver-container.git
cd gpu-driver-container/vgpu-manager/ubuntu24.04

# Place the .run alongside the Dockerfile (do not check it in)
cp /path/to/NVIDIA-Linux-x86_64-595.58.02-vgpu-kvm.run .

# --platform linux/amd64 is mandatory on arm64 build hosts (Apple
# Silicon): GPU nodes are amd64 and the kubelet pull fails with
# 'no matching manifest' if the image was built native on arm64.
docker build \
  --platform linux/amd64 \
  --build-arg DRIVER_VERSION=595.58.02 \
  -t registry.example.com/nvidia/vgpu-manager:595.58.02-ubuntu24.04 .

# docker login first if your registry needs auth.
docker push registry.example.com/nvidia/vgpu-manager:595.58.02-ubuntu24.04
```

The container's entrypoint downloads kernel headers at pod start time and compiles `nvidia.ko` against the running kernel, so a single image works across kernel patch versions for the same Ubuntu release. The proprietary `.run` is the **Linux KVM** variant (not the Ubuntu KVM `.deb`, which ships pre-built modules for stock kernels only).

> **EULA:** never push the resulting image to a publicly readable registry. Use a private registry (in-cluster Harbor works well as a non-proxy project).

## Deploying with the vgpu variant

Create a `Package` CR pointing at your image coordinates:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.gpu-operator
spec:
  variant: vgpu
  components:
    gpu-operator:
      values:
        gpu-operator:
          vgpuManager:
            repository: registry.example.com/nvidia
            image: vgpu-manager
            version: "595.58.02-ubuntu24.04"
            # imagePullSecrets lives per-component (vgpuManager,
            # driver, validator, dcgmExporter, …). The value is a
            # list of strings, not [{name: ...}].
            imagePullSecrets:
            - nvidia-registry-secret
```

The `nvidia-registry-secret` should be a docker-registry Secret created beforehand in `cozy-gpu-operator`.

Verify the DaemonSet is running and `nvidia.ko` loads on every GPU node:

```bash
kubectl -n cozy-gpu-operator get pods -l app=nvidia-vgpu-manager-daemonset
kubectl -n cozy-gpu-operator exec -it <pod> -- nvidia-smi
```

`nvidia-smi` should enumerate the physical GPUs and report `Host VGPU Mode : SR-IOV`.

## Profile assignment (SR-IOV path)

> **The `vgpu` variant is experimental on Ada+ and ships without a profile-assignment loop.** NVIDIA's `vgpu-device-manager` walks `/sys/class/mdev_bus/`, which does not exist on Ada+ — the DaemonSet errors with "no parent devices found for GPU at index '0'" and is therefore disabled by default in `values-vgpu.yaml`. Until an SR-IOV-aware controller is shipped, profile assignment is an out-of-band step that must be re-applied after every node reboot (`current_vgpu_type` resets to 0 on PCIe re-enumeration). Without this step, `permittedHostDevices.pciHostDevices` will report zero allocatable resources and no VM can request the vGPU. **Do not deploy the `vgpu` variant in production until you have an automated profile-assignment mechanism in place** — typically a small DaemonSet that reads a ConfigMap (`<bus-id> = <profile-id>`) and writes the corresponding `current_vgpu_type` files at boot.

Once `nvidia.ko` is loaded the driver enables SR-IOV (16 VFs per L40S by default). Each VF needs a vGPU profile written to its sysfs:

```bash
# from inside the nvidia-vgpu-manager-daemonset pod (privileged, hostPID)
echo 1155 > /sys/bus/pci/devices/0000:02:00.5/nvidia/current_vgpu_type
```

The numeric profile ID can be discovered per-VF:

```bash
cat /sys/bus/pci/devices/0000:02:00.5/nvidia/creatable_vgpu_types
```

For Pascal–Ampere GPUs (V100, T4, A100, A30) the mdev model still applies. Flip `vgpuDeviceManager.enabled: true` in your Package CR overrides — NVIDIA's device manager works correctly there.

## KubeVirt configuration

After [kubevirt#16890](https://github.com/kubevirt/kubevirt/pull/16890), `virt-handler` recognises SR-IOV VFs bound to the `nvidia` driver as candidates whenever a vGPU profile is configured (`current_vgpu_type` ≠ 0). PFs are skipped automatically.

Patch the `KubeVirt` CR to permit the resource:

```yaml
spec:
  configuration:
    permittedHostDevices:
      pciHostDevices:
      - pciVendorSelector: "10DE:26B9"   # L40S — same device ID for PF and VF
        resourceName: nvidia.com/L40S-24Q
```

On L40S (and other Ada-Lovelace cards) the SR-IOV VFs report the same PCI device ID as the PF — `lspci -nn -d 10de:` on the host shows both as `[10de:26b9]`. `virt-handler` distinguishes them by `is-VF + has-vGPU-profile`, so a single `pciVendorSelector` matches the right set. Verify on your specific GPU before assuming this — some other generations split PF/VF IDs.

`externalResourceProvider: true` is **not** required here (and should be omitted) — the device plugin lives inside `virt-handler` itself, no external sandbox-device-plugin advertises this resource.

Verify allocatable capacity:

```bash
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.status.allocatable.nvidia\.com/L40S-24Q}{"\n"}{end}'
```

## Licensing (DLS)

vGPU 17/20 uses the NVIDIA Delegated License Service. The legacy `ServerAddress=` / `ServerPort=7070` lines in `gridd.conf` are no longer authoritative — `nvidia-gridd` (running **inside the guest**) reads the DLS endpoint from the ClientConfigToken file directly.

The host vGPU Manager DaemonSet does not request a license — it only enables SR-IOV and loads `nvidia.ko`. Licensing is consumed entirely by the guest. The gpu-operator chart's `driver.licensingConfig.secretName` would mount the Secret into the **driver pod on the host**, where it has no effect for SR-IOV vGPU; do not wire the licensing Secret through it.

Instead, deliver the token and `gridd.conf` to the guest via cloud-init or a containerDisk overlay:

```yaml
# inside the VirtualMachine cloudInitNoCloud userData
write_files:
- path: /etc/nvidia/ClientConfigToken/client_configuration_token.tok
  # 0744 follows NVIDIA's recommendation in the Licensing User Guide
  # so nvidia-gridd (which does not necessarily run as the file owner)
  # can read it.
  permissions: '0744'
  encoding: b64
  content: <base64 token>
- path: /etc/nvidia/gridd.conf
  permissions: '0644'
  content: |
    # FeatureType selects which vGPU Software license the guest requests.
    # 0 — unlicensed state (no license requested; Q profiles run in
    #     reduced mode after the grace period).
    # 1 — NVIDIA vGPU. The driver auto-selects the correct license
    #     type from the configured vGPU profile (Q → vWS, B → vPC,
    #     A → vCS / Compute). Use this for SR-IOV vGPU profiles.
    # 2 — explicitly NVIDIA RTX Virtual Workstation.
    # 4 — explicitly NVIDIA Virtual Compute Server.
    FeatureType=1
```

Verify activation inside the guest:

```bash
nvidia-smi -q | grep 'License Status'
# License Status   : Licensed
```

If the guest reports `Unlicensed (Unrestricted)` for more than a couple of minutes, check `journalctl _COMM=nvidia-gridd` for handshake errors against the DLS endpoint baked into the token.

### Migrating from chart v25.x

Operators upgrading from the previous Cozystack release (gpu-operator chart v25.3.0) should also note that the upstream chart deprecated `driver.licensingConfig.configMapName` in favour of `driver.licensingConfig.secretName`. The old key still works but emits a deprecation warning at render time. If your existing `Package` CR set the licensing reference via `configMapName`, switch it to `secretName` on this upgrade — the Secret content (`gridd.conf` and the ClientConfigToken) does not need to change. This applies to passthrough deployments that drove host-side licensing through the gpu-operator chart; SR-IOV vGPU does not consume the host-side licensing knob at all (see "Licensing (DLS)" above).

## Sample VirtualMachine

Either `hostDevices` or `gpus` accepts the resource (the upstream KubeVirt API resolves both PCI and mediated-device pools), but the convention is to use `hostDevices` for VF-style PCI passthrough:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: vgpu-smoke
  namespace: tenant-example
spec:
  runStrategy: Always
  template:
    spec:
      domain:
        cpu:
          cores: 4
        memory:
          guest: 8Gi
        devices:
          disks:
          - name: rootdisk
            disk:
              bus: virtio
          interfaces:
          - name: default
            masquerade: {}
          hostDevices:
          - name: gpu0
            deviceName: nvidia.com/L40S-24Q
      networks:
      - name: default
        pod: {}
      volumes:
      - name: rootdisk
        # A 2.4 GiB containerDisk overlay is too small to install
        # the GRID guest driver in-place. Use a CDI DataVolume of
        # 20 GiB+ in production.
        containerDisk:
          image: quay.io/containerdisks/ubuntu:24.04
```

Inside the guest, install the GRID driver from the `.run` (the GUEST `.run`, distinct from the host `vgpu-kvm` package), then `nvidia-smi` should report the configured profile:

```text
| 0  NVIDIA L40S-24Q                Off |   00000000:0E:00.0 Off |                    0 |
|        17 MiB / 24576 MiB    P0    Default                                                |
```

## Profile reference (L40S)

L40S supports the full Q (RTX vWS), B (vPC), A (vCS / Compute) profile families. The numeric IDs come from the driver and are visible in `creatable_vgpu_types`:

| Profile | Frame Buffer | Max instances per L40S | Use case |
| --- | --- | --- | --- |
| L40S-1Q | 1 GB | 48 | Light 3D / VDI |
| L40S-2Q | 2 GB | 24 | Medium 3D / VDI |
| L40S-4Q | 4 GB | 12 | Heavy 3D / VDI |
| L40S-6Q | 6 GB | 8 | Professional 3D |
| L40S-8Q | 8 GB | 6 | AI / ML inference |
| L40S-12Q | 12 GB | 4 | AI / ML training |
| L40S-24Q | 24 GB | 2 | Large AI workloads |
| L40S-48Q | 48 GB | 1 | Full GPU equivalent |

Other GPU families have analogous tables in the [NVIDIA Virtual GPU Software Documentation](https://docs.nvidia.com/grid/latest/grid-vgpu-user-guide/).

## OS support summary

| Host OS | passthrough (`default`) | vGPU (`vgpu`) |
| --- | --- | --- |
| Ubuntu 24.04 | ✅ supported upstream | ✅ supported upstream (`vgpu-manager/ubuntu24.04`) |
| Ubuntu 22.04 | ✅ | ✅ |
| Ubuntu 20.04 | ✅ | ✅ |
| Ubuntu 26.04 | ⚠️ patch needed in `nvidia-driver` for usr-merge | ⚠️ same patch + own Dockerfile fork |
| Talos Linux | ✅ (open `vfio-pci`) | ❌ NVIDIA does not grant redistribution rights for the proprietary `.run`; we tried and the path is blocked |
