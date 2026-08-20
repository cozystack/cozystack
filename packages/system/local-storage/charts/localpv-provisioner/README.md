#  OpenEBS LocalPV Provisioner

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
![Chart Lint and Test](https://github.com/openebs/dynamic-localpv-provisioner/workflows/Chart%20Lint%20and%20Test/badge.svg)
![Release Charts](https://github.com/openebs/dynamic-localpv-provisioner/workflows/Release%20Charts/badge.svg?branch=develop)

A Helm chart for openebs dynamic localpv provisioner. This chart bootstraps OpenEBS Dynamic LocalPV provisioner deployment on a [Kubernetes](http://kubernetes.io) cluster using the  [Helm](https://helm.sh) package manager.


**Homepage:** <http://www.openebs.io/>

## Get Repo Info

```console
helm repo add openebs-localpv https://openebs.github.io/dynamic-localpv-provisioner
helm repo update
```

_See [helm repo](https://helm.sh/docs/helm/helm_repo/) for command documentation._

## Install Chart

Please visit the [link](https://openebs.github.io/dynamic-localpv-provisioner/) for install instructions via helm3.

```console
# Helm
helm install [RELEASE_NAME] openebs-localpv/localpv-provisioner --namespace [NAMESPACE] --create-namespace
```

_See [configuration](#configuration) below._

_See [helm install](https://helm.sh/docs/helm/helm_install/) for command documentation._

## Uninstall Chart

```console
# Helm
helm uninstall [RELEASE_NAME] --namespace [NAMESPACE]
```

This removes all the Kubernetes components associated with the chart and deletes the release.

_See [helm uninstall](https://helm.sh/docs/helm/helm_uninstall/) for command documentation._

## Upgrading Chart

```console
# Helm
helm upgrade [RELEASE_NAME] [CHART] --install --namespace [NAMESPACE]
```


## Configuration

The following table lists the configurable parameters of the OpenEBS Dynamic LocalPV Provisioner chart and their default values.

You can modify different parameters by specifying the desired value in the `helm install` command by using the `--set` and/or the `--set-string` flag(s).

```console
helm install openebs-localpv openebs-localpv/localpv-provisioner --namespace openebs --create-namespace
```

Sample command to install the provisioner with nodeAffinityLabels "openebs.io/node-affinity-key-1" and "openebs.io/node-affinity-key-2" on the hostpath StorageClass:
```console
helm install openebs-localpv openebs-localpv/localpv-provisioner --namespace openebs --create-namespace \
	--set-string hostpathClass.nodeAffinityLabels="{openebs.io/node-affinity-key-1,openebs.io/node-affinity-key-2}"
```

| Parameter                                 | Description                                                                                                                        | Default                       |
|-------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------|-------------------------------|
| `globalImageRegistryOverride`             | When set to true, the value of `global.imageRegistry` will override all image registry settings defined at local level             | `"false"`                     |
| `global.imagePullSecrets`                 | Global image pull secrets (merged with local imagePullSecrets and not overridden)                                                  | `[]`                          |
| `global.imagePullPolicy`                  | Global override for image pull policy                                                                                              | `""`                          |
| `global.analytics.enabled`                | Global override for analytics                                                                                                      | `null`                        |
| `analytics.enabled`                       | Enable sending stats to Google Analytics                                                                                           | `true`                        |
| `analytics.pingInterval`                  | Duration(hours) between sending ping stat                                                                                          | `24h`                         |
| `extraLabels`                             | Additional labels to add to all chart resources                                                                                    | `{}`                          |
| `global.imageRegistry`                    | Global override for container image registry (when `globalImageRegistryOverride` is set to true)                                   | `"docker.io"`                 |
| `helperPod.image.registry`                | Registry for helper image                                                                                                          | `""`                          |
| `helperPod.image.repository`              | Image for helper pod                                                                                                               | `"openebs/linux-utils"`       |
| `helperPod.image.pullPolicy`              | Pull policy for helper pod                                                                                                         | `IfNotPresent`                |
| `helperPod.image.tag`                     | Image tag for helper image                                                                                                         | `4.5.0`                       |
| `helperPod.timeoutSecs`                   | The maximum number of seconds to wait for a helperPod (init, cleanup, quota) to complete                                           | `120`                         |
| `hostpathClass.basePath`                  | BasePath for openebs-hostpath StorageClass                                                                                         | `"/var/openebs/local"`        |
| `hostpathClass.enabled`                   | Enables creation of default Hostpath StorageClass                                                                                  | `true`                        |
| `hostpathClass.isDefaultClass`            | Make openebs-hostpath the default StorageClass                                                                                     | `"false"`                     |
| `hostpathClass.nodeAffinityLabels`        | Custom node label(or labels) key to uniquely identify nodes. `kubernetes.io/hostname` is the default label key for node selection. | `[]`                          |
| `hostpathClass.xfsQuota.enabled`          | Enable XFS Quota (requires XFS filesystem)                                                                                         | `false`                       |
| `hostpathClass.ext4Quota.enabled`         | Enable EXT4 Quota (requires EXT4 filesystem)                                                                                       | `false`                       |
| `hostpathClass.reclaimPolicy`             | ReclaimPolicy for Hostpath PVs                                                                                                     | `"Delete"`                    |
| `imagePullSecrets`                        | Provides image pull secrect                                                                                                        | `""`                          |
| `localpv.controller.workers`              | The no. of concurrent worker goroutines for processing PVC create/delete events                                                    | `4`                           |
| `localpv.enabled`                         | Enable LocalPV Provisioner                                                                                                         | `true`                        |
| `localpv.image.registry`                  | Registry for LocalPV Provisioner image                                                                                             | `""`                          |
| `localpv.image.repository`                | Image repository for LocalPV Provisioner                                                                                           | `openebs/localpv-provisioner` |
| `localpv.image.pullPolicy`                | Image pull policy for LocalPV Provisioner                                                                                          | `IfNotPresent`                |
| `localpv.image.tag`                         | Image tag for LocalPV Provisioner                                                                                                                                                           | `4.5.1`                       |
| `localpv.updateStrategy.type`             | Update strategy for LocalPV Provisioner                                                                                            | `RollingUpdate`               |
| `localpv.annotations`                     | Annotations for LocalPV Provisioner metadata                                                                                       | `""`                          |
| `localpv.podAnnotations`                  | Annotations for LocalPV Provisioner pods metadata                                                                                  | `""`                          |
| `localpv.privileged`                      | Run LocalPV Provisioner with extra privileges                                                                                      | `true`                        |
| `localpv.resources`                       | Resource and request and limit for containers                                                                                      | `""`                          |
| `localpv.podLabels`                       | Appends labels to the pods                                                                                                         | `""`                          |
| `localpv.nodeSelector`                    | Nodeselector for LocalPV Provisioner pods                                                                                          | `""`                          |
| `localpv.nodeDeployment.enabled`          | Enabled the NodeDeployment mode of installation                                                                                    | `""`                          |
| `localpv.tolerations`                     | LocalPV Provisioner pod toleration values                                                                                          | `""`                          |
| `localpv.securityContext`                 | Seurity context for container                                                                                                      | `""`                          |
| `localpv.healthCheck.initialDelaySeconds` | Delay before liveness probe is initiated                                                                                           | `30`                          |
| `localpv.healthCheck.periodSeconds`       | How often to perform the liveness probe                                                                                            | `60`                          |
| `localpv.replicas`                        | No. of LocalPV Provisioner replica                                                                                                 | `1`                           |
| `localpv.enableLeaderElection`            | Enable leader election                                                                                                             | `true`                        |
| `localpv.affinity`                        | LocalPV Provisioner pod affinity                                                                                                   | `{}`                          |
| `localpv.priorityClassName`               | Sets priorityClassName in pod                                                                                                      | `""`                          |
| `rbac.create`                             | Enable RBAC Resources                                                                                                              | `true`                        |
| `rbac.pspEnabled`                         | Create pod security policy resources                                                                                               | `false`                       |


A YAML file that specifies the values for the parameters can be provided while installing the chart. For example,

```bash
helm install <release-name> -f values.yaml --namespace openebs openebs-localpv/localpv-provisioner
```

> **Tip**: You can use the default [values.yaml](values.yaml)
