# proxmox-network — Proxmox tenant networks

The package gives the platform, not the tenant, the bridge, the VLAN and the IP pool of every Proxmox-backed tenant cluster. On the raw path a tenant writes `proxmox.network.bridge`, `vlan` and `ipv4Config` into its `Kubernetes` and `KubernetesNodes` values, so nothing keeps one tenant's workers off another tenant's VLAN. With this package the tenant writes one name, `proxmox.network.name`, and the platform's zone table says what it means.

Nothing runs. The package renders a cluster-scoped `ProxmoxNetworkZone` per VLAN-aware bridge, one `InClusterIPPool` per tenant network in the namespace that owns it, and three `ValidatingAdmissionPolicies` that keep Cluster API machines on those networks. A VLAN is in use exactly while it is listed in the table.

## Objects

| Object | Scope | Written by | Read by |
|---|---|---|---|
| `ProxmoxNetworkZone` | cluster | this package, from `zones` | the admission policy (as its parameter), the `kubernetes` and `kubernetes-nodes` charts (`lookup`) |
| `InClusterIPPool pxnet-<name>` | the owning namespace | this package | capmox, through each machine's `ipv4PoolRef`; tenants may read it |
| `ValidatingAdmissionPolicy` and bindings | cluster | this package | the apiserver |

Tenants have no write access to `proxmox.cozystack.io`, `ipam.cluster.x-k8s.io` or `infrastructure.cluster.x-k8s.io`; the platform's Helm releases render capmox's objects from tenant values, and the policies judge what they render.

## Zone table

```yaml
zones:
  - name: default
    bridge: vmbr70          # VLAN-aware, the same name on every hypervisor node
    mtu: 1500
    uplinkVLAN: 70          # the outer VLAN that carries vmbr70 between hypervisors
    uplinkBridges: [vmbr0]  # bridges over the uplink's parent device (bond0)
    networks:
      - namespace: tenant-a
        name: workers
        vlan: 201
        addresses: ["198.18.1.10-198.18.1.250"]
        gateway: 198.18.1.1
        prefix: 24
```

One zone per bridge, `(namespace, name)` unique across zones, every network's VLAN unique within its zone and different from the uplink VLAN: the chart refuses the rest at render time, and the CRD refuses a hand-edited zone the same way.

The tenant then writes, in its `Kubernetes` application and in every `KubernetesNodes` pool that should share the network:

```yaml
proxmox:
  network:
    name: workers
```

The `kubernetes-nodes` chart gives net0 the zone's bridge, the network's VLAN and MTU and an `ipv4PoolRef` to `pxnet-workers`; capmox claims every worker address from that pool. The `kubernetes` chart gives the `ProxmoxCluster` an `ipv4Config` that holds only the gateway, an address the IPAM provider never hands out, because capmox insists on one and builds a pool of its own from it. A raw `bridge`, `vlan` or `ipv4Config` next to the name is refused.

## The router

L3 between tenant VLANs is the operator's router, as on the raw path: it serves `gateway` on each VLAN and decides what a tenant network reaches (the control-plane LoadBalancer range, DNS, NTP, the Internet) and what it does not (other tenants' VLANs). The platform renders the table the router needs: `kubectl get pxzone -o yaml` lists every VLAN with its CIDR and gateway. Routing between tenant VLANs inside the platform's own network is the subject of the follow-up proposal.

## Admission

Three policies, all on `ProxmoxMachine`, `ProxmoxMachineTemplate` and `ProxmoxCluster`, on CREATE and on UPDATE of the network part of the spec (capmox keeps updating its machines, and its webhook does not refuse a changed network):

- **`proxmox-network-zone-binding`**, one binding per zone with the zone object as its parameter. On the zone's bridge, a NIC must be a `(bridge, VLAN, pool)` triple of a network its namespace owns in that zone; in a namespace that owns one, a machine must declare `network.default`, or the VM keeps the template's NIC. The zone's `uplinkVLAN` is refused as the tag of a NIC on any other bridge, and no NIC may sit on an `uplinkBridges` entry: both are ways onto the trunk under the zone bridge with any inner tag the guest picks. Each binding judges only its zone's bridge, so a namespace with networks in two zones passes both. A binding whose zone is gone denies every machine (`parameterNotFoundAction: Deny`).
- **`proxmox-network-cluster-pools`**: in a namespace that owns a network, a `ProxmoxCluster`'s own `ipv4Config` may list only gateways of that namespace's networks.
- **`proxmox-network-raw-networks`**, only with `admission.rawNetworks: Warn` or `Deny`: every machine NIC must sit on a zone bridge with a platform pool. `Allow` (the default) leaves clusters on the raw path alone; `Warn` admits them with a warning and an audit annotation; `Deny` refuses them. Platform namespaces (`cozy-*`, `kube-system`) and `admission.exemptNamespaces` are not judged by this one.

What admission does not cover: VMs created outside Cluster API, and a bridge over the uplink's parent device that is not listed in `uplinkBridges`. For those the hypervisor has to drop guest VLAN tags on such bridges.

## Migration from the raw path

1. Enable the package (`bundles.enabledPackages: [cozystack.proxmox-network]`) with the zone table; `rawNetworks` stays `Allow`.
2. For each tenant, add its networks to the table, then set `proxmox.network.name` in its `Kubernetes` application and `KubernetesNodes` pools and drop `ipv4Config`, `bridge` and `vlan`. The pool rolls its workers onto the new NIC.
3. Set `rawNetworks: Warn`, read the audit annotations, then `Deny`.

Removing a network from the table deletes its pool. The IPAM provider's webhook refuses that while an address is still claimed, so the release's upgrade fails visibly until the network's last VM is gone; remove the VMs first.

## Observability

`ProxmoxNetworkPoolNearlyFull` fires when a `pxnet-*` pool is over 90% used, on `capi_ipam_inclusterippool_addresses_*`, which kube-state-metrics exports through the custom resource state in `packages/system/monitoring-agents`. A tenant whose network is not in the table, listed twice, or without its pool yet sees the reason on its HelmRelease: the charts fail the render with it.

## Values

See `values.yaml`. The e2e suite in `hack/e2e-chainsaw/proxmox-network` renders this chart from its `test-values.yaml` and exercises the CRD, the pools and every policy rule against the apiserver, without a hypervisor.
