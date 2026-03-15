# opensearch-operator

The OpenSearch Operator Helm chart for Kubernetes

## Getting started

The Operator can be easily installed using helm on any CNCF-certified Kubernetes cluster. Please refer to the [User Guide](https://github.com/opensearch-project/opensearch-k8s-operator/blob/main/docs/userguide/main.md) for more information.

### Installation Using Helm

#### Get Repo Info

```shell
helm repo add opensearch-operator https://opensearch-project.github.io/opensearch-k8s-operator/
helm repo update
```

#### Install Chart

```shell
helm install [RELEASE_NAME] opensearch-operator/opensearch-operator
```

#### Uninstall Chart

```shell
helm uninstall [RELEASE_NAME]
```

#### Upgrade Chart

```shell
helm repo update
helm upgrade [RELEASE_NAME] opensearch-operator/opensearch-operator
```

## Values

The following table lists the configurable parameters of the Helm chart.

| Parameter | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `nameOverride` | string | `""` |  |
| `fullnameOverride` | string | `""` |  |
| `podAnnotations` | object | `{}` |  |
| `podLabels` | object | `{}` |  |
| `nodeSelector` | object | `{}` |  |
| `tolerations` | list | `[]` |  |
| `securityContext.runAsNonRoot` | bool | `true` |  |
| `priorityClassName` | string | `""` |  |
| `manager.securityContext.allowPrivilegeEscalation` | bool | `false` |  |
| `manager.extraEnv` | list | `[]` |  |
| `manager.resources.limits.cpu` | string | `"1000m"` |  |
| `manager.resources.limits.memory` | string | `"500Mi"` |  |
| `manager.resources.requests.cpu` | string | `"200m"` |  |
| `manager.resources.requests.memory` | string | `"350Mi"` |  |
| `manager.livenessProbe.failureThreshold` | int | `3` |  |
| `manager.livenessProbe.httpGet.path` | string | `"/healthz"` |  |
| `manager.livenessProbe.httpGet.port` | int | `8081` |  |
| `manager.livenessProbe.periodSeconds` | int | `15` |  |
| `manager.livenessProbe.successThreshold` | int | `1` |  |
| `manager.livenessProbe.timeoutSeconds` | int | `3` |  |
| `manager.livenessProbe.initialDelaySeconds` | int | `10` |  |
| `manager.readinessProbe.failureThreshold` | int | `3` |  |
| `manager.readinessProbe.httpGet.path` | string | `"/readyz"` |  |
| `manager.readinessProbe.httpGet.port` | int | `8081` |  |
| `manager.readinessProbe.periodSeconds` | int | `15` |  |
| `manager.readinessProbe.successThreshold` | int | `1` |  |
| `manager.readinessProbe.timeoutSeconds` | int | `3` |  |
| `manager.readinessProbe.initialDelaySeconds` | int | `10` |  |
| `manager.parallelRecoveryEnabled` | bool | `true` |  |
| `manager.pprofEndpointsEnabled` | bool | `false` |  |
| `manager.image.repository` | string | `"opensearchproject/opensearch-operator"` |  |
| `manager.image.tag` | string | `""` |  |
| `manager.image.pullPolicy` | string | `"Always"` |  |
| `manager.imagePullSecrets` | list | `[]` |  |
| `manager.dnsBase` | string | `"cluster.local"` |  |
| `manager.loglevel` | string | `"info"` |  |
| `manager.watchNamespace` | string | `nil` |  |
| `manager.metricsBindAddress` | string | `"127.0.0.1:8080"` |  |
| `installCRDs` | bool | `true` |  |
| `serviceAccount.create` | bool | `true` |  |
| `serviceAccount.name` | string | `""` |  |
| `useRoleBindings` | bool | `false` |  |
| `webhook.enabled` | bool | `true` |  |
| `webhook.port` | int | `9443` |  |
| `webhook.failurePolicy` | string | `"Fail"` |  |
| `webhook.secretName` | string | `""` |  |
| `webhook.certManager.enabled` | bool | `true` |  |

Specify each parameter using the `--set key=value[,key=value]` argument to `helm install`.

## Namespace-scoped RBAC

By default, the operator uses cluster-scoped RBAC resources (ClusterRole and ClusterRoleBinding). If you want to restrict the operator's permissions to a specific namespace, you can enable namespace-scoped RBAC by setting `useRoleBindings: true`.

### Installation with namespace-scoped RBAC

```shell
helm install opensearch-operator opensearch-operator/opensearch-operator \
  --set useRoleBindings=true \
  --set manager.watchNamespace=<your-namespace>
```

### Important considerations

When `useRoleBindings` is enabled:

- **Manager and proxy roles** will be created as namespace-scoped `Role` resources instead of `ClusterRole`
- **Metrics ClusterRole** will NOT be created, as Kubernetes does not allow namespace-scoped Roles to grant permissions to non-resource URLs (like `/metrics`)
- **Metrics endpoint access**: The operator's `/metrics` endpoint is exposed via the kube-apiserver and requires authentication (via `TokenReviews`) and authorization (via `SubjectAccessReviews`). If you need to access metrics with monitoring tools (e.g., Prometheus), you must manually create the appropriate `ClusterRole` and `ClusterRoleBinding`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: opensearch-operator-metrics-reader
rules:
- nonResourceURLs:
  - /metrics
  verbs:
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: opensearch-operator-metrics-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: opensearch-operator-metrics-reader
subjects:
- kind: ServiceAccount
  name: <your-monitoring-service-account>
  namespace: <monitoring-namespace>
```

Opensearch-operator Helm Chart version: `3.0.1`
