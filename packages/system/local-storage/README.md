# OpenEBS LocalPV Hostpath

This optional system package provisions node-local hostpath volumes through OpenEBS Dynamic LocalPV 4.5.1. Its StorageClass is `cozystack-local-hostpath`, uses `WaitForFirstConsumer`, and is not the cluster default. Volumes are bound to one node; the package does not provide replication, CSI snapshots or automatic recovery from node/disk loss. The default `Delete` reclaim policy removes the volume directory after the PVC is deleted.

## Installation and configuration

The platform registers `cozystack.local-storage` as a PackageSource even when its bundles are disabled. Install its `default` variant explicitly with `cozypkg add cozystack.local-storage` and acknowledge privileged components when prompted. The networking Package dependency must be available; current cozypkg resolves dependencies, while applying a Package manifest directly requires preparing those dependencies separately. Alternatively, a system bundle can include `cozystack.local-storage` in `bundles.enabledPackages`; `bundles.disabledPackages` takes precedence. No built-in variant installs it automatically.

For explicit configuration, use a Package resource:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.local-storage
spec:
  variant: default
  components:
    local-storage:
      values:
        localpv-provisioner:
          hostpathClass:
            name: cozystack-local-hostpath
            isDefaultClass: false
            reclaimPolicy: Delete
```

Use `storageClassName: cozystack-local-hostpath` in PVCs. Setting `hostpathClass.isDefaultClass: true` is an administrator opt-in: first inspect and explicitly resolve any existing default StorageClass. The chart does not demote or adopt other classes. A PVC with `storageClassName: ""` explicitly requests no class; omitting the field allows Kubernetes to select the default. Choose a different name before installation if `cozystack-local-hostpath` already exists under another owner; changing a live StorageClass name creates a new class rather than migrating existing volumes.

Only one OpenEBS LocalPV controller using the provisioner identity `openebs.io/local` may manage the cluster. Do not install this package beside a separately managed controller with that identity: a distinct StorageClass name does not isolate their reconciliation. This does not conflict with the ordinary Cozystack LINSTOR provisioner.

The upstream Deployment creates privileged helper Pods in the release namespace (normally `cozy-local-storage`) to create and remove directories under `/var/openebs/local` on the selected node. That directory must be writable and backed by the intended persistent filesystem on every eligible node. RBAC limits helper Pod, leader-election Lease and optional analytics ConfigMap operations to the release namespace. PV/PVC discovery and reconciliation, node discovery, StorageClass reads and PVC events remain cluster-scoped. A privileged storage controller still has host filesystem access; namespace-scoped Pod permissions are not a tenant filesystem isolation boundary.

Drain or remove applications and verify PVC/PV reclamation before uninstalling the controller. `Retain` leaves data and PV reclamation to the administrator. Hostpath volumes do not survive losing their backing node or directory. This package adds no container-cluster E2E workflow or storage migration.

## Maintenance

`make update` downloads chart 4.5.1, reapplies `patches/rbac.diff`, refreshes the provisioner/helper image index pins. `make test` renders the chart and checks storage, configuration and RBAC boundaries locally. Image pins are third-party pass-through references in the umbrella values; they are not rebuilt or mirrored by Cozystack. The pinned indices include Linux amd64, arm64, arm/v7 and ppc64le; actual provisioning remains subject to node filesystem and security prerequisites.

The RBAC patch follows [LocalPV helper calls](https://github.com/openebs/dynamic-localpv-provisioner/blob/v4.5.1/cmd/provisioner-localpv/app/helper_hostpath.go), [startup and analytics](https://github.com/openebs/dynamic-localpv-provisioner/blob/v4.5.1/cmd/provisioner-localpv/app/start.go), and [external provisioner controller v13](https://github.com/kubernetes-sigs/sig-storage-lib-external-provisioner/blob/v13.0.0/controller/controller.go). Recheck these calls when updating upstream.

## Parameters

The upstream values root is `localpv-provisioner`. This system chart follows the vendored chart values convention; it does not expose an ApplicationDefinition or generated API schema.

| Value under `localpv-provisioner` | Default | Effect |
| --- | --- | --- |
| `hostpathClass.name` | `cozystack-local-hostpath` | Name of the managed StorageClass. |
| `hostpathClass.isDefaultClass` | `false` | Explicit administrator opt-in to default-class selection. |
| `hostpathClass.enabled` | `true` | Whether to create the StorageClass. |
| `hostpathClass.reclaimPolicy` | `Delete` | Use `Retain` for manual reclamation. |
| `hostpathClass.basePath` | empty | Override the directory inherited from `localpv.basePath`. |
| `localpv.basePath` | `/var/openebs/local` | Writable host directory used by helper Pods. |
| `analytics.enabled` | `false` | Send upstream usage telemetry. |
| `localpv.image` | upstream 4.5.1, pinned index | Provisioner image, refreshed by `make update-image-pins`. |
| `helperPod.image` | upstream 4.5.0, pinned index | Privileged filesystem helper image. |
| `serviceAccount.name` | generated from the release | Optional account name used by the controller and helpers. |
| `rbac.create` | `true` | Create the package RBAC; when false the administrator must provide permissions. |

Other supported upstream values, including image pull secrets, node selectors and helper timeouts, are documented in the [vendored chart](charts/localpv-provisioner/README.md).
