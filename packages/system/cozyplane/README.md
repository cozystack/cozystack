# cozyplane Helm chart

Packages cozyplane — a multi-tenant eBPF CNI (flat default network + VPC
tenancy + VPC peering) — as the node agent DaemonSet, the controller
Deployment, the `local.sdn.cozystack.io` CRDs (underlay IPAM), RBAC, and the
VPCBinding `export` admission policy. The tenant API is not here: the
`sdn.cozystack.io` group is served only by the separate
[cozyplane-apiserver](../cozyplane-apiserver) chart.

cozyplane is the **primary CNI**. Install it on a cluster with no other CNI
(or in place of one being removed), and keep a Service implementation — stock
kube-proxy or Cilium in kube-proxy-replacement mode — alongside it. See
[../../docs/user-guide.md](../../docs/user-guide.md) for the full deployment and
usage walkthrough and [../../docs/control-plane.md](../../docs/control-plane.md)
for the tenancy model.

## Requirements

- Kubernetes >= 1.30 (the `export` ValidatingAdmissionPolicy; set
  `exportPolicy.enabled=false` for older clusters).
- A Linux kernel with BTF (`/sys/kernel/btf/vmlinux`), 5.10+.
- No per-node `spec.podCIDR` requirement: the fabric pool is flat and cluster-wide
  (`--cluster-cidr`), so cozyplane works with or without the node-ipam controller.

## Install

```bash
helm install cozyplane ./chart/cozyplane --namespace cozy-cozyplane --create-namespace
```

This chart installs the CNI and the `local.sdn.cozystack.io` group
(`FabricIP` — underlay IPAM, which has to work before cert-manager, etcd and
cozyplane's own apiserver, all of which are default-network pods).

The tenant kinds — `VPC`, `Port`, `SecurityGroup`, `HostFirewall` and the rest of
`sdn.cozystack.io` — have no CRDs and are served only by
[cozyplane-apiserver](../cozyplane-apiserver), so install that chart too for
tenancy. The two groups are deliberately separate: serving one group through
both a CRD and an APIService collides the kube-apiserver's OpenAPI merge, after
which `kubectl apply` fails for every object in the group. See
[docs/api-groups.md](../../docs/api-groups.md).

## Configuration

All knobs are documented inline in [`values.yaml`](values.yaml). The ones you are
most likely to set:

- `image` — the cozyplane container image (digest-pinned by the release
  pipeline).
- `mtu` — pod MTU (underlay MTU minus ~50 bytes of Geneve overhead).
- `cniConfName` — the CNI conflist filename; use a low prefix such as
  `00-cozyplane.conflist` to sort ahead of a co-installed CNI (e.g. Cilium).
- `genevePort` — override only to avoid a clash with another overlay on 6081.
- `exportPolicy.enabled` — the VPCBinding export admission gate (needs k8s 1.30+).
- `crds.enabled` — the `local.sdn.cozystack.io` CRDs (default true; disable
  only if you install them out-of-band). It does not affect the tenant group,
  which has no CRDs.
- `egress.*` — cluster networking facts (pod/service CIDRs, cluster DNS) that
  drive node masquerade and the pool-less per-VPC egress gateway pod (a
  `VPCGateway` *with* a pool needs none -- its NAT is eBPF); add node/management networks to
  `egress.internalCIDRs`.

## What gets installed

- `cozyplane-agent` (DaemonSet, hostNetwork + privileged): the datapath manager
  and CNI binary installer, one per node.
- `cozyplane-controller` (Deployment): assigns VNIs, reaps Ports on VPCBinding
  revocation, and maintains VPCPeering status.
- The `local.sdn.cozystack.io` CRDs: `fabricips`. The tenant kinds
  (`vpcs`, `ports`, `securitygroups`, …) are served by
  [cozyplane-apiserver](../cozyplane-apiserver), not by this chart.
- RBAC for both components, the aggregated tenant roles (`cozyplane-tenant-edit` /
  `-view`, docs/multitenancy.md), and the export
  ValidatingAdmissionPolicy.
