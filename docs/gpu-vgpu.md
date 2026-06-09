# GPU Operator: vGPU Support

This document describes how to configure the GPU Operator package with NVIDIA vGPU support so that a single physical GPU can be sliced and shared across multiple virtual machines.

**Last verified:** 2026-04-29 against KubeVirt `main` (`virt-handler` nightly `20260429_74d7c52588`) + this PR's `vgpu` variant + NVIDIA vGPU 20.0 host driver `595.58.02` + GRID guest driver `595.58.03`.

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

The `gpu-operator` package exposes three variants. This document is vGPU-focused; the variant inventory is shared.

- **`default`** — passthrough mode (`vfio-pci`). Whole GPU goes to a single VM. Talos is supported here; the kernel module is the open-source `vfio-pci`, no proprietary driver is needed on the host.
- **`vgpu`** — SR-IOV vGPU mode. One physical GPU is sliced into multiple VFs, each VF bound to a vGPU profile that the guest sees as its own GPU.
- **`container`** — containerized GPU workloads (CUDA pods, ML training) via the standard NVIDIA device plugin on hosts that already provide both the NVIDIA driver and `nvidia-container-toolkit` (the typical apt-installed Ubuntu/Debian shape). Sandbox workloads are off, `devicePlugin` is on, `driver` / `toolkit` / `vfioManager` / `cdi` are off so that the operator does not fight the host install. Orthogonal to the two VM variants — it does not pass GPUs to KubeVirt VMs. Note that `apt install nvidia-container-toolkit` installs binaries only — it does not configure containerd. Because this variant disables the operator's toolkit component (which would normally do that wiring), the host must additionally have run `nvidia-ctk runtime configure --runtime=containerd` (followed by a containerd restart) and exposed the `nvidia` runtime as the default or via a RuntimeClass before the device plugin can serve GPUs.

## Building the vGPU Manager image

