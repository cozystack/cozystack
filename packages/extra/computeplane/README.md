# ComputePlane

A managed, isolated environment for running code-executing (untrusted-code)
catalog applications — notebooks, workflow "code" nodes, plugin systems.

This tenant module provisions a single-tenant, Cozystack-managed cluster by
deploying the ordinary `apps/kubernetes` chart (Kamaji control plane +
KubeVirt-VM worker nodes) with operator-fixed values, sourced from this
package's own source-only `ExternalArtifact`
(`cozystack-computeplane-application-kubevirt-kubernetes`). The tenant gets the
same substrate as any managed `kind: Kubernetes` cluster, but holds no admin
kubeconfig for it and can change only the cluster shape the module exposes —
the security posture (Cozystack-enablement addons, withheld credentials) is
baked into the chart and cannot be overridden.

The cluster's Helm release is named `computeplane`, so Kamaji writes its admin
kubeconfig to the Secret `computeplane-admin-kubeconfig` (key
`super-admin.svc`) — the fixed contract consumed by the HelmReleases of
`placement: ComputePlane` applications.

See the full design in
[cozystack/community design-proposals/compute-plane](https://github.com/cozystack/community/tree/main/design-proposals/compute-plane).

## Parameters

### Cluster shape

| Name         | Description                                                                                                                                                                                                                                                                                                                                                                                                                            | Type     | Value |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `nodeGroups` | Worker node groups of the ComputePlane cluster (cluster shape only — the security posture is fixed by the module and cannot be overridden). Uses the same structure as the `kubernetes` application's `nodeGroups`. When left empty, the wrapped `kubernetes` chart emits its default scale-from-zero `md0` group, so the cluster sits idle until a pending Pod (e.g. the ingress-nginx controller) triggers the cluster-autoscaler. | `object` | `{}`  |

