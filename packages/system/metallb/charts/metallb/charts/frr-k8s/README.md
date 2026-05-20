# frr-k8s

![Version: 0.0.25](https://img.shields.io/badge/Version-0.0.25-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.0.25](https://img.shields.io/badge/AppVersion-v0.0.25-informational?style=flat-square)

A cloud native wrapper of FRR

**Homepage:** <https://metallb.universe.tf>

## Source Code

* <https://github.com/metallb/frr-k8s>

## Requirements

Kubernetes: `>= 1.19.0-0`

| Repository | Name | Version |
|------------|------|---------|
|  | crds | 0.0.25 |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| crds.enabled | bool | `true` | Enable installation of CRDs. |
| crds.validationFailurePolicy | string | `"Fail"` | Validation failure policy for CRDs. Can be Fail or Ignore. |
| frrk8s.affinity | object | `{}` | Affinity for pod assignment. |
| frrk8s.alwaysBlock | string | `""` | A comma separated list of cidrs to always block for incoming routes. |
| frrk8s.bgpDebounceTimeout | integer | `nil` | BGP debounce timeout for FRR configuration reloads, in milliseconds. Default (when unset) is 3000 ms.This feature is experimental |
| frrk8s.disableCertRotation | bool | `false` | Specifies whether the cert rotator works as part of the webhook. |
| frrk8s.frr.acceptIncomingBGPConnections | bool | `false` | Allow FRR to accept incoming BGP connections. |
| frrk8s.frr.image.pullPolicy | string | `nil` | The FRR image pull policy. |
| frrk8s.frr.image.repository | string | `"quay.io/frrouting/frr"` | The FRR image repository. |
| frrk8s.frr.image.tag | string | `"10.4.3"` | The FRR image tag. |
| frrk8s.frr.metricsBindAddress | string | `"127.0.0.1"` | Bind address for FRR metrics. |
| frrk8s.frr.metricsPort | int | `7573` | Port for FRR metrics. |
| frrk8s.frr.resources | object | `{}` | Resource limits and requests for the FRR container. |
| frrk8s.frr.secureMetricsPort | int | `9141` | Secure metrics port for FRR. |
| frrk8s.frrMetrics.resources | object | `{}` | Resource limits and requests for the FRR metrics container. |
| frrk8s.frrStatus.pollInterval | string | `"2m"` | Polling interval for FRR status updates. |
| frrk8s.frrStatus.resources | object | `{}` | Resource limits and requests for the FRR status container. |
| frrk8s.image.pullPolicy | string | `nil` | The frr-k8s image pull policy. |
| frrk8s.image.repository | string | `"quay.io/metallb/frr-k8s"` | The frr-k8s image repository. |
| frrk8s.image.tag | string | `nil` | The frr-k8s image tag. If not set, defaults to the chart appVersion. |
| frrk8s.labels | object | `{"app":"frr-k8s"}` | Additional labels to add to the pod. |
| frrk8s.livenessProbe.enabled | bool | `true` | Enable liveness probe. |
| frrk8s.livenessProbe.failureThreshold | int | `3` | Number of failures before the probe is considered failed. |
| frrk8s.livenessProbe.initialDelaySeconds | int | `10` | Number of seconds after the container has started before liveness probes are initiated. |
| frrk8s.livenessProbe.periodSeconds | int | `10` | How often (in seconds) to perform the probe. |
| frrk8s.livenessProbe.successThreshold | int | `1` | Minimum consecutive successes for the probe to be considered successful. |
| frrk8s.livenessProbe.timeoutSeconds | int | `1` | Number of seconds after which the probe times out. |
| frrk8s.logLevel | string | `"info"` | Controller log level that is passed as a CLI flag. Must be one of: `all`, `debug`, `info`, `warn`, `error` or `none` |
| frrk8s.nodeSelector | object | `{}` | Node selector for pod assignment. |
| frrk8s.podAnnotations | object | `{}` | Additional annotations to add to the pod. |
| frrk8s.priorityClassName | string | `""` | Priority class name for the pod. |
| frrk8s.readinessProbe.enabled | bool | `true` | Enable readiness probe. |
| frrk8s.readinessProbe.failureThreshold | int | `3` | Number of failures before the probe is considered failed. |
| frrk8s.readinessProbe.initialDelaySeconds | int | `10` | Number of seconds after the container has started before readiness probes are initiated. |
| frrk8s.readinessProbe.periodSeconds | int | `10` | How often (in seconds) to perform the probe. |
| frrk8s.readinessProbe.successThreshold | int | `1` | Minimum consecutive successes for the probe to be considered successful. |
| frrk8s.readinessProbe.timeoutSeconds | int | `1` | Number of seconds after which the probe times out. |
| frrk8s.reloader.resources | object | `{}` | Resource limits and requests for the reloader container. |
| frrk8s.resources | object | `{}` | Resource limits and requests for the frr-k8s controller container. |
| frrk8s.restartOnRotatorSecretRefresh | bool | `false` | Specifies whether the pod restarts when the rotator refreshes the cert secret. Useful for webhook stability during redeployments. |
| frrk8s.runtimeClassName | string | `""` | Runtime class name for the pod. |
| frrk8s.serviceAccount.annotations | object | `{}` | Additional annotations to add to the ServiceAccount. |
| frrk8s.serviceAccount.create | bool | `true` | Specifies whether a ServiceAccount should be created. |
| frrk8s.serviceAccount.name | string | `""` | The name of the ServiceAccount to use. If not set and create is true, a name is generated using the fullname template. |
| frrk8s.startupProbe.enabled | bool | `true` | Enable startup probe. |
| frrk8s.startupProbe.failureThreshold | int | `30` | Number of failures before the probe is considered failed. |
| frrk8s.startupProbe.periodSeconds | int | `5` | How often (in seconds) to perform the probe. |
| frrk8s.tolerateMaster | bool | `true` | Tolerate master nodes for pod scheduling. |
| frrk8s.tolerations | list | `[]` | Tolerations for pod assignment. |
| frrk8s.updateStrategy.type | string | `"RollingUpdate"` | Specify the FRR-K8s daemonset update strategy. |
| frrk8s.webhookPort | int | `19443` | Port for the webhook server. |
| fullnameOverride | string | `""` | String to override the default fully qualified app name. |
| nameOverride | string | `""` | String to override the default chart name. |
| prometheus.namespace | string | `""` | The namespace where Prometheus is deployed. Required when ".Values.prometheus.rbacPrometheus == true" and "prometheus.serviceMonitor.enabled=true". |
| prometheus.rbacPrometheus | bool | `false` | Give Prometheus permission to scrape metallb's namespace. |
| prometheus.scrapeAnnotations | bool | `false` | Add Prometheus metric auto-collection annotations to pods. |
| prometheus.secureMetricsPort | int | `9140` | Port frr-k8s will listen on for secure metrics. |
| prometheus.serviceAccount | string | `""` | The service account used by Prometheus. Required when ".Values.prometheus.rbacPrometheus == true" and "prometheus.serviceMonitor.enabled=true" |
| prometheus.serviceMonitor.additionalLabels | object | `{}` | Additional labels to add to the ServiceMonitor. |
| prometheus.serviceMonitor.annotations | object | `{}` | Optional additional annotations for the controller serviceMonitor. |
| prometheus.serviceMonitor.enabled | bool | `false` | Enable support for Prometheus Operator. |
| prometheus.serviceMonitor.interval | string | `nil` | Scrape interval. If not set, the Prometheus default scrape interval is used. |
| prometheus.serviceMonitor.jobLabel | string | `"app.kubernetes.io/name"` | Job label for scrape target. |
| prometheus.serviceMonitor.metricRelabelings | list | `[]` | Metric relabel configs to apply to samples before ingestion. |
| prometheus.serviceMonitor.relabelings | list | `[]` | Relabel configs to apply to samples before ingestion. |
| prometheus.serviceMonitor.tlsConfig.insecureSkipVerify | bool | `true` | Disables SSL certificate verification |
| rbac.create | bool | `true` | Specifies whether to install and use RBAC rules. |
| tls.cipherSuites | string | `""` | Comma-separated list of TLS cipher suites. If empty, uses Go defaults. Only applies to TLS 1.2. |
| tls.curvePreferences | string | `""` | Comma-separated list of numeric CurveID values (e.g. 29,4588). See https://pkg.go.dev/crypto/tls#CurveID. If empty, uses Go defaults. |
| tls.metricsTLSSecret | string | `""` | The name of the secret to be mounted in the pods to provide TLS certificates for metrics endpoints. If not present, a self-signed certificate is auto-generated. |
| tls.minVersion | string | `""` | Minimum TLS version (VersionTLS12 or VersionTLS13). Defaults to VersionTLS13. |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.10.0](https://github.com/norwoodj/helm-docs/releases/v1.10.0)
