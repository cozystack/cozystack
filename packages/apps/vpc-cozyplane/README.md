# VPC (cozyplane)

A Virtual Private Cloud backed by the cozyplane CNI: an isolated overlay network for the tenant's workloads, with optional peering to other tenants' VPCs and an explicit north-south boundary.

## Service details

This is the VPC application for the `cozyplane` networking variant. It serves the same catalog kind as the kube-ovn/multus-backed VPC application, but creates `sdn.cozystack.io` resources: a namespaced `VPC` (the overlay, with a cluster-unique network id assigned by the platform), a `VPCBinding` that authorizes the tenant's own namespace to attach workloads (attachment is default-deny, ownership and use are separate), one `VPCPeering` half per declared peer, and an optional `VPCGateway`.

Workloads attach to the VPC with the `sdn.cozystack.io/vpc` annotation naming the VPC; there are no secondary network interfaces and no NetworkAttachmentDefinitions on this variant.

## Deployment notes

VPC name must be unique within a tenant. Address ranges may overlap with other VPCs — isolation is by overlay, not address space.

Peering is declared bidirectionally: each owner lists the other in `peers`, and the peering is live only while both declarations exist. Removing either side stops cross-VPC traffic immediately. The two VPCs' address ranges must not overlap.

By default a VPC is a closed island. Enabling the gateway opens many-to-one egress for the VPC's workloads (`gateway.nat`) and, separately, admits `Service` type=LoadBalancer traffic onto the VPC's workloads (`gateway.ingressLoadBalancer`). Cluster-internal destinations remain refused either way; cluster DNS keeps working without the gateway.

## Parameters

### Common parameters

| Name                          | Description                                                                                                            | Type       | Value   |
| ----------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `cidrs`                       | Address ranges (IPv4 and/or IPv6) of the VPC. May overlap with other VPCs: isolation is by overlay, not address space. | `[]string` | `[]`    |
| `mtu`                         | MTU advertised to workloads in this VPC. 0 selects the platform default.                                               | `int`      | `0`     |
| `peers`                       | VPC peering connections (bidirectional declaration required)                                                           | `[]object` | `[]`    |
| `peers[i].vpcName`            | Name of the remote VPC (without "virtualprivatecloud-" prefix)                                                         | `string`   | `""`    |
| `peers[i].tenantNamespace`    | Namespace of the remote tenant                                                                                         | `string`   | `""`    |
| `gateway`                     | North-south boundary of the VPC                                                                                        | `object`   | `{}`    |
| `gateway.enabled`             | Open a path to the outside. Without a gateway the VPC is a closed island: no way out, no way in.                       | `bool`     | `false` |
| `gateway.nat`                 | Many-to-one egress for workloads with no external address of their own.                                                | `bool`     | `true`  |
| `gateway.ingressLoadBalancer` | Admit Service type=LoadBalancer traffic onto this VPC's workloads.                                                     | `bool`     | `false` |
| `gateway.loadBalancerClass`   | The LoadBalancer implementation that allocates the egress identity. Empty selects the cluster default.                 | `string`   | `""`    |

