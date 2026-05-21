# Kubernetes Worker Node Pool

`kubernetes-nodes` is the second half of the Kubernetes-app split (see
[cozystack/community#8](https://github.com/cozystack/community/pull/8)).
It manages a single worker node pool — `MachineDeployment` +
`KubevirtMachineTemplate` + `TalosConfigTemplate` + `MachineHealthCheck`
— attached to a parent `kubernetes` cluster running in the same
namespace.

Linkage is by Cluster CR owner reference: this chart looks up the CAPI
`Cluster` resource named `kubernetes` (from `values.yaml`) and resolves
its `controlPlaneRef` (KamajiControlPlane) and `clusterNetwork` to seed
the worker machineconfig. All resources rendered by this chart are
ownerReferenced to that `Cluster` so they get garbage-collected when
the parent `kubernetes` HelmRelease goes away.

> This chart is part of Phase 2 of the Talos migration. Phase 1
> (Talos bootstrap inside the monolithic `kubernetes` chart) shipped
> separately; see cozystack/cozystack#2610.

## Status

Work in progress — see the draft PR for the current scope.
