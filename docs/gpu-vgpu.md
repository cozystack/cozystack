# GPU Operator: vGPU Support

This document describes how to configure the GPU Operator package with NVIDIA vGPU support for sharing a single physical GPU across multiple virtual machines using mediated devices.

## Prerequisites

- NVIDIA GPU with vGPU support (e.g., NVIDIA L40S, A100, A30, etc.)
- Talos Linux as the host OS
- NVIDIA vGPU Software license (NVIDIA AI Enterprise or vGPU subscription)
- Access to the NVIDIA Licensing Portal ([ui.licensing.nvidia.com](https://ui.licensing.nvidia.com))

## Variants

The GPU Operator package supports two variants:

- **`default`** — GPU passthrough mode (vfio-pci). The entire GPU is passed through to a single VM.
- **`vgpu`** — vGPU mode. A physical GPU is shared between multiple VMs using NVIDIA mediated devices.

## Building the vGPU Manager Image

The vGPU Manager driver is proprietary and must be obtained from NVIDIA. The GPU Operator expects a pre-built driver container image — it does not install the driver from a raw `.run` file at runtime.

1. Log in to the [NVIDIA Licensing Portal](https://ui.licensing.nvidia.com)
2. Navigate to **Software Downloads** and download the NVIDIA vGPU Software package for your GPU
3. Build the driver container image using NVIDIA's Makefile-based build system:

```bash
# Clone the NVIDIA driver container repository
git clone https://gitlab.com/nvidia/container-images/driver.git
cd driver

# Place the downloaded .run file in the appropriate directory
cp NVIDIA-Linux-x86_64-550.90.05-vgpu-kvm.run vgpu/

# Build using the provided Makefile
make OS_TAG=ubuntu22.04 \
  VGPU_DRIVER_VERSION=550.90.05 \
  PRIVATE_REGISTRY=registry.example.com/nvidia

# Push to your private registry
docker push registry.example.com/nvidia/vgpu-manager:550.90.05
```

> **Important:** The build process compiles kernel modules against the host kernel version. Refer to the [NVIDIA GPU Operator vGPU documentation](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/install-gpu-operator-vgpu.html) for the complete build procedure and supported OS/kernel combinations.
>
> Uploading the vGPU driver to a publicly available registry is a violation of the NVIDIA vGPU EULA.

## Deploying with vGPU Variant

Create a Package CR with the `vgpu` variant and provide your vGPU Manager image coordinates:

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
            version: "550.90.05"
```

If your registry requires authentication, create an `imagePullSecret` in the `cozy-gpu-operator` namespace and reference it:

```yaml
gpu-operator:
  vgpuManager:
    repository: registry.example.com/nvidia
    version: "550.90.05"
    imagePullSecrets:
    - name: nvidia-registry-secret
```

## NVIDIA License Server (NLS) Configuration

vGPU requires a license server. Configure NLS by passing the license server address via a Secret:

1. Create a Secret with the NLS client configuration in the `cozy-gpu-operator` namespace:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: licensing-config
  namespace: cozy-gpu-operator
stringData:
  gridd.conf: |
    ServerAddress=nls.example.com
    ServerPort=443
    FeatureType=1  # 1 for vGPU (vPC/vWS), 2 for Virtual Compute Server (vCS)
    # ServerPort depends on your NLS deployment (commonly 443 for DLS or 7070 for legacy NLS)
```

2. Reference the Secret in the Package values:

```yaml
gpu-operator:
  vgpuManager:
    repository: registry.example.com/nvidia
    version: "550.90.05"
  driver:
    licensingConfig:
      secretName: licensing-config
```

## vGPU Profiles

Each GPU model supports specific vGPU profiles that determine how the GPU is partitioned. To list available profiles for your GPU, consult the [NVIDIA vGPU User Guide](https://docs.nvidia.com/grid/latest/grid-vgpu-user-guide/).

Example profiles for NVIDIA L40S:

| Profile | Frame Buffer | Max Instances | Use Case |
| --- | --- | --- | --- |
| NVIDIA L40S-1Q | 1 GB | 48 | Light 3D/VDI |
| NVIDIA L40S-2Q | 2 GB | 24 | Medium 3D/VDI |
| NVIDIA L40S-4Q | 4 GB | 12 | Heavy 3D/VDI |
| NVIDIA L40S-6Q | 6 GB | 8 | Professional 3D |
| NVIDIA L40S-8Q | 8 GB | 6 | AI/ML inference |
| NVIDIA L40S-12Q | 12 GB | 4 | AI/ML training |
| NVIDIA L40S-24Q | 24 GB | 2 | Large AI workloads |
| NVIDIA L40S-48Q | 48 GB | 1 | Full GPU equivalent |

Custom vGPU device configuration can be provided via a ConfigMap:

```yaml
gpu-operator:
  vgpuDeviceManager:
    enabled: true
    config:
      name: vgpu-devices-config
      default: default
```

## KubeVirt Integration

To use vGPU with KubeVirt VMs, configure `mediatedDeviceTypes` in the KubeVirt CR. This maps vGPU profiles to node selectors:

```yaml
apiVersion: kubevirt.io/v1
kind: KubeVirt
metadata:
  name: kubevirt
spec:
  configuration:
    mediatedDevicesConfiguration:
      mediatedDeviceTypes:
      - nvidia-592    # NVIDIA L40S-24Q
    permittedHostDevices:
      mediatedDevices:
      - mdevNameSelector: NVIDIA L40S-24Q
        resourceName: nvidia.com/NVIDIA_L40S-24Q
```

Then reference the vGPU resource in a VirtualMachine spec:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
spec:
  template:
    spec:
      domain:
        devices:
          gpus:
          - name: gpu1
            deviceName: nvidia.com/NVIDIA_L40S-24Q
```
