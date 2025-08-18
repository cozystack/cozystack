# flux-operator

![Version: 0.28.0](https://img.shields.io/badge/Version-0.28.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.28.0](https://img.shields.io/badge/AppVersion-v0.28.0-informational?style=flat-square)

The [Flux Operator](https://github.com/controlplaneio-fluxcd/flux-operator) provides a
declarative API for the installation and upgrade of CNCF [Flux](https://fluxcd.io) and the
ControlPlane [enterprise distribution](https://control-plane.io/enterprise-for-flux-cd/).

The operator automates the patching for hotfixes and CVEs affecting the Flux controllers container images
and enables the configuration of multi-tenancy lockdown on Kubernetes and OpenShift clusters.

## Prerequisites

- Kubernetes 1.22+
- Helm 3.8+

## Installing the Chart

To install the operator in the `flux-system` namespace:

```console
helm install flux-operator oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator \
  --namespace flux-system \
  --create-namespace \
  --wait
```

To deploy the Flux controllers and to configure automated updates,
see the Flux Operator [documentation](https://fluxcd.control-plane.io/operator/).

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"kubernetes.io/os","operator":"In","values":["linux"]}]}]}}}` | Pod affinity and anti-affinity settings. |
| apiPriority | object | `{"enabled":false,"extraServiceAccounts":[],"level":"workload-high"}` | Kubernetes [API priority and fairness](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/) settings. |
| commonAnnotations | object | `{}` | Common annotations to add to all deployed objects including pods. |
| commonLabels | object | `{}` | Common labels to add to all deployed objects including pods. |
| extraArgs | list | `[]` | Container extra arguments. |
| extraEnvs | list | `[]` | Container extra environment variables. |
| extraVolumeMounts | list | `[]` | Container extra volume mounts. |
| extraVolumes | list | `[]` | Pod extra volumes. |
| fullnameOverride | string | `""` |  |
| hostNetwork | bool | `false` | If `true`, the container ports (`8080` and `8081`) are exposed on the host network. |
| image | object | `{"imagePullPolicy":"IfNotPresent","pullSecrets":[],"repository":"ghcr.io/controlplaneio-fluxcd/flux-operator","tag":""}` | Container image settings. The image tag defaults to the chart appVersion. |
| installCRDs | bool | `true` | Install and upgrade the custom resource definitions. |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":8081},"initialDelaySeconds":15,"periodSeconds":20}` | Container liveness probe settings. |
| logLevel | string | `"info"` | Container logging level flag. |
| marketplace | object | `{"account":"","license":"","type":""}` | Marketplace settings. |
| multitenancy | object | `{"defaultServiceAccount":"flux-operator","enabled":false}` | Enable [multitenancy lockdown](https://fluxcd.control-plane.io/operator/resourceset/#role-based-access-control) for the ResourceSet APIs. |
| nameOverride | string | `""` |  |
| nodeSelector | object | `{}` | Pod Node Selector settings. |
| podSecurityContext | object | `{}` | Pod security context settings. |
| priorityClassName | string | `""` | Pod priority class name. Recommended value is system-cluster-critical. |
| rbac.create | bool | `true` | Grant the cluster-admin role to the flux-operator service account (required for the Flux Instance deployment). |
| rbac.createAggregation | bool | `true` | Grant the Kubernetes view, edit and admin roles access to ResourceSet APIs. |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":8081},"initialDelaySeconds":5,"periodSeconds":10}` | Container readiness probe settings. |
| reporting | object | `{"interval":"5m"}` | Flux [reporting](https://fluxcd.control-plane.io/operator/fluxreport/) settings. |
| resources | object | `{"limits":{"cpu":"2000m","memory":"1Gi"},"requests":{"cpu":"100m","memory":"64Mi"}}` | Container resources requests and limits settings. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}` | Container security context settings. The default is compliant with the pod security restricted profile. |
| serviceAccount | object | `{"automount":true,"create":true,"name":""}` | Pod service account settings. The name of the service account defaults to the release name. |
| serviceMonitor | object | `{"create":false,"interval":"60s","labels":{},"scrapeTimeout":"30s"}` | Prometheus Operator scraping settings. |
| tolerations | list | `[]` | Pod tolerations settings. |

## Source Code

* <https://github.com/controlplaneio-fluxcd/flux-operator>
* <https://github.com/controlplaneio-fluxcd/charts>
