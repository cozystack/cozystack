# linstor-scheduler

Deploys a new Kubernetes scheduler, extended by
the [linstor-scheduler-extender](https://github.com/piraeusdatastore/linstor-scheduler-extender).

> [!IMPORTANT]
> The LINSTOR Scheduler is no longer maintained. Prefer using `volumeBindingMode: WaitForFirstConsumer` on your
> StorageClasses.

The schedule is volume placement aware. That means that it prefers placing Pods on the same nodes as any Persistent
Volume they might use. This works for any setup using LINSTOR, i.e. Piraeus Datastore or LINBIT SDS.

## Installation

The scheduler is meant to be installed in the same namespace as LINSTOR itself, otherwise additional steps may be
required.

If installed along side Piraeus Operator, the LINSTOR endpoint is determined automatically. Otherwise, you need
to set `linstor.endpoint` and `linstor.clientSecret` values as appropriate.

The following command will install the scheduler for a typical Piraeus Data-Store configuration with TLS enabled:

```
helm repo add piraeus-charts https://piraeus.io/helm-charts/
helm install linstor-scheduler piraeus-charts/linstor-scheduler --set linstorEndpoint=https://piraeus-op-cs.piraeus.svc:3371 --set linstorClientSecret=piraeus-client-secret
```

## Usage

To use the scheduler, you need to configure it on your Pods (or Pod templates):

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: some-pod
spec:
  schedulerName: linstor-scheduler
  ...
```

## Configuration

The following options are available:

| Option                                        | Usage                                                                                                  | Default                                                                                            |
|-----------------------------------------------|--------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `replicaCount`                                | Number of replicas to deploy.                                                                          | `1`                                                                                                |
| `linstor.endpoint`                            | URL of the LINSTOR Controller API.                                                                     | `""`                                                                                               |
| `linstor.clientSecret`                        | TLS secret to use to authenticate with the LINSTOR API                                                 | `""`                                                                                               |
| `extender.image.repository`                   | Repository to pull the linstor-scheduler-extender image from.                                          | `quay.io/piraeusdatastore/linstor-scheduler-extender`                                              |
| `extender.image.pullPolicy`                   | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                                 | `IfNotPresent`                                                                                     |
| `extender.image.tag`                          | Override the tag to pull. If not given, defaults to charts `AppVersion`.                               | `""`                                                                                               |
| `extender.resources`                          | Resources to request and limit on the container.                                                       | `{}`                                                                                               |
| `extender.securityContext`                    | Configure container security context. Defaults to dropping all capabilties and running as user 1000.   | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true, runAsNonRoot: true, runAsUser: 1000}` |
| `scheduler.image.repository`                  | Repository to pull the kubernetes scheduler image from.                                                | `registry.k8s.io/kube-scheduler`                                                                   |
| `scheduler.image.pullPolicy`                  | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                                 | `IfNotPresent`                                                                                     |
| `scheduler.image.tag`                         | Override the tag to pull. If not given, defaults to kubernetes version.                                | `""`                                                                                               |
| `scheduler.image.compatibleKubernetesRelease` | Compatible kubernetes version for this scheduler, used to generate configuration in the right version. | `""`                                                                                               |
| `scheduler.resources`                         | Resources to request and limit on the container.                                                       | `{}`                                                                                               |
| `scheduler.securityContext`                   | Configure container security context. Defaults to dropping all capabilties and running as user 1000.   | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true, runAsNonRoot: true, runAsUser: 1000}` |
| `imagePullSecrets`                            | Image pull secrets to add to the deployment.                                                           | `[]`                                                                                               |
| `podAnnotations`                              | Annotations to add to every pod in the deployment.                                                     | `{}`                                                                                               |
| `podSecurityContext`                          | Security context to set on the webhook pod.                                                            | `{}`                                                                                               |
| `nodeSelector`                                | Node selector to add to each webhook pod.                                                              | `{}`                                                                                               |
| `tolerations`                                 | Tolerations to add to each webhook pod.                                                                | `[]`                                                                                               |
| `affinity`                                    | Affinity to set on each webhook pod.                                                                   | `{}`                                                                                               |
| `rbac.create`                                 | Create the necessary roles and bindings for the snapshot controller.                                   | `true`                                                                                             |
| `serviceAccount.create`                       | Create the service account resource                                                                    | `true`                                                                                             |
| `serviceAccount.name`                         | Sets the name of the service account. If left empty, will use the release name as default              | `""`                                                                                               |
| `podDisruptionBudget.enabled`                 | Enable creation of a pod disruption budget to protect the availability of the scheduler                | `true`                                                                                             |
| `autoscaling.enabled`                         | Enable creation of a horizontal pod autoscaler to ensure availability in case of high usage`           | `"false`                                                                                           |
