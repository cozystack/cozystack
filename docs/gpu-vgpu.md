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

The vGPU Manager driver is proprietary and must be obtained from NVIDIA:

1. Log in to the [NVIDIA Licensing Portal](https://ui.licensing.nvidia.com)
2. Download the NVIDIA vGPU Software package for your GPU and target Linux version
3. Build a container image containing the vGPU Manager driver:

```bash
# Example Containerfile
FROM ubuntu:22.04
ARG DRIVER_VERSION
COPY NVIDIA-Linux-x86_64-${DRIVER_VERSION}-vgpu-kvm.run /opt/
RUN chmod +x /opt/NVIDIA-Linux-x86_64-${DRIVER_VERSION}-vgpu-kvm.run
```

4. Push the image to your private registry:

```bash
docker build --build-arg DRIVER_VERSION=550.90.05 --tag registry.example.com/nvidia/vgpu-manager:550.90.05 .
docker push registry.example.com/nvidia/vgpu-manager:550.90.05
```

> Refer to the [NVIDIA GPU Operator documentation](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/install-gpu-operator-vgpu.html) for detailed instructions on building the vGPU Manager image.

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

vGPU requires a license server. Configure NLS by passing the license server address via a ConfigMap:

1. Create a ConfigMap with the NLS client configuration in the `cozy-gpu-operator` namespace:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: licensing-config
  namespace: cozy-gpu-operator
data:
  gridd.conf: |
    ServerAddress=nls.example.com
    ServerPort=443
    FeatureType=1
```

2. Reference the ConfigMap in the Package values:

```yaml
gpu-operator:
  vgpuManager:
    repository: registry.example.com/nvidia
    version: "550.90.05"
  driver:
    licensingConfig:
      configMapName: licensing-config
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
