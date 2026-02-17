# openbao

![Version: 0.25.3](https://img.shields.io/badge/Version-0.25.3-informational?style=flat-square) ![AppVersion: v2.5.0](https://img.shields.io/badge/AppVersion-v2.5.0-informational?style=flat-square)

Official OpenBao Chart

**Homepage:** <https://github.com/openbao/openbao-helm>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| OpenBao | <openbao-security@lists.openssf.org> | <https://openbao.org> |

## Source Code

* <https://github.com/openbao/openbao-helm>

## Requirements

Kubernetes: `>= 1.30.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| csi.agent.enabled | bool | `true` |  |
| csi.agent.extraArgs | list | `[]` |  |
| csi.agent.image.pullPolicy | string | `"IfNotPresent"` | image pull policy to use for agent image. if tag is "latest", set to "Always" |
| csi.agent.image.registry | string | `"quay.io"` | image registry to use for agent image |
| csi.agent.image.repository | string | `"openbao/openbao"` | image repo to use for agent image |
| csi.agent.image.tag | string | `""` | image tag to use for agent image - defaults to chart appVersion |
| csi.agent.logFormat | string | `"standard"` |  |
| csi.agent.logLevel | string | `"info"` |  |
| csi.agent.resources | object | `{}` |  |
| csi.daemonSet.annotations | object | `{}` |  |
| csi.daemonSet.endpoint | string | `"/provider/openbao.sock"` |  |
| csi.daemonSet.extraLabels | object | `{}` |  |
| csi.daemonSet.kubeletRootDir | string | `"/var/lib/kubelet"` |  |
| csi.daemonSet.providersDir | string | `"/etc/kubernetes/secrets-store-csi-providers"` |  |
| csi.daemonSet.securityContext.container | object | `{}` |  |
| csi.daemonSet.securityContext.pod | object | `{}` |  |
| csi.daemonSet.updateStrategy.maxUnavailable | string | `""` |  |
| csi.daemonSet.updateStrategy.type | string | `"RollingUpdate"` |  |
| csi.debug | bool | `false` |  |
| csi.enabled | bool | `false` | True if you want to install a openbao-csi-provider daemonset.  Requires installing the secrets-store-csi-driver separately, see: https://secrets-store-csi-driver.sigs.k8s.io/getting-started/installation  With the driver and provider installed, you can mount OpenBao secrets into volumes similar to the OpenBao Agent injector, and you can also sync those secrets into Kubernetes secrets. |
| csi.extraArgs | list | `[]` |  |
| csi.hmacSecretName | string | `""` |  |
| csi.image.pullPolicy | string | `"IfNotPresent"` | image pull policy to use for csi image. if tag is "latest", set to "Always" |
| csi.image.registry | string | `"quay.io"` | image registry to use for csi image |
| csi.image.repository | string | `"openbao/openbao-csi-provider"` | image repo to use for csi image |
| csi.image.tag | string | `"2.0.0"` | image tag to use for csi image |
| csi.livenessProbe.failureThreshold | int | `2` |  |
| csi.livenessProbe.initialDelaySeconds | int | `5` |  |
| csi.livenessProbe.periodSeconds | int | `5` |  |
| csi.livenessProbe.successThreshold | int | `1` |  |
| csi.livenessProbe.timeoutSeconds | int | `3` |  |
| csi.pod.affinity | object | `{}` |  |
| csi.pod.annotations | object | `{}` |  |
| csi.pod.extraLabels | object | `{}` |  |
| csi.pod.nodeSelector | object | `{}` |  |
| csi.pod.tolerations | list | `[]` |  |
| csi.priorityClassName | string | `""` |  |
| csi.readinessProbe.failureThreshold | int | `2` |  |
| csi.readinessProbe.initialDelaySeconds | int | `5` |  |
| csi.readinessProbe.periodSeconds | int | `5` |  |
| csi.readinessProbe.successThreshold | int | `1` |  |
| csi.readinessProbe.timeoutSeconds | int | `3` |  |
| csi.resources | object | `{}` |  |
| csi.serviceAccount.annotations | object | `{}` |  |
| csi.serviceAccount.extraLabels | object | `{}` |  |
| csi.volumeMounts | list | `[]` | volumeMounts is a list of volumeMounts for the main server container. These are rendered via toYaml rather than pre-processed like the extraVolumes value. The purpose is to make it easy to share volumes between containers. |
| csi.volumes | list | `[]` | volumes is a list of volumes made available to all containers. These are rendered via toYaml rather than pre-processed like the extraVolumes value. The purpose is to make it easy to share volumes between containers. |
| extraObjects | list | `[]` |  |
| global.enabled | bool | `true` | enabled is the master enabled switch. Setting this to true or false will enable or disable all the components within this chart by default. |
| global.externalBaoAddr | string | `""` | External openbao server address for the injector and CSI provider to use. Setting this will disable deployment of a openbao server. |
| global.externalVaultAddr | string | `""` | Deprecated: Please use global.externalBaoAddr instead. |
| global.imagePullSecrets | list | `[]` | Image pull secret to use for registry authentication. Alternatively, the value may be specified as an array of strings. |
| global.namespace | string | `""` | The namespace to deploy to. Defaults to the `helm` installation namespace. |
| global.openshift | bool | `false` | If deploying to OpenShift |
| global.psp | object | `{"annotations":"seccomp.security.alpha.kubernetes.io/allowedProfileNames: docker/default,runtime/default\napparmor.security.beta.kubernetes.io/allowedProfileNames: runtime/default\nseccomp.security.alpha.kubernetes.io/defaultProfileName:  runtime/default\napparmor.security.beta.kubernetes.io/defaultProfileName:  runtime/default\n","enable":false}` | Create PodSecurityPolicy for pods |
| global.psp.annotations | string | `"seccomp.security.alpha.kubernetes.io/allowedProfileNames: docker/default,runtime/default\napparmor.security.beta.kubernetes.io/allowedProfileNames: runtime/default\nseccomp.security.alpha.kubernetes.io/defaultProfileName:  runtime/default\napparmor.security.beta.kubernetes.io/defaultProfileName:  runtime/default\n"` | Annotation for PodSecurityPolicy. This is a multi-line templated string map, and can also be set as YAML. |
| global.serverTelemetry.prometheusOperator | bool | `false` | Enable integration with the Prometheus Operator See the top level serverTelemetry section below before enabling this feature. |
| global.tlsDisable | bool | `true` | TLS for end-to-end encrypted transport |
| injector.affinity | string | `"podAntiAffinity:\n  requiredDuringSchedulingIgnoredDuringExecution:\n    - labelSelector:\n        matchLabels:\n          app.kubernetes.io/name: {{ template \"openbao.name\" . }}-agent-injector\n          app.kubernetes.io/instance: \"{{ .Release.Name }}\"\n          component: webhook\n      topologyKey: kubernetes.io/hostname\n"` |  |
| injector.agentDefaults.cpuLimit | string | `"500m"` |  |
| injector.agentDefaults.cpuRequest | string | `"250m"` |  |
| injector.agentDefaults.memLimit | string | `"128Mi"` |  |
| injector.agentDefaults.memRequest | string | `"64Mi"` |  |
| injector.agentDefaults.template | string | `"map"` |  |
| injector.agentDefaults.templateConfig.exitOnRetryFailure | bool | `true` |  |
| injector.agentDefaults.templateConfig.staticSecretRenderInterval | string | `""` |  |
| injector.agentImage | object | `{"pullPolicy":"IfNotPresent","registry":"quay.io","repository":"openbao/openbao","tag":""}` | agentImage sets the repo and tag of the OpenBao image to use for the OpenBao Agent containers.  This should be set to the official OpenBao image.  OpenBao 1.3.1+ is required. |
| injector.agentImage.pullPolicy | string | `"IfNotPresent"` | image pull policy to use for agent image. if tag is "latest", set to "Always" |
| injector.agentImage.registry | string | `"quay.io"` | image registry to use for agent image |
| injector.agentImage.repository | string | `"openbao/openbao"` | image repo to use for agent image |
| injector.agentImage.tag | string | `""` | image tag to use for agent image - defaults to chart appVersion |
| injector.annotations | object | `{}` |  |
| injector.authPath | string | `"auth/kubernetes"` |  |
| injector.certs.caBundle | string | `""` |  |
| injector.certs.certName | string | `"tls.crt"` |  |
| injector.certs.keyName | string | `"tls.key"` |  |
| injector.certs.secretName | string | `nil` |  |
| injector.enabled | string | `"-"` | True if you want to enable openbao agent injection. @default: global.enabled |
| injector.externalVaultAddr | string | `""` | Deprecated: Please use global.externalBaoAddr instead. |
| injector.extraEnvironmentVars | object | `{}` |  |
| injector.extraLabels | object | `{}` |  |
| injector.failurePolicy | string | `"Ignore"` |  |
| injector.hostNetwork | bool | `false` |  |
| injector.image.pullPolicy | string | `"IfNotPresent"` | image pull policy to use for k8s image. if tag is "latest", set to "Always" |
| injector.image.registry | string | `"docker.io"` | image registry to use for k8s image |
| injector.image.repository | string | `"hashicorp/vault-k8s"` | image repo to use for k8s image |
| injector.image.tag | string | `"1.7.2"` | image tag to use for k8s image |
| injector.leaderElector | object | `{"enabled":true}` | If multiple replicas are specified, by default a leader will be determined so that only one injector attempts to create TLS certificates. |
| injector.livenessProbe.failureThreshold | int | `2` | When a probe fails, Kubernetes will try failureThreshold times before giving up |
| injector.livenessProbe.initialDelaySeconds | int | `5` | Number of seconds after the container has started before probe initiates |
| injector.livenessProbe.periodSeconds | int | `2` | How often (in seconds) to perform the probe |
| injector.livenessProbe.successThreshold | int | `1` | Minimum consecutive successes for the probe to be considered successful after having failed |
| injector.livenessProbe.timeoutSeconds | int | `5` | Number of seconds after which the probe times out. |
| injector.logFormat | string | `"standard"` | Configures the log format of the injector. Supported log formats: "standard", "json". |
| injector.logLevel | string | `"info"` | Configures the log verbosity of the injector. Supported log levels include: trace, debug, info, warn, error |
| injector.metrics | object | `{"enabled":false}` | If true, will enable a node exporter metrics endpoint at /metrics. |
| injector.namespaceSelector | object | `{}` |  |
| injector.nodeSelector | object | `{}` |  |
| injector.objectSelector | object | `{}` |  |
| injector.podDisruptionBudget | object | `{}` |  |
| injector.port | int | `8080` | Configures the port the injector should listen on |
| injector.priorityClassName | string | `""` |  |
| injector.readinessProbe.failureThreshold | int | `2` | When a probe fails, Kubernetes will try failureThreshold times before giving up |
| injector.readinessProbe.initialDelaySeconds | int | `5` | Number of seconds after the container has started before probe initiates |
| injector.readinessProbe.periodSeconds | int | `2` | How often (in seconds) to perform the probe |
| injector.readinessProbe.successThreshold | int | `1` | Minimum consecutive successes for the probe to be considered successful after having failed |
| injector.readinessProbe.timeoutSeconds | int | `5` | Number of seconds after which the probe times out. |
| injector.replicas | int | `1` |  |
| injector.resources | object | `{}` |  |
| injector.revokeOnShutdown | bool | `false` |  |
| injector.securityContext.container | object | `{}` |  |
| injector.securityContext.pod | object | `{}` |  |
| injector.service.annotations | object | `{}` |  |
| injector.service.extraLabels | object | `{}` |  |
| injector.serviceAccount.annotations | object | `{}` |  |
| injector.startupProbe.failureThreshold | int | `12` | When a probe fails, Kubernetes will try failureThreshold times before giving up |
| injector.startupProbe.initialDelaySeconds | int | `5` | Number of seconds after the container has started before probe initiates |
| injector.startupProbe.periodSeconds | int | `5` | How often (in seconds) to perform the probe |
| injector.startupProbe.successThreshold | int | `1` | Minimum consecutive successes for the probe to be considered successful after having failed |
| injector.startupProbe.timeoutSeconds | int | `5` | Number of seconds after which the probe times out. |
| injector.strategy | object | `{}` |  |
| injector.tolerations | list | `[]` |  |
| injector.topologySpreadConstraints | list | `[]` |  |
| injector.webhook.annotations | object | `{}` |  |
| injector.webhook.failurePolicy | string | `"Ignore"` |  |
| injector.webhook.matchPolicy | string | `"Exact"` |  |
| injector.webhook.namespaceSelector | object | `{}` |  |
| injector.webhook.objectSelector | string | `"matchExpressions:\n- key: app.kubernetes.io/name\n  operator: NotIn\n  values:\n  - {{ template \"openbao.name\" . }}-agent-injector\n"` |  |
| injector.webhook.timeoutSeconds | int | `30` |  |
| injector.webhookAnnotations | object | `{}` |  |
| server.affinity | string | `"podAntiAffinity:\n  requiredDuringSchedulingIgnoredDuringExecution:\n    - labelSelector:\n        matchLabels:\n          app.kubernetes.io/name: {{ template \"openbao.name\" . }}\n          app.kubernetes.io/instance: \"{{ .Release.Name }}\"\n          component: server\n      topologyKey: kubernetes.io/hostname\n"` |  |
| server.annotations | object | `{}` |  |
| server.auditStorage.accessMode | string | `"ReadWriteOnce"` |  |
| server.auditStorage.annotations | object | `{}` |  |
| server.auditStorage.enabled | bool | `false` |  |
| server.auditStorage.labels | object | `{}` |  |
| server.auditStorage.mountPath | string | `"/openbao/audit"` |  |
| server.auditStorage.size | string | `"10Gi"` |  |
| server.auditStorage.storageClass | string | `nil` |  |
| server.authDelegator.enabled | bool | `true` |  |
| server.configAnnotation | bool | `false` |  |
| server.dataStorage.accessMode | string | `"ReadWriteOnce"` |  |
| server.dataStorage.annotations | object | `{}` |  |
| server.dataStorage.enabled | bool | `true` |  |
| server.dataStorage.labels | object | `{}` |  |
| server.dataStorage.mountPath | string | `"/openbao/data"` |  |
| server.dataStorage.size | string | `"10Gi"` |  |
| server.dataStorage.storageClass | string | `nil` |  |
| server.dev.devRootToken | string | `"root"` |  |
| server.dev.enabled | bool | `false` |  |
| server.enabled | string | `"-"` |  |
| server.extraArgs | string | `""` | extraArgs is a string containing additional OpenBao server arguments. |
| server.extraContainers | string | `nil` |  |
| server.extraEnvironmentVars | object | `{}` |  |
| server.extraInitContainers | list | `[]` | extraInitContainers is a list of init containers. Specified as a YAML list. This is useful if you need to run a script to provision TLS certificates or write out configuration files in a dynamic way. |
| server.extraLabels | object | `{}` |  |
| server.extraPorts | list | `[]` | extraPorts is a list of extra ports. Specified as a YAML list. This is useful if you need to add additional ports to the statefulset in dynamic way. |
| server.extraSecretEnvironmentVars | list | `[]` |  |
| server.extraVolumes | list | `[]` |  |
| server.gateway.httpRoute.activeService | bool | `true` |  |
| server.gateway.httpRoute.annotations | object | `{}` |  |
| server.gateway.httpRoute.apiVersion | string | `"gateway.networking.k8s.io/v1"` |  |
| server.gateway.httpRoute.enabled | bool | `false` |  |
| server.gateway.httpRoute.filters | list | `[]` |  |
| server.gateway.httpRoute.hosts[0] | string | `"chart-example.local"` |  |
| server.gateway.httpRoute.labels | object | `{}` |  |
| server.gateway.httpRoute.matches.path.type | string | `"PathPrefix"` |  |
| server.gateway.httpRoute.matches.path.value | string | `"/"` |  |
| server.gateway.httpRoute.matches.timeouts | object | `{}` |  |
| server.gateway.httpRoute.parentRefs | list | `[]` |  |
| server.gateway.tlsPolicy.activeService | bool | `true` |  |
| server.gateway.tlsPolicy.annotations | object | `{}` |  |
| server.gateway.tlsPolicy.apiVersion | string | `"gateway.networking.k8s.io/v1"` |  |
| server.gateway.tlsPolicy.enabled | bool | `false` |  |
| server.gateway.tlsPolicy.labels | object | `{}` |  |
| server.gateway.tlsPolicy.targetRefs | list | `[]` |  |
| server.gateway.tlsPolicy.validation | object | `{}` |  |
| server.gateway.tlsRoute.activeService | bool | `true` |  |
| server.gateway.tlsRoute.annotations | object | `{}` |  |
| server.gateway.tlsRoute.apiVersion | string | `"gateway.networking.k8s.io/v1alpha3"` |  |
| server.gateway.tlsRoute.enabled | bool | `false` |  |
| server.gateway.tlsRoute.hosts | list | `[]` |  |
| server.gateway.tlsRoute.labels | object | `{}` |  |
| server.gateway.tlsRoute.parentRefs | list | `[]` |  |
| server.ha.apiAddr | string | `nil` |  |
| server.ha.clusterAddr | string | `nil` |  |
| server.ha.config | string | `"ui = true\n\nlistener \"tcp\" {\n  tls_disable = 1\n  address = \"[::]:8200\"\n  cluster_address = \"[::]:8201\"\n}\nstorage \"consul\" {\n  path = \"openbao\"\n  address = \"HOST_IP:8500\"\n}\n\nservice_registration \"kubernetes\" {}\n\n# Example configuration for using auto-unseal, using Google Cloud KMS. The\n# GKMS keys must already exist, and the cluster must have a service account\n# that is authorized to access GCP KMS.\n#seal \"gcpckms\" {\n#   project     = \"openbao-helm-dev-246514\"\n#   region      = \"global\"\n#   key_ring    = \"openbao-helm-unseal-kr\"\n#   crypto_key  = \"openbao-helm-unseal-key\"\n#}\n\n# Example configuration for enabling Prometheus metrics.\n# If you are using Prometheus Operator you can enable a ServiceMonitor resource below.\n# You may wish to enable unauthenticated metrics in the listener block above.\n#telemetry {\n#  prometheus_retention_time = \"30s\"\n#  disable_hostname = true\n#}\n"` |  |
| server.ha.disruptionBudget.enabled | bool | `true` |  |
| server.ha.disruptionBudget.maxUnavailable | string | `nil` |  |
| server.ha.enabled | bool | `false` |  |
| server.ha.raft.config | string | `"ui = true\n\nlistener \"tcp\" {\n  tls_disable = 1\n  address = \"[::]:8200\"\n  cluster_address = \"[::]:8201\"\n  # Enable unauthenticated metrics access (necessary for Prometheus Operator)\n  #telemetry {\n  #  unauthenticated_metrics_access = \"true\"\n  #}\n}\n\nstorage \"raft\" {\n  path = \"/openbao/data\"\n}\n\nservice_registration \"kubernetes\" {}\n"` |  |
| server.ha.raft.enabled | bool | `false` |  |
| server.ha.raft.setNodeId | bool | `false` |  |
| server.ha.replicas | int | `3` |  |
| server.hostAliases | list | `[]` |  |
| server.hostNetwork | bool | `false` |  |
| server.image.pullPolicy | string | `"IfNotPresent"` | image pull policy to use for server image. if tag is "latest", set to "Always" |
| server.image.registry | string | `"quay.io"` | image registry to use for server image |
| server.image.repository | string | `"openbao/openbao"` | image repo to use for server image |
| server.image.tag | string | `""` | image tag to use for server image - defaults to chart appVersion |
| server.ingress.activeService | bool | `true` |  |
| server.ingress.annotations | object | `{}` |  |
| server.ingress.enabled | bool | `false` |  |
| server.ingress.extraPaths | list | `[]` |  |
| server.ingress.hosts[0].host | string | `"chart-example.local"` |  |
| server.ingress.hosts[0].paths | list | `[]` |  |
| server.ingress.ingressClassName | string | `""` |  |
| server.ingress.labels | object | `{}` |  |
| server.ingress.pathType | string | `"Prefix"` |  |
| server.ingress.tls | list | `[]` |  |
| server.livenessProbe.enabled | bool | `false` |  |
| server.livenessProbe.execCommand | list | `[]` |  |
| server.livenessProbe.failureThreshold | int | `2` |  |
| server.livenessProbe.initialDelaySeconds | int | `60` |  |
| server.livenessProbe.path | string | `"/v1/sys/health?standbyok=true"` |  |
| server.livenessProbe.periodSeconds | int | `5` |  |
| server.livenessProbe.port | int | `8200` |  |
| server.livenessProbe.successThreshold | int | `1` |  |
| server.livenessProbe.timeoutSeconds | int | `3` |  |
| server.logFormat | string | `""` |  |
| server.logLevel | string | `""` |  |
| server.networkPolicy.egress | list | `[]` |  |
| server.networkPolicy.enabled | bool | `false` |  |
| server.networkPolicy.ingress[0].from[0].namespaceSelector | object | `{}` |  |
| server.networkPolicy.ingress[0].ports[0].port | int | `8200` |  |
| server.networkPolicy.ingress[0].ports[0].protocol | string | `"TCP"` |  |
| server.networkPolicy.ingress[0].ports[1].port | int | `8201` |  |
| server.networkPolicy.ingress[0].ports[1].protocol | string | `"TCP"` |  |
| server.nodeSelector | object | `{}` |  |
| server.persistentVolumeClaimRetentionPolicy | object | `{}` |  |
| server.podManagementPolicy | string | `"OrderedReady"` |  |
| server.postStart | list | `[]` |  |
| server.preStopSleepSeconds | int | `5` |  |
| server.priorityClassName | string | `""` |  |
| server.readinessProbe.enabled | bool | `true` |  |
| server.readinessProbe.failureThreshold | int | `2` |  |
| server.readinessProbe.initialDelaySeconds | int | `5` |  |
| server.readinessProbe.periodSeconds | int | `5` |  |
| server.readinessProbe.port | int | `8200` |  |
| server.readinessProbe.successThreshold | int | `1` |  |
| server.readinessProbe.timeoutSeconds | int | `3` |  |
| server.resources | object | `{}` |  |
| server.route.activeService | bool | `true` |  |
| server.route.annotations | object | `{}` |  |
| server.route.enabled | bool | `false` |  |
| server.route.host | string | `"chart-example.local"` |  |
| server.route.labels | object | `{}` |  |
| server.route.tls.termination | string | `"passthrough"` |  |
| server.service.active.annotations | object | `{}` |  |
| server.service.active.enabled | bool | `true` |  |
| server.service.active.extraLabels | object | `{}` |  |
| server.service.annotations | object | `{}` |  |
| server.service.enabled | bool | `true` |  |
| server.service.externalTrafficPolicy | string | `"Cluster"` |  |
| server.service.extraLabels | object | `{}` |  |
| server.service.extraPorts | list | `[]` | extraPorts is a list of extra ports. Specified as a YAML list. This is useful if you need to add additional ports to the server service in dynamic way. |
| server.service.instanceSelector.enabled | bool | `true` |  |
| server.service.ipFamilies | list | `[]` |  |
| server.service.ipFamilyPolicy | string | `""` |  |
| server.service.port | int | `8200` |  |
| server.service.publishNotReadyAddresses | bool | `true` |  |
| server.service.standby.annotations | object | `{}` |  |
| server.service.standby.enabled | bool | `true` |  |
| server.service.standby.extraLabels | object | `{}` |  |
| server.service.targetPort | int | `8200` |  |
| server.serviceAccount.annotations | object | `{}` |  |
| server.serviceAccount.create | bool | `true` |  |
| server.serviceAccount.createSecret | bool | `false` |  |
| server.serviceAccount.extraLabels | object | `{}` |  |
| server.serviceAccount.name | string | `""` |  |
| server.serviceAccount.serviceDiscovery.enabled | bool | `true` |  |
| server.shareProcessNamespace | bool | `false` | shareProcessNamespace enables process namespace sharing between OpenBao and the extraContainers This is useful if OpenBao must be signaled, e.g. to send a SIGHUP for a log rotation |
| server.standalone.config | string | `"ui = true\n\nlistener \"tcp\" {\n  tls_disable = 1\n  address = \"[::]:8200\"\n  cluster_address = \"[::]:8201\"\n  # Enable unauthenticated metrics access (necessary for Prometheus Operator)\n  #telemetry {\n  #  unauthenticated_metrics_access = \"true\"\n  #}\n}\nstorage \"file\" {\n  path = \"/openbao/data\"\n}\n\n# Example configuration for using auto-unseal, using Google Cloud KMS. The\n# GKMS keys must already exist, and the cluster must have a service account\n# that is authorized to access GCP KMS.\n#seal \"gcpckms\" {\n#   project     = \"openbao-helm-dev\"\n#   region      = \"global\"\n#   key_ring    = \"openbao-helm-unseal-kr\"\n#   crypto_key  = \"openbao-helm-unseal-key\"\n#}\n\n# Example configuration for enabling Prometheus metrics in your config.\n#telemetry {\n#  prometheus_retention_time = \"30s\"\n#  disable_hostname = true\n#}\n"` |  |
| server.standalone.enabled | string | `"-"` |  |
| server.statefulSet.annotations | object | `{}` |  |
| server.statefulSet.securityContext.container | object | `{}` |  |
| server.statefulSet.securityContext.pod | object | `{}` |  |
| server.terminationGracePeriodSeconds | int | `10` |  |
| server.tolerations | list | `[]` |  |
| server.topologySpreadConstraints | list | `[]` |  |
| server.updateStrategyType | string | `"OnDelete"` |  |
| server.volumeMounts | string | `nil` |  |
| server.volumes | string | `nil` |  |
| serverTelemetry.grafanaDashboard.defaultLabel | bool | `true` |  |
| serverTelemetry.grafanaDashboard.enabled | bool | `false` |  |
| serverTelemetry.grafanaDashboard.extraAnnotations | object | `{}` |  |
| serverTelemetry.grafanaDashboard.extraLabel | object | `{}` |  |
| serverTelemetry.prometheusRules.enabled | bool | `false` |  |
| serverTelemetry.prometheusRules.rules | list | `[]` |  |
| serverTelemetry.prometheusRules.selectors | object | `{}` |  |
| serverTelemetry.serviceMonitor.authorization | object | `{}` |  |
| serverTelemetry.serviceMonitor.enabled | bool | `false` |  |
| serverTelemetry.serviceMonitor.interval | string | `"30s"` |  |
| serverTelemetry.serviceMonitor.port | string | `""` | Port which Prometheus uses when scraping metrics. If empty will use `openbao.scheme` helper for its value |
| serverTelemetry.serviceMonitor.scheme | string | `""` | scheme to use when Prometheus scrapes metrics. If empty will use `openbao.scheme` helper for its value |
| serverTelemetry.serviceMonitor.scrapeClass | string | `""` |  |
| serverTelemetry.serviceMonitor.scrapeTimeout | string | `"10s"` |  |
| serverTelemetry.serviceMonitor.selectors | object | `{}` |  |
| serverTelemetry.serviceMonitor.tlsConfig | object | `{}` |  |
| snapshotAgent.annotations | object | `{}` |  |
| snapshotAgent.config.baoAuthPath | string | `"kubernetes"` |  |
| snapshotAgent.config.baoRole | string | `"snapshot"` |  |
| snapshotAgent.config.s3Bucket | string | `"openbao-snapshots"` |  |
| snapshotAgent.config.s3ExpireDays | string | `"14"` |  |
| snapshotAgent.config.s3Host | string | `"s3.eu-east-1.amazonaws.com"` |  |
| snapshotAgent.config.s3Uri | string | `"s3://openbao-snapshots"` |  |
| snapshotAgent.config.s3cmdExtraFlag | string | `"-v"` |  |
| snapshotAgent.enabled | bool | `false` |  |
| snapshotAgent.extraEnvironmentVars | object | `{}` | Map of extra environment variables to set in the snapshot-agent cronjob |
| snapshotAgent.extraSecretEnvironmentVars | list | `[]` | List of extra environment variables to set in the snapshot-agent cronjob These variables take value from existing Secret objects. |
| snapshotAgent.extraVolumeMounts | list | `[]` | List of additional volumeMounts for the snapshot cronjob container. |
| snapshotAgent.extraVolumes | list | `[]` | List of extraVolumes made available to the snapshot cronjob container. |
| snapshotAgent.image.repository | string | `"ghcr.io/openbao/openbao-snapshot-agent"` |  |
| snapshotAgent.image.tag | string | `"0.2.4"` |  |
| snapshotAgent.resources | object | `{}` |  |
| snapshotAgent.restartPolicy | string | `"OnFailure"` |  |
| snapshotAgent.s3CredentialsSecret | string | `"my-s3-credentials"` |  |
| snapshotAgent.schedule | string | `"*/15 * * * *"` |  |
| snapshotAgent.securityContext.container | object | `{}` |  |
| snapshotAgent.securityContext.pod | object | `{}` |  |
| snapshotAgent.serviceAccount.annotations | object | `{}` |  |
| snapshotAgent.serviceAccount.create | bool | `true` |  |
| snapshotAgent.serviceAccount.extraLabels | object | `{}` |  |
| snapshotAgent.serviceAccount.name | string | `""` |  |
| ui.activeOpenbaoPodOnly | bool | `false` |  |
| ui.annotations | object | `{}` |  |
| ui.enabled | bool | `false` |  |
| ui.externalPort | int | `8200` |  |
| ui.externalTrafficPolicy | string | `"Cluster"` |  |
| ui.extraLabels | object | `{}` |  |
| ui.publishNotReadyAddresses | bool | `true` |  |
| ui.serviceIPFamilies | list | `[]` |  |
| ui.serviceIPFamilyPolicy | string | `""` |  |
| ui.serviceNodePort | string | `nil` |  |
| ui.serviceType | string | `"ClusterIP"` |  |
| ui.targetPort | int | `8200` |  |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
