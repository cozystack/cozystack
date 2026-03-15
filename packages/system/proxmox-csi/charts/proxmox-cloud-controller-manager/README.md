# proxmox-cloud-controller-manager

![Version: 0.2.8](https://img.shields.io/badge/Version-0.2.8-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.5.1](https://img.shields.io/badge/AppVersion-v0.5.1-informational?style=flat-square)

Cloud Controller Manager plugin for Proxmox

The Cloud Controller Manager (CCM) is responsible for managing node resources in cloud-based Kubernetes environments.

Key functions of the Cloud Controller Manager:
- `Node Management`: It manages nodes by initializing new nodes when they join the cluster (e.g., during scaling up) and removing nodes when they are no longer needed (e.g., during scaling down).
- `Cloud-Specific Operations`: The CCM ensures that the cloud provider's API is integrated into the Kubernetes cluster to control and automate tasks like load balancing, storage provisioning, and node lifecycle management.

**Homepage:** <https://github.com/sergelogvinov/proxmox-cloud-controller-manager>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| sergelogvinov |  | <https://github.com/sergelogvinov> |

## Source Code

* <https://github.com/sergelogvinov/proxmox-cloud-controller-manager>

## Requirements

You need to set `--cloud-provider=external` in the kubelet argument for all nodes in the cluster.

## Proxmox permissions

```shell
# Create role CCM
pveum role add CCM -privs "VM.Audit"
# Create user and grant permissions
pveum user add kubernetes@pve
pveum aclmod / -user kubernetes@pve -role CCM
pveum user token add kubernetes@pve ccm -privsep 0
```

## Helm values example

```yaml
# proxmox-ccm.yaml

config:
  clusters:
    - url: https://cluster-api-1.exmple.com:8006/api2/json
      insecure: false
      token_id: "kubernetes@pve!csi"
      token_secret: "key"
      region: cluster-1

enabledControllers:
  # Remove `cloud-node` if you use it with Talos CCM
  - cloud-node
  - cloud-node-lifecycle

# Deploy CCM only on control-plane nodes
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
      - matchExpressions:
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
tolerations:
  - key: node-role.kubernetes.io/control-plane
    effect: NoSchedule
```

Deploy chart:

```shell
helm upgrade -i --namespace=kube-system -f proxmox-ccm.yaml \
    proxmox-cloud-controller-manager oci://ghcr.io/sergelogvinov/charts/proxmox-cloud-controller-manager
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| replicaCount | int | `1` |  |
| image.repository | string | `"ghcr.io/sergelogvinov/proxmox-cloud-controller-manager"` | Proxmox CCM image. |
| image.pullPolicy | string | `"IfNotPresent"` | Always or IfNotPresent |
| image.tag | string | `""` | Overrides the image tag whose default is the chart appVersion. |
| imagePullSecrets | list | `[]` |  |
| nameOverride | string | `""` |  |
| fullnameOverride | string | `""` |  |
| extraArgs | list | `[]` | Any extra arguments for talos-cloud-controller-manager |
| enabledControllers | list | `["cloud-node","cloud-node-lifecycle"]` | List of controllers should be enabled. Use '*' to enable all controllers. Support only `cloud-node,cloud-node-lifecycle` controllers. |
| logVerbosityLevel | int | `2` | Log verbosity level. See https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md for description of individual verbosity levels. |
| existingConfigSecret | string | `nil` | Proxmox cluster config stored in secrets. |
| existingConfigSecretKey | string | `"config.yaml"` | Proxmox cluster config stored in secrets key. |
| config | object | `{"clusters":[]}` | Proxmox cluster config. |
| serviceAccount | object | `{"annotations":{},"create":true,"name":""}` | Pods Service Account. ref: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/ |
| priorityClassName | string | `"system-cluster-critical"` | CCM pods' priorityClassName. |
| initContainers | list | `[]` | Add additional init containers to the CCM pods. ref: https://kubernetes.io/docs/concepts/workloads/pods/init-containers/ |
| hostAliases | list | `[]` | hostAliases Deployment pod host aliases ref: https://kubernetes.io/docs/tasks/network/customize-hosts-file-for-pods/ |
| podAnnotations | object | `{}` | Annotations for data pods. ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/ |
| podSecurityContext | object | `{"fsGroup":10258,"fsGroupChangePolicy":"OnRootMismatch","runAsGroup":10258,"runAsNonRoot":true,"runAsUser":10258}` | Pods Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"seccompProfile":{"type":"RuntimeDefault"}}` | Container Security Context. ref: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-pod |
| resources | object | `{"requests":{"cpu":"10m","memory":"32Mi"}}` | Resource requests and limits. ref: https://kubernetes.io/docs/user-guide/compute-resources/ |
| useDaemonSet | bool | `false` | Deploy CCM  in Daemonset mode. CCM will use hostNetwork. It allows to use CCM without CNI plugins. |
| updateStrategy | object | `{"rollingUpdate":{"maxUnavailable":1},"type":"RollingUpdate"}` | Deployment update strategy type. ref: https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#updating-a-deployment |
| nodeSelector | object | `{}` | Node labels for data pods assignment. ref: https://kubernetes.io/docs/user-guide/node-selection/ |
| tolerations | list | `[{"effect":"NoSchedule","key":"node-role.kubernetes.io/control-plane","operator":"Exists"},{"effect":"NoSchedule","key":"node.cloudprovider.kubernetes.io/uninitialized","operator":"Exists"}]` | Tolerations for data pods assignment. ref: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/ |
| affinity | object | `{}` | Affinity for data pods assignment. ref: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/#affinity-and-anti-affinity |
| extraVolumes | list | `[]` | Additional volumes for Pods |
| extraVolumeMounts | list | `[]` | Additional volume mounts for Pods |
