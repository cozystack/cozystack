# kamaji

![Version: 0.0.0](https://img.shields.io/badge/Version-0.0.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.0.0](https://img.shields.io/badge/AppVersion-v0.0.0-informational?style=flat-square)

Kamaji is the Hosted Control Plane Manager for Kubernetes.

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Dario Tranchitella | <dario@tranchitella.eu> | <https://clastix.io> |
| Massimiliano Giovagnoli | <me@maxgio.it> |  |
| Adriano Pezzuto | <me@bsctl.io> | <https://clastix.io> |

## Source Code

* <https://github.com/clastix/kamaji>

## Requirements

Kubernetes: `>=1.21.0-0`

| Repository | Name | Version |
|------------|------|---------|
| https://clastix.github.io/charts | kamaji-etcd | >=0.9.2 |

[Kamaji](https://github.com/clastix/kamaji) requires a [multi-tenant `etcd`](https://github.com/clastix/kamaji-internal/blob/master/deploy/getting-started-with-kamaji.md#setup-internal-multi-tenant-etcd) cluster.
This Helm Chart starting from v0.1.1 provides the installation of an internal `etcd` in order to streamline the local test. If you'd like to use an externally managed etcd instance, you can specify the overrides and by setting the value `etcd.deploy=false`.

> For production use an externally managed `etcd` is highly recommended, the `etcd` addon offered by this Chart is not considered production-grade.

## Install Kamaji

To add clastix helm repository:

        helm repo add clastix https://clastix.github.io/charts

To install the Chart with the release name `kamaji`:

        helm upgrade --install --namespace kamaji-system --create-namespace kamaji clastix/kamaji

Show the status:

        helm status kamaji -n kamaji-system

Upgrade the Chart

        helm upgrade kamaji -n kamaji-system clastix/kamaji

Uninstall the Chart

        helm uninstall kamaji -n kamaji-system

## Customize the installation

There are two methods for specifying overrides of values during Chart installation: `--values` and `--set`.

The `--values` option is the preferred method because it allows you to keep your overrides in a YAML file, rather than specifying them all on the command line. Create a copy of the YAML file `values.yaml` and add your overrides to it.

Specify your overrides file when you install the Chart:

        helm upgrade kamaji --install --namespace kamaji-system --create-namespace clastix/kamaji --values myvalues.yaml

The values in your overrides file `myvalues.yaml` will override their counterparts in the Chart's values.yaml file. Any values in `values.yaml` that weren’t overridden will keep their defaults.

If you only need to make minor customizations, you can specify them on the command line by using the `--set` option. For example:

        helm upgrade kamaji --install --namespace kamaji-system --create-namespace clastix/kamaji --set etcd.deploy=false

Here the values you can override:

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Kubernetes affinity rules to apply to Kamaji controller pods |
| defaultDatastoreName | string | `"default"` | If specified, all the Kamaji instances with an unassigned DataStore will inherit this default value. |
| extraArgs | list | `[]` | A list of extra arguments to add to the kamaji controller default ones |
| fullnameOverride | string | `""` |  |
| healthProbeBindAddress | string | `":8081"` | The address the probe endpoint binds to. (default ":8081") |
| image.pullPolicy | string | `"Always"` |  |
| image.repository | string | `"clastix/kamaji"` | The container image of the Kamaji controller. |
| image.tag | string | `nil` | Overrides the image tag whose default is the chart appVersion. |
| imagePullSecrets | list | `[]` |  |
| kamaji-etcd.datastore.enabled | bool | `true` |  |
| kamaji-etcd.datastore.name | string | `"default"` |  |
| kamaji-etcd.deploy | bool | `true` |  |
| kamaji-etcd.fullnameOverride | string | `"kamaji-etcd"` |  |
| livenessProbe | object | `{"httpGet":{"path":"/healthz","port":"healthcheck"},"initialDelaySeconds":15,"periodSeconds":20}` | The livenessProbe for the controller container |
| loggingDevel.enable | bool | `false` | Development Mode defaults(encoder=consoleEncoder,logLevel=Debug,stackTraceLevel=Warn). Production Mode defaults(encoder=jsonEncoder,logLevel=Info,stackTraceLevel=Error) (default false) |
| metricsBindAddress | string | `":8080"` | The address the metric endpoint binds to. (default ":8080") |
| nameOverride | string | `""` |  |
| nodeSelector | object | `{}` | Kubernetes node selector rules to schedule Kamaji controller |
| podAnnotations | object | `{}` | The annotations to apply to the Kamaji controller pods. |
| podSecurityContext | object | `{"runAsNonRoot":true}` | The securityContext to apply to the Kamaji controller pods. |
| readinessProbe | object | `{"httpGet":{"path":"/readyz","port":"healthcheck"},"initialDelaySeconds":5,"periodSeconds":10}` | The readinessProbe for the controller container |
| replicaCount | int | `1` | The number of the pod replicas for the Kamaji controller. |
| resources.limits.cpu | string | `"200m"` |  |
| resources.limits.memory | string | `"100Mi"` |  |
| resources.requests.cpu | string | `"100m"` |  |
| resources.requests.memory | string | `"20Mi"` |  |
| securityContext | object | `{"allowPrivilegeEscalation":false}` | The securityContext to apply to the Kamaji controller container only. It does not apply to the Kamaji RBAC proxy container. |
| serviceAccount.annotations | object | `{}` |  |
| serviceAccount.create | bool | `true` |  |
| serviceAccount.name | string | `"kamaji-controller-manager"` |  |
| serviceMonitor.enabled | bool | `false` | Toggle the ServiceMonitor true if you have Prometheus Operator installed and configured |
| telemetry | object | `{"disabled":false}` | Disable the analytics traces collection |
| temporaryDirectoryPath | string | `"/tmp/kamaji"` | Directory which will be used to work with temporary files. (default "/tmp/kamaji") |
| tolerations | list | `[]` | Kubernetes node taints that the Kamaji controller pods would tolerate |

## Installing and managing etcd as DataStore

Kamaji supports multiple data store, although `etcd` is the default one: thus, an initial cluster will be created upon the Chart installation.

The `DataStore` resource can be configured with the proper values in case of overrides when using a different driver, otherwise all the required data will be inherited by the Chart values.
