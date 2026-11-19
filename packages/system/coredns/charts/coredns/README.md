# CoreDNS

[CoreDNS](https://coredns.io/) is a DNS server that chains plugins and provides DNS Services

# TL;DR;

```console
$ helm repo add coredns https://coredns.github.io/helm
$ helm --namespace=kube-system install coredns coredns/coredns
```

## Introduction

This chart bootstraps a [CoreDNS](https://github.com/coredns/coredns) deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager. This chart will provide DNS Services and can be deployed in multiple configuration to support various scenarios listed below:

- CoreDNS as a cluster dns service and a drop-in replacement for Kube/SkyDNS. This is the default mode and CoreDNS is deployed as cluster-service in kube-system namespace. This mode is chosen by setting `isClusterService` to true.
- CoreDNS as an external dns service. In this mode CoreDNS is deployed as any kubernetes app in user specified namespace. The CoreDNS service can be exposed outside the cluster by using using either the NodePort or LoadBalancer type of service. This mode is chosen by setting `isClusterService` to false.
- CoreDNS as an external dns provider for kubernetes federation. This is a sub case of 'external dns service' which uses etcd plugin for CoreDNS backend. This deployment mode as a dependency on `etcd-operator` chart, which needs to be pre-installed.

## Prerequisites

- Kubernetes 1.10 or later

## Installing the Chart

The chart can be installed as follows:

```console
$ helm repo add coredns https://coredns.github.io/helm
$ helm --namespace=kube-system install coredns coredns/coredns
```

The command deploys CoreDNS on the Kubernetes cluster in the default configuration. The [configuration](#configuration) section lists various ways to override default configuration during deployment.

> **Tip**: List all releases using `helm list --all-namespaces`

## OCI installing

The chart can also be installed using the following:

```console
$ helm --namespace=kube-system install coredns oci://ghcr.io/coredns/charts/coredns --version 1.38.0
```

The command deploys the `1.38.0` version of CoreDNS on the Kubernetes cluster in the default configuration.

## Helm Unit Testing & Debugging Guide

This document explains how to write, run, and debug Helm unit tests for this chart using [helm-unittest](https://github.com/helm-unittest/helm-unittest).

---

### Prerequisites

Install the Helm unittest plugin:

```bash
helm plugin install https://github.com/helm-unittest/helm-unittest
```

###  Running Unit Tests

Run all unit tests in the chart folder (e.g., `./coredns`):

```bash
helm unittest ./coredns
```

To output results in **JUnit XML** format:

```bash
mkdir -p test-results
helm unittest --strict \
  --output-type JUnit \
  --output-file test-results/helm-unittest-report.xml \
  ./coredns
```

---

### Debugging Helm Charts

Render the chart templates with real values to debug:

```bash
helm template ./coredns  --debug
```
## YAML Intellisense in VS Code

Add this line at the top of your unit test YAML files for schema validation and autocompletion in VS Code:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/helm-unittest/helm-unittest/main/schema/helm-testsuite.json
```

This improves YAML editing and error highlighting for test definitions.

## Uninstalling the Chart

To uninstall/delete the `coredns` deployment:

```console
$ helm uninstall coredns
```

The command removes all the Kubernetes components associated with the chart and deletes the release.

## Configuration

| Parameter                                      | Description                                                                                                                               | Default                                                      |
| :--------------------------------------------- |:------------------------------------------------------------------------------------------------------------------------------------------| :----------------------------------------------------------- |
| `image.repository`                             | The image repository to pull from                                                                                                         | coredns/coredns                                              |
| `image.tag`                                    | The image tag to pull from (derived from Chart.yaml)                                                                                      | ``                                                      |
| `image.pullPolicy`                             | Image pull policy                                                                                                                         | IfNotPresent                                                 |
| `image.pullSecrets`                            | Specify container image pull secrets                                                                                                      | `[]`                                                         |
| `replicaCount`                                 | Number of replicas                                                                                                                        | 1                                                            |
| `resources.limits.cpu`                         | Container maximum CPU                                                                                                                     | `100m`                                                       |
| `resources.limits.memory`                      | Container maximum memory                                                                                                                  | `128Mi`                                                      |
| `resources.requests.cpu`                       | Container requested CPU                                                                                                                   | `100m`                                                       |
| `resources.requests.memory`                    | Container requested memory                                                                                                                | `128Mi`                                                      |
| `serviceType`                                  | Kubernetes Service type                                                                                                                   | `ClusterIP`                                                  |
| `prometheus.service.enabled`                   | Set this to `true` to create Service for Prometheus metrics                                                                               | `false`                                                      |
| `prometheus.service.annotations`               | Annotations to add to the metrics Service                                                                                                 | `{prometheus.io/scrape: "true", prometheus.io/port: "9153"}` |
| `prometheus.service.selector`                  | Pod selector                                                                                                                              | `{}`                                                         |
| `prometheus.monitor.enabled`                   | Set this to `true` to create ServiceMonitor for Prometheus operator                                                                       | `false`                                                      |
| `prometheus.monitor.additionalLabels`          | Additional labels that can be used so ServiceMonitor will be discovered by Prometheus                                                     | {}                                                           |
| `prometheus.monitor.namespace`                 | Selector to select which namespaces the Endpoints objects are discovered from.                                                            | `""`                                                         |
| `prometheus.monitor.interval`                  | Scrape interval for polling the metrics endpoint. (E.g. "30s")                                                                            | `""`                                                         |
| `prometheus.monitor.scrapeTimeout`             | Timeout for each scrape request. (E.g. "10s")                                                                                             | `""`                                                         |
| `prometheus.monitor.scheme`                    | HTTP scheme used for scraping. (`http` or `https`)                                                                                        | `""`                                                         |
| `prometheus.monitor.honorLabels`               | Honor labels from the target when scraping                                                                                                | `false`                                                      |
| `prometheus.monitor.tlsConfig`                 | TLS configuration for the metrics endpoint (e.g. `insecureSkipVerify`, certificates)                                                     | `{}`                                                         |
| `prometheus.monitor.relabelings`               | List of target relabeling rules applied before scraping                                                                                   | `[]`                                                         |
| `prometheus.monitor.metricRelabelings`         | List of metric relabeling rules applied after scraping                                                                                    | `[]`                                                         |
| `prometheus.monitor.selector`                  | Service selector                                                                                                                          | `{}`                                                         |
| `service.clusterIP`                            | IP address to assign to service                                                                                                           | `""`                                                         |
| `service.clusterIPs`                           | IP addresses to assign to service                                                                                                         | `[]`                                                         |
| `service.loadBalancerIP`                       | IP address to assign to load balancer (if supported)                                                                                      | `""`                                                         |
| `service.externalIPs`                          | External IP addresses                                                                                                                     | []                                                           |
| `service.externalTrafficPolicy`                | Enable client source IP preservation                                                                                                      | []                                                           |
| `service.ipFamilyPolicy`                       | Service dual-stack policy                                                                                                                 | `""`                                                         |
| `service.annotations`                          | Annotations to add to service                                                                                                             | {}                                                           |
| `service.selector`                             | Pod selector                                                                                                                              | `{}`                                                         |
| `service.trafficDistribution`                  | Service traffic routing strategy                                                                                                          |                                                              |
| `serviceAccount.create`                        | If true, create & use serviceAccount                                                                                                      | false                                                        |
| `serviceAccount.name`                          | If not set & create is true, use template fullname                                                                                        |                                                              |
| `rbac.create`                                  | If true, create & use RBAC resources                                                                                                      | true                                                         |
| `rbac.pspEnable`                               | Specifies whether a PodSecurityPolicy should be created.                                                                                  | `false`                                                      |
| `rbac.multiclusterEnable`                      | Specifies whether the kubernetes plugin multicluster RBAC should be created.                                                              | `false`                                                      |
| `isClusterService`                             | Specifies whether chart should be deployed as cluster-service or normal k8s app.                                                          | true                                                         |
| `priorityClassName`                            | Name of Priority Class to assign pods                                                                                                     | `""`                                                         |
| `securityContext`                              | securityContext definition for pods                                                                                                       | capabilities.add.NET_BIND_SERVICE                            |
| `servers`                                      | Configuration for CoreDNS and plugins                                                                                                     | See values.yml                                               |
| `livenessProbe.enabled`                        | Enable/disable the Liveness probe                                                                                                         | `true`                                                       |
| `livenessProbe.initialDelaySeconds`            | Delay before liveness probe is initiated                                                                                                  | `60`                                                         |
| `livenessProbe.periodSeconds`                  | How often to perform the probe                                                                                                            | `10`                                                         |
| `livenessProbe.timeoutSeconds`                 | When the probe times out                                                                                                                  | `5`                                                          |
| `livenessProbe.failureThreshold`               | Minimum consecutive failures for the probe to be considered failed after having succeeded.                                                | `5`                                                          |
| `livenessProbe.successThreshold`               | Minimum consecutive successes for the probe to be considered successful after having failed.                                              | `1`                                                          |
| `readinessProbe.enabled`                       | Enable/disable the Readiness probe                                                                                                        | `true`                                                       |
| `readinessProbe.initialDelaySeconds`           | Delay before readiness probe is initiated                                                                                                 | `30`                                                         |
| `readinessProbe.periodSeconds`                 | How often to perform the probe                                                                                                            | `10`                                                         |
| `readinessProbe.timeoutSeconds`                | When the probe times out                                                                                                                  | `5`                                                          |
| `readinessProbe.failureThreshold`              | Minimum consecutive failures for the probe to be considered failed after having succeeded.                                                | `5`                                                          |
| `readinessProbe.successThreshold`              | Minimum consecutive successes for the probe to be considered successful after having failed.                                              | `1`                                                          |
| `affinity`                                     | Affinity settings for pod assignment                                                                                                      | {}                                                           |
| `nodeSelector`                                 | Node labels for pod assignment                                                                                                            | {}                                                           |
| `tolerations`                                  | Tolerations for pod assignment                                                                                                            | []                                                           |
| `zoneFiles`                                    | Configure custom Zone files                                                                                                               | []                                                           |
| `extraContainers`                              | Optional array of sidecar containers                                                                                                      | []                                                           |
| `extraVolumes`                                 | Optional array of volumes to create                                                                                                       | []                                                           |
| `extraVolumeMounts`                            | Optional array of volumes to mount inside the CoreDNS container                                                                           | []                                                           |
| `extraSecrets`                                 | Optional array of secrets to mount inside the CoreDNS container                                                                           | []                                                           |
| `env`                                          | Optional array of environment variables for CoreDNS container                                                                             | []                                                           |
| `customLabels`                                 | Optional labels for Deployment(s), Pod, Service, ServiceMonitor objects                                                                   | {}                                                           |
| `customAnnotations`                            | Optional annotations for Deployment(s), Pod, Service, ServiceMonitor objects                                                              |
| `rollingUpdate.maxUnavailable`                 | Maximum number of unavailable replicas during rolling update                                                                              | `1`                                                          |
| `rollingUpdate.maxSurge`                       | Maximum number of pods created above desired number of pods                                                                               | `25%`                                                        |
| `podDisruptionBudget`                          | Optional PodDisruptionBudget                                                                                                              | {}                                                           |
| `podAnnotations`                               | Optional Pod only Annotations                                                                                                             | {}                                                           |
| `podLabels`                                    | Optional Pod only Labels                                                                                                                  | {}                                                           |
| `dnsConfig`                                    | Optional Pod DNS configuration. When set, `dnsPolicy` is automatically set to `None`                                                      | {}                                                           |
| `terminationGracePeriodSeconds`                | Optional duration in seconds the pod needs to terminate gracefully.                                                                       | 30                                                           |
| `hpa.enabled`                                  | Enable Hpa autoscaler instead of proportional one                                                                                         | `false`                                                      |
| `hpa.minReplicas`                              | Hpa minimum number of CoreDNS replicas                                                                                                    | `1`                                                          |
| `hpa.maxReplicas`                              | Hpa maximum number of CoreDNS replicas                                                                                                    | `2`                                                          |
| `hpa.metrics`                                  | Metrics definitions used by Hpa to scale up and down                                                                                      | {}                                                           |
| `autoscaler.enabled`                           | Optionally enabled a cluster-proportional-autoscaler for CoreDNS                                                                          | `false`                                                      |
| `autoscaler.coresPerReplica`                   | Number of cores in the cluster per CoreDNS replica                                                                                        | `256`                                                        |
| `autoscaler.nodesPerReplica`                   | Number of nodes in the cluster per CoreDNS replica                                                                                        | `16`                                                         |
| `autoscaler.min`                               | Min size of replicaCount                                                                                                                  | 0                                                            |
| `autoscaler.max`                               | Max size of replicaCount                                                                                                                  | 0 (aka no max)                                               |
| `autoscaler.includeUnschedulableNodes`         | Should the replicas scale based on the total number or only schedulable nodes                                                             | `false`                                                      |
| `autoscaler.preventSinglePointFailure`         | If true does not allow single points of failure to form                                                                                   | `true`                                                       |
| `autoscaler.customFlags`                       | A list of custom flags to pass into cluster-proportional-autoscaler                                                                       | (no args)                                                    |
| `autoscaler.image.repository`                  | The image repository to pull autoscaler from                                                                                              | registry.k8s.io/cpa/cluster-proportional-autoscaler          |
| `autoscaler.image.tag`                         | The image tag to pull autoscaler from                                                                                                     | `1.8.5`                                                      |
| `autoscaler.image.pullPolicy`                  | Image pull policy for the autoscaler                                                                                                      | IfNotPresent                                                 |
| `autoscaler.image.pullSecrets`                 | Specify container image pull secrets                                                                                                      | `[]`                                                         |
| `autoscaler.priorityClassName`                 | Optional priority class for the autoscaler pod. `priorityClassName` used if not set.                                                      | `""`                                                         |
| `autoscaler.affinity`                          | Affinity settings for pod assignment for autoscaler                                                                                       | {}                                                           |
| `autoscaler.nodeSelector`                      | Node labels for pod assignment for autoscaler                                                                                             | {}                                                           |
| `autoscaler.tolerations`                       | Tolerations for pod assignment for autoscaler                                                                                             | []                                                           |
| `autoscaler.resources.limits.cpu`              | Container maximum CPU for cluster-proportional-autoscaler                                                                                 | `20m`                                                        |
| `autoscaler.resources.limits.memory`           | Container maximum memory for cluster-proportional-autoscaler                                                                              | `10Mi`                                                       |
| `autoscaler.resources.requests.cpu`            | Container requested CPU for cluster-proportional-autoscaler                                                                               | `20m`                                                        |
| `autoscaler.resources.requests.memory`         | Container requested memory for cluster-proportional-autoscaler                                                                            | `10Mi`                                                       |
| `autoscaler.configmap.annotations`             | Annotations to add to autoscaler config map. For example to stop CI renaming them                                                         | {}                                                           |
| `autoscaler.livenessProbe.enabled`             | Enable/disable the Liveness probe                                                                                                         | `true`                                                       |
| `autoscaler.livenessProbe.initialDelaySeconds` | Delay before liveness probe is initiated                                                                                                  | `10`                                                         |
| `autoscaler.livenessProbe.periodSeconds`       | How often to perform the probe                                                                                                            | `5`                                                          |
| `autoscaler.livenessProbe.timeoutSeconds`      | When the probe times out                                                                                                                  | `5`                                                          |
| `autoscaler.livenessProbe.failureThreshold`    | Minimum consecutive failures for the probe to be considered failed after having succeeded.                                                | `3`                                                          |
| `autoscaler.livenessProbe.successThreshold`    | Minimum consecutive successes for the probe to be considered successful after having failed.                                              | `1`                                                          |
| `autoscaler.extraContainers`                   | Optional array of sidecar containers                                                                                                      | []                                                           |
| `deployment.enabled`                           | Optionally disable the main deployment and its respective resources.                                                                      | `true`                                                       |
| `deployment.name`                              | Name of the deployment if `deployment.enabled` is true. Otherwise the name of an existing deployment for the autoscaler or HPA to target. | `""`                                                         |
| `deployment.annotations`                       | Annotations to add to the main deployment                                                                                                 | `{}`                                                         |
| `deployment.selector`                          | Pod selector                                                                                                                              | `{}`                                                         |
| `deployment.dnsPolicy`                         | DNS policy for the pod (Default, ClusterFirst, ClusterFirstWithHostNet, None)                                                             | `"Default"`                                                  |
| `deployment.dnsConfig`                         | Custom DNS configuration (nameservers, searches, options). Required when dnsPolicy is "None"                                              | `{}`                                                         |
| `clusterRole.nameOverride`                     | ClusterRole name override                                                                                                                 |                                                              |

See `values.yaml` for configuration notes. Specify each parameter using the `--set key=value[,key=value]` argument to `helm install`. For example,

```console
$ helm install coredns \
  coredns/coredns \
  --set rbac.create=false
```

The above command disables automatic creation of RBAC rules.

Alternatively, a YAML file that specifies the values for the above parameters can be provided while installing the chart. For example,

```console
$ helm install coredns coredns/coredns -f values.yaml
```

> **Tip**: You can use the default [values.yaml](/charts/coredns/values.yaml)

## Caveats

The chart will automatically determine which protocols to listen on based on
the protocols you define in your zones. This means that you could potentially
use both "TCP" and "UDP" on a single port.
Some cloud environments like "GCE" or "Azure container service" cannot
create external loadbalancers with both "TCP" and "UDP" protocols. So
When deploying CoreDNS with `serviceType="LoadBalancer"` on such cloud
environments, make sure you do not attempt to use both protocols at the same
time.

## Autoscaling

By setting `autoscaler.enabled = true` a
[cluster-proportional-autoscaler](https://github.com/kubernetes-incubator/cluster-proportional-autoscaler)
will be deployed. This will default to a coredns replica for every 256 cores, or
16 nodes in the cluster. These can be changed with `autoscaler.coresPerReplica`
and `autoscaler.nodesPerReplica`. When cluster is using large nodes (with more
cores), `coresPerReplica` should dominate. If using small nodes,
`nodesPerReplica` should dominate.

This also creates a ServiceAccount, ClusterRole, and ClusterRoleBinding for
the autoscaler deployment.

`replicaCount` is ignored if this is enabled.

By setting `hpa.enabled = true` a [Horizontal Pod Autoscaler](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/)
is enabled for Coredns deployment. This can scale number of replicas based on meitrics
like CpuUtilization, MemoryUtilization or Custom ones.

## Adopting existing CoreDNS resources

If you do not want to delete the existing CoreDNS resources in your cluster, you can adopt the resources into a release as of Helm 3.2.0.

You will also need to annotate and label your existing resources to allow Helm to assume control of them. See: https://github.com/helm/helm/pull/7649

```
annotations:
  meta.helm.sh/release-name: your-release-name
  meta.helm.sh/release-namespace: your-release-namespace
labels:
  app.kubernetes.io/managed-by: Helm
```

Once you have annotated and labeled all the resources this chart specifies, you may need to locally template the chart and compare against existing manifest to ensure there are no changes/diffs.s If
you have been careful this should not diff and leave all the resources unmodified and now under management of helm.

Some values to investigate to help adopt your existing manifests to the Helm release are:

- k8sAppLabelOverride
- service.name
- customLabels

In some cases, you will need to orphan delete your existing deployment since selector labels are immutable.

```
kubectl delete deployment coredns --cascade=orphan
```

This will delete the deployment and leave the replicaset to ensure no downtime in the cluster. You will need to manually delete the replicaset AFTER Helm has released a new deployment.

Here is an example script to modify the annotations and labels of existing resources:

WARNING: Substitute YOUR_HELM_RELEASE_NAME_HERE with the name of your helm release.

```
#!/usr/bin/env bash

set -euo pipefail

for kind in config service serviceAccount; do
    echo "setting annotations and labels on $kind/coredns"
    kubectl -n kube-system annotate --overwrite $kind coredns meta.helm.sh/release-name=YOUR_HELM_RELEASE_NAME_HERE
    kubectl -n kube-system annotate --overwrite $kind coredns meta.helm.sh/release-namespace=kube-system
    kubectl -n kube-system label --overwrite $kind coredns app.kubernetes.io/managed-by=Helm
done
```

NOTE: Sometimes, previous deployments of kube-dns that have been migrated to CoreDNS still use kube-dns for the service name as well.

```
echo "setting annotations and labels on service/kube-dns"
kubectl -n kube-system annotate --overwrite service kube-dns meta.helm.sh/release-name=YOUR_HELM_RELEASE_NAME_HERE
kubectl -n kube-system annotate --overwrite service kube-dns meta.helm.sh/release-namespace=kube-system
kubectl -n kube-system label --overwrite service kube-dns app.kubernetes.io/managed-by=Helm
```
