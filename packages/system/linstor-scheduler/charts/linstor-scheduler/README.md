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
| `autoscaling.enabled`                         | Enable creation of a horizontal pod autoscaler to ensure availability in case of high usage            | `false`                                                                                            |
| `admission.enabled`                           | Deploy the admission webhook that auto-sets `schedulerName` on Pods using LINSTOR-backed PVCs.          | `false`                                                                                            |
| `admission.replicaCount`                      | Number of webhook replicas to deploy.                                                                  | `2`                                                                                                |
| `admission.driver`                            | CSI driver name treated as LINSTOR-backed. Pods whose PVCs resolve to it get the mutation.             | `linstor.csi.linbit.com`                                                                           |
| `admission.failurePolicy`                     | Webhook `failurePolicy`. `Ignore` keeps Pod creation working if the webhook is unavailable.            | `Ignore`                                                                                           |
| `admission.timeoutSeconds`                    | Webhook call timeout. Kept under the Kubernetes default of 10s to limit Pod-creation latency.          | `5`                                                                                                |
| `admission.namespaceSelector`                 | Namespace selector for the webhook. Empty selects all namespaces (including `kube-system`).             | `{}`                                                                                               |
| `admission.resources`                         | Resources to request and limit on the webhook container.                                               | `{}`                                                                                               |
| `admission.podSecurityContext`                | Pod-level security context for the webhook pod.                                                        | `{runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000}`                           |
| `admission.securityContext`                   | Container-level security context for the webhook container.                                            | `{allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true}`     |
| `admission.createTLS`                         | How to provision the webhook TLS: `cert-manager`, `helm`, or `""` (bring your own). See note below.    | `cert-manager`                                                                                     |
| `admission.tls.secretName`                    | Name of the TLS secret. Empty defaults to `<fullname>-admission-tls`.                                   | `""`                                                                                               |
| `admission.tls.caBundle`                      | CA bundle (base64 PEM) injected into the webhook. Only used when `createTLS` is `""`.                   | `""`                                                                                               |
| `admission.tls.certManager.rotationPolicy`    | `privateKey.rotationPolicy` of the cert-manager CA. `Never` keeps the CA key (and caBundle) stable.    | `Never`                                                                                            |
| `admission.affinity`                          | Affinity for the webhook pods. Empty applies a soft anti-affinity spreading replicas across nodes.     | `{}`                                                                                               |
| `admission.reloadOnCertChange`                | Add Stakater Reloader annotations so the pods restart when the TLS secret changes (see note below).    | `false`                                                                                            |

## Admission webhook (optional)

As noted above, the LINSTOR Scheduler itself is no longer maintained and `volumeBindingMode: WaitForFirstConsumer` is the preferred approach for new deployments. For clusters that still run this scheduler, the optional admission webhook (`admission.enabled`, disabled by default) removes the need to set `schedulerName` on every Pod: it watches Pod creation and, when a Pod uses a PersistentVolumeClaim backed by the LINSTOR CSI driver, sets `schedulerName` to this scheduler automatically. It changes nothing when disabled.

> **Note:** `admission.createTLS: helm` regenerates the CA and certificate on every `helm upgrade`; it suits one-shot installs, not GitOps or continuously-reconciled setups (Flux/Argo) where the certificate would churn on each reconcile. Use `cert-manager` or bring your own secret (`""`) there.

> **Note:** the default `failurePolicy: Ignore` fails open, so an unreachable webhook never blocks Pod creation (affected Pods just fall back to the default scheduler). Setting `failurePolicy: Fail` makes the webhook mandatory for every Pod `CREATE` in the selected namespaces; with `createTLS: cert-manager` this also requires the cert-manager CA injector to be running so the `caBundle` is populated, otherwise all matching Pod creation is blocked until injection completes. Because the empty default `namespaceSelector` also intercepts the webhook's own namespace and `kube-system`, running with `failurePolicy: Fail` can deadlock a restart of the webhook (its own new Pods cannot be admitted while all replicas are down) — exclude the release namespace and `kube-system` via `admission.namespaceSelector` if you switch to `Fail`.

> **Note:** the webhook binary loads its serving certificate once at startup and does not hot-reload it. When the certificate is renewed — cert-manager rotates the leaf roughly yearly, or you rotate an external secret — the running Pods keep serving the old certificate until they restart, after which the webhook silently stops mutating. Set `admission.reloadOnCertChange: true` (requires the Stakater Reloader controller) to roll the Deployment automatically when the secret changes, or restart the webhook Pods manually after each renewal. In `createTLS: helm` mode a `helm upgrade` regenerates the certificate and already rolls the Pods.
