# LocalPV storage for a future container-cluster E2E lane

A container-cluster test environment needs dynamically provisioned PVCs without depending on DRBD or ZFS kernel modules. The independently installable `cozystack.local-storage` package supplies OpenEBS LocalPV Hostpath for that purpose. It can also serve deliberately selected node-local workloads outside an E2E environment.

## Delivered package boundary

The package registers a PackageSource and offers an explicit `default` variant. It creates `cozystack-local-hostpath` with delayed binding and `Delete` reclamation. It neither replaces the existing `local` StorageClass nor becomes the default automatically. Operators may explicitly enable the default annotation after dealing with existing defaults, or use the StorageClass by name. A system bundle can opt in to the package; no built-in variant enables it.

The vendored chart is pinned to 4.5.1. Its upstream images are retained with immutable index pins. A reproducible patch restricts cluster-wide permissions to volume reconciliation and discovery; helper Pods, leader-election Leases and optional analytics state are managed in the installation namespace. Helpers remain privileged because they operate on host directories.

The controller identity is fixed upstream to `openebs.io/local`, so a second independently installed OpenEBS LocalPV controller is not supported. Data remains tied to a node and its host directory; this is not replicated storage or a CSI snapshot implementation. See the [package documentation](../../packages/system/local-storage/README.md) for configuration, prerequisites and removal.

## Future lane work

[The container-cluster E2E proposal](https://github.com/cozystack/cozystack/issues/3238) still needs its own provisioning, networking, package dependency selection and CI integration. Those choices are not implemented by this package. Existing application tests can only be assigned to such a lane after checking their real storage, networking and virtualization dependencies; the package makes no claim that a particular suite already works there.

Before a lane depends on this package, verify PVC creation with a consumer, delayed binding, write/read across Pod recreation, directory reclamation after deletion, coexistence with ordinary Cozystack storage and denial of helper Pod operations in tenant namespaces. A chart render does not establish those runtime properties.
