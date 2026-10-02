# cozyplane-apiserver Helm chart

The `sdn.cozystack.io` API group served by a real **aggregated API server**
(backed by a dedicated etcd) instead of CRDs — the design target: custom
validation, the export/peer verb checks in-server, and subresources (e.g. the
`/ports` observability subresource) beyond CRD ergonomics.

This is a **separate chart from [cozyplane](../cozyplane)** by design. cozyplane
is the CNI and must install before almost everything — including cert-manager —
while this apiserver needs cert-manager `Certificate`s (its serving cert, the
etcd PKI). Splitting lets the CNI slot stay cert-manager-free and this chart
install later, once cert-manager is up (in Cozystack: a component that
`dependsOn` cert-manager).

## The takeover

The cozyplane chart installs the group's **CRDs** (its `crds.enabled` default),
so `sdn.cozystack.io` is served — and tenancy works — from the moment the CNI
lands. Installing this chart creates an explicit `APIService` for
`v1alpha1.sdn.cozystack.io`, which **atomically replaces** the CRDs' implicit
serving of the group: from that moment every request goes to the aggregated
server and its etcd. On a fresh cluster the CRD store is empty when that happens
(tenants come later), so the takeover needs no migration. On a cluster with
existing CRD-stored objects, export them first and re-apply after — the CRD
store is not visible through the aggregated server:

```sh
kubectl get vpcs,ports,vpcbindings,vpcpeerings,securitygroups,floatingips,servicevips -A -o yaml > sdn-backup.yaml
helm install cozyplane-apiserver ./chart/cozyplane-apiserver -n cozyplane-system
kubectl apply -f sdn-backup.yaml
kubectl -n <cozyplane-namespace> rollout restart deploy/cozyplane-controller ds/cozyplane-agent
```

(Strip `resourceVersion`/`uid`/`status` on re-apply, or use `kubectl create`.)

The final restart is load-bearing: watch streams opened against the CRD serving
survive the takeover (the kube-apiserver closes idle watches only after 30–60
minutes), so without it the controller and agents keep watching the shadowed
CRD store. Restart **after** the import so agent startup pruning sees a
populated store and no-ops instead of tearing down live datapath state.

## Requirements

- cert-manager (serving certificate, etcd PKI).
- For the production etcd: the aenix-io etcd-operator (`etcd.operator.enabled`),
  replicated and PVC-backed. The default is a built-in single-pod etcd with
  emptyDir storage — dev/evaluation only; a pod reschedule erases every
  `sdn.cozystack.io` object.
- The [cozyplane](../cozyplane) chart (any order relative to it — but the CNI
  normally lands first).
