# LINSTOR Affinity Controller

The LINSTOR Affinity Controller keeps the affinity of your volumes in sync between Kubernetes and LINSTOR.

Affinity is used by Kubernetes to track on which node a specific resource can be accessed. For example, you can use
affinity to restrict access to a volume to a specific zone. While this is all supported by Piraeus and LINSTOR, and you
could tune your volumes to support almost any cluster topology, there was one important thing missing: updating affinity
after volume migration.

After the initial PersistentVolume (PV) object in Kubernetes is created, it is not possible to alter the affinity
later[^1]. This becomes a problem if your volumes need to migrate, for example if using ephemeral infrastructure, where
nodes are created and discard on demand. Using a strict affinity setting could mean that your volume is not accessible
from where you want it to: the LINSTOR resource might be there, but Kubernetes will see the volume as only accessible on
some other nodes. So you had to specify a rather relaxed affinity setting for your volumes, at the cost of less optimal
workload placement.

There is one other solution (or rather workaround): recreating your PersistentVolume whenever the backing LINSTOR
resource changed. This is where the LINSTOR Affinity Controller comes in: it automates these required steps, so that
using strict affinity just works. With strict affinity, the Kubernetes scheduler can place workloads on the same nodes
as the volumes they are using, benefiting from local data access for increased read performance.

It also enables strict affinity settings should you use ephemeral infrastructure: even if you rotate out all nodes,
your PV affinity will always match the actual volume placement in LINSTOR.

## Deployment

The best way to deploy the LINSTOR Affinity Controller is by helm chart. If deployed to the same namespace
as [our operator](https://github.com/piraeusdatastore/piraeus-operator) this is quite simple:

```
helm repo add piraeus-charts https://piraeus.io/helm-charts/
helm install linstor-affinity-controller piraeus-charts/linstor-affinity-controller
```

If deploying to a different namespace, ensure that `linstor.endpoint` and `linstor.clientSecret` are set appropriately.
For more information on the available options, see below.

### Options

The following options can be set on the chart:

| Option                        | Usage                                                                                        | Default                                                       |
|-------------------------------|----------------------------------------------------------------------------------------------|---------------------------------------------------------------|
| `replicaCount`                | Number of replicas to deploy.                                                                | `1`                                                           |
| `options.v`                   | Set verbosity for controller                                                                 | `1`                                                           |
| `options.leaderElection`      | Enable leader election to coordinate betwen multiple replicas.                               | `true`                                                        |
| `options.reconcileRate`       | Set the reconcile rate, i.e. how often the cluster state will be checked and updated         | `15s`                                                         |
| `options.resyncRate`          | How often the controller will resync it's internal cache of Kubernetes resources             | `15m`                                                         |
| `options.propertyNamespace`   | Namespace used by LINSTOR CSI to search for node labels.                                     | `""` (auto-detected based on existing node labels on startup) |
| `linstor.endpoint`            | URL of the LINSTOR Controller API.                                                           | `""` (auto-detected when using Piraeus-Operator)              |
| `linstor.clientSecret`        | TLS secret to use to authenticate with the LINSTOR API                                       | `""` (auto-detected when using Piraeus-Operator)              |
| `image.repository`            | Repository to pull the linstor-affinity-controller image from.                               | `quay.io/piraeusdatastore/linstor-affinity-controller`        |
| `image.pullPolicy`            | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                       | `IfNotPresent`                                                |
| `image.tag`                   | Override the tag to pull. If not given, defaults to charts `AppVersion`.                     | `""`                                                          |
| `resources`                   | Resources to request and limit on the container.                                             | `{requests: {cpu: 50m, mem: 100Mi}}`                          |
| `securityContext`             | Configure container security context.                                                        | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true}` |
| `podSecurityContext`          | Security context to set on the pod.                                                          | `{runAsNonRoot: true, runAsUser: 1000}`                       |
| `imagePullSecrets`            | Image pull secrets to add to the deployment.                                                 | `[]`                                                          |
| `podAnnotations`              | Annotations to add to every pod in the deployment.                                           | `{}`                                                          |
| `nodeSelector`                | Node selector to add to a pod.                                                               | `{}`                                                          |
| `tolerations`                 | Tolerations to add to a pod.                                                                 | `[]`                                                          |
| `affinity`                    | Affinity to set on a pod.                                                                    | `{}`                                                          |
| `rbac.create`                 | Create the necessary roles and bindings for the controller.                                  | `true`                                                        |
| `serviceAccount.create`       | Create the service account resource                                                          | `true`                                                        |
| `serviceAccount.name`         | Sets the name of the service account. If left empty, will use the release name as default    | `""`                                                          |
| `podDisruptionBudget.enabled` | Enable creation of a pod disruption budget to protect the availability of the scheduler      | `true`                                                        |
| `autoscaling.enabled`         | Enable creation of a horizontal pod autoscaler to ensure availability in case of high usage` | `"false`                                                      |
| `monitoring.enabled`          | Enable creation of resources for monitoring via Prometheus Operator                          | `"false"`                                                     |

***

[^1]: That is not 100% true: you can _add_ affinity if it was previously unset, but once set, it can't be modified.