The proprietary vGPU Manager driver must be obtained from NVIDIA and packaged into a container image that the gpu-operator chart pulls — it is not installed from a raw `.run` at runtime. NVIDIA owns this build path; their [`gpu-driver-container`](https://github.com/NVIDIA/gpu-driver-container) repository ships per-OS Dockerfiles under `vgpu-manager/<os>/` and is the source of truth for build args, base images, and supported OS releases. Follow the README in that repository.

The proprietary `.run` is the **Linux KVM** variant (not the Ubuntu KVM `.deb`, which ships pre-built modules for stock kernels only). It comes from the [NVIDIA Licensing Portal](https://ui.licensing.nvidia.com) under an NVIDIA AI Enterprise / vGPU subscription.

> **EULA:** never push the resulting image to a publicly readable registry. Use a private registry (in-cluster Harbor works well as a non-proxy project).

## Deploying with the vgpu variant

The platform's `iaas` bundle deploys the gpu-operator Package CR when `cozystack.gpu-operator` is in `bundles.enabledPackages` and `bundles.iaas.gpuOperatorVariant: vgpu` is set. The vGPU Manager image is proprietary and not redistributable, so the bundle does not ship a default tag — the operator builds the container per the upstream [`gpu-driver-container`](https://github.com/NVIDIA/gpu-driver-container) recipe and supplies the private-registry coordinates through platform values:

```yaml
bundles:
  iaas:
    enabled: true
    gpuOperatorVariant: vgpu
  enabledPackages:
  - cozystack.gpu-operator

gpu:
  vgpuManager:
    repository: registry.example.com/nvidia
    image: vgpu-manager
    version: "595.58.02-ubuntu24.04"
    # imagePullSecrets lives per-component (vgpuManager, driver,
    # validator, dcgmExporter, …). The value is a list of strings,
    # not [{name: ...}].
    imagePullSecrets:
    - nvidia-registry-secret
```

The platform forwards `gpu.vgpuManager` into the emitted gpu-operator Package CR's `components.gpu-operator.values.gpu-operator.vgpuManager`, so the bundle handles the variant + image coordinates in one place. If you need to override anything else on the gpu-operator chart (driver, validator, dcgmExporter, custom node selectors), hand-craft a `Package` CR named `cozystack.gpu-operator` with the full `components.gpu-operator.values` block — that takes precedence over the bundle render.

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

When `cozystack.gpu-operator` is in `bundles.enabledPackages` (and not also in `bundles.disabledPackages`), the platform mirrors the chosen GPU variant into the `KubeVirt` CR automatically. There is no manual `kubectl patch` step.

If you opt out of bundle management and hand-craft a `cozystack.gpu-operator` Package CR directly — typically to apply overrides the bundle does not expose (driver settings, custom node selectors, validator / dcgmExporter tweaks, etc.) — the platform does NOT auto-wire `HostDevices` or `permittedHostDevices` into the KubeVirt CR. In that flow you also hand-craft a `cozystack.kubevirt` Package CR with `components.kubevirt.values.extraFeatureGates: [HostDevices]` and the appropriate `permittedHostDevices` block. The escape-hatch values shape under `.gpu` (below) is intentionally documented in the bundle-managed flow only; the manual Package-CR override path takes precedence over the bundle render whenever both exist.

- `developerConfiguration.featureGates` gets `HostDevices` appended (current KubeVirt splits this from the `GPU` gate; the admission webhook rejects `spec.template.spec.domain.devices.hostDevices` without it).
- `permittedHostDevices.pciHostDevices` is filled from `packages/core/platform/files/gpu-passthrough-defaults.yaml` when `bundles.iaas.gpuOperatorVariant: default` (the package default). The table covers Hopper (H100/H200), Ada Lovelace (L4/L40/L40S), Ampere (A100 PCIe/SXM, A40, A30, A10), Turing (T4), Volta (V100/V100S). All entries carry `externalResourceProvider: true` because the resource names come from `nvidia-sandbox-device-plugin`, not from KubeVirt's in-tree device plugin.
- `permittedHostDevices.mediatedDevices` is filled from `packages/core/platform/files/gpu-vgpu-defaults.yaml` when `bundles.iaas.gpuOperatorVariant: vgpu`. This list only EXPOSES, by profile name (`mdevNameSelector`), mdevs that the GPU Operator's vGPU Device Manager CREATES on the node; the platform does not ship a numeric `mediatedDevicesConfiguration` default (those `nvidia-NNN` type ids are per-SKU/driver sysfs indices with no portable value — set `.gpu.mediatedDevicesConfiguration` yourself, with host-verified ids, only if you want KubeVirt rather than the Device Manager to create mdevs). The starter set covers Pascal–Ampere mdev profiles (A100-40C/80C, A40-24Q/48Q, A30-24C, A10-24Q, V100D-32C, T4-16Q) — the same family range as the upstream `vgpu-device-manager` walks `/sys/class/mdev_bus/` for. Ada Lovelace / Blackwell SR-IOV vGPU is out of scope for the chart's default list; advertise those VFs via the user-override hook below.

### Extending or replacing the default table

The platform exposes three knobs under `.gpu`:

```yaml
gpu:
  # Extend the platform defaults with cluster-specific entries. Both list
  # keys are read in both variants: pciHostDevices feeds the passthrough
  # (vfio-pci) path AND the post-kubevirt#16890 SR-IOV vGPU VF path on
  # Ada Lovelace / Blackwell; mediatedDevices feeds the pre-#16890 mdev
  # path on Pascal–Ampere. Both render into the same KubeVirt CR.
  permittedHostDevices:
    pciHostDevices:
    - pciVendorSelector: "10DE:26B9"   # L40S, advertised as a VF for SR-IOV vGPU
      resourceName: nvidia.com/L40S-24Q
      # externalResourceProvider is intentionally omitted here: after
      # kubevirt/kubevirt#16890, virt-handler's in-tree device plugin
      # advertises the resource directly, no sandbox plugin in the loop.
    mediatedDevices: []
  # mediatedDevicesConfiguration makes KubeVirt itself create mdevs (vgpu mode). No platform default: mdev creation is normally delegated to the vGPU Device Manager (name-based), and these mediatedDeviceTypes are host/driver-specific nvidia-NNN sysfs indices (look yours up via /sys/bus/pci/devices/<BDF>/mdev_supported_types/*/name). Set this only to opt into KubeVirt-driven creation; mergeOverwrite REPLACES a supplied top-level key wholesale.
  mediatedDevicesConfiguration: {}
  # Wipe the platform defaults entirely and ship only the cluster's
  # curated lists. Useful for non-NVIDIA-only clusters and strict
  # allowlist requirements.
  replaceDefaults: false
```

`replaceDefaults: false` (the default) appends user entries to the NVIDIA defaults. `replaceDefaults: true` drops the NVIDIA table entirely — if you don't then supply your own pciHostDevices / mediatedDevices list, the rendered KubeVirt CR has no `permittedHostDevices` block and the admission webhook will reject every GPU VM.

### Notes on `nvidia-sandbox-device-plugin` resource names

The `resourceName` strings in `gpu-passthrough-defaults.yaml` are what `nvidia-sandbox-device-plugin` (`nvcr.io/nvidia/kubevirt-gpu-device-plugin`) advertises: it derives each slug mechanically from the device's PCI-IDs database name by uppercasing it, turning `/`, `.` and whitespace into `_`, and stripping the remaining non-alphanumerics (the `[` / `]`). So `TU104GL [Tesla T4]` becomes `nvidia.com/TU104GL_TESLA_T4` and `GA100GL [A30 PCIe]` becomes `nvidia.com/GA100GL_A30_PCIE` — the slug carries every token the PCI-IDs string holds (the `GL` die suffix, the `Tesla` brand on Turing/Volta, form factor, memory), not a tidy `<arch>_<model>`. The names track the pci.ids snapshot bundled in the plugin image, so a different plugin build can publish a different string — check with `kubectl describe node <node> | grep nvidia.com/` and override via `.gpu.permittedHostDevices.pciHostDevices` (or wipe the table with `replaceDefaults: true` and curate it yourself). PCI vendor:device IDs themselves are stable across driver versions.

### SR-IOV PF vs VF (Ada Lovelace and newer)

On L40S (and other Ada-Lovelace cards) the SR-IOV VFs report the same PCI device ID as the PF — `lspci -nn -d 10de:` on the host shows both as `[10de:26b9]`. `virt-handler` distinguishes them by `is-VF + has-vGPU-profile`, so a single `pciVendorSelector` matches the right set. Verify on your specific GPU before assuming this — some other generations split PF/VF IDs.

`externalResourceProvider: true` is **not** required when the resource is advertised by `virt-handler`'s in-tree device plugin (the SR-IOV path post kubevirt#16890). The platform passthrough defaults include the flag because that path is driven by the external sandbox plugin.

### Verifying allocatable capacity

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
  # 0744 follows NVIDIA's recommendation in the Virtual GPU Software
  # Licensing User Guide ("Configuring a Licensed Client on Linux"):
  # nvidia-gridd does not necessarily run as the file owner.
  # https://docs.nvidia.com/vgpu/latest/grid-licensing-user-guide/
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

The `container` variant column assumes the host already ships the NVIDIA driver and `nvidia-container-toolkit` via the distro package manager, with the `nvidia` runtime registered in containerd (`nvidia-ctk runtime configure --runtime=containerd`). With `driver.enabled=false` the operator uses the pre-installed host driver at its standard location, so a stock apt install needs no `hostPaths.driverInstallDir` override. Talos installs the driver under a non-standard prefix, so the operator does not find it at the default location — see `packages/system/gpu-operator/examples/` for the Talos-specific path with a compat DaemonSet and an explicit `hostPaths.driverInstallDir` override.

| Host OS | passthrough (`default`) | vGPU (`vgpu`) | container (`container`) |
| --- | --- | --- | --- |
| Ubuntu 24.04 | ✅ supported upstream | ✅ supported upstream (`vgpu-manager/ubuntu24.04`) | ✅ apt-installed driver + nvidia-container-toolkit |
| Ubuntu 22.04 | ✅ | ✅ | ✅ |
| Ubuntu 20.04 | ✅ | ✅ | ✅ |
| Ubuntu 26.04 | ⚠️ patch needed in `nvidia-driver` for usr-merge | ⚠️ same patch + own Dockerfile fork | ✅ |
| Talos Linux | ✅ (open `vfio-pci`) | ❌ NVIDIA does not grant redistribution rights for the proprietary `.run`; we tried and the path is blocked | ⚠️ host driver lands in a non-standard prefix — use `examples/values-native-talos.yaml` (compat DaemonSet + `hostPaths.driverInstallDir` override) as a starting point instead |
