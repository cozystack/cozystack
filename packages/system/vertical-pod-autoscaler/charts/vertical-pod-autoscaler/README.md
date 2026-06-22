# Vertical Pod Autoscaler

[Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler) is a set of components that automatically adjust the amount of CPU and memory requested by pods running in the Kubernetes Cluster.

**DISCLAIMER**: This is an unofficial chart not supported by Vertical Pod Autoscaler authors.

## TL;DR;

```bash
$ helm repo add cowboysysop https://cowboysysop.github.io/charts/
$ helm install my-release cowboysysop/vertical-pod-autoscaler
```

or for an OCI-based registry:

```bash
$ helm install my-release oci://ghcr.io/cowboysysop/charts/vertical-pod-autoscaler
```

## Introduction

This chart bootstraps a Vertical Pod Autoscaler deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Prerequisites

- Kubernetes >= 1.24
- Metrics Server >= 0.2 (you can use the [bitnami/metrics-server](https://artifacthub.io/packages/helm/bitnami/metrics-server) chart)
- Helm >= 3.9

## Installing

Install the chart using:

```bash
$ helm repo add cowboysysop https://cowboysysop.github.io/charts/
$ helm install my-release cowboysysop/vertical-pod-autoscaler
```

or for an OCI-based registry:

```bash
$ helm install my-release oci://ghcr.io/cowboysysop/charts/vertical-pod-autoscaler
```

These commands deploy Vertical Pod Autoscaler on the Kubernetes cluster in the default configuration and with the release name `my-release`. The deployment configuration can be customized by specifying the customization parameters with the `helm install` command using the `--values` or `--set` arguments. Find more information in the [configuration section](#configuration) of this document.

## Upgrading

Upgrade the chart deployment using:

```bash
$ helm upgrade my-release cowboysysop/vertical-pod-autoscaler
```

or for an OCI-based registry:

```bash
$ helm upgrade my-release oci://ghcr.io/cowboysysop/charts/vertical-pod-autoscaler
```

The command upgrades the existing `my-release` deployment with the most latest release of the chart.

### Upgrading to version 11.0.0

The chart now uses forked versions of the Bitnami charts to reference the Bitnami Legacy repository:

- https://github.com/bitnami/containers/issues/83267

Pod and container security contexts have been hardened and some values are now configured at
the container level instead of the pod level.

### Upgrading to version 10.0.0

The application has been updated to a major release, see the release notes for breaking changes:

- https://github.com/kubernetes/autoscaler/releases/tag/vertical-pod-autoscaler-1.3.0

Information about services are no more injected into pod's environment variable.

### Upgrading to version 9.0.0

The chart is now tested with Kubernetes >= 1.24 and Helm >= 3.9.

Future upgrades may introduce undetected breaking changes if you continue to use older versions.

### Upgrading to version 8.0.0

Some parameters related to port management have been modified:

- Parameter `admissionController.metrics.service.port` has been renamed `admissionController.metrics.service.ports.metrics`.
- Parameter `recommender.metrics.service.port` has been renamed `recommender.metrics.service.ports.metrics`.
- Parameter `updater.metrics.service.port` has been renamed `updater.metrics.service.ports.metrics`.

### Upgrading to version 7.0.0

Some parameters related to image management have been modified:

- Registry prefix in `image.repository` parameters is now configured in `image.registry`.
- Parameter `imagePullSecrets` has been renamed `global.imagePullSecrets`.

### Upgrading to version 6.0.0

The application version is no more compatible with Kubernetes 1.19, 1.20 and 1.21.

### Upgrading to version 5.0.0

The application validates that all fields that specify CPU and memory have supported resolution:

- CPU is a whole number of milli CPUs
- Memory is a whole number of bytes

### Upgrading to version 4.0.0

The application version is no more compatible with Kubernetes 1.16.

Custom resource definitions are now created and upgraded with a pre-install/pre-upgrade job.

### Upgrading to version 3.0.0

The chart is no more compatible with Helm 2.

Refer to the [Helm documentation](https://helm.sh/docs/topics/v2_v3_migration/) for more information.

### Upgrading to version 2.0.0

The port names have been changed to be compatible with Istio service mesh.

## Uninstalling

Uninstall the `my-release` deployment using:

```bash
$ helm uninstall my-release
```

The command deletes the release named `my-release` and frees all the kubernetes resources associated with the release.

**TIP**: Specify the `--purge` argument to the above command to remove the release from the store and make its name free for later use.

Delete the `vpa-webhook-config` mutating webhook configuration automatically created by Vertical Pod Autoscaler admission controller component using:

```bash
$ kubectl delete mutatingwebhookconfiguration vpa-webhook-config
```

Optionally, delete the custom resource definitions created by the chart using:

**WARNING**: It will also try to delete all instances of the custom resource definitions.

```bash
$ kubectl delete crd verticalpodautoscalers.autoscaling.k8s.io
$ kubectl delete crd verticalpodautoscalercheckpoints.autoscaling.k8s.io
```

## Configuration

### Global parameters

| Name                      | Description                                     | Default |
| ------------------------- | ----------------------------------------------- | ------- |
| `global.imageRegistry`    | Global Docker image registry                    | `""`    |
| `global.imagePullSecrets` | Global Docker registry secret names as an array | `[]`    |

### Common parameters

| Name                | Description                                                                                                  | Default |
| ------------------- | ------------------------------------------------------------------------------------------------------------ | ------- |
| `kubeVersion`       | Override Kubernetes version                                                                                  | `""`    |
| `nameOverride`      | Partially override `vertical-pod-autoscaler.fullname` template with a string (will prepend the release name) | `""`    |
| `fullnameOverride`  | Fully override `vertical-pod-autoscaler.fullname` template with a string                                     | `""`    |
| `namespaceOverride` | Fully override `common.names.namespace` template with a string                                               | `""`    |
| `commonAnnotations` | Annotations to add to all deployed objects                                                                   | `{}`    |
| `commonLabels`      | Labels to add to all deployed objects                                                                        | `{}`    |
| `extraDeploy`       | Array of extra objects to deploy with the release                                                            | `[]`    |

### Admission controller parameters

| Name                                                           | Description                                                                                                         | Default                                |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- | -------------------------------------- |
| `admissionController.enabled`                                  | Enable the component                                                                                                | `true`                                 |
| `admissionController.replicaCount`                             | Number of replicas                                                                                                  | `1`                                    |
| `admissionController.revisionHistoryLimit`                     | Number of old history to retain to allow rollback                                                                   | `10`                                   |
| `admissionController.image.registry`                           | Image registry                                                                                                      | `registry.k8s.io`                      |
| `admissionController.image.repository`                         | Image repository                                                                                                    | `autoscaling/vpa-admission-controller` |
| `admissionController.image.tag`                                | Image tag                                                                                                           | `1.5.0`                                |
| `admissionController.image.digest`                             | Image digest                                                                                                        | `""`                                   |
| `admissionController.image.pullPolicy`                         | Image pull policy                                                                                                   | `IfNotPresent`                         |
| `admissionController.pdb.create`                               | Specifies whether a pod disruption budget should be created                                                         | `false`                                |
| `admissionController.pdb.minAvailable`                         | Minimum number/percentage of pods that should remain scheduled                                                      | `1`                                    |
| `admissionController.pdb.maxUnavailable`                       | Maximum number/percentage of pods that may be made unavailable                                                      | `nil`                                  |
| `admissionController.serviceAccount.create`                    | Specifies whether a service account should be created                                                               | `true`                                 |
| `admissionController.serviceAccount.annotations`               | Service account annotations                                                                                         | `{}`                                   |
| `admissionController.serviceAccount.name`                      | The name of the service account to use (Generated using the `vertical-pod-autoscaler.fullname` template if not set) | `nil`                                  |
| `admissionController.enableServiceLinks`                       | Whether information about services should be injected into pod's environment variable                               | `false`                                |
| `admissionController.hostAliases`                              | Pod host aliases                                                                                                    | `[]`                                   |
| `admissionController.deploymentAnnotations`                    | Additional deployment annotations                                                                                   | `{}`                                   |
| `admissionController.podAnnotations`                           | Additional pod annotations                                                                                          | `{}`                                   |
| `admissionController.podLabels`                                | Additional pod labels                                                                                               | `{}`                                   |
| `admissionController.podSecurityContext`                       | Pod security context                                                                                                |                                        |
| `admissionController.podSecurityContext.seccompProfile.type`   | Set pod's Security Context seccomp profile                                                                          | `RuntimeDefault`                       |
| `admissionController.hostNetwork`                              | Use the host network                                                                                                | `false`                                |
| `admissionController.priorityClassName`                        | Priority class name                                                                                                 | `nil`                                  |
| `admissionController.runtimeClassName`                         | Runtime class name                                                                                                  | `""`                                   |
| `admissionController.topologySpreadConstraints`                | Topology Spread Constraints for pod assignment                                                                      | `[]`                                   |
| `admissionController.securityContext`                          | Container security context                                                                                          |                                        |
| `admissionController.securityContext.allowPrivilegeEscalation` | Set container's Security Context allowPrivilegeEscalation                                                           | `false`                                |
| `admissionController.securityContext.capabilities.drop`        | List of capabilities to be dropped                                                                                  | `["ALL"]`                              |
| `admissionController.securityContext.readOnlyRootFilesystem`   | Set container's Security Context readOnlyRootFilesystem                                                             | `true`                                 |
| `admissionController.securityContext.runAsNonRoot`             | Whether the container must run as a non-root user                                                                   | `true`                                 |
| `admissionController.securityContext.runAsUser`                | The UID to run the entrypoint of the container process                                                              | `65534`                                |
| `admissionController.securityContext.runAsGroup`               | The GID to run the entrypoint of the container process                                                              | `65534`                                |
| `admissionController.containerPorts.https`                     | Container port for HTTPS                                                                                            | `8000`                                 |
| `admissionController.containerPorts.metrics`                   | Container port for Metrics                                                                                          | `8944`                                 |
| `admissionController.livenessProbe.enabled`                    | Enable liveness probe                                                                                               | `true`                                 |
| `admissionController.livenessProbe.initialDelaySeconds`        | Delay before the liveness probe is initiated                                                                        | `0`                                    |
| `admissionController.livenessProbe.periodSeconds`              | How often to perform the liveness probe                                                                             | `10`                                   |
| `admissionController.livenessProbe.timeoutSeconds`             | When the liveness probe times out                                                                                   | `1`                                    |
| `admissionController.livenessProbe.failureThreshold`           | Minimum consecutive failures for the liveness probe to be considered failed after having succeeded                  | `3`                                    |
| `admissionController.livenessProbe.successThreshold`           | Minimum consecutive successes for the liveness probe to be considered successful after having failed                | `1`                                    |
| `admissionController.readinessProbe.enabled`                   | Enable readiness probe                                                                                              | `true`                                 |
| `admissionController.readinessProbe.initialDelaySeconds`       | Delay before the readiness probe is initiated                                                                       | `0`                                    |
| `admissionController.readinessProbe.periodSeconds`             | How often to perform the readiness probe                                                                            | `10`                                   |
| `admissionController.readinessProbe.timeoutSeconds`            | When the readiness probe times out                                                                                  | `1`                                    |
| `admissionController.readinessProbe.failureThreshold`          | Minimum consecutive failures for the readiness probe to be considered failed after having succeeded                 | `3`                                    |
| `admissionController.readinessProbe.successThreshold`          | Minimum consecutive successes for the readiness probe to be considered successful after having failed               | `1`                                    |
| `admissionController.startupProbe.enabled`                     | Enable startup probe                                                                                                | `false`                                |
| `admissionController.startupProbe.initialDelaySeconds`         | Delay before the startup probe is initiated                                                                         | `0`                                    |
| `admissionController.startupProbe.periodSeconds`               | How often to perform the startup probe                                                                              | `10`                                   |
| `admissionController.startupProbe.timeoutSeconds`              | When the startup probe times out                                                                                    | `1`                                    |
| `admissionController.startupProbe.failureThreshold`            | Minimum consecutive failures for the startup probe to be considered failed after having succeeded                   | `3`                                    |
| `admissionController.startupProbe.successThreshold`            | Minimum consecutive successes for the startup probe to be considered successful after having failed                 | `1`                                    |
| `admissionController.service.annotations`                      | Service annotations                                                                                                 | `{}`                                   |
| `admissionController.service.type`                             | Service type                                                                                                        | `ClusterIP`                            |
| `admissionController.service.clusterIP`                        | Static cluster IP address or None for headless service when service type is ClusterIP                               | `nil`                                  |
| `admissionController.service.ipFamilyPolicy`                   | Service IP family policy                                                                                            | `""`                                   |
| `admissionController.service.ipFamilies`                       | Service IP families                                                                                                 | `[]`                                   |
| `admissionController.service.sessionAffinity`                  | Control where client requests go, to the same pod or round-robin                                                    | `None`                                 |
| `admissionController.service.sessionAffinityConfig`            | Additional settings for the sessionAffinity                                                                         | `{}`                                   |
| `admissionController.service.ports.https`                      | Service port for HTTPS (do not change it)                                                                           | `443`                                  |
| `admissionController.resources`                                | CPU/Memory resource requests/limits                                                                                 | `{}`                                   |
| `admissionController.nodeSelector`                             | Node labels for pod assignment                                                                                      | `{}`                                   |
| `admissionController.tolerations`                              | Tolerations for pod assignment                                                                                      | `[]`                                   |
| `admissionController.affinity`                                 | Map of node/pod affinities                                                                                          | `{}`                                   |
| `admissionController.extraArgs`                                | Additional container arguments                                                                                      |                                        |
| `admissionController.extraArgs.v`                              | Number for the log level verbosity                                                                                  | `2`                                    |
| `admissionController.extraEnvVars`                             | Additional container environment variables                                                                          | `[]`                                   |
| `admissionController.extraEnvVarsCM`                           | Name of existing ConfigMap containing additional container environment variables                                    | `nil`                                  |
| `admissionController.extraEnvVarsSecret`                       | Name of existing Secret containing additional container environment variables                                       | `nil`                                  |
| `admissionController.extraVolumes`                             | Optionally specify extra list of additional volumes                                                                 | `[]`                                   |
| `admissionController.extraVolumeMounts`                        | Optionally specify extra list of additional volumeMounts                                                            | `[]`                                   |
| `admissionController.metrics.service.annotations`              | Metrics service annotations                                                                                         | `{}`                                   |
| `admissionController.metrics.service.type`                     | Metrics service type                                                                                                | `ClusterIP`                            |
| `admissionController.metrics.service.clusterIP`                | Metrics static cluster IP address or None for headless service when service type is ClusterIP                       | `nil`                                  |
| `admissionController.metrics.service.ipFamilyPolicy`           | Metrics service IP family policy                                                                                    | `""`                                   |
| `admissionController.metrics.service.ipFamilies`               | Metrics service IP families                                                                                         | `[]`                                   |
| `admissionController.metrics.service.ports.metrics`            | Metrics service port for Metrics                                                                                    | `8944`                                 |
| `admissionController.metrics.serviceMonitor.enabled`           | Specifies whether a service monitor should be created                                                               | `false`                                |
| `admissionController.metrics.serviceMonitor.namespace`         | Namespace in which to create the service monitor                                                                    | `""`                                   |
| `admissionController.metrics.serviceMonitor.annotations`       | Service monitor annotations                                                                                         | `{}`                                   |
| `admissionController.metrics.serviceMonitor.labels`            | Additional service monitor labels                                                                                   | `{}`                                   |
| `admissionController.metrics.serviceMonitor.jobLabel`          | The name of the label on the target service to use as the job name in Prometheus                                    | `""`                                   |
| `admissionController.metrics.serviceMonitor.honorLabels`       | Whether to choose the metric’s labels on collisions with target labels                                              | `false`                                |
| `admissionController.metrics.serviceMonitor.interval`          | Interval at which metrics should be scraped                                                                         | `""`                                   |
| `admissionController.metrics.serviceMonitor.scrapeTimeout`     | Timeout after which the scrape is ended                                                                             | `""`                                   |
| `admissionController.metrics.serviceMonitor.metricRelabelings` | Specify additional relabeling of metrics                                                                            | `[]`                                   |
| `admissionController.metrics.serviceMonitor.relabelings`       | Specify general relabeling                                                                                          | `[]`                                   |
| `admissionController.tls.caCert`                               | TLS CA certificate (Generated using the `genCA` function if not set)                                                | `""`                                   |
| `admissionController.tls.cert`                                 | TLS certificate (Generated using the `genSignedCert` function if not set)                                           | `""`                                   |
| `admissionController.tls.key`                                  | TLS private key (Generated using the `genSignedCert` function if not set)                                           | `""`                                   |
| `admissionController.tls.existingSecret`                       | Name of existing TLS Secret to use                                                                                  | `""`                                   |

### Recommender parameters

| Name                                                   | Description                                                                                                         | Default                       |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| `recommender.replicaCount`                             | Number of replicas                                                                                                  | `1`                           |
| `recommender.revisionHistoryLimit`                     | Number of old history to retain to allow rollback                                                                   | `10`                          |
| `recommender.image.registry`                           | Image registry                                                                                                      | `registry.k8s.io`             |
| `recommender.image.repository`                         | Image repository                                                                                                    | `autoscaling/vpa-recommender` |
| `recommender.image.tag`                                | Image tag                                                                                                           | `1.5.0`                       |
| `recommender.image.digest`                             | Image digest                                                                                                        | `""`                          |
| `recommender.image.pullPolicy`                         | Image pull policy                                                                                                   | `IfNotPresent`                |
| `recommender.pdb.create`                               | Specifies whether a pod disruption budget should be created                                                         | `false`                       |
| `recommender.pdb.minAvailable`                         | Minimum number/percentage of pods that should remain scheduled                                                      | `1`                           |
| `recommender.pdb.maxUnavailable`                       | Maximum number/percentage of pods that may be made unavailable                                                      | `nil`                         |
| `recommender.serviceAccount.create`                    | Specifies whether a service account should be created                                                               | `true`                        |
| `recommender.serviceAccount.annotations`               | Service account annotations                                                                                         | `{}`                          |
| `recommender.serviceAccount.name`                      | The name of the service account to use (Generated using the `vertical-pod-autoscaler.fullname` template if not set) | `nil`                         |
| `recommender.enableServiceLinks`                       | Whether information about services should be injected into pod's environment variable                               | `false`                       |
| `recommender.hostAliases`                              | Pod host aliases                                                                                                    | `[]`                          |
| `recommender.deploymentAnnotations`                    | Additional deployment annotations                                                                                   | `{}`                          |
| `recommender.podAnnotations`                           | Additional pod annotations                                                                                          | `{}`                          |
| `recommender.podLabels`                                | Additional pod labels                                                                                               | `{}`                          |
| `recommender.podSecurityContext`                       | Pod security context                                                                                                |                               |
| `recommender.podSecurityContext.seccompProfile.type`   | Set pod's Security Context seccomp profile                                                                          | `RuntimeDefault`              |
| `recommender.priorityClassName`                        | Priority class name                                                                                                 | `nil`                         |
| `recommender.runtimeClassName`                         | Runtime class name                                                                                                  | `""`                          |
| `recommender.topologySpreadConstraints`                | Topology Spread Constraints for pod assignment                                                                      | `[]`                          |
| `recommender.securityContext`                          | Container security context                                                                                          |                               |
| `recommender.securityContext.allowPrivilegeEscalation` | Set container's Security Context allowPrivilegeEscalation                                                           | `false`                       |
| `recommender.securityContext.capabilities.drop`        | List of capabilities to be dropped                                                                                  | `["ALL"]`                     |
| `recommender.securityContext.readOnlyRootFilesystem`   | Set container's Security Context readOnlyRootFilesystem                                                             | `true`                        |
| `recommender.securityContext.runAsNonRoot`             | Whether the container must run as a non-root user                                                                   | `true`                        |
| `recommender.securityContext.runAsUser`                | The UID to run the entrypoint of the container process                                                              | `65534`                       |
| `recommender.securityContext.runAsGroup`               | The GID to run the entrypoint of the container process                                                              | `65534`                       |
| `recommender.containerPorts.metrics`                   | Container port for Metrics                                                                                          | `8942`                        |
| `recommender.livenessProbe.enabled`                    | Enable liveness probe                                                                                               | `true`                        |
| `recommender.livenessProbe.initialDelaySeconds`        | Delay before the liveness probe is initiated                                                                        | `0`                           |
| `recommender.livenessProbe.periodSeconds`              | How often to perform the liveness probe                                                                             | `10`                          |
| `recommender.livenessProbe.timeoutSeconds`             | When the liveness probe times out                                                                                   | `1`                           |
| `recommender.livenessProbe.failureThreshold`           | Minimum consecutive failures for the liveness probe to be considered failed after having succeeded                  | `3`                           |
| `recommender.livenessProbe.successThreshold`           | Minimum consecutive successes for the liveness probe to be considered successful after having failed                | `1`                           |
| `recommender.readinessProbe.enabled`                   | Enable readiness probe                                                                                              | `true`                        |
| `recommender.readinessProbe.initialDelaySeconds`       | Delay before the readiness probe is initiated                                                                       | `0`                           |
| `recommender.readinessProbe.periodSeconds`             | How often to perform the readiness probe                                                                            | `10`                          |
| `recommender.readinessProbe.timeoutSeconds`            | When the readiness probe times out                                                                                  | `1`                           |
| `recommender.readinessProbe.failureThreshold`          | Minimum consecutive failures for the readiness probe to be considered failed after having succeeded                 | `3`                           |
| `recommender.readinessProbe.successThreshold`          | Minimum consecutive successes for the readiness probe to be considered successful after having failed               | `1`                           |
| `recommender.startupProbe.enabled`                     | Enable startup probe                                                                                                | `false`                       |
| `recommender.startupProbe.initialDelaySeconds`         | Delay before the startup probe is initiated                                                                         | `0`                           |
| `recommender.startupProbe.periodSeconds`               | How often to perform the startup probe                                                                              | `10`                          |
| `recommender.startupProbe.timeoutSeconds`              | When the startup probe times out                                                                                    | `1`                           |
| `recommender.startupProbe.failureThreshold`            | Minimum consecutive failures for the startup probe to be considered failed after having succeeded                   | `3`                           |
| `recommender.startupProbe.successThreshold`            | Minimum consecutive successes for the startup probe to be considered successful after having failed                 | `1`                           |
| `recommender.resources`                                | CPU/Memory resource requests/limits                                                                                 | `{}`                          |
| `recommender.nodeSelector`                             | Node labels for pod assignment                                                                                      | `{}`                          |
| `recommender.tolerations`                              | Tolerations for pod assignment                                                                                      | `[]`                          |
| `recommender.affinity`                                 | Map of node/pod affinities                                                                                          | `{}`                          |
| `recommender.extraArgs`                                | Additional container arguments                                                                                      |                               |
| `recommender.extraArgs.v`                              | Number for the log level verbosity                                                                                  | `2`                           |
| `recommender.extraEnvVars`                             | Additional container environment variables                                                                          | `[]`                          |
| `recommender.extraEnvVarsCM`                           | Name of existing ConfigMap containing additional container environment variables                                    | `nil`                         |
| `recommender.extraEnvVarsSecret`                       | Name of existing Secret containing additional container environment variables                                       | `nil`                         |
| `recommender.extraVolumes`                             | Optionally specify extra list of additional volumes                                                                 | `[]`                          |
| `recommender.extraVolumeMounts`                        | Optionally specify extra list of additional volumeMounts                                                            | `[]`                          |
| `recommender.metrics.service.annotations`              | Metrics service annotations                                                                                         | `{}`                          |
| `recommender.metrics.service.type`                     | Metrics service type                                                                                                | `ClusterIP`                   |
| `recommender.metrics.service.clusterIP`                | Metrics static cluster IP address or None for headless service when service type is ClusterIP                       | `nil`                         |
| `recommender.metrics.service.ipFamilyPolicy`           | Metrics service IP family policy                                                                                    | `""`                          |
| `recommender.metrics.service.ipFamilies`               | Metrics service IP families                                                                                         | `[]`                          |
| `recommender.metrics.service.ports.metrics`            | Metrics service port for Metrics                                                                                    | `8942`                        |
| `recommender.metrics.serviceMonitor.enabled`           | Specifies whether a service monitor should be created                                                               | `false`                       |
| `recommender.metrics.serviceMonitor.namespace`         | Namespace in which to create the service monitor                                                                    | `""`                          |
| `recommender.metrics.serviceMonitor.annotations`       | Service monitor annotations                                                                                         | `{}`                          |
| `recommender.metrics.serviceMonitor.labels`            | Additional service monitor labels                                                                                   | `{}`                          |
| `recommender.metrics.serviceMonitor.jobLabel`          | The name of the label on the target service to use as the job name in Prometheus                                    | `""`                          |
| `recommender.metrics.serviceMonitor.honorLabels`       | Whether to choose the metric’s labels on collisions with target labels                                              | `false`                       |
| `recommender.metrics.serviceMonitor.interval`          | Interval at which metrics should be scraped                                                                         | `""`                          |
| `recommender.metrics.serviceMonitor.scrapeTimeout`     | Timeout after which the scrape is ended                                                                             | `""`                          |
| `recommender.metrics.serviceMonitor.metricRelabelings` | Specify additional relabeling of metrics                                                                            | `[]`                          |
| `recommender.metrics.serviceMonitor.relabelings`       | Specify general relabeling                                                                                          | `[]`                          |

### Updater parameters

| Name                                               | Description                                                                                                         | Default                   |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- | ------------------------- |
| `updater.enabled`                                  | Enable the component                                                                                                | `true`                    |
| `updater.replicaCount`                             | Number of replicas                                                                                                  | `1`                       |
| `updater.revisionHistoryLimit`                     | Number of old history to retain to allow rollback                                                                   | `10`                      |
| `updater.image.registry`                           | Image registry                                                                                                      | `registry.k8s.io`         |
| `updater.image.repository`                         | Image repository                                                                                                    | `autoscaling/vpa-updater` |
| `updater.image.tag`                                | Image tag                                                                                                           | `1.5.0`                   |
| `updater.image.digest`                             | Image digest                                                                                                        | `""`                      |
| `updater.image.pullPolicy`                         | Image pull policy                                                                                                   | `IfNotPresent`            |
| `updater.pdb.create`                               | Specifies whether a pod disruption budget should be created                                                         | `false`                   |
| `updater.pdb.minAvailable`                         | Minimum number/percentage of pods that should remain scheduled                                                      | `1`                       |
| `updater.pdb.maxUnavailable`                       | Maximum number/percentage of pods that may be made unavailable                                                      | `nil`                     |
| `updater.serviceAccount.create`                    | Specifies whether a service account should be created                                                               | `true`                    |
| `updater.serviceAccount.annotations`               | Service account annotations                                                                                         | `{}`                      |
| `updater.serviceAccount.name`                      | The name of the service account to use (Generated using the `vertical-pod-autoscaler.fullname` template if not set) | `nil`                     |
| `updater.enableServiceLinks`                       | Whether information about services should be injected into pod's environment variable                               | `false`                   |
| `updater.hostAliases`                              | Pod host aliases                                                                                                    | `[]`                      |
| `updater.deploymentAnnotations`                    | Additional deployment annotations                                                                                   | `{}`                      |
| `updater.podAnnotations`                           | Additional pod annotations                                                                                          | `{}`                      |
| `updater.podLabels`                                | Additional pod labels                                                                                               | `{}`                      |
| `updater.podSecurityContext`                       | Pod security context                                                                                                |                           |
| `updater.podSecurityContext.seccompProfile.type`   | Set pod's Security Context seccomp profile                                                                          | `RuntimeDefault`          |
| `updater.priorityClassName`                        | Priority class name                                                                                                 | `nil`                     |
| `updater.runtimeClassName`                         | Runtime class name                                                                                                  | `""`                      |
| `updater.topologySpreadConstraints`                | Topology Spread Constraints for pod assignment                                                                      | `[]`                      |
| `updater.securityContext`                          | Container security context                                                                                          |                           |
| `updater.securityContext.allowPrivilegeEscalation` | Set container's Security Context allowPrivilegeEscalation                                                           | `false`                   |
| `updater.securityContext.capabilities.drop`        | List of capabilities to be dropped                                                                                  | `["ALL"]`                 |
| `updater.securityContext.readOnlyRootFilesystem`   | Set container's Security Context readOnlyRootFilesystem                                                             | `true`                    |
| `updater.securityContext.runAsNonRoot`             | Whether the container must run as a non-root user                                                                   | `true`                    |
| `updater.securityContext.runAsUser`                | The UID to run the entrypoint of the container process                                                              | `65534`                   |
| `updater.securityContext.runAsGroup`               | The GID to run the entrypoint of the container process                                                              | `65534`                   |
| `updater.containerPorts.metrics`                   | Container port for Metrics                                                                                          | `8943`                    |
| `updater.livenessProbe.enabled`                    | Enable liveness probe                                                                                               | `true`                    |
| `updater.livenessProbe.initialDelaySeconds`        | Delay before the liveness probe is initiated                                                                        | `0`                       |
| `updater.livenessProbe.periodSeconds`              | How often to perform the liveness probe                                                                             | `10`                      |
| `updater.livenessProbe.timeoutSeconds`             | When the liveness probe times out                                                                                   | `1`                       |
| `updater.livenessProbe.failureThreshold`           | Minimum consecutive failures for the liveness probe to be considered failed after having succeeded                  | `3`                       |
| `updater.livenessProbe.successThreshold`           | Minimum consecutive successes for the liveness probe to be considered successful after having failed                | `1`                       |
| `updater.readinessProbe.enabled`                   | Enable readiness probe                                                                                              | `true`                    |
| `updater.readinessProbe.initialDelaySeconds`       | Delay before the readiness probe is initiated                                                                       | `0`                       |
| `updater.readinessProbe.periodSeconds`             | How often to perform the readiness probe                                                                            | `10`                      |
| `updater.readinessProbe.timeoutSeconds`            | When the readiness probe times out                                                                                  | `1`                       |
| `updater.readinessProbe.failureThreshold`          | Minimum consecutive failures for the readiness probe to be considered failed after having succeeded                 | `3`                       |
| `updater.readinessProbe.successThreshold`          | Minimum consecutive successes for the readiness probe to be considered successful after having failed               | `1`                       |
| `updater.startupProbe.enabled`                     | Enable startup probe                                                                                                | `false`                   |
| `updater.startupProbe.initialDelaySeconds`         | Delay before the startup probe is initiated                                                                         | `0`                       |
| `updater.startupProbe.periodSeconds`               | How often to perform the startup probe                                                                              | `10`                      |
| `updater.startupProbe.timeoutSeconds`              | When the startup probe times out                                                                                    | `1`                       |
| `updater.startupProbe.failureThreshold`            | Minimum consecutive failures for the startup probe to be considered failed after having succeeded                   | `3`                       |
| `updater.startupProbe.successThreshold`            | Minimum consecutive successes for the startup probe to be considered successful after having failed                 | `1`                       |
| `updater.resources`                                | CPU/Memory resource requests/limits                                                                                 | `{}`                      |
| `updater.nodeSelector`                             | Node labels for pod assignment                                                                                      | `{}`                      |
| `updater.tolerations`                              | Tolerations for pod assignment                                                                                      | `[]`                      |
| `updater.affinity`                                 | Map of node/pod affinities                                                                                          | `{}`                      |
| `updater.extraArgs`                                | Additional container arguments                                                                                      |                           |
| `updater.extraArgs.v`                              | Number for the log level verbosity                                                                                  | `2`                       |
| `updater.extraEnvVars`                             | Additional container environment variables                                                                          | `[]`                      |
| `updater.extraEnvVarsCM`                           | Name of existing ConfigMap containing additional container environment variables                                    | `nil`                     |
| `updater.extraEnvVarsSecret`                       | Name of existing Secret containing additional container environment variables                                       | `nil`                     |
| `updater.extraVolumes`                             | Optionally specify extra list of additional volumes                                                                 | `[]`                      |
| `updater.extraVolumeMounts`                        | Optionally specify extra list of additional volumeMounts                                                            | `[]`                      |
| `updater.metrics.service.annotations`              | Metrics service annotations                                                                                         | `{}`                      |
| `updater.metrics.service.type`                     | Metrics service type                                                                                                | `ClusterIP`               |
| `updater.metrics.service.clusterIP`                | Metrics static cluster IP address or None for headless service when service type is ClusterIP                       | `nil`                     |
| `updater.metrics.service.ipFamilyPolicy`           | Metrics service IP family policy                                                                                    | `""`                      |
| `updater.metrics.service.ipFamilies`               | Metrics service IP families                                                                                         | `[]`                      |
| `updater.metrics.service.ports.metrics`            | Metrics service port for Metrics                                                                                    | `8943`                    |
| `updater.metrics.serviceMonitor.enabled`           | Specifies whether a service monitor should be created                                                               | `false`                   |
| `updater.metrics.serviceMonitor.namespace`         | Namespace in which to create the service monitor                                                                    | `""`                      |
| `updater.metrics.serviceMonitor.annotations`       | Service monitor annotations                                                                                         | `{}`                      |
| `updater.metrics.serviceMonitor.labels`            | Additional service monitor labels                                                                                   | `{}`                      |
| `updater.metrics.serviceMonitor.jobLabel`          | The name of the label on the target service to use as the job name in Prometheus                                    | `""`                      |
| `updater.metrics.serviceMonitor.honorLabels`       | Whether to choose the metric’s labels on collisions with target labels                                              | `false`                   |
| `updater.metrics.serviceMonitor.interval`          | Interval at which metrics should be scraped                                                                         | `""`                      |
| `updater.metrics.serviceMonitor.scrapeTimeout`     | Timeout after which the scrape is ended                                                                             | `""`                      |
| `updater.metrics.serviceMonitor.metricRelabelings` | Specify additional relabeling of metrics                                                                            | `[]`                      |
| `updater.metrics.serviceMonitor.relabelings`       | Specify general relabeling                                                                                          | `[]`                      |

### CRDs parameters

| Name                                            | Description                                               | Default                 |
| ----------------------------------------------- | --------------------------------------------------------- | ----------------------- |
| `crds.enabled`                                  | Enable CRDs                                               | `true`                  |
| `crds.image.registry`                           | Image registry                                            | `docker.io`             |
| `crds.image.repository`                         | Image repository                                          | `bitnamilegacy/kubectl` |
| `crds.image.tag`                                | Image tag                                                 | `1.29.3`                |
| `crds.image.digest`                             | Image digest                                              | `""`                    |
| `crds.image.pullPolicy`                         | Image pull policy                                         | `IfNotPresent`          |
| `crds.podAnnotations`                           | Additional pod annotations                                | `{}`                    |
| `crds.podLabels`                                | Additional pod labels                                     | `{}`                    |
| `crds.podSecurityContext`                       | Pod security context                                      |                         |
| `crds.podSecurityContext.seccompProfile.type`   | Set pod's Security Context seccomp profile                | `RuntimeDefault`        |
| `crds.securityContext`                          | Container security context                                |                         |
| `crds.securityContext.allowPrivilegeEscalation` | Set container's Security Context allowPrivilegeEscalation | `false`                 |
| `crds.securityContext.capabilities.drop`        | List of capabilities to be dropped                        | `["ALL"]`               |
| `crds.securityContext.readOnlyRootFilesystem`   | Set container's Security Context readOnlyRootFilesystem   | `true`                  |
| `crds.securityContext.runAsNonRoot`             | Whether the container must run as a non-root user         | `true`                  |
| `crds.securityContext.runAsUser`                | The UID to run the entrypoint of the container process    | `65534`                 |
| `crds.securityContext.runAsGroup`               | The GID to run the entrypoint of the container process    | `65534`                 |
| `crds.resources`                                | CPU/Memory resource requests/limits                       | `{}`                    |
| `crds.nodeSelector`                             | Node labels for pod assignment                            | `{}`                    |
| `crds.tolerations`                              | Tolerations for pod assignment                            | `[]`                    |
| `crds.affinity`                                 | Map of node/pod affinities                                | `{}`                    |

### Tests parameters

| Name                     | Description       | Default              |
| ------------------------ | ----------------- | -------------------- |
| `tests.image.registry`   | Image registry    | `ghcr.io`            |
| `tests.image.repository` | Image repository  | `cowboysysop/pytest` |
| `tests.image.tag`        | Image tag         | `1.2.0`              |
| `tests.image.digest`     | Image digest      | `""`                 |
| `tests.image.pullPolicy` | Image pull policy | `IfNotPresent`       |

## Setting parameters

Specify the parameters you which to customize using the `--set` argument to the `helm install` command. For instance,

```bash
$ helm install my-release \
    --set nameOverride=my-name cowboysysop/vertical-pod-autoscaler
```

or for an OCI-based registry:

```bash
$ helm install my-release \
    --set nameOverride=my-name oci://ghcr.io/cowboysysop/charts/vertical-pod-autoscaler
```

The above command sets the `nameOverride` to `my-name`.

Alternatively, a YAML file that specifies the values for the above parameters can be provided while installing the chart. For example,

```bash
$ helm install my-release \
    --values values.yaml cowboysysop/vertical-pod-autoscaler
```

or for an OCI-based registry:

```bash
$ helm install my-release \
    --values values.yaml oci://ghcr.io/cowboysysop/charts/vertical-pod-autoscaler
```

**TIP**: You can use the default [values.yaml](values.yaml).

## Limitations

Due to hard-coded values in Vertical Pod Autoscaler, the chart configuration has some limitations:

- Admission controller component service name is `vpa-webhook`
- Admission controller component service port is `443`
- Mutating webhook configuration name automatically created by admission controller component is `vpa-webhook-config`

## License

The source code of this chart is under [MIT License](LICENSE).

It also uses source code under Apache 2.0 License from the [Bitnami repository](https://github.com/bitnami/charts).
