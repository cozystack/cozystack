# proxmox-csi-plugin

![Version: 0.1.6](https://img.shields.io/badge/Version-0.1.6-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.3.0](https://img.shields.io/badge/AppVersion-v0.3.0-informational?style=flat-square)

A CSI plugin for Proxmox

**Homepage:** <https://github.com/sergelogvinov/proxmox-csi-plugin>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| sergelogvinov |  | <https://github.com/sergelogvinov> |

## Source Code

* <https://github.com/sergelogvinov/proxmox-csi-plugin>

Example:

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

Deploy chart:

```shell
helm upgrade -i --namespace=csi-proxmox -f proxmox-csi.yaml \
		proxmox-csi-plugin charts/proxmox-csi-plugin/
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| replicaCount | int | `1` |  |
| imagePullSecrets | list | `[]` |  |
| nameOverride | string | `""` |  |
| fullnameOverride | string | `""` |  |
| priorityClassName | string | `"system-cluster-critical"` | Controller pods priorityClassName. |
| serviceAccount | object | `{"annotations":{},"create":true,"name":""}` | Pods Service Account. ref: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/ |
| provisionerName | string | `"csi.proxmox.sinextra.dev"` | CSI Driver provisioner name. Currently, cannot be customized. |
| clusterID | string | `"kubernetes"` | Cluster name. Currently, cannot be customized. |
| logVerbosityLevel | int | `5` | Log verbosity level. See https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md for description of individual verbosity levels. |
| timeout | string | `"3m"` | Connection timeout between sidecars. |
| existingConfigSecret | string | `nil` | Proxmox cluster config stored in secrets. |
| existingConfigSecretKey | string | `"config.yaml"` | Proxmox cluster config stored in secrets key. |
| configFile | string | `"/etc/proxmox/config.yaml"` | Proxmox cluster config path. |
| config | object | `{"clusters":[]}` | Proxmox cluster config. |
| storageClass | list | `[]` | Storage class defenition. |
| controller.plugin.image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/sergelogvinov/proxmox-csi-controller","tag":""}` | Controller CSI Driver. |
| controller.plugin.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Controller resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.attacher.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-attacher","tag":"v4.3.0"}` | CSI Attacher. |
| controller.attacher.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Attacher resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.provisioner.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-provisioner","tag":"v3.5.0"}` | CSI Provisioner. |
| controller.provisioner.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Provisioner resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| controller.resizer.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-resizer","tag":"v1.8.0"}` | CSI Resizer. |
| controller.resizer.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Resizer resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| node.plugin.image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/sergelogvinov/proxmox-csi-node","tag":""}` | Node CSI Driver. |
| node.plugin.resources | object | `{}` | Node CSI Driver resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| node.driverRegistrar.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/csi-node-driver-registrar","tag":"v2.8.0"}` | Node CSI driver registrar. |
| node.driverRegistrar.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Node registrar resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| node.nodeSelector | object | `{}` | Node labels for node-plugin assignment. ref: https://kubernetes.io/docs/user-guide/node-selection/ |
| node.tolerations | list | `[{"effect":"NoSchedule","key":"node.kubernetes.io/unschedulable","operator":"Exists"},{"effect":"NoSchedule","key":"node.kubernetes.io/disk-pressure","operator":"Exists"}]` | Tolerations for node-plugin assignment. ref: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/ |
| livenessprobe.image | object | `{"pullPolicy":"IfNotPresent","repository":"registry.k8s.io/sig-storage/livenessprobe","tag":"v2.10.0"}` | Common livenessprobe sidecar. |
| livenessprobe.failureThreshold | int | `5` | Failure threshold for livenessProbe |
| livenessprobe.initialDelaySeconds | int | `10` | Initial delay seconds for livenessProbe |
| livenessprobe.timeoutSeconds | int | `10` | Timeout seconds for livenessProbe |
| livenessprobe.periodSeconds | int | `60` | Period seconds for livenessProbe |
| livenessprobe.resources | object | `{"requests":{"cpu":"10m","memory":"16Mi"}}` | Liveness probe resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| podAnnotations | object | `{}` | Annotations for controller pod. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/ |
| podSecurityContext | object | `{"fsGroup":65532,"fsGroupChangePolicy":"OnRootMismatch","runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532}` | Controller Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"seccompProfile":{"type":"RuntimeDefault"}}` | Controller Container Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| updateStrategy | object | `{"rollingUpdate":{"maxUnavailable":1},"type":"RollingUpdate"}` | Controller deployment update stategy type. ref: https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#updating-a-deployment |
| nodeSelector | object | `{}` | Node labels for controller assignment. ref: https://kubernetes.io/docs/user-guide/node-selection/ |
| tolerations | list | `[]` | Tolerations for controller assignment. ref: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/ |
| affinity | object | `{}` | Affinity for controller assignment. ref: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#affinity-and-anti-affinity |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.11.0](https://github.com/norwoodj/helm-docs/releases/v1.11.0)
