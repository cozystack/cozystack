# snapshot-controller

Deploys the [snapshot-controller](https://github.com/kubernetes-csi/external-snapshotter) in a cluster.
The controller is required for CSI snapshotting to work and is not specific to any CSI driver.

While many Kubernetes distributions already package this controller, some do not. If your cluster does ***NOT***
have the following CRDs, you likely also do not have a snapshot controller deployed:

```
kubectl get crd volumesnapshotclasses.snapshot.storage.k8s.io
kubectl get crd volumesnapshots.snapshot.storage.k8s.io
kubectl get crd volumesnapshotcontents.snapshot.storage.k8s.io
```

## Usage

See [below](#configuration) for available configuration options.

```
helm repo add piraeus-charts https://piraeus.io/helm-charts/
helm install snapshot-controller piraeus-charts/snapshot-controller
```

## Using cert-manager
The following configuration allows you to create [cert-manager](https://cert-manager.io/docs/configuration/issuers/) certificates for the webhook.

First create an issuer for selfsigned certificates in the namespace of the snapshot-controller installation:
```YAML
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
```

After that we can configure the webhook (values.yaml):
```YAML
webhook:
  tls:
    # disable tls generation with helm
    autogenerate: false
    # configure certManager issuer
    certManagerIssuerRef: 
      name: selfsigned
      kind: Issuer
```

## Upgrades

Upgrades can be done using the normal Helm upgrade mechanism

```
helm repo update
helm upgrade snapshot-controller piraeus-charts/snapshot-controller
```

### Upgrading from Chart Version v4

The following changes have been made when moving to Chart v5.0.0+:

* CRDs are deployed as part of the regular resource. Installation can be controlled using the `installCRDs` value.
* The Snapshot Conversion Webhook can be deployed to convert Volume Group Snapshot Content between v1beta1 and
  v1beta2. It is disabled by default; enable it with `webhook.enabled=true` only if your cluster still holds group
  snapshot objects in those older API versions. When disabled, no conversion strategy is injected into the
  volumegroupsnapshotcontents CRD (it defaults to `None`, matching upstream).
* `volumeSnapshotClasses` and `volumeGroupSnapshotClasses` have been moved from `controller` to the top of the values.

Upgrades may fail to apply because of the changed deployment strategy for CRDs with an error such as:

```
Error: UPGRADE FAILED: Unable to continue with update: CustomResourceDefinition "volumesnapshotclasses.snapshot.storage.k8s.io" in namespace "" exists and cannot be imported into the current release: invalid ownership metadata; label validation error: missing key "app.kubernetes.io/managed-by": must be set to "Helm"; annotation validation error: missing key "meta.helm.sh/release-name": must be set to "snapshot-controller"; annotation validation error: missing key "meta.helm.sh/release-namespace": must be set to "snapshot-controller"
```

In this case, in order to proceed with the upgrade, annotate and label the CRDs like so:

```
kubectl annotate crds volumegroupsnapshotclasses.groupsnapshot.storage.k8s.io meta.helm.sh/release-name=snapshot-controller meta.helm.sh/release-namespace=snapshot-controller
kubectl label crds volumegroupsnapshotclasses.groupsnapshot.storage.k8s.io app.kubernetes.io/managed-by=Helm
kubectl annotate crds volumegroupsnapshotcontents.groupsnapshot.storage.k8s.io meta.helm.sh/release-name=snapshot-controller meta.helm.sh/release-namespace=snapshot-controller
kubectl label crds volumegroupsnapshotcontents.groupsnapshot.storage.k8s.io app.kubernetes.io/managed-by=Helm
kubectl annotate crds volumegroupsnapshots.groupsnapshot.storage.k8s.io meta.helm.sh/release-name=snapshot-controller meta.helm.sh/release-namespace=snapshot-controller
kubectl label crds volumegroupsnapshots.groupsnapshot.storage.k8s.io app.kubernetes.io/managed-by=Helm
kubectl annotate crds volumesnapshotclasses.snapshot.storage.k8s.io meta.helm.sh/release-name=snapshot-controller meta.helm.sh/release-namespace=snapshot-controller
kubectl label crds volumesnapshotclasses.snapshot.storage.k8s.io app.kubernetes.io/managed-by=Helm
kubectl annotate crds volumesnapshotcontents.snapshot.storage.k8s.io meta.helm.sh/release-name=snapshot-controller meta.helm.sh/release-namespace=snapshot-controller
kubectl label crds volumesnapshotcontents.snapshot.storage.k8s.io app.kubernetes.io/managed-by=Helm
kubectl annotate crds volumesnapshots.snapshot.storage.k8s.io meta.helm.sh/release-name=snapshot-controller meta.helm.sh/release-namespace=snapshot-controller
kubectl label crds volumesnapshots.snapshot.storage.k8s.io app.kubernetes.io/managed-by=Helm
```

Then, retry the upgrade.

## Configuration

### Snapshot controller

The following options are available:

| Option                                 | Usage                                                                                                                          | Default                                                                                            |
|----------------------------------------|--------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `installCRDs`                          | Install the CustomResourceDefinitions necessary for Kubernetes Volume Snapshots.                                               | `true`                                                                                             |
| `keepCRDs`                             | Annotate the CRDs with `helm.sh/resource-policy: keep`, so `helm uninstall` does not delete them and the VolumeSnapshots they hold. | `false`                                                                                        |
| `controller.enabled`                   | Toggle to disable the deployment of the snapshot controller.                                                                   | `true`                                                                                             |
| `controller.fullnameOverride`          | Set the base name of deployed resources. Defaults to `snapshot-controller`.                                                    | `""`                                                                                               |
| `controller.args`                      | Arguments to pass to the snapshot controller. Note: Keys will be converted to kebab-case, i.e. `oneArg` -> `--one-arg`         | `...`                                                                                              |
| `controller.replicaCount`              | Number of replicas to deploy.                                                                                                  | `1`                                                                                                |
| `controller.revisionHistoryLimit`      | Number of revisions to keep.                                                                                                   | `10`                                                                                               |
| `controller.image.repository`          | Repository to pull the image from.                                                                                             | `registry.k8s.io/sig-storage/snapshot-controller`                                                  |
| `controller.image.pullPolicy`          | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                                                         | `IfNotPresent`                                                                                     |
| `controller.image.tag`                 | Override the tag to pull. If not given, defaults to charts `AppVersion`.                                                       | `""`                                                                                               |
| `controller.imagePullSecrets`          | Image pull secrets to add to the deployment.                                                                                   | `[]`                                                                                               |
| `controller.podAnnotations`            | Annotations to add to every pod in the deployment.                                                                             | `{}`                                                                                               |
| `controller.podLabels`                 | Labels to add to every pod in the deployment.                                                                                  | `{}`                                                                                               |
| `controller.podSecurityContext`        | Security context to set on the webhook pod.                                                                                    | `{}`                                                                                               |
| `controller.priorityClassName`         | Priority Class to set on the deployment pods.                                                                                  | `""`                                                                                               |
| `controller.securityContext`           | Configure container security context. Defaults to dropping all capabilties and running as user 1000.                           | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true, runAsNonRoot: true, runAsUser: 1000}` |
| `controller.resources`                 | Resources to request and limit on the pod.                                                                                     | `{}`                                                                                               |
| `controller.nodeSelector`              | Node selector to add to each webhook pod.                                                                                      | `{}`                                                                                               |
| `controller.tolerations`               | Tolerations to add to each webhook pod.                                                                                        | `[]`                                                                                               |
| `controller.topologySpreadConstraints` | Topology spread constraints to set on each pod.                                                                                | `[]`                                                                                               |
| `controller.affinity`                  | Affinity to set on each webhook pod.                                                                                           | `{}`                                                                                               |
| `controller.pdb`                       | PodDisruptionBudget to set on the webhook pod.                                                                                 | `{}`                                                                                               |
| `controller.rbac.create`               | Create the necessary roles and bindings for the snapshot controller.                                                           | `true`                                                                                             |
| `controller.serviceAccount.create`     | Create the service account resource                                                                                            | `true`                                                                                             |
| `controller.serviceAccount.name`       | Sets the name of the service account. If left empty, will use the release name as default                                      | `""`                                                                                               |
| `controller.hostNetwork`               | Change `hostNetwork` to `true` when you want the pod to share its host's network namespace.                                    | `false`                                                                                            |
| `controller.dnsConfig`                 | DNS settings for controller pod.                                                                                               | `{}`                                                                                               |
| `controller.dnsPolicy`                 | DNS Policy for controller pod. For Pods running with hostNetwork, set to `ClusterFirstWithHostNet`.                            | `ClusterFirst`                                                                                     |
| `webhook.enabled`                      | Deploy the conversion webhook and inject the `Webhook` conversion strategy into the volumegroupsnapshotcontents CRD. Only needed for clusters still holding v1beta1/v1beta2 group snapshot objects. | `false`                                                            |
| `webhook.fullnameOverride`             | Set the base name of deployed resources. Defaults to `snapshot-controller-conversion-webhook`.                                 | `""`                                                                                               |
| `webhook.args`                         | Arguments to pass to the snapshot conversion webhook. Note: Keys will be converted to kebab-case, i.e. `oneArg` -> `--one-arg` | `...`                                                                                              |
| `webhook.tls.certificateSecret`        | Name of the certificate secret to use for serving TLS.                                                                         | `""`                                                                                               |
| `webhook.tls.autogenerate`             | Automatically generate the TLS secret.                                                                                         | `true`                                                                                             |
| `webhook.tls.renew`                    | Always generate a new TLS secret when autogenerating the TLS secret.                                                           | `false`                                                                                            |
| `webhook.tls.certManagerIssuerRef`     | Set `kind` and `name` of the cert-manager Issuer to use instead of using Helm to generate the TLS secret.                      | `{}`                                                                                               |
| `webhook.tls.caBundle`                 | PEM-encoded TLS CA Bundle. If set, Helm will create a Secret with the content of caBundle, certificate and key.                | `""`                                                                                               |
| `webhook.tls.certificate`              | PEM-encoded TLS certificate. If set, Helm will create a Secret with the content of caBundle, certificate and key.              | `""`                                                                                               |
| `webhook.tls.key`                      | PEM-encided TLS key. If set, Helm will create a Secret with the content of caBundle, certificate and key.                      | `""`                                                                                               |
| `webhook.replicaCount`                 | Number of replicas to deploy.                                                                                                  | `1`                                                                                                |
| `webhook.revisionHistoryLimit`         | Number of revisions to keep.                                                                                                   | `10`                                                                                               |
| `webhook.image.repository`             | Repository to pull the image from.                                                                                             | `ghcr.io/piraeusdatastore/snapshot-conversion-webhook`                                             |
| `webhook.image.pullPolicy`             | Pull policy to use. Possible values: `IfNotPresent`, `Always`, `Never`                                                         | `IfNotPresent`                                                                                     |
| `webhook.image.tag`                    | Override the tag to pull. If not given, defaults to charts `AppVersion`.                                                       | `""`                                                                                               |
| `webhook.imagePullSecrets`             | Image pull secrets to add to the deployment.                                                                                   | `[]`                                                                                               |
| `webhook.podAnnotations`               | Annotations to add to every pod in the deployment.                                                                             | `{}`                                                                                               |
| `webhook.podLabels`                    | Labels to add to every pod in the deployment.                                                                                  | `{}`                                                                                               |
| `webhook.podSecurityContext`           | Security context to set on the webhook pod.                                                                                    | `{}`                                                                                               |
| `webhook.priorityClassName`            | Priority Class to set on the deployment pods.                                                                                  | `""`                                                                                               |
| `webhook.securityContext`              | Configure container security context. Defaults to dropping all capabilties and running as user 1000.                           | `{capabilities: {drop: [ALL]}, readOnlyRootFilesystem: true, runAsNonRoot: true, runAsUser: 1000}` |
| `webhook.resources`                    | Resources to request and limit on the pod.                                                                                     | `{}`                                                                                               |
| `webhook.nodeSelector`                 | Node selector to add to each webhook pod.                                                                                      | `{}`                                                                                               |
| `webhook.tolerations`                  | Tolerations to add to each webhook pod.                                                                                        | `[]`                                                                                               |
| `webhook.topologySpreadConstraints`    | Topology spread constraints to set on each pod.                                                                                | `[]`                                                                                               |
| `webhook.affinity`                     | Affinity to set on each webhook pod.                                                                                           | `{}`                                                                                               |
| `webhook.pdb`                          | PodDisruptionBudget to set on the webhook pod.                                                                                 | `{}`                                                                                               |
| `webhook.serviceAccount.create`        | Create the service account resource                                                                                            | `true`                                                                                             |
| `webhook.serviceAccount.name`          | Sets the name of the service account. If left empty, will use the release name as default                                      | `""`                                                                                               |
| `webhook.hostNetwork`                  | Change `hostNetwork` to `true` when you want the pod to share its host's network namespace.                                    | `false`                                                                                            |
| `webhook.dnsConfig`                    | DNS settings for controller pod.                                                                                               | `{}`                                                                                               |
| `webhook.dnsPolicy`                    | DNS Policy for controller pod. For Pods running with hostNetwork, set to `ClusterFirstWithHostNet`.                            | `ClusterFirst`                                                                                     |
| `volumeSnapshotClasses`                | Volume Snapshot Classes to deploy.                                                                                             | `[]`                                                                                               |
| `volumeGroupSnapshotClasses`           | Volume Group Snapshot Classes to deploy.                                                                                       | `[]`                                                                                               |
