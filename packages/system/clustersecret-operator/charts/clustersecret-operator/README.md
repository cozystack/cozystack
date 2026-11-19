# clustersecret-operator

![Version: 0.3.68](https://img.shields.io/badge/Version-0.3.68-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.3.68](https://img.shields.io/badge/AppVersion-v0.3.68-informational?style=flat-square)

A Helm chart for https://github.com/sap/clustersecret-operator

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| fullnameOverride | string | `""` | Override full name |
| nameOverride | string | `""` | Override name |
| global.image.tag | string | `""` | Image tag (defauls to .Chart.AppVersion) |
| global.image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| global.imagePullSecrets | list | `[]` | Image pull secrets |
| global.affinity | object | `{}` | Affinity settings |
| global.nodeSelector | object | `{}` | Node selector |
| global.topologySpreadConstraints | list | `[]` | Topology spread constraints (if unspecified, default constraints for hostname and zone will be generated) |
| global.defaultHostNameSpreadPolicy | string | `"ScheduleAnyway"` | Default topology spread policy for hostname |
| global.defaultZoneSpreadPolicy | string | `"ScheduleAnyway"` | Default topology spread policy for zone |
| global.tolerations | list | `[]` | Tolerations |
| global.priorityClassName | string | `""` | Priority class |
| global.podSecurityContext | object | `{}` | Pod security context |
| global.podAnnotations | object | `{}` | Additional pod annotations |
| global.podLabels | object | `{}` | Additional pod labels |
| global.securityContext | object | `{}` | Container security context |
| global.logLevel | int | `0` | Log level |
| controller.replicaCount | int | `1` | Replica count |
| controller.image.repository | string | `"ghcr.io/sap/clustersecret-operator/controller"` | Image repository |
| controller.image.tag | string | `""` | Image tag (defauls to .Chart.AppVersion) |
| controller.image.pullPolicy | string | `""` | Image pull policy |
| controller.imagePullSecrets | list | `[]` | Image pull secrets |
| controller.affinity | object | `{}` | Affinity settings |
| controller.nodeSelector | object | `{}` | Node selector |
| controller.topologySpreadConstraints | list | `[]` | Topology spread constraints (if unspecified, default constraints for hostname and zone will be generated) |
| controller.defaultHostNameSpreadPolicy | string | `""` | Default topology spread policy for hostname |
| controller.defaultZoneSpreadPolicy | string | `""` | Default topology spread policy for zone |
| controller.tolerations | list | `[]` | Tolerations |
| controller.priorityClassName | string | `""` | Priority class |
| controller.podSecurityContext | object | `{}` | Pod security context |
| controller.podAnnotations | object | `{}` | Additional pod annotations |
| controller.podLabels | object | `{}` | Additional pod labels |
| controller.securityContext | object | `{}` | Container security context |
| controller.resources.limits.memory | string | `"200Mi"` | Memory limit |
| controller.resources.limits.cpu | float | `0.1` | CPU limit |
| controller.resources.requests.memory | string | `"20Mi"` | Memory request |
| controller.resources.requests.cpu | float | `0.01` | CPU request |
| controller.logLevel | int | `0` | Log level |
| webhook.replicaCount | int | `1` | Replica count |
| webhook.image.repository | string | `"ghcr.io/sap/clustersecret-operator/webhook"` | Image repository |
| webhook.image.tag | string | `""` | Image tag (defauls to .Chart.AppVersion) |
| webhook.image.pullPolicy | string | `""` | Image pull policy |
| webhook.imagePullSecrets | list | `[]` | Image pull secrets |
| webhook.affinity | object | `{}` | Affinity settings |
| webhook.nodeSelector | object | `{}` | Node selector |
| webhook.topologySpreadConstraints | list | `[]` | Topology spread constraints (if unspecified, default constraints for hostname and zone will be generated) |
| webhook.defaultHostNameSpreadPolicy | string | `""` | Default topology spread policy for hostname |
| webhook.defaultZoneSpreadPolicy | string | `""` | Default topology spread policy for zone |
| webhook.tolerations | list | `[]` | Tolerations |
| webhook.priorityClassName | string | `""` | Priority class |
| webhook.podSecurityContext | object | `{}` | Pod security context |
| webhook.podAnnotations | object | `{}` | Additional pod annotations |
| webhook.podLabels | object | `{}` | Additional pod labels |
| webhook.securityContext | object | `{}` | Container security context |
| webhook.resources.limits.memory | string | `"100Mi"` | Memory limit |
| webhook.resources.limits.cpu | float | `0.1` | CPU limit |
| webhook.resources.requests.memory | string | `"20Mi"` | Memory request |
| webhook.resources.requests.cpu | float | `0.01` | CPU request |
| webhook.service.type | string | `"ClusterIP"` | Service type |
| webhook.service.port | int | `443` | Service port |
| webhook.logLevel | int | `0` | Log level |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.11.0](https://github.com/norwoodj/helm-docs/releases/v1.11.0)
