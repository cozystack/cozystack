# proxmox-csi-plugin

![Version: 0.5.12](https://img.shields.io/badge/Version-0.5.12-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.20.0](https://img.shields.io/badge/AppVersion-v0.20.0-informational?style=flat-square)

Container Storage Interface plugin for Proxmox

The Container Storage Interface (CSI) plugin is a specification designed to standardize the way container orchestration systems like Kubernetes, interact with different storage systems. The CSI plugin abstracts the underlying storage, enabling the seamless integration of different storage solutions (such as local block devices, file systems, or cloud-based storage) with containerized applications.

This plugin allows Kubernetes to use `Proxmox VE` storage as a persistent storage solution for stateful applications.
Supported storage types:
- Directory
- LVM
- LVM-thin
- ZFS
- NFS
- Ceph

**Homepage:** <https://github.com/sergelogvinov/proxmox-csi-plugin>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| sergelogvinov |  | <https://github.com/sergelogvinov> |

## Source Code

* <https://github.com/sergelogvinov/proxmox-csi-plugin>

## Proxmox permissions

```shell
# Create role CSI
pveum role add CSI -privs "VM.Audit VM.Config.Disk Datastore.Allocate Datastore.AllocateSpace Datastore.Audit"
# Or if you need to use Replication feature (zfs replication)
pveum role add CSI -privs "VM.Audit VM.Allocate VM.Clone VM.Config.CPU VM.Config.Disk VM.Config.HWType VM.Config.Memory VM.Config.Options VM.Migrate VM.PowerMgmt Datastore.Allocate Datastore.AllocateSpace Datastore.Audit"

# Create user and grant permissions
pveum user add kubernetes-csi@pve
pveum aclmod / -user kubernetes-csi@pve -role CSI
pveum user token add kubernetes-csi@pve csi -privsep 0
```

## Helm values example

```yaml
# proxmox-csi.yaml

config:
  clusters:
    - url: https://cluster-api-1.exmple.com:8006/api2/json
      insecure: false
      token_id: "kubernetes-csi@pve!csi"
      token_secret: "key"
      region: cluster-1

# Deploy Node CSI driver only on proxmox nodes
node:
  nodeSelector:
    # It will work only with Talos CCM, remove it overwise
    node.cloudprovider.kubernetes.io/platform: nocloud
  tolerations:
    - operator: Exists

# Deploy CSI controller only on control-plane nodes
nodeSelector:
  node-role.kubernetes.io/control-plane: ""
tolerations:
  - key: node-role.kubernetes.io/control-plane
    effect: NoSchedule

# Define storage classes
# See https://pve.proxmox.com/wiki/Storage
storageClass:
  - name: proxmox-data-xfs
    storage: data
    reclaimPolicy: Delete
    fstype: xfs
  - name: proxmox-data
    storage: data
    reclaimPolicy: Delete
    fstype: ext4
    cache: writethrough
```

## Deploy

```shell
# Prepare namespace
kubectl create ns csi-proxmox
kubectl label ns csi-proxmox pod-security.kubernetes.io/enforce=privileged
# Install Proxmox CSI plugin
helm upgrade -i --namespace=csi-proxmox -f proxmox-csi.yaml \
    proxmox-csi-plugin oci://ghcr.io/sergelogvinov/charts/proxmox-csi-plugin
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| replicaCount | int | `1` |  |
| imagePullSecrets | list | `[]` |  |
| nameOverride | string | `""` |  |
| fullnameOverride | string | `""` |  |
| createNamespace | bool | `false` | Create namespace. Very useful when using helm template. |
| priorityClassName | string | `"system-cluster-critical"` | Controller pods priorityClassName. |
| serviceAccount | object | `{"annotations":{},"create":true,"name":""}` | Pods Service Account. ref: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/ |
| provisionerName | string | `"csi.proxmox.sinextra.dev"` | CSI Driver provisioner name. Currently, cannot be customized. |
| clusterID | string | `"kubernetes"` | Cluster name. Currently, cannot be customized. |
| logVerbosityLevel | int | `5` | Log verbosity level. See https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md for description of individual verbosity levels. |
| timeout | string | `"3m"` | Connection timeout between sidecars. |
| options.enableCapacity | bool | `true` | Enable or disable capacity feature. ref: https://github.com/kubernetes-csi/external-provisioner |
| existingConfigSecret | string | `nil` | Proxmox cluster config stored in secrets. |
| existingConfigSecretKey | string | `"config.yaml"` | Proxmox cluster config stored in secrets key. |
| configFile | string | `"/etc/proxmox/config.yaml"` | Proxmox cluster config path. |
| config | object | `{"clusters":[],"features":{"provider":"default"}}` | Proxmox cluster config. ref: https://github.com/sergelogvinov/proxmox-csi-plugin/blob/main/docs/install.md |
| storageClass | list | `[]` | Storage class definition. |
| controller.podAnnotations | object | `{}` | Annotations for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/ |
| controller.podLabels | object | `{}` | Labels for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/ |
| controller.plugin.image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/sergelogvinov/proxmox-csi-controller","tag":""}` | Controller CSI Driver. |
| controller.plugin.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Controller resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.attacher.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-attacher","tag":"v4.12.0"}` | CSI Attacher. ref: https://github.com/kubernetes-csi/external-attacher |
| controller.attacher.args | list | `["--default-fstype=ext4"]` | Attacher arguments. example: --default-fstype=ext4 |
| controller.attacher.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Attacher resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.provisioner.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-provisioner","tag":"v6.3.0"}` | CSI Provisioner. ref: https://github.com/kubernetes-csi/external-provisioner |
| controller.provisioner.args | list | `["--default-fstype=ext4"]` | Provisioner arguments. example: --feature-gates=VolumeAttributesClass=true |
| controller.provisioner.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Provisioner resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.resizer.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-resizer","tag":"v2.2.1"}` | CSI Resizer. refs: https://github.com/kubernetes-csi/external-resizer |
| controller.resizer.args | list | `[]` | Resizer arguments. example: --feature-gates=VolumeAttributesClass=true |
| controller.resizer.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Resizer resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.snapshotter.enabled | bool | `false` |  |
| controller.snapshotter.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-snapshotter","tag":"v8.6.0"}` | CSI Snapshotter. refs: https://github.com/kubernetes-csi/external-snapshotter |
| controller.snapshotter.args | list | `[]` | Snapshotter arguments. example: --feature-gates=CSIVolumeGroupSnapshot=true |
| controller.snapshotter.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Snapshotter resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| node.plugin.image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/sergelogvinov/proxmox-csi-node","tag":""}` | Node CSI Driver. |
| node.plugin.resources | object | `{}` | Node CSI Driver resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| node.driverRegistrar.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-node-driver-registrar","tag":"v2.17.0"}` | Node CSI driver registrar. ref: https://github.com/kubernetes-csi/node-driver-registrar |
| node.driverRegistrar.args | list | `[]` | Driver registrar arguments. example: --timeout=60s |
| node.driverRegistrar.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Node registrar resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| node.kubeletDir | string | `"/var/lib/kubelet"` | Location of the /var/lib/kubelet directory as some k8s distribution differ from the standard. Standard: /var/lib/kubelet, k0s: /var/lib/k0s/kubelet, microk8s: /var/snap/microk8s/common/var/lib/kubelet |
| node.nodeSelector | object | `{}` | Node labels for node-plugin assignment. ref: https://kubernetes.io/docs/user-guide/node-selection/ |
| node.tolerations | list | `[{"effect":"NoSchedule","key":"karpenter.sh/disrupted","operator":"Exists"},{"effect":"NoSchedule","key":"node.kubernetes.io/unschedulable","operator":"Exists"},{"effect":"NoSchedule","key":"node.kubernetes.io/disk-pressure","operator":"Exists"}]` | Tolerations for node-plugin assignment. ref: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/ |
| node.affinity | object | `{}` | Affinity for node-plugin assignment. ref: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#affinity-and-anti-affinity |
| livenessprobe.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/livenessprobe","tag":"v2.19.0"}` | Common livenessprobe sidecar. |
| livenessprobe.failureThreshold | int | `5` | Failure threshold for livenessProbe |
| livenessprobe.initialDelaySeconds | int | `10` | Initial delay seconds for livenessProbe |
| livenessprobe.timeoutSeconds | int | `10` | Timeout seconds for livenessProbe |
| livenessprobe.periodSeconds | int | `60` | Period seconds for livenessProbe |
| livenessprobe.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Liveness probe resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| initContainers | list | `[]` | Add additional init containers for the CSI controller pods. ref: https://kubernetes.io/docs/concepts/workloads/pods/init-containers/ |
| hostAliases | list | `[]` | hostAliases Deployment pod host aliases ref: https://kubernetes.io/docs/tasks/network/customize-hosts-file-for-pods/ |
| podAnnotations | object | `{}` | Annotations for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/ |
| podLabels | object | `{}` | Labels for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/ |
| podSecurityContext | object | `{"fsGroup":65532,"fsGroupChangePolicy":"OnRootMismatch","runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532}` | Controller Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"seccompProfile":{"type":"RuntimeDefault"}}` | Controller Container Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| updateStrategy | object | `{"rollingUpdate":{"maxUnavailable":1},"type":"RollingUpdate"}` | Controller deployment update strategy type. ref: https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#updating-a-deployment |
| metrics | object | `{"enabled":false,"port":8080,"type":"annotation"}` | Prometheus metrics |
| metrics.enabled | bool | `false` | Enable Prometheus metrics. |
| metrics.port | int | `8080` | Prometheus metrics port. |
| nodeSelector | object | `{}` | Node labels for controller assignment. ref: https://kubernetes.io/docs/user-guide/node-selection/ |
| tolerations | list | `[]` | Tolerations for controller assignment. ref: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/ |
| affinity | object | `{}` | Affinity for controller assignment. ref: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#affinity-and-anti-affinity |
| extraVolumes | list | `[]` | Additional volumes for Pods |
| extraVolumeMounts | list | `[]` |  |
