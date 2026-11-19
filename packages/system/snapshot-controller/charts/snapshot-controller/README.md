# snapshot-controller

Deploys the [snapshot-controller](https://github.com/kubernetes-csi/external-snapshotter) and the
[snapshot-validation-webhook](https://github.com/kubernetes-csi/external-snapshotter/#validating-webhook) in a cluster.
The controller is required for CSI snapshotting to work and is not specific to any CSI driver. The webhook is configured
to validate every `VolumeSnapshot` and `VolumeSnapshotContent` resource by sending it to the validation webhook.

While many Kubernetes distributions already package this controller, some do not. If your cluster does ***NOT***
have the following CRDs, you likely also do not have a snapshot controller deployed:

```
kubectl get crd volumesnapshotclasses.snapshot.storage.k8s.io
kubectl get crd volumesnapshots.snapshot.storage.k8s.io
kubectl get crd volumesnapshotcontents.snapshot.storage.k8s.io
```

## Usage

The *snapshot-controller* should be deployed together with the *snapshot-validation-webhook* which can be done by this
simple Helm commands. See [below](#configuration) for available configuration options.

```
helm repo add piraeus-charts https://piraeus.io/helm-charts/
helm install snapshot-controller piraeus-charts/snapshot-controller
```

## Upgrades

Upgrades can be done using the normal Helm upgrade mechanism

```
helm repo update
helm upgrade snapshot-controller piraeus-charts/snapshot-controller
```

To enjoy all the latest features of the snapshot controller, you may want to upgrade your CRDs as well:

```
kubectl replace -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v5.0.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
kubectl replace -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v5.0.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
kubectl replace -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v5.0.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
```

## Upgrade from older CRDs

In an effort to tighten validation, the CSI project started enforcing stricter requirements on `VolumeSnapshot` and
`VolumeSnapshotContent` resources when switching from `v1beta1` to `v1` CRDs. This validation webhook is part of
enforcing these requirements. When upgrading you [have to ensure non of your resources violate the requirements for `v1`].

The upgrade procedure can be summarized by the following steps:

1. Remove the old snapshot controller, if any (since you are upgrading, you probably already have one deployed manually).
2. Install the snapshot controller and the validation webhook using one of the [`3.x.x` releases]:

   ```
   helm install piraeus-charts/snapshot-controller --set controller.image.tag=v3.0.3 --set webhook.image.tag=v3.0.3
   ```
3. Ensure that none of the resources are labelled as invalid:

   ```
   kubectl get volumesnapshots --selector=snapshot.storage.kubernetes.io/invalid-snapshot-resource="" --all-namespaces
   kubectl get volumesnapshotcontents --selector=snapshot.storage.kubernetes.io/invalid-snapshot-resource="" --all-namespaces
   ```

   If the above commands output any resource, they have to be removed

4. Upgrade the CRDs

   ```
   kubectl replace -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v5.0.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
   kubectl replace -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v5.0.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
   kubectl replace -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v5.0.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
   ```

5. Upgrade to the latest version:

   ```
   helm upgrade piraeus-charts/snapshot-controller --set controller.image.tag=v5.0.0 --set webhook.image.tag=v5.0.0
   ```

## Configuration

### Snapshot controller
The following options are available:

| Option                                   | Usage                                                                                                                  | Default                                                                                            |
|------------------------------------------|------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `controller.enabled`                     | Toggle to disable the deployment of the snapshot controller.                                                           | `true`                                                                                             |
| `controller.fullnameOverride`            | Set the base name of deployed resources. Defaults to `snapshot-controller`.                                            | `""`                                                                                               |
| `controller.args`                        | Arguments to pass to the snapshot controller. Note: Keys will be converted to kebab-case, i.e. `oneArg` -> `--one-arg` | `...`                                                                                              |
| `controller.replicaCount`                | Number of replicas to deploy.                                                                                          | `1`                                                                                                |
| `controller.revisionHistoryLimit`        | Number of revisions to keep.                                                                                           | `10`                                                                                               |
| `controller.image.repository`            | Repository to pull the image from.                                                                                     | `registry.k8s.io/sig-storage/snapshot-controller`                                                  |
| `controller.image.pullPolicy`            | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                                                 | `IfNotPresent`                                                                                     |
| `controller.image.tag`                   | Override the tag to pull. If not given, defaults to charts `AppVersion`.                                               | `""`                                                                                               |
| `controller.imagePullSecrets`            | Image pull secrets to add to the deployment.                                                                           | `[]`                                                                                               |
| `controller.podAnnotations`              | Annotations to add to every pod in the deployment.                                                                     | `{}`                                                                                               |
| `controller.podLabels`                   | Labels to add to every pod in the deployment.                                                                          | `{}`                                                                                               |
| `controller.podSecurityContext`          | Security context to set on the webhook pod.                                                                            | `{}`                                                                                               |
| `controller.priorityClassName`           | Priority Class to set on the deployment pods.                                                                          | `""`                                                                                               |
| `controller.securityContext`             | Configure container security context. Defaults to dropping all capabilties and running as user 1000.                   | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true, runAsNonRoot: true, runAsUser: 1000}` |
| `controller.resources`                   | Resources to request and limit on the pod.                                                                             | `{}`                                                                                               |
| `controller.nodeSelector`                | Node selector to add to each webhook pod.                                                                              | `{}`                                                                                               |
| `controller.tolerations`                 | Tolerations to add to each webhook pod.                                                                                | `[]`                                                                                               |
| `controller.topologySpreadConstraints`   | Topology spread constraints to set on each pod.                                                                        | `[]`                                                                                               |
| `controller.affinity`                    | Affinity to set on each webhook pod.                                                                                   | `{}`                                                                                               |
| `controller.pdb`                         | PodDisruptionBudget to set on the webhook pod.                                                                         | `{}`                                                                                               |
| `controller.rbac.create`                 | Create the necessary roles and bindings for the snapshot controller.                                                   | `true`                                                                                             |
| `controller.serviceAccount.create`       | Create the service account resource                                                                                    | `true`                                                                                             |
| `controller.serviceAccount.name`         | Sets the name of the service account. If left empty, will use the release name as default                              | `""`                                                                                               |
| `controller.hostNetwork`                 | Change `hostNetwork` to `true` when you want the pod to share its host's network namespace.                            | `false`                                                                                            |
| `controller.dnsConfig`                   | DNS settings for controller pod.                                                                                       | `{}`                                                                                               |
| `controller.dnsPolicy`                   | DNS Policy for controller pod. For Pods running with hostNetwork, set to `ClusterFirstWithHostNet`.                    | `ClusterFirst`                                                                                     |


### Snapshot Validation Webhook
Webhooks in Kubernetes are required to run on HTTPS. To that end, this charts needs to be configured with one of the
following options:

* An auto-generated certificate, valid for 10 years. This is the default. If you want to renew the certificate,
  set `webhook.tls.renew` to `true` and run an upgrade.

* A [cert-manager.io](https://cert-manager.io) issuer able to create a certificate for the webhook service.

  To use this method, create an override file like:
  ```yaml
  webhook:
    tls:
      certManagerIssuerRef:
        name: internal-issuer
        kind: ClusterIssuer
  ```

  To apply the override, use `--values <override-file>`.

* A pre-existing  [`kubernetes.io/tls`] secret and the certificate of the CA used to sign said tls secret.

  To use this method, set `--set webhook.tls.certificateSecret=<secretname>`.
  The secret must be in the same namespace as the deployment and be valid for `<release-name>.<namespace>.svc`.

***NOTE:*** When using a custom CNI (such as Weave or Calico) on Amazon EKS, the webhook cannot be reached.

> Internal error occurred: failed calling webhook "snapshot-validation-webhook.snapshot.storage.k8s.io": failed to call webhook: Post "https://snapshot-validation-webhook.kube-system.svc:443/volumesnapshot?timeout=2s": Address is not allowed

This happens because the control plane cannot be configured to run on a custom CNI on EKS, so the CNIs
differ between control plane and worker nodes.

To address this, the webhook can be run in the host network so it can be reached.
```yaml
webhook:
  hostNetwork: true
  dnsPolicy: ClusterFirstWithHostNet
```

There are additional options that allow customization outside of HTTPS concerns. This is the full list of options
available.

| Option                                       | Usage                                                                                                                  | Default                                                                                            |
|----------------------------------------------|------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `webhook.enabled`                            | Toggle to disable the deployment of the snapshot validation webhook.                                                   | `true`                                                                                             |
| `webhook.fullnameOverride`                   | Set the base name of deployed resources. Defaults to `snapshot-validation-webhook`.                                    | `""`                                                                                               |
| `webhook.args`                               | Arguments to pass to the snapshot controller. Note: Keys will be converted to kebab-case, i.e. `oneArg` -> `--one-arg` | `...`                                                                                              |
| `webhook.replicaCount`                       | Number of replicas to deploy.                                                                                          | `1`                                                                                                |
| `webhook.revisionHistoryLimit`               | Number of revisions to keep.                                                                                           | `10`                                                                                               |
| `webhook.image.repository`                   | Repository to pull the image from.                                                                                     | `registry.k8s.io/sig-storage/snapshot-validation-webhook`                                          |
| `webhook.image.pullPolicy`                   | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                                                 | `IfNotPresent`                                                                                     |
| `webhook.image.tag`                          | Override the tag to pull. If not given, defaults to charts `AppVersion`.                                               | `""`                                                                                               |
| `webhook.webhook.timeoutSeconds`             | Timeout to use when contacting webhook server.                                                                         | `2`                                                                                                |
| `webhook.webhook.failurePolicy`              | Policy to apply when webhook is unavailable. Possible values: `Fail`, `Ignore`.                                        | `Fail`                                                                                             |
| `webhook.tls.certificateSecret`              | Name of the static tls secret to use for serving the HTTPS endpoint.                                                   | `""`                                                                                               |
| `webhook.tls.autogenerate`                   | Automatically generate the TLS secret for serving the HTTPS endpoint.                                                  | `true`                                                                                             |
| `webhook.tls.renew`                          | Force renewal of certificate when auto-generating.                                                                     | `false`                                                                                            |
| `webhook.tls.certManagerIssuerRef`           | Issuer to use for provisioning the TLS certificate. If this is used, `tls.certificateSecret` can be left empty.        | `{}`                                                                                               |
| `webhook.imagePullSecrets`                   | Image pull secrets to add to the deployment.                                                                           | `[]`                                                                                               |
| `webhook.podAnnotations`                     | Annotations to add to every pod in the deployment.                                                                     | `{}`                                                                                               |
| `webhook.podLabels`                          | Labels to add to every pod in the deployment.                                                                          | `{}`                                                                                               |
| `webhook.networkPolicy.enabled`              | Should a network policy be created.                                                                                    | `false`                                                                                            |
| `webhook.networkPolicy.ingress`              | Additional ingress rules to be added to the network policy.                                                            | `{}`                                                                                               |
| `webhook.podDisruptionBudget.enabled`        | Should a pod disruption budget be created.                                                                             | `false`                                                                                            |
| `webhook.podDisruptionBudget.maxUnavailable` | The maximum number of pods that are allowed to be unavailable.                                                         | `""`                                                                                               |
| `webhook.podDisruptionBudget.minAvailable`   | The minimum number of pods that are required to be available.                                                          | `""`                                                                                               |
| `webhook.priorityClassName`                  | The name of the priority class to assign to the deployment.                                                            | `""`                                                                                               |
| `webhook.topologySpreadConstraints`          | A list of topology constraints to assign to the deployment.                                                            | `[]`                                                                                               |
| `webhook.podSecurityContext`                 | Security context to set on the webhook pod.                                                                            | `{}`                                                                                               |
| `webhook.securityContext`                    | Configure container security context. Defaults to dropping all capabilties and running as user 1000.                   | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true, runAsNonRoot: true, runAsUser: 1000}` |
| `webhook.resources`                          | Resources to request and limit on the pod.                                                                             | `{}`                                                                                               |
| `webhook.nodeSelector`                       | Node selector to add to each webhook pod.                                                                              | `{}`                                                                                               |
| `webhook.tolerations`                        | Tolerations to add to each webhook pod.                                                                                | `[]`                                                                                               |
| `webhook.affinity`                           | Affinity to set on each webhook pod.                                                                                   | `{}`                                                                                               |
| `webhook.serviceAccount.create`              | Create the service account resource                                                                                    | `true`                                                                                             |
| `webhook.serviceAccount.name`                | Sets the name of the service account. If left empty, will use the release name as default                              | `""`                                                                                               |
| `webhook.tests.nodeSelector`                 | Node selector to add to each helm test pod.                                                                            | `{}`                                                                                               |
| `webhook.tests.tolerations`                  | Tolerations to add to each helm test pod.                                                                              | `[]`                                                                                               |
| `webhook.tests.affinity`                     | Affinity to set on each helm test pod.                                                                                 | `{}`                                                                                               |
| `webhook.hostNetwork`                        | Change `hostNetwork` to `true` when you want the pod to share its host's network namespace.                            | `false`                                                                                            |
| `webhook.dnsConfig`                          | DNS settings for webhook pod.                                                                                          | `{}`                                                                                               |
| `webhook.dnsPolicy`                          | DNS Policy for webhook pod. For Pods running with hostNetwork, set to `ClusterFirstWithHostNet`                        | `ClusterFirst`                                                                                     |

[`3.x.x` releases]: https://github.com/kubernetes-csi/external-snapshotter/releases
[have to ensure non of your resources violate the requirements for `v1`]: https://github.com/kubernetes-csi/external-snapshotter#validating-webhook
