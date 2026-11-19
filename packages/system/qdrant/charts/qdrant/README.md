# Qdrant helm chart

[Qdrant documentation](https://qdrant.tech/documentation/)

## TLDR

```bash
helm repo add qdrant https://qdrant.github.io/qdrant-helm
helm repo update
helm upgrade -i your-qdrant-installation-name qdrant/qdrant
```

## Description

This chart installs and bootstraps a Qdrant instance.

## Prerequisites

- Kubernetes v1.24+ (as you need grpc probe)
- Helm
- PV provisioner (by the infrastructure)

## Installation & Setup

You can install the chart from source via:

```bash
helm upgrade -i your-qdrant-installation-name charts/qdrant
```

Uninstall via:

```bash
helm uninstall your-qdrant-installation-name
```

Delete the volume with

```bash
kubectl delete pvc -l app.kubernetes.io/instance=your-qdrant-installation-name
```

## Configuration

For documentation of the settings please refer to [Qdrant Configuration File](https://github.com/qdrant/qdrant/blob/master/config/config.yaml)
All of these configuration options could be overwritten under config in `values.yaml`.
A modification example is provided there.

### Overrides

You can override any value in the Qdrant configuration by setting the Helm values under the key `config`. Those settings get included verbatim in a file called `config/production.yml` which is explained further here [Qdrant Order and Priority](https://qdrant.tech/documentation/guides/configuration/#order-and-priority) as well as an [example](https://github.com/qdrant/qdrant-helm/blob/b0bb6fc6d3eb9c0813c79bb5a78dc21aebc2b81d/charts/qdrant/values.yaml#L140).

### Distributed setup

Running a distributed cluster just needs a few changes in your `values.yaml` file.
Increase the number of replicas to the desired number of nodes and set `config.cluster.enabled` to true.

Depending on your environment or cloud provider you might need to change the service in the `values.yaml` as well.
For example on AWS EKS you would need to change the `cluster.type` to `NodePort`.

## Updating StatefulSets

This Helm chart uses a Kubernetes [StatefulSet](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/) to manage your Qdrant cluster. StatefulSets have many fields that are immutable, meaning that you cannot change these fields without deleting and recreating the StatefulSet. If you try to change these fields, you will get an error like this:

```
Error: UPGRADE FAILED: cannot patch "qdrant" with kind StatefulSet: StatefulSet.apps "qdrant" is invalid: spec: Forbidden: updates to statefulset spec for fields other than 'replicas', 'ordinals', 'template', 'updateStrategy', 'persistentVolumeClaimRetentionPolicy' and 'minReadySeconds' are forbidden
```

If you need to change any immutable field, the process is described below, using the most common example of expanding a PVC volume.

1. Delete the StatefulSet while leaving the Pods running:

    ```bash
    kubectl delete statefulset --cascade=orphan qdrant
    ```

2. Manually edit all PersistentVolumeClaims to increase their sizes:

    ```bash
    # For each PersistentVolumeClaim:
    kubectl edit pvc qdrant-storage-qdrant-0
    ```

3. Update your Helm values to match the new PVC size.
4. Reinstall the Helm chart using your updated values:

    ```bash
    helm upgrade --install qdrant qdrant/qdrant -f my-values.yaml
    ```

Some storage providers allow resizing volumes in-place, but most require a pod restart before the new size will take effect:

```bash
kubectl rollout restart statefulset qdrant
```

### Immutable Pod fields

In addition to immutable fields on StatefulSets, Pods also have some fields which are immutable, which means the above method may not work for some changes, such as setting `snapshotPersistence.enabled: true`. In that case, after following the above method, you'll see an error like this when you `kubectl describe` your StatefulSet:

```
pod updates may not change fields other than `spec.containers[*].image`,
`spec.initContainers[*].image`,`spec.activeDeadlineSeconds`,
`spec.tolerations` (only additions to existing tolerations),
`spec.terminationGracePeriodSeconds` (allow it to be set to 1 if it was previously negative)
```

To fix this, you must manually delete all of your Qdrant pods, starting with node-0. This will cause your cluster to go down, but will allow the StatefulSet to recreate your Pods with the correct configuration.

## Restoring from Snapshots

This helm chart allows you to restore a snapshot into your Qdrant cluster either from an internal or external PersistentVolumeClaim.

### Restoring from the built-in PVC

If you have set `snapshotPersistence.enabled: true` (recommended for production), this helm chart will create a separate PersistentVolume for snapshots, and any snapshots you create will be stored in that PersistentVolume.

To restore from one of these snapshots, set the following values:

```yaml
snapshotRestoration:
  enabled: true
  # Set blank to indicate we are not using an external PVC
  pvcName: ""
  snapshots:
  - /qdrant/snapshots/<collection_name>/<filename>/:<collection_name>
```

And run "helm upgrade". This will restart your cluster and restore the specified collection from the snapshot. Qdrant will refuse to overwrite an existing collection, so ensure the collection is deleted before restoring.

After the snapshot is restored, remove the above values and run "helm upgrade" again to trigger another rolling restart. Otherwise, the snapshot restore will be attempted again if your cluster ever restarts.

### Restoring from an external PVC

If you wish to restore from an externally-created snapshot, using the API is recommended: https://qdrant.github.io/qdrant/redoc/index.html#tag/collections/operation/recover_from_uploaded_snapshot

If the file is too large, you can separately create a PersistentVolumeClaim, store your data in there, and refer to this separate PersistentVolumeClaim in this helm chart.

Once you have created this PersistentVolumeClaim (must be in the same namespace as your Qdrant cluster), set the following values:

```yml
snapshotRestoration:
  enabled: true
  pvcName: "<the name of your PVC>"
  snapshots:
  - /qdrant/snapshots/<collection_name>/<filename>/:<collection_name>
```

And run "helm upgrade". This will restart your cluster and restore the specified collection from the snapshot. Qdrant will refuse to overwrite an existing collection, so ensure the collection is deleted before restoring.

After the snapshot is restored, remove the above values and run "helm upgrade" again to trigger another rolling restart. Otherwise, the snapshot restore will be attempted again if your cluster ever restarts.

## Metrics endpoints

Metrics are available through rest api (default port set to 6333) at `/metrics`

Refer to [qdrant metrics configuration](https://qdrant.tech/documentation/telemetry/#metrics) for more information.
