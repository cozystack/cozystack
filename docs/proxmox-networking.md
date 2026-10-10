# Proxmox networking: VPC subnets backed by Proxmox VLANs

**Status:** Proposed (v1alpha1). Prototype: `proxmox.cozystack.io/v1alpha1`, `cmd/proxmox-network-controller`, `packages/system/proxmox-network`, and the `proxmox` fields of the `vpc`, `kubernetes` and `kubernetes-nodes` charts.

This is the networking part of running tenant Kubernetes workers on Proxmox VE. Whether Proxmox becomes a supported platform, and who maintains it, is discussed in [#3481](https://github.com/cozystack/cozystack/issues/3481). The design proposal for this feature, with its alternatives and the decisions it needs, is [`design-proposals/proxmox-vpc-networking`](https://github.com/cozystack/community/tree/main/design-proposals/proxmox-vpc-networking) in `cozystack/community`. This document describes the implementation in this repository.

The examples use one topology throughout. Every value in it is an example, not a default, except where a section says so:

- the hypervisors' uplink is a bond, `bond0`, and the tenant transport rides on an outer VLAN of it, `70`, as the VLAN device `bond0.70`;
- the zone bridge is `vmbr70`, VLAN-aware, on `bond0.70`; `vmbr0` is the hypervisors' own bridge directly on `bond0`;
- tenant VLANs come from `101-199`, and the transit VLAN is `100`, with the example transit network `10.70.0.0/24` and its gateway `10.70.0.1`;
- the Kube-OVN provider network is `pxtenant` (the package default) on the trunk NIC `ens19` (also the package default), the transit subnet is `px-transit`, and tenant subnets use `10.208.0.0/16`;
- the uplink runs a 9000-byte MTU, so the zone `mtu` is 8996 (§3.4).

## 1. Summary

A tenant extends a subnet of its Cozystack VPC onto Proxmox by adding `proxmox: {}` to the subnet. The platform then:

- turns the Kube-OVN subnet into a VLAN-backed (underlay) logical switch of the tenant's own VPC router;
- allocates a VLAN for it from an infrastructure-owned range and carries that VLAN to the cluster nodes over one trunk NIC;
- creates one `InClusterIPPool` for the part of the subnet that Proxmox VMs use;
- lets a Proxmox-backed `Kubernetes` cluster or `KubernetesNodes` pool refer to the subnet by name. capmox then gets bridge, VLAN tag and IPAM pool from the platform, never from the tenant.

The tenant sees one object, the VPC subnet. The VLAN, the bridge and the IP pool are implementation details that the tenant cannot set.

```
Cozystack VPC subnet           (tenant intent: name, CIDR)
        |
        v
Kube-OVN Vpc / Subnet          (logical switch + router port, isolation, routing)
        |                       Subnet.spec.vlan --> Kube-OVN Vlan (id, provider)
        v
ProxmoxNetwork                 (provider object: zone, VLAN, bridge, pool, status)
        |
        +--> InClusterIPPool   (CAPI IPAM authority for VM addresses)
        |
        v
ProxmoxNetworkZone             (infrastructure: bridge, VLAN range, trunk)
        |
        v
Proxmox VLAN-aware bridge      (802.1Q transport, both hypervisors)
```

## 2. Goals and non-goals

Goals:

- The VPC subnet is the single source of truth. Kube-OVN implements it. VLAN IDs are transport details chosen by the platform.
- Proxmox VMs and Kubernetes pods share the same logical network. A VM and a pod on the same subnet are L2 neighbours, and the VPC router routes between the VPC's subnets.
- `cluster-api-ipam-provider-in-cluster` allocates every CAPI machine address, through the existing `IPAddressClaim`/`IPAddress` contract that capmox already speaks.
- Tenant isolation holds at every layer: VPC router, Kube-OVN logical switch, Proxmox VLAN and physical VLAN filtering.
- Every step is declarative and idempotent. It reconverges after a controller restart or a Proxmox API outage, and holds resources until their last consumer is gone.

Non-goals (first implementation):

- no new IPAM, no VM lifecycle controller, no new tenant abstraction, no SDN of our own. capmox still creates VMs; Kube-OVN still owns the logical network;
- no SDN apply triggered by a tenant operation, and no SDN objects in the first implementation. Proxmox VE 9 runs `ifreload -a` on every node at each SDN apply, which is a host-wide risk we do not take for a tenant operation. §13 gives the phased plan: admin-created SDN objects later (A1, B-pool); SDN as transport needs only values, its optional read-only checks and VNets per network need the API changes of §13.5;
- no inbound path (EIP/DNAT) from outside the VPC to a VM. Workers reach their control plane outbound (konnectivity), so it is not needed for a CAPI cluster;
- no ClusterIP access from VMs. VMs reach the pods of their own VPC subnets; the management cluster's Services stay out of reach unless a Service is exposed by a LoadBalancer the egress path can reach.

## 3. Architecture

### 3.1 Context (C4 level 1)

```
                 +-----------------------+
 Tenant user --->| Cozystack API         |  VirtualPrivateCloud, Kubernetes,
                 | (apps.cozystack.io)   |  KubernetesNodes applications
                 +-----------+-----------+
                             |
 Platform admin ---> ProxmoxNetworkZone, Kube-OVN ProviderNetwork, transit
                             |
                 +-----------v-----------+        +-------------------------+
                 | Cozystack management  |------->| Proxmox VE cluster      |
                 | cluster (Kube-OVN,    | API    | (capmox, CCM, CSI)      |
                 | Cilium, CAPI, capmox) |<======>| VLAN-aware bridge       |
                 +-----------------------+ 802.1Q +-------------------------+
                                                        |
                                                  physical L2 (VLAN filtering)
```

### 3.2 Containers (C4 level 2)

```
[vpc chart]            renders Vpc (policyRoutes), Subnet(vlan, logicalGateway),
   |                   NAD, InClusterIPPool, ProxmoxNetwork, VpcEgressGateway
   v
[proxmox-network-controller]
   |--> Kube-OVN Vlan (allocates id, holds finalizer)
   |--> ProxmoxNetwork.status (vlan, bridge, pool usage, conditions)
   |--> Namespace annotation proxmox.cozystack.io/networks (for admission)
   |--> finalizers on Subnet / Vlan until the pool is empty
   |--> port security off on the VPC egress gateway pods
   |--> OVN NB: HA_Chassis_Group px-<router port> on each network's router
   |    port (the subnet gateway), holding the zone's trunk nodes (§3.5)
   v
[kube-ovn-controller]   logical switch + localnet port tagged with the VLAN,
   |                    router port (subnet gateway) on the tenant VPC router
   v
[kube-ovn-cni on nodes] OVS bridge on the trunk NIC (ProviderNetwork)

[kubernetes / kubernetes-nodes charts]
   |  look up ProxmoxNetwork in the release namespace
   v
[capmox] ProxmoxMachine.network.{default,additionalDevices} = bridge, vlan,
   |      ipv4PoolRef --> IPAddressClaim
   v
[ipam-in-cluster] IPAddress from the subnet's InClusterIPPool
```

### 3.3 Datapath

```
 Proxmox VM (net0 tag=101)     Proxmox VM (net0 tag=102)
        |                               |
 ---- vmbr70 (VLAN-aware, on bond0.70, every PVE node) ----------
        |  trunk 100-199 (one virtio NIC per worker, no IP)
        v
 worker NIC   -> OVS br-pxtenant -> localnet.subnet-a (tag 101) --+
                                 -> localnet.subnet-b (tag 102) --+-- different
                                                                  |   logical
 logical switch subnet-a  <-- router port 10.208.64.1 --> VPC-A   |   switches,
   + pods with a net1 on subnet-a                       router    |   different
 logical switch subnet-b  <-- router port 10.208.128.1 -> VPC-B   |   routers
                                                       router ----+
```

A VM uses the subnet gateway, which is the VPC router port (`logicalGateway: true`). The controller makes that port a distributed gateway port, without which OVN never answers the VM's ARP for it (§3.5). East-west traffic to the other subnets of the same VPC is routed by OVN. A Proxmox-backed subnet is not private and accepts whatever its router routes to it. Two limits apply to overlay subnets:

- An overlay subnet is `private: true`, and Kube-OVN's private ACL drops every packet from outside it. Proxmox subnets reach it only when it lists their CIDRs in `allowSubnets`.
- The overlay pod has to run on a node that carries the trunk. A reply from a pod on a node the provider network excludes is routed into the VLAN switch on that node's chassis, which has no bridge mapping for the provider network, and is predicted to be lost (§12, question 7).

There is no route between two VPCs unless both declare a peering and route to each other. Each VPC lists `routes[]` to the peer's subnet CIDRs, with `nextHopIP` set to the peer's end of the peering link. The link is a /30 in 169.254.0.0/16 derived from the sorted pair of VPC ids: the VPC whose id sorts first holds the first host address, the other the second. The chart renders `localConnectIP` with its /30, because Kube-OVN parses it as a CIDR and fails the whole Vpc reconcile on a bare address. A private overlay subnet on the other side still has to admit the CIDRs through `allowSubnets`. With egress enabled, these routes keep their next hop (§3.4), so Proxmox VMs reach the peer over the peering link and not through the egress gateway.

### 3.4 Egress (north-south)

```
 VM -> VPC router --(policy route, source = Proxmox subnets)--> VpcEgressGateway pod
        eth0 in the VPC subnet, net1 on the transit subnet (VLAN 100), SNAT
        -> transit gateway (hypervisor or physical router) -> LoadBalancer IPs
           of tenant control planes, DNS, NTP, registries
```

With `egress.enabled` the VPC router carries these policies, highest priority first:

| Priority | Match | Action | Written by |
|---|---|---|---|
| 31000 | `ip4.dst == <own subnet>` | allow | Kube-OVN |
| 30500 | `ip4.dst == <route>`, one per IPv4 `routes[].cidr` narrower than /0, host bits cleared | allow | vpc chart (`Vpc.spec.policyRoutes`) |
| 29150 / 29100 | source is a Proxmox-backed subnet | reroute to the gateway pods | Kube-OVN (plus a 29090 drop with BFD) |

OVN applies router policies after the routing table, so without the 30500 entries the gateway would also take traffic that a static route, for example to a peered VPC, sends elsewhere. A default route gets no allow, so the gateway stays the Proxmox subnets' way out; IPv6 routes get none, since the gateway reroutes IPv4 only. Kube-OVN keeps one policy per (priority, match) and uses 29000-29500, 30000, 30100 and 31000 itself. The chart uses only 30500 and keeps 30400 for the platform drop described in §8.

The transit subnet is a platform-owned Kube-OVN underlay subnet on the same trunk (VLAN 100). The egress gateways of every VPC share its L2, so its ACLs keep each gateway to the router. Both rules are from-lport: they run on the sender's chassis with `inport` set to the sending port. A to-lport rule could not tell gateways on different chassis apart, because the frame arrives there through the localnet port.

- priority 2001, allow `inport == "localnet.<transit>"`: traffic entering from the router and the outside;
- priority 2000, drop `ip4.dst` inside the transit CIDR other than the router address.

A gateway thus reaches the router and, through it, the outside, but no other address on the transit network. ovn-trace confirms all three cases: gateway A to the router is allowed, gateway A to gateway B is dropped, and the router to gateway B is allowed.

The transit gateway forwards only to the destinations the platform allows (control-plane LoadBalancer range, DNS, NTP, the Internet) and has no route back into any VPC CIDR. Today that is the only thing that stops routed traffic from one VPC to another VPC's CIDR (§8).

#### MTU budget

| Interface | MTU |
|---|---|
| VM NIC | zone `mtu`, else the bridge's (Proxmox default) |
| pod on a Proxmox-backed subnet, the egress gateway's eth0 included | the vpc chart sets `Subnet.spec.mtu` to the zone `mtu`; without a zone `mtu`, Kube-OVN's global MTU (1400 with Geneve over 1500-byte NICs) |
| egress gateway net1 (transit subnet) | `transit.mtu` of the package; unset, the provider network's (the trunk NIC's) |
| trunk NIC of a node (provider network) | the zone bridge's MTU, set on the Proxmox NIC (`mtu=`), which virtio passes to the guest |
| overlay pod | Kube-OVN's global MTU (1400) |

The zone `mtu` is the one number to choose. It must fit the uplink with both tags: the outer tag rides on the uplink VLAN device, but the inner tenant tag is payload of that device, so the zone `mtu` is the uplink VLAN device's MTU minus 4. In the example topology `bond0`, `bond0.70`, `vmbr70` and the trunk NICs run 9000, and the zone, the transit network and the transit router's `vmbr70.100` 8996. A switch on the uplink path must pass 9000 + 14 + 8 bytes (two tags) plus FCS.

Kube-OVN gives a pod on a `logicalGateway` subnet its global MTU, not the provider network's, unless the Subnet sets `spec.mtu`; only the transit subnet, which is not `logicalGateway`, falls back to the provider's. OVN sets no `gateway_mtu` or `check_pkt_larger` on these routers, and OVS drops an oversize frame without an ICMP error. As a result:

- The zone `mtu` must reach every pod on a Proxmox-backed subnet, or a full-size VM packet into the gateway's smaller eth0 is lost: full-size VM egress becomes a black hole (TCP towards a server that advertises a full-size MSS, UDP above the gateway's MTU). The vpc chart renders `spec.mtu` from the zone for this. Kube-OVN applies `spec.mtu` at CNI ADD, so gateway and probe pods must be recreated after a change; the zone `mtu` reaches only VMs that capmox creates after it. With `egress.internalCidr` the gateway's eth0 sits on an overlay subnet, and only a zone MTU at the overlay's size or MSS clamping in the gateway helps.
- VM to overlay pod above the overlay MTU (1400) is dropped for UDP and ICMP, while TCP survives on the endpoint MSS. This is a known limit.
- Past the transit router the path MTU is the router's uplinks (for example 1500 towards the MetalLB range, and 1492 on a PPPoE Internet uplink). The router is a Linux host and returns fragmentation-needed, which the gateway relays back through its connection tracking.

The acceptance check is a DF packet of the VM's full MTU from a VM to the transit router through the egress gateway: it must arrive, or the VM must get fragmentation-needed. An Internet target cannot show the gap when the uplink's own path MTU is smaller, as it is behind a PPPoE uplink. TCP to a Kamaji LoadBalancer cannot show it either, because the overlay server's MSS (1360) sizes the segments.

### 3.5 Subnet gateway

A VM on a Proxmox VLAN reaches its subnet's VPC router port only through the logical switch's localnet port. Kube-OVN builds OVN (branch-25.03) with its patch 477695a0, "northd: skip arp/nd request for lrp addresses from localnet ports". For every router port that has neither `gateway_chassis` nor `ha_chassis_group`, the patch installs this flow in the peer switch:

```
ls_in_arp_rsp, priority 105:
  inport == "localnet.<subnet>" && arp.tpa == <gateway> && arp.op == 1  ->  drop;
```

(and the same for ND). Kube-OVN gives a subnet's router port neither, so a VM never resolves its gateway. It still reaches pods on its own subnet at L2, but nothing routed: not the other subnets of the VPC, not the egress gateway, not the Internet.

The controller therefore turns each network's router port into a distributed gateway port (DGP):

- **Router port.** `<vpc>-<subnet>`, Kube-OVN's name, built from `Subnet.spec.vpc` and the subnet name.
- **Group.** One `HA_Chassis_Group` named `px-<vpc>-<subnet>`, with `external_ids` `owner=proxmox-network` and `network=<namespace>/<name>`. The router port's `ha_chassis_group` points at it.
- **Chassis.** One `HA_Chassis` for each node that:
  - carries the label `<zone.spec.providerNetwork>.provider-network.kubernetes.io/ready=true`;
  - is not being deleted;
  - has a non-empty `ovn.kubernetes.io/chassis` annotation.

  A chassis shared by two nodes is listed once.
- **Priorities.** The nodes are ranked by rendezvous hashing: each node scores a hash of `vpc/<Subnet.spec.vpc>` and its own name (FNV-1a, finished with the splitmix64 mixer), and the highest score gets the highest priority. The priorities run 100, 90, 80 and so on (from `10*n` down when there are more than ten nodes), all distinct. The key is the VPC, not the network: all Proxmox router ports of one VPC share their active chassis. A VM's packet to another subnet of its VPC, or to the egress gateway on the VPC's internal subnet, then leaves the router on the chassis it entered and goes out through a localnet port. Ports of one VPC on different chassis send it through the Geneve tunnel, and there Cilium's kube-proxy replacement (`bpf_host` on `genev_sys_6081`, which Cozystack lists in Cilium's `devices`) rewrites a LoadBalancer IP to its backend pod address, which the VPC cannot reach: in a test run with the key per network, the workers of one tenant never reached their control plane until the key changed to the VPC. Different VPCs land on different trunk nodes, and a change of trunk nodes moves only the VPCs it has to: a new node becomes active for about 1/n of them, and a node that leaves moves only the VPCs it was active for. With `egress.internalCidr` the gateway's internal subnet is an overlay subnet, so VM egress to a LoadBalancer IP still crosses the tunnel; keep the internal subnet on a Proxmox subnet (the default) on clusters with Cilium in front of Geneve.
- **Effect.** The 105 drop goes away. The chassis with the highest priority claims `cr-<router port>`, and it answers ARP and routes for the VLAN. OVN fails over to the next priority on its own.
- **Resync.** The group is recomputed when a node gains or loses the ready label, changes its chassis annotation or starts being deleted, and every 5 minutes. The periodic pass also repairs a pointer someone cleared and a router port that Kube-OVN recreated without the group.

**What the controller writes.** It writes only:

- the groups it owns, by `external_ids`;
- their `HA_Chassis` rows;
- the `ha_chassis_group` column of its networks' router ports.

It adopts a group of the right name that has no owner, for example one made by hand before the controller managed gateways. It does not touch a group of that name that has another owner, or that is owned for another network. Nor does it take a router port that already has `gateway_chassis` or points at another group: it sets the pointer only while both columns are empty, and a `wait` in the same transaction aborts the write if either has changed since the read. Both cases are `ForeignGroup` (§7).

**Cost.** All routed traffic to and from a Proxmox subnet passes through that subnet's active chassis, as with a centralized gateway. The hashing spreads the subnets over the trunk nodes; it does not spread one subnet's traffic. L2 traffic inside the VLAN (VM to VM, VM to a pod on the subnet) does not pass the active chassis.

**Several DGPs per router.** A VPC router can now carry several distributed gateway ports: one per Proxmox subnet, plus the BFD or external port Kube-OVN may add. The manual experiment that preceded the controller ran that way, and VMs on every subnet reached their gateway and everything routed behind it.

**Stopgap.** This is a write to the OVN northbound database outside Kube-OVN's API. It is meant to last only until Kube-OVN sets the group itself for `logicalGateway` underlay subnets that have a localnet port (§12, question 1). Kube-OVN v1.15.10 sets `ha_chassis_group` only on its BFD port and its external gateway port (`pkg/controller/vpc.go` `reconcileVpcBfdLRP`, `external_gw.go`). The NB connection and its credentials are described in §8 and §10.

## 4. Resources and ownership

| Resource | Scope | Created by | Owned/GC by | Tenant access |
|---|---|---|---|---|
| `ProxmoxNetworkZone` | cluster | platform admin (system package values) | admin | none |
| Kube-OVN `ProviderNetwork` (trunk) | cluster | system package | admin | none |
| transit `Vlan`/`Subnet`/NAD | cluster / platform ns | system package | admin | none |
| Kube-OVN `Vpc` | cluster | vpc chart (Helm) | Helm release | via app |
| Kube-OVN `Subnet` (Proxmox-backed) | cluster | vpc chart | Helm; controller finalizer keeps it while VMs hold addresses | via app |
| NAD for the subnet | tenant ns | vpc chart | Helm | use |
| `InClusterIPPool` | tenant ns | vpc chart | Helm; `ProtectPool` finalizer (ipam provider) while addresses are in use | read |
| `ProxmoxNetwork` | tenant ns | vpc chart | Helm; controller finalizer | read |
| Kube-OVN `Vlan` | cluster | controller | controller (finalizer) | none |
| OVN NB `HA_Chassis_Group` `px-<vpc>-<subnet>`, its `HA_Chassis`, and `ha_chassis_group` on the router port | OVN NB | controller | controller (`external_ids` owner/network); deleted before the VLAN is released, and by the periodic collection when the network is gone (§6.3) | none |
| `VpcEgressGateway` | zone's gateway ns (platform) | vpc chart | Helm | none |
| `ProxmoxCluster.ipv4Config` | tenant ns | kubernetes chart | Helm | via app |
| `ProxmoxMachineTemplate.network` | tenant ns | kubernetes-nodes chart | Helm | via app |
| `IPAddressClaim`/`IPAddress` | tenant ns | capmox / ipam provider | CAPI machine | read |

Names are deterministic: the VPC is `vpc-<sha256(ns/release)[:6]>`, a subnet is `subnet-<sha256(ns/vpc/name)[:8]>`, and the `Vlan`, `InClusterIPPool`, `ProxmoxNetwork` and NAD all carry the subnet's name. Any chart can therefore compute every name from `(namespace, vpc, subnet)` without a lookup.

### 4.1 API

```yaml
apiVersion: proxmox.cozystack.io/v1alpha1
kind: ProxmoxNetworkZone            # cluster-scoped, infrastructure admin only
metadata:
  name: default
spec:
  bridge: vmbr70                    # VLAN-aware bridge present on every allowed PVE node
  providerNetwork: pxtenant         # Kube-OVN ProviderNetwork that carries the trunk
  vlanRanges: ["101-199"]           # the only VLANs the allocator may hand out
  reservedVLANs: [150]              # never allocated
  mtu: 1500                         # optional, written into VM NICs
  namespaceSelector: {}             # which namespaces may bind networks to this zone
  staticAssignments:                # admin-only explicit VLANs
    - namespace: tenant-legacy
      name: subnet-0a1b2c3d
      vlan: 120
  egress:
    externalSubnet: px-transit      # Kube-OVN subnet the VPC egress gateways attach to
    gatewayNamespace: cozy-proxmox-network  # platform namespace the gateways run in
status:
  conditions: [{type: Ready, status: "True"}]
  totalVLANs: 98
  allocatedVLANs: 2
  freeVLANs: 96
  networks: 2
---
apiVersion: proxmox.cozystack.io/v1alpha1
kind: ProxmoxNetwork                # namespaced, written by the vpc chart, read-only to tenants
metadata:
  name: subnet-3f2a9c1e
  namespace: tenant-a
spec:
  zone: default
  subnet: subnet-3f2a9c1e           # Kube-OVN Subnet (cluster-scoped)
  ipPool: subnet-3f2a9c1e           # InClusterIPPool in this namespace
status:
  vlan: 101
  vlanName: subnet-3f2a9c1e         # Kube-OVN Vlan object
  bridge: vmbr70
  cidr: 10.208.64.0/24
  gateway: 10.208.64.1
  prefix: 24
  ipPool: {total: 100, used: 3, free: 97}
  conditions:
    - {type: VLANAllocated, status: "True"}
    - {type: SubnetReady,   status: "True"}
    - {type: IPPoolReady,   status: "True"}
    - {type: GatewayReady,  status: "True"}   # reason ChassisAssigned (§3.5, §7)
    - {type: Ready,         status: "True"}
    # while deleting only: {type: SwitchDeleted, status: "False", reason: ListedOnVlan} (§6.3)
```

## 5. Mappings

### 5.1 VPC subnet to Kube-OVN

| VPC app value | Kube-OVN `Subnet` | Notes |
|---|---|---|
| `cidr` | `cidrBlock` | unchanged |
| (subnet first address) | `gateway` | VPC router port; `logicalGateway: true` |
| `proxmox: {}` | `vlan: <subnet name>` | Vlan created by the controller |
| `proxmox.vmRange` | `excludeIps: [vmRange]` | Kube-OVN never allocates VM addresses |
| — | `provider: <nad>.<ns>.ovn` | pods attach as a secondary network, as today |
| — | `private: false` | routed by the VPC router (other subnets, egress). Kube-OVN applies `allowSubnets` only through the private ACL, so the chart refuses `allowSubnets` on a Proxmox-backed subnet (an empty list passes). Overlay subnets keep `private: true` and list the Proxmox subnets in `allowSubnets` when they should be reachable from them |
| (zone `mtu`) | `mtu` | pods, the egress gateway included, get the VMs' MTU; without a zone `mtu` nothing is set and Kube-OVN's global MTU applies (MTU budget in §3.4) |

### 5.2 Subnet to Proxmox VLAN and bridge

`ProxmoxNetwork.status.vlan` comes from the zone's allocator and `status.bridge` from `zone.spec.bridge`. A VM NIC on the subnet is `bridge=<status.bridge>,tag=<status.vlan>`. The cluster nodes do not take one NIC per VLAN: each node in the provider network has one trunk NIC on the same bridge (`trunks=<zone range>`), and Kube-OVN tags the localnet port.

Example (`tenant-a/vpc-a/private`):

```
vpc-a/private
  -> subnet:   subnet-3f2a9c1e
  -> vlan-id:  101            (allocated)
  -> bridge:   vmbr70
  -> cidr:     10.208.64.0/24
  -> gateway:  10.208.64.1    (VPC-A router port)
  -> VM range: 10.208.64.100-10.208.64.199 (InClusterIPPool subnet-3f2a9c1e)
```

### 5.3 Subnet to IPAM

Exactly one authoritative split of the subnet, computed from one value set in one chart:

```
10.208.64.0/24
  .1                gateway (VPC router port; excluded from both allocators)
  .2 - .99          Kube-OVN IPAM (pods on the subnet)
  .100 - .199       InClusterIPPool  == Subnet.excludeIps   (Proxmox VMs)
  .200 - .254       Kube-OVN IPAM
```

- `vmRange` defaults to the middle of the subnet and can be set explicitly. The chart refuses a range outside the CIDR or one that contains the gateway.
- The controller verifies the split on every reconcile. A pool whose addresses are not all inside `cidrBlock` and inside `excludeIps` sets `IPPoolReady=False/PoolOverlapsSubnet`, and the network is not Ready.
- In-cluster IPAM does not check overlaps between pools. Overlap is prevented because the only writer of pools in tenant namespaces is the platform: tenants have no RBAC on `ipam.cluster.x-k8s.io`.
- `InClusterIPPool` is namespaced and a claim resolves it only in its own namespace, so one tenant cannot claim from another tenant's subnet. `GlobalInClusterIPPool` is not used for tenant networks.
- The pool carries a `metric` annotation of `100 + <subnet index>`, so a VM with two NICs never gets two default routes with the same metric.
- IPv6: the same split works for a dual-stack subnet with an `ipv6PoolRef`. It is not enabled, because it has only been run on clusters whose Cilium has no IPv6.

### 5.4 CAPI and capmox

```yaml
# Kubernetes application (cluster level)
proxmox:
  network:
    vpc: vpc-a
    subnet: private
```

- `ProxmoxCluster.spec.ipv4Config` is still mandatory in capmox v0.7, and capmox builds a cluster pool from it. When the cluster uses a VPC subnet, the chart renders a sentinel `ipv4Config` whose only address is the gateway, which the provider always excludes. The resulting pool has zero allocatable addresses, so a machine that somehow lacks `ipv4PoolRef` fails to get an address instead of taking one twice.
- `ProxmoxMachineTemplate.spec.network.default` becomes `{bridge, vlan, mtu, ipv4PoolRef: InClusterIPPool/<subnet>}`. capmox creates `IPAddressClaim <machine>-net0-inet` against the subnet pool. The address is released when the machine is deleted.
- Raw `bridge`/`vlan` and raw `ipv4Config` stay for clusters that do not use a VPC. Mixing them with `network.subnet` is refused.

### 5.5 Multi-NIC

```yaml
# KubernetesNodes application (pool level)
proxmox:
  network:            {vpc: vpc-a, subnet: private}    # net0
  additionalNetworks:
    - {name: net1, vpc: vpc-a, subnet: data}            # net1
```

Each additional entry becomes a capmox `additionalDevices` element with its own `ipv4PoolRef`, bridge and VLAN, all resolved from that subnet's `ProxmoxNetwork`. For every NIC the chain is the same: `subnet -> ProxmoxNetwork -> InClusterIPPool -> IPAddressClaim -> IPAddress -> Proxmox NIC (bridge, tag)`. A NIC on a public network is the same mechanism with a platform-owned zone whose namespaceSelector admits the tenant.

## 6. Lifecycle

### 6.1 Create

```
VPC app with proxmox subnet
  -> Helm: Vpc, Subnet(vlan=<name>), NAD, InClusterIPPool, ProxmoxNetwork
  -> controller: zone check -> allocate VLAN -> create Vlan (finalizer)
                 -> finalizer on Subnet -> namespace annotation
  -> kube-ovn: Subnet validated (Vlan exists, no conflict) -> logical switch,
               localnet tag, router port
  -> controller: SubnetReady, IPPoolReady
                 -> HA chassis group on the router port (GatewayReady) -> Ready
```

The Subnet is created before its Vlan exists. Kube-OVN keeps retrying with `failed to get vlan` until the controller creates it, so ordering needs no coordination.

Ready waits for `GatewayReady` (§3.5), except during the NB grace of §7. A network whose VMs could not reach their gateway is not Ready, so the kubernetes charts refuse to place machines on it (§6.2). `GatewayReady` is `Unknown/WaitingForVLAN` until a VLAN is allocated, `Unknown/SubnetUnavailable` until the Subnet is bound, and `False/RouterPortMissing` until Kube-OVN has created the router port. Kube-OVN patches the Subnet's status right after it creates the switch and the router port, and the controller watches Subnets, so the next pass finds the port. Until then the network is also retried every 30s. With gateway management off (`ovn.manageGatewayChassis: false`), the condition is `Unknown/Disabled` and Ready ignores it.

### 6.2 Attach a VM

```
Kubernetes/KubernetesNodes app -> chart looks up ProxmoxNetwork (must be Ready)
  -> ProxmoxMachineTemplate.network (bridge, vlan, ipv4PoolRef)
  -> capmox: IPAddressClaim -> ipam: IPAddress -> clone VM, set netN
     bridge+tag, write nocloud network-config -> VM boots on the VLAN
```

A ProxmoxNetwork that is not Ready fails the chart render with the condition's message, so the HelmRelease shows why the cluster is not created.

### 6.3 Delete

```
VM deleted -> claim deleted -> IPAddress released -> pool used--
VPC subnet removed:
  Helm deletes Subnet, pool, ProxmoxNetwork -> all three stay Terminating
  -> controller waits until the pool holds no address AND no      Ready=False/
     ProxmoxMachine (Terminating ones included) references it      InUse
  -> removes its finalizer from the Subnet; Kube-OVN removes      SubnetDeleting
     its own once the subnet's IPs are gone
  -> once the Subnet object is gone, clears the network's HA      GatewayRelease-
     chassis group from any router port still pointing at it      Pending (while
     and deletes the group with its chassis                       the NB is down)
  -> once the Subnet object is gone, waits until Kube-OVN drops   SwitchDeleting
     it from Vlan.status.subnets, which Kube-OVN does after
     deleting the logical switch and its localnet port
  -> deletes the Vlan (VLAN id returns to the zone) and releases
     the ProxmoxNetwork
```

Helm's delete of the pool is accepted even while VMs hold addresses. The in-cluster IPAM provider's delete webhook refuses a pool with addresses in use, and that left `helm uninstall` of a VPC release with VMs on it stuck in `uninstalling`. The vpc chart therefore annotates each `InClusterIPPool` with `ipam.cluster.x-k8s.io/skip-validate-delete-webhook`, which only skips that check. The provider's `ProtectPool` finalizer still holds the deleted pool until its last address is released (`internal/controllers/inclusterippool.go:194-196` in v1.0.3), so the pool stays Terminating next to the Subnet and the ProxmoxNetwork.

A VLAN is never released while a VM may still sit on it: the pool count alone is not enough, because the IPAM provider releases an address as soon as its claim is deleted, which a namespace deletion does while capmox is still destroying the VM. Nor is it released while the Subnet exists: Kube-OVN keeps the logical switch's localnet port tagged with the VLAN until the switch is deleted, and validates the Vlan before it removes its own finalizer. For the same reason a subnet cannot drop `proxmox` and keep its name; the vpc chart refuses that upgrade.

The Subnet object going away is not enough either. Kube-OVN deletes the logical switch, its localnet port and its router port afterwards, in its delete handler, and only then drops the name from `Vlan.status.subnets`. Our allocator and Kube-OVN's own conflict check see only Vlan objects, so a VLAN released earlier could go to another network while the old localnet port with that tag still sits on the provider.

That delete handler runs from an in-memory queue. If a kube-ovn-controller restart or leader change loses the event, the next leader removes the orphaned switch in its startup GC, but nothing ever prunes the name. Each deleted subnet still listed on the Vlan therefore holds the VLAN until Kube-OVN prunes it, or for at most `--subnet-prune-timeout` (default 10m) from when the controller first saw it gone. After that the controller releases the VLAN and logs `Kube-OVN did not prune deleted subnets from the Vlan in time; releasing the VLAN anyway`. The timers live on the Vlan, in the annotation `proxmox.cozystack.io/deleted-subnets`: JSON that maps each subnet name to that time (RFC 3339, UTC). They survive controller restarts and carry over to the orphan collection if the network's finalizer is removed by hand (§7). A subnet deleted later gets its own full wait. While the network waits, it carries `SwitchDeleted=False/ListedOnVlan`, whose `lastTransitionTime` marks when the wait began and is not a timer, and the Ready message gives the latest release time.

Two rules keep a timer from an earlier network from cutting a wait short:

- A network that is not being deleted drops the annotation when it adopts its Vlan. This covers a network recreated under the same name before the zone collects the old Vlan.
- A deleting network restarts any timer that started before its own `deletionTimestamp`. This covers a network that never adopted the Vlan, for example on `ZoneNotFound`, `NamespaceNotAllowed` or `VLANProviderMismatch`.

Either way a subnet that the new network deletes under an old name gets a full timeout. A controller clock behind the API server's delays the release by the skew and does nothing worse.

A subnet that still exists and is listed on the Vlan holds the VLAN with no time limit (`VLANStillReferenced`), even when its `spec.vlan` no longer names that Vlan. Kube-OVN keeps the localnet port's tag when `spec.vlan` is cleared or names a Vlan that does not exist. When it names another existing Vlan, Kube-OVN retags the port from that Vlan's update handler (`reconcileVlan` adds the subnet to the new Vlan's status, and `handleUpdateVlan` calls `setLocalnetTag`), and never prunes the name from the old Vlan. The controller cannot see the port, so it keeps holding the VLAN. That errs on the safe side, and only a subnet made or edited by hand can cause it.

The timeout leaves a residual risk: the VLAN is released while the old switch still exists if Kube-OVN takes longer than the timeout to delete it. Two cases get there:

- The delete handler keeps failing, for example during an OVN NB or API outage. Its queue retries with a backoff that starts at 5ms and doubles up to 1000s, so the retry that follows an outage comes up to twice the outage's length after the first attempt, and up to 1000s after the outage ends. Two consecutive retries fall about 5.5 and 10.9 minutes after the first attempt, so an outage of about 5.5 minutes already pushes the delete past the 10m timeout.
- No kube-ovn-controller runs for longer than the timeout. The VLAN is then released before the next leader's startup GC removes the switch.

A network that gets the VLAN in that window shares it with the old localnet port until Kube-OVN deletes the switch. Where such outages are expected, raise `--subnet-prune-timeout` above twice the longest of them (package value `proxmoxNetworkController.subnetPruneTimeout`, a Go duration such as `30m`).

The HA chassis group (§3.5) goes after the Subnet and before the VLAN.

- The group is found by its `external_ids` (`network=<namespace>/<name>`), so the deleted Subnet's `spec.vpc` is not needed.
- If Kube-OVN has not yet deleted the router port, the controller clears the port's pointer in the same transaction.
- While the network is still `InUse` the group stays, so the VMs keep their gateway for as long as the router exists.
- An NB that cannot be reached holds the release: Ready shows `GatewayReleasePending` and the step is retried every 30s. The VLAN is not freed meanwhile. Kube-OVN needs the NB to delete the switch anyway.
- A network that never got a VLAN never reached the gateway step, so it skips this one; the collection below is its backstop.

A group whose network is gone without this step is deleted by a periodic collection: the leader runs it at start and every 5 minutes. That covers a finalizer removed by hand, and a network deleted while gateway management was off. The collection deletes only groups with `owner=proxmox-network` whose `network` names a ProxmoxNetwork that the API reports as NotFound. Unowned groups are never collected, including groups made by hand for networks that were deleted before the controller managed gateways. Those are removed by hand (§10).

## 7. Failure handling

| Failure | Detection | Behaviour | Condition / metric |
|---|---|---|---|
| VLAN already in use (Kube-OVN conflict, manual Vlan) | allocator sees used IDs from every Vlan on the provider; kube-ovn sets `Vlan.status.conflict` | allocator skips it; a conflict on our Vlan is surfaced | `VLANAllocated=False/VLANConflict` |
| Zone exhausted | allocator finds no free ID | network stays not Ready, retried on zone change | `VLANAllocated=False/ZoneExhausted`, `cozy_proxmox_zone_vlans_free == 0` |
| Zone missing or namespace not admitted | zone lookup | no allocation | `VLANAllocated=False/ZoneNotFound` or `NamespaceNotAllowed` |
| VLAN not trunked to a PVE node / bridge missing | capmox VM start fails, or no traffic | capmox `VMProvisionFailed` on the machine; the E2E checks the trunk | `ProxmoxMachine` conditions; `cozy_proxmox_network_consumers` stays 0 |
| IP pool exhausted | ipam provider cannot satisfy a claim | machine waits; nothing is allocated twice; the VMs already on the network keep working, so it stays Ready | pool status `free=0`; `IPPoolReady=True/PoolExhausted`; alert above 90% |
| Subnet CIDR conflict | kube-ovn rejects overlapping CIDRs in one VPC; Cilium requires cluster-wide unique pod CIDRs | Subnet not Ready | `SubnetReady=False/<kube-ovn reason>` |
| IPAM allocation failure | capmox `IPAddressClaim` not bound | machine waits | claim conditions; alert on `free=0` |
| Proxmox API unavailable | capmox | capmox retries; nothing on our side depends on the Proxmox API | capmox conditions |
| Partial VM creation | capmox | capmox owns retries and cleanup; our resources are untouched | capmox conditions |
| VM created, network attachment failed | capmox / guest | machine never joins; MachineHealthCheck replaces it; the claim is released with it | machine conditions |
| Network deleted while VMs are attached | finalizers | Subnet, Vlan, pool and ProxmoxNetwork stay, so traffic inside each held VLAN keeps working (VM to VM, VM to pod). The VpcEgressGateway goes with the release (unless its finalizer sticks, below), so the VMs lose egress and their control-plane connection. Kube-OVN may also delete the Vpc and its router once every Subnet left in it is terminating, so routed traffic is not guaranteed. The machines are covered below | `Ready=False/InUse`, then `SubnetDeleting` and `SwitchDeleting` (§6.3); `cozy_proxmox_network_deleting` |
| VPC release uninstalled while VMs hold pool addresses | the in-cluster IPAM provider's delete webhook refuses a pool with addresses in use | the vpc chart annotates each pool with `ipam.cluster.x-k8s.io/skip-validate-delete-webhook`, so the delete is accepted and the uninstall goes through. The provider's `ProtectPool` finalizer keeps the pool Terminating until its last address is released (§6.3). Without the annotation `helm uninstall` stayed `uninstalling` while the VMs existed | pool Terminating; `Ready=False/InUse` |
| Kube-OVN loses a subnet delete (restart or leader change) | the deleted subnet stays in `Vlan.status.subnets` | VLAN held for up to `--subnet-prune-timeout` (10m) from when the controller first saw that subnet gone, then released with a log line (§6.3) | `Ready=False/SwitchDeleting`, `SwitchDeleted=False/ListedOnVlan` |
| Network finalizer removed by hand (orphaned zone Vlan) | the zone finds a Vlan with its labels whose ProxmoxNetwork is gone | a live listed subnet, or a Subnet whose `spec.vlan` names the Vlan, holds it with no time limit. Deleted listed subnets hold it until Kube-OVN prunes them or their timers (§6.3) run out; then the zone collects it and logs `Kube-OVN did not prune deleted subnets from the orphaned Vlan in time; collecting it anyway`. The zone uses the same `--subnet-prune-timeout` as the networks | `cozy_proxmox_orphaned_vlans`. `ProxmoxNetworkOrphanedVLANs` (for: 30m) therefore fires only for orphans that live subnets hold, unless the timeout is above 30m |
| Trunk NIC down on a node Kube-OVN reports ready (on Talos: the default LinkSpec, §10) | NIC not admin UP, OVS `link_state` down, `link_resets` growing; the E2E preflight checks all three | the node keeps the provider network's ready label, so gateways and pods on Proxmox subnets are still placed there and lose their VLAN traffic | — |
| Inner 802.1Q tag lost between hypervisors (NIC VLAN offload, §10) | neither the controller nor the E2E preflight sees it; `tcpdump -e` on the receiving uplink and the bridge fdb do (§10) | everything that crosses hosts is lost: a VM to its gateway and to pods, the transit router to the egress gateways. The far bridge puts every tenant VLAN of the other host into its VLAN 1 | — |
| Router port without an HA chassis group (management off, NB unreachable before the first pass, someone cleared it, Kube-OVN recreated the port) | `GatewayReady` not True; `ovn-sbctl lflow-list <subnet>` shows the 105 drop; the E2E control cells | VMs get no ARP answer for their gateway: L2 inside the VLAN works, nothing routed does. The controller writes or repairs the group at its next pass (Node event, 5m resync, 30s after an NB error) | `GatewayReady=False/<reason>`, `cozy_proxmox_network_gateway_ready == 0`, `ProxmoxNetworkGatewayNotReady` (10m) |
| OVN NB unreachable, TLS failure, or a concurrent change to a row the controller writes | NB transaction or handshake fails, an update matches no row, or the `wait` finds the router port changed since the read | an existing group keeps working, because OVN keeps it. A network whose gateway was up keeps `GatewayReady`, and with it Ready, for 10 minutes from the first failure: True when it was True, `Unknown/NBUnavailable` when it was not known yet but the network was Ready (management just turned on). After that, or at once for a network whose gateway was never up, it goes not Ready. It is retried every 30s while the rest of its status is still evaluated. A deleting network holds its VLAN (`GatewayReleasePending`, §6.3) | `GatewayReady=False/NBUnavailable`, `Ready=False`; `cozy_proxmox_gateway_nb_errors_total`, `ProxmoxNetworkGatewayNBErrors` (10m) |
| No trunk node with a chassis | no node with the provider's ready label and a chassis annotation | nothing is written; an existing group keeps its chassis | `GatewayReady=False/NoTrunkNodes` |
| Kube-OVN has not created the router port, or the Subnet has no `spec.vpc` | NB has no `<vpc>-<subnet>` | nothing is written; retried every 30s | `GatewayReady=False/RouterPortMissing` |
| A group named `px-<vpc>-<subnet>` belongs to someone else, or to another network; or the router port already has `gateway_chassis` or points at another group | the group's `external_ids`; the router port's `gateway_chassis` and `ha_chassis_group` | the controller writes nothing, and the message names what it found. Decide by hand (§10) | `GatewayReady=False/ForeignGroup` |
| The active chassis's node fails | OVN's BFD between the group's chassis | `cr-<router port>` moves to the next priority; routed traffic of that subnet stops until then. Not measured yet (§12, question 1) | — |
| Controller restart | — | reconcile is idempotent: it adopts the Vlan named after the subnet, recomputes the used set from live Vlans | — |
| Node or PVE host failure | kube-ovn, capmox | the logical switch is distributed; VMs on the lost host are capmox's concern | — |

Allocation is serialised (one worker) and reads Vlans through the uncached API reader. A crash between creating a Vlan and writing status is safe: the Vlan's name is the subnet name, so the next reconcile finds it and keeps its ID.

Deleting a VPC under a running cluster cuts the workers off their control plane, so their Nodes go `Ready=Unknown`. The pool's MachineHealthCheck remediates only while the unhealthy machines stay within `maxUnhealthy`. With the default of 50% it does nothing when the whole pool sits on the deleted network. A machine it does replace still gets an address from the Terminating pool, because the in-cluster IPAM provider does not check for a pool being deleted. The replacement boots on the held VLAN, cannot join either, and is replaced again after `nodeStartupTimeout`. The network stays `InUse` all along. Delete the cluster or the pool before the VPC, or together with it.

The VpcEgressGateway can outlive the release. If Kube-OVN deletes the Vpc's router before the gateway's delete handler has run, the handler fails on the missing router (`not found logical router`) on every retry. The gateway then stays Terminating with its finalizer `kubeovn.io/kube-ovn-controller`, and its Deployment and pods stay with it, though no traffic reaches them once the router is gone. To clear it, check that the router is gone (`ovn-nbctl lr-list` no longer lists the VPC), then remove the finalizer by hand. The gateway's BFD entries are already deleted and its router policies went with the router; its port group `VEG.<first 12 hex digits of sha256(<ns>/<name>)>` and the address sets with the same name plus `.ipv4` and `.ipv6` stay in the NB until they are deleted by hand.

Outside the 10-minute NB grace (table above), Ready follows `GatewayReady` while gateway management is on, so a gateway that stays down for 15 minutes fires `ProxmoxNetworkNotReady` (15m) as well as `ProxmoxNetworkGatewayNotReady` (10m). A network that is not Ready keeps the VMs already on it (their group stays in OVN), but the kubernetes charts place no new machines on it.

One narrow race remains. **Collection against re-creation:** if the collection decides a network is gone just as a network of the same name is created and adopts the group, the new network's gateway can be missing until its next resync (5 minutes).

Kube-OVN writing the same column is not a race. The controller sets a router port's `ha_chassis_group` only while it and `gateway_chassis` are empty, and the `wait` of §3.5 aborts the write if either changed since the read. If a future Kube-OVN sets its own group on subnet router ports, as it does today for its BFD port (where it names the group after the port), the two do not overwrite each other. Each network whose port Kube-OVN has taken turns `GatewayReady=False/ForeignGroup`, and so not Ready, and a `px-` group it already had stays in the NB unused. Turn gateway management off before such an upgrade (§10, §12 question 1).

## 8. Security

Defence in depth for tenant A (VLAN 101) and tenant B (VLAN 102):

| Layer | Mechanism |
|---|---|
| Cozystack | VPC per tenant; tenants have no RBAC on `kubeovn.io`, `ipam.cluster.x-k8s.io`, `infrastructure.cluster.x-k8s.io` or `proxmox.cozystack.io` writes; the charts derive VLAN, bridge and pool from objects in the release namespace only |
| Kube-OVN | one VPC router per tenant VPC and one logical switch per subnet; no peering unless both VPCs declare it and route to each other. Overlay subnets are `private: true`. Proxmox-backed subnets are not: they accept whatever their own router routes to them |
| Proxmox | one VLAN per subnet; each VM NIC is an access port of exactly one VLAN on a VLAN-aware bridge; the trunk exists only on cluster nodes; tenant VLANs ride inside the outer VLAN (70 in the examples), so no tag a guest sends on a tenant VLAN reaches the hypervisors' own VLANs. That holds only while no guest NIC sits on a bridge that enslaves the uplink's parent device (`vmbr0` over `bond0`). Such a NIC is a trunk on which the guest sets the outer tag itself, and the other host hands those frames to bond0.70 before its bridge sees them, so they enter vmbr70 with any inner tag. Admission keeps Cluster API machines off those bridges; any other guest on them needs the hypervisor to drop guest 802.1Q/802.1ad frames (§10) |
| Physical | a switch on the uplink path carries only the outer VLAN for tenants, or the hypervisors share a point-to-point link |
| Admission | three `ValidatingAdmissionPolicies`, below |
| Egress gateway pods | Cozystack's kube-ovn webhook turns port security on for every tenant pod, which would drop the gateway's forwarded traffic. The controller turns it off only on pods whose ownership chain (ReplicaSet, Deployment, VpcEgressGateway) ends at a gateway the vpc chart rendered |
| Routing | the vpc chart refuses a static route whose next hop is outside the VPC's own subnets, or outside the peering link range 169.254.0.0/16 when peers are declared, so a tenant cannot route into another VPC through the transit network. With egress, each route keeps its next hop through a 30500 allow (§3.4) |
| Transit | the from-lport ACL pair keeps each egress gateway to the router (§3.4); the transit router has no route into VPC CIDRs and forwards only to the LoadBalancer range, DNS, NTP and the Internet |

For a Proxmox-backed subnet, isolation from other VPCs rests on four things: one router per VPC, one VLAN per subnet, the transit ACL pair, and the transit router not forwarding into VPC CIDRs. Where the transit router is a Linux host, that last one is its drop of private destinations towards its uplink, and it carries the L3 boundary on its own. Traffic from A's Proxmox subnet to B's CIDR matches no allow, so A's router reroutes it to A's egress gateway, which SNATs it onto the transit network before anything drops it. The `A -> B L3` cell below therefore proves the transit router, not OVN; the E2E follows it with a check of the OVN boundary, which is an expected failure until §12, question 9 is decided (§11). Moving this boundary into OVN needs a platform-owned list of the tenant address space, for example on the ProxmoxNetworkZone. The vpc chart would render it as a 30400 drop between the route allows and the reroute. Tenant values cannot carry a boundary that protects other tenants (§12, question 9).

**OVN NB access (gateway management, §3.5).** The controller connects to the OVN northbound database as Kube-OVN does.

- **Credentials.** It uses Kube-OVN's client certificate, from the secret `cozy-kubeovn/kube-ovn-tls` (keys `cacert`, `cert`, `key`). The controller reads that one secret through the API: one `get` per new NB connection, no list, no watch and no copy. The parsed material is cached by `resourceVersion` and re-read after a failed handshake. Errors name only the secret and the key, never the material.
- **RBAC.** A Role and RoleBinding `proxmox-network-controller-ovn-tls` in the secret's namespace grant `get` on `secrets` with `resourceNames: [<name>]` and nothing else. They are rendered only while management is on. The ClusterRole adds `get/list/watch` on `nodes` and has no access to secrets.
- **TLS.** The server's certificate chain is checked against `cacert`, and the certificate must allow server authentication (or carry no extended key usage). The host name is not checked, as with `ovn-nbctl` and Kube-OVN's own client: Cozystack's kube-ovn chart issues one certificate for servers and clients alike, with Helm's `genSignedCert`, common name `ovn` and no subject alternative names.
- **Scope.** OVN has no per-row authorization, so the certificate allows any NB write. The limit is the controller's code: it writes only the rows of §3.5 and checks the `owner` and `network` external_ids before touching a group.

The new surface is therefore a compromised controller, which could rewrite the whole NB as Kube-OVN could. `ovn.manageGatewayChassis: false` removes the Role and every NB write. Groups already written stay in the NB and keep working, but nothing writes, repairs or deletes them any more, so the VMs of a new network have no gateway (§3.5).

The admission policies, all bound with `admission.validationActions`:

- `proxmox-network-machine-binding` (`admission.enabled`), on CREATE of `ProxmoxMachine` and `ProxmoxMachineTemplate` in every namespace:
  - a NIC on a zone bridge must use a (bridge, VLAN, pool) triple from the namespace annotation the controller maintains, so a namespace without Proxmox networks cannot reach the trunk through the raw bridge/vlan path. Draining networks stay listed;
  - a NIC on any other bridge may not be tagged with a zone's `uplinkVLAN`;
  - no NIC, tagged or untagged, may sit on a zone's `uplinkBridges`. Untagged, it is the trunk described above. Tagged, it makes Proxmox enslave `bond0.<tag>` into a new bridge, and that may be a device the host uses itself;
  - in a namespace that owns networks, every NIC must be one of them and `network.default` must be set.
- `proxmox-network-vm-attachments` (`admission.vmNetworkNamespace`): a KubeVirt VM or VMI attaches only NetworkAttachmentDefinitions and Kube-OVN subnets of its own namespace. The rule covers `spec.networks` and the annotations KubeVirt copies onto the virt-launcher pod. A multus network with `default: true` must be written `<ns>/<name>`: KubeVirt copies it verbatim into `v1.multus-cni.io/default-network`, and Multus looks an unqualified default network up in its own namespace (kube-system), while Kube-OVN uses the pod's.
- `proxmox-network-pod-attachments` (`admission.podNetworkNamespace`) applies the same rule to pods, including those other controllers create:
  - `k8s.v1.cni.cncf.io/networks` may name only NADs of the pod's namespace; an empty value is allowed;
  - `v1.multus-cni.io/default-network` must be exactly one `<ns>/<name>[@<ifname>]` entry;
  - `*.kubernetes.io/logical_switch` keys are refused on CREATE, because Kube-OVN takes the subnet name without a namespace check. The only exception is `ovn.kubernetes.io/logical_switch` set to one of the namespace's default switches. Pins to the namespace's own subnets are refused too (those are reached through NADs), and so are copies of running pods that keep kube-ovn's switches (`kubectl debug --copy-to`, restores).

On UPDATE the attachment policies judge only new or changed entries, and they skip objects that are being deleted. They exempt `cozy-*`, kube-system, the release namespace, each zone's `egress.gatewayNamespace` and `admission.exemptNamespaces`, which is the escape hatch.

Known limits:

- Zone bridges, uplink VLANs and uplink bridges come only from the package values. An unlisted bridge over the uplink's parent stays a trunk into the zone.
- Admission does not cover VMs created outside Cluster API.
- A zone `gatewayNamespace` that points at a tenant namespace exempts that namespace.
- Multus has no namespace isolation, so these policies are the only guard against cross-namespace NAD references.
- The transit router admits the whole LoadBalancer range on the control-plane ports for every tenant. Where one address pool serves everything, that range also holds the shared ingress addresses and every other tenant's control plane.

Expected matrix. The E2E asserts each cell except peering, which no step covers yet, with a positive control for each deny:

```
A -> B  L2 (ARP, unicast to MAC)   DENY
A -> B  L3 (via own gateway)       DENY   (proves the transit router, above)
A -> B  broadcast                  DENY
A -> B's egress gateway (transit)  DENY
A -> A  same subnet, VM <-> pod    ALLOW
A -> A  other subnet of the VPC    ALLOW  (overlay pods on trunk nodes, §3.3)
A -> B  after both declare peering ALLOW  (explicit, routes on both sides)
```

A VLAN is not the only boundary: if the trunk leaked a tag, the frame would still land in tenant A's logical switch, whose only router is VPC-A's.

## 9. Observability

The controller serves Prometheus metrics on `:8080/metrics`:

| Metric | Type | Labels |
|---|---|---|
| `cozy_proxmox_zone_vlans_total` | gauge | zone |
| `cozy_proxmox_zone_vlans_allocated` | gauge | zone |
| `cozy_proxmox_zone_vlans_free` | gauge | zone |
| `cozy_proxmox_zone_networks` | gauge | zone |
| `cozy_proxmox_network_ready` | gauge (0/1) | namespace, network, zone, vlan |
| `cozy_proxmox_network_ippool_addresses` | gauge | namespace, network, state=total/used/free |
| `cozy_proxmox_network_consumers` | gauge | namespace, network |
| `cozy_proxmox_network_deleting` | gauge (0/1) | namespace, network |
| `cozy_proxmox_vlan_allocations_total` | counter | zone, result=allocated/exhausted/conflict |
| `cozy_proxmox_network_reconcile_errors_total` | counter | reason |
| `cozy_proxmox_network_reconcile_duration_seconds` | histogram | — |
| `cozy_proxmox_orphaned_vlans` | gauge | zone |
| `cozy_proxmox_network_gateway_chassis` | gauge | namespace, network |
| `cozy_proxmox_network_gateway_ready` | gauge (0/1) | namespace, network |
| `cozy_proxmox_gateway_groups_collected_total` | counter | — |
| `cozy_proxmox_gateway_nb_errors_total` | counter | — |

Alerts (a PrometheusRule in the package, `alerts.enabled`): network not Ready for 15 minutes, pool usage above 90%, zone without free VLANs, orphaned VLANs, network stuck deleting for an hour, controller not scraped for 10 minutes. VMs per network is `cozy_proxmox_network_consumers`; IP allocation failures show as `free == 0` plus capmox claim conditions. capmox's own failure conditions remain the source for attachment failures.

The gateway (§3.5) adds:

- `cozy_proxmox_network_gateway_chassis` is the number of `HA_Chassis` in the network's own group after the last pass that reached the NB. It is 0 when there is no group yet, when the router port is missing, and on `ForeignGroup`. On `NoTrunkNodes` it counts the chassis the existing group keeps.
- `cozy_proxmox_network_gateway_ready` mirrors `GatewayReady`: 1 only while it is True. It is absent while gateway management is off, and both series go when the network does.
- `cozy_proxmox_gateway_nb_errors_total` counts gateway passes and releases that could not reach or update the NB.
- `ProxmoxNetworkGatewayNotReady` fires when `gateway_ready == 0` for 10 minutes on a network that is not being deleted (severity warning). It tells the operator the VMs have no gateway; the condition's reason says why.
- `ProxmoxNetworkGatewayNBErrors` fires when NB errors keep growing for 10 minutes (severity warning), which covers the grace during which a network stays Ready (§7).
- The group itself shows in `ovn-nbctl --no-leader-only --columns=name,external_ids,ha_chassis list ha_chassis_group`. The proof that it works is the absence of the 105 drop: `ovn-sbctl --no-leader-only lflow-list <subnet> | grep 'ls_in_arp_rsp.*priority=105'` must print nothing.

## 10. Operations

Install (infrastructure admin). The package has two components, the CRDs (`proxmox-network-crds`, kept on uninstall) and then the controller package:

1. On every PVE node: a VLAN-aware bridge on its own outer VLAN, for example `bond0.70 -> vmbr70` with `bridge-vlan-aware yes` and `bridge-vids 100-199`. Disable IPv6 on the zone bridge, for example with a `post-up` of `sysctl -w net.ipv6.conf.vmbr70.disable_ipv6=1`, so the host has no link-local address on the transport. Add the outer VLAN to the switch ports of the backup path. Do not use `ifreload -a` on a host whose interfaces file has stale entries; `ifup bond0.70 vmbr70` brings up only the new ones. On any other bridge that enslaves the uplink's parent device (vmbr0 over bond0), drop guest-originated 802.1Q/802.1ad frames, for example with ebtables on the guest ports, or make the bridge VLAN-aware with `bridge-vids` that exclude the uplink and host VLANs. Admission covers only Cluster API machines (§8). The uplink must carry the inner tag between the hosts; see the hypervisor requirements below.
2. On each node that should carry tenant networks: one extra virtio NIC, `bridge=vmbr70,trunks=100-199`, with no address. On Talos, mark it `ignore: true` in the machine config (`machine.network.interfaces: [{interface: <trunk NIC>, ignore: true}]`, where the trunk NIC is the package's `providerNetwork.defaultInterface`, `ens19` by default, or the node's `customInterfaces` entry); `dhcp: false` is not enough. Otherwise Talos's default LinkSpec tries to take the NIC out of ovs-system about once a minute, fails with `error enslaving/unslaving link ... operation not supported` in the machined log, and leaves the link DOWN, while Kube-OVN still labels the node ready. The symptoms are the NIC DOWN in `ip link` and a growing `ovs-vsctl get interface <nic> link_resets`. After patching a running node, restart its kube-ovn-cni pod: Talos drops its LinkSpec but does not bring the link up; kube-ovn-cni does that when it starts.
3. Enable the `proxmox-network` package and set the zone(s), the provider network interface and node selection, and the transit subnet. `excludeNodes` holds Node names: a name that matches no Node excludes nothing, and the node it meant keeps the provider network from becoming ready. For every zone also set `uplinkVLAN` and `uplinkBridges`, listing every bridge that enslaves the uplink's parent device. In the example topology that is `uplinkVLAN: 70`, `uplinkBridges: [vmbr0]`. Both are admission-only values, not zone fields, and leaving one unset leaves that path open.

   Gateway management (§3.5) is on by default.

   - `ovn.manageGatewayChassis` (default `true`) turns it on.
   - `ovn.nbAddress` (default `ssl:ovn-nb.cozy-kubeovn.svc:6641`) is a comma-separated list of `ssl:`/`tcp:` addresses, tried in order. Writes must reach the RAFT leader, which Kube-OVN's `ovn-nb` Service selects (`ovn-nb-leader=true`).
   - `ovn.tlsSecret.{namespace,name}` defaults to `cozy-kubeovn/kube-ovn-tls`.

   The controller flags are `--manage-gateway-chassis`, `--ovn-nb-address` and `--ovn-tls-secret=<namespace>/<name>`. Do not write a cluster domain into the address: a cluster's domain need not be `cluster.local`, and the Service name is resolved through the search domains.

   An NB connection is reused only while it was used in the last 4 seconds and is less than a minute old, so in practice only within one burst of reconciles. A leader change behind the Service is therefore followed within a minute. Nor is a request ever sent on an idle connection that the server's inactivity probe is about to cut, where it could fail half-way with no way to tell whether it was applied.
4. The transit router, on the transit VLAN with the transit gateway address:
   - it forwards from the transit network, with SNAT, only to the control-plane LoadBalancer range, DNS (UDP 53), NTP (UDP 123) and the Internet;
   - the LoadBalancer range must equal the address pool the Kamaji LoadBalancers come from, and that pool needs a free address for each Proxmox-backed cluster;
   - it drops every other private destination and has no route into any VPC CIDR;
   - its rules must decide transit traffic in both directions ahead of any other FORWARD rule. A hook another service inserts at FORWARD 1, such as a WireGuard guard, cannot bypass the egress side, but it can accept new traffic into the transit network;
   - the rules must be persisted, or checked after every netfilter reload: a reload removes them, and under FORWARD policy ACCEPT the router then fails open.
5. Check `kubectl get proxmoxnetworkzones` is Ready.

Hypervisor requirements:

- The uplink must carry both tags of a double-tagged frame from host to host. Tenant VLANs and the transit VLAN ride as inner 802.1Q tags inside the outer VLAN on the bond. A frame that loses its inner tag lands in the far bridge's VLAN 1, which reaches the trunk NICs untagged. No localnet port matches it, so it never reaches a logical switch: everything that crosses hosts breaks, and the tenant VLANs of the other host are no longer apart on that bridge.
- Some NICs lose the inner tag on send. This was seen in both directions with Mellanox ConnectX-3 ports (`mlx4_en`) in an active-backup `bond0`, with `rx-vlan-offload`, `tx-vlan-offload` and `rx-vlan-stag-hw-parse` on. The sender's `bond0` shows `vlan 70, vlan 100`, the receiver's `bond0` or active slave shows `vlan 70, ethertype ARP`, and the receiving bridge learns the sender's MAC in VLAN 1. The kernel still holds both tags at `bond0`; the NIC's hardware insertion of the outer tag on send overwrites the inner one. Other NICs may behave the same way, so test every host pair.
- To test a host pair, send traffic on an inner VLAN from one host to the other, for example a ping from the transit router to an egress gateway. On the receiving host, `tcpdump -nei <active slave> vlan` must show both tags, and `bridge fdb show br <zone bridge>` must list the sender's MAC under the inner VLAN, not `vlan 1`. Test both directions. Offload settings are per slave, so the backup slaves must match the active ones, or a bond failover brings the loss back. The E2E preflight checks the trunk link only and cannot see this; a VM-to-VM ping across hosts on one tenant VLAN is the end-to-end check.
- The fix: `ethtool -K <slave> txvlan off` on every slave of the uplink bond, backup slaves included, so the kernel inserts the outer tag. `mlx4_en` changes this without a port reset. Turning it off on one side was enough for that direction, which places the fault in hardware insertion, not in receive stripping (`rx-vlan-offload` stays on). Persist it, for example as a `post-up` in the stanza of the bond or of its VLAN device, and check it after host changes (§12, question 11).
- MTU: the uplink must carry the zone `mtu` plus both tags. In the example topology `bond0`, `bond0.70`, `vmbr70` and the trunk NICs run 9000, and the zone, the transit network and the router's `vmbr70.100` 8996 (§3.4).

Day 2:

- `kubectl get proxmoxnetworks -A` lists every Proxmox-backed subnet with its VLAN, bridge and pool usage.
- A network stuck in `InUse` is waiting for machines: `kubectl -n <ns> get ipaddresses` shows who holds addresses. One in `SwitchDeleting` is waiting for Kube-OVN and is released by the time in its Ready message at the latest. Its per-subnet timers are on the Vlan: `kubectl get vlans.kubeovn.io <subnet> -o jsonpath='{.metadata.annotations.proxmox\.cozystack\.io/deleted-subnets}'`.
- Extending a zone range is safe. Shrinking it never takes a VLAN away from a network that holds one; the zone's Ready message lists VLANs stranded outside the new ranges until those networks are deleted.
- Never delete a Kube-OVN `Vlan` with zone labels by hand; delete the VPC subnet instead.
- `kubectl get proxmoxnetworks -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,GW:'.status.conditions[?(@.type=="GatewayReady")].reason'` shows each network's gateway state. To read the groups, run this in the ovn-central pod: `ovn-nbctl --no-leader-only --columns=name,external_ids,ha_chassis list ha_chassis_group`.
- A `ForeignGroup` network needs a decision by hand; the condition's message says which case it is. For a `px-` group with another owner, delete that group or set its `external_ids` to `owner=proxmox-network,network=<ns>/<name>`. For a router port that points at another group or has `gateway_chassis`, find out who set it. Only if nothing manages it, clear that column on the NB leader (`ovn-nbctl clear logical_router_port <vpc>-<subnet> <column>`); the next pass, within 5 minutes, writes the network's own group.
- Groups without an owner whose router port is gone are not collected. They are groups made by hand for networks that were deleted before the controller managed gateways. List the groups with `ovn-nbctl --no-leader-only --columns=name,external_ids list ha_chassis_group`, take the `px-` ones without `owner=proxmox-network`, check that the port is gone (`ovn-nbctl --no-leader-only get logical_router_port <port> name` fails), then run `ovn-nbctl ha-chassis-group-del <group>` on the NB leader.
- Manual rollback for one network, with management off: on the NB leader, run `ovn-nbctl clear logical_router_port <vpc>-<subnet> ha_chassis_group`, then `ovn-nbctl ha-chassis-group-del px-<vpc>-<subnet>`. The VMs on that network then lose their gateway.
- Before upgrading Kube-OVN to a version that manages `ha_chassis_group` on subnet router ports:
  1. set `ovn.manageGatewayChassis: false`;
  2. upgrade;
  3. check that the router ports point at Kube-OVN's groups;
  4. delete the `px-*` groups.

  Otherwise each network whose router port Kube-OVN takes over turns `ForeignGroup` and not Ready, and the kubernetes charts place no new machines on it (§7).

Proxmox-backed clusters:

- The cloud-controller-manager must see the VMs. When its token is scoped to a PVE resource pool (`/pool/<pool>`), the VMs must go into that pool (`proxmox.pool` of the KubernetesNodes app). A VM outside it is `instance not found` to the CCM: its Node gets no `providerID`, and the Machine stays `Provisioned`.
- The kubernetes chart runs CCM image v0.16.1. v0.15.0 panics at start on its own version string (`0.15.0`, where Kubernetes' component-base wants `v<semver>`).

## 11. Testing

- Unit: allocator (ranges, reserved, static, exhaustion, idempotency, conflict), reconcile (create, adopt, ready, delete with and without consumers, the wait for Kube-OVN to prune the Vlan and its per-subnet timeouts, timers dropped on adoption or restarted from before the deletion, orphan), chart renders (helm-unittest) for `vpc`, `kubernetes`, `kubernetes-nodes` and `proxmox-network` (admission policies included). Beyond that:
  - The gateway, against an in-memory OVN NB with atomic transactions, named-uuid resolution, reference checks and `HA_Chassis` garbage collection. It covers:
    - creation, and adoption of an unowned group in place;
    - foreign groups and foreign router ports left alone, and a write aborted when the router port changes under it;
    - priority order, and trunk-node changes;
    - drift repair: a cleared pointer, and a recreated port;
    - `RouterPortMissing`, `NoTrunkNodes`, `NBUnavailable` and `WaitingForVLAN`;
    - the 10-minute NB grace, also when management has just been turned on;
    - release before the VLAN, with the NB down and then back;
    - the periodic collection;
    - management off.
  - The OVSDB client's wire format, JSON-RPC handling and TLS, the last against certificates issued as Helm's `genSignedCert` issues Kube-OVN's (common name `ovn`, no SAN).
- E2E, as two Chainsaw suites (see `hack/e2e-chainsaw/README.md` for how suites run):
  - `hack/e2e-chainsaw/proxmox-network/` runs in CI on the regular e2e cluster, which has no Proxmox. It covers what needs no hypervisor: the package and its CRDs, a zone, VLAN allocation and release, the objects the vpc chart renders for a Proxmox-backed subnet, the `ProxmoxNetwork` status, and admission.
  - `hack/e2e-chainsaw/proxmox-network-env/` needs a Cozystack cluster on Proxmox VE with the hypervisor side of §10 in place: the zone bridge, the trunk NICs and the transit router. CI cannot provide that, as with `hack/e2e-chainsaw/kubernetes-proxmox`, so it runs against such an environment by hand or on a self-hosted runner.

  The environment suite builds two tenants, each with two Proxmox-backed subnets and one overlay subnet, and a Proxmox-backed CAPI cluster per tenant, with a two-NIC pool in tenant A. It checks the isolation matrix of §8 with a positive control for every denied cell, a controller restart and a capmox outage without duplicate allocations or recreated machines, and a deletion that leaves no Vlan, pool or address behind. Its steps:
  - preflight: every `excludeNodes` name exists (an empty list is valid), the provider network is ready on every node it does not exclude, and on each ready node the trunk NIC is admin UP, its OVS `link_state` is up and `link_resets` stays steady;
  - control: each egress gateway's port has no port security in the OVN NB. For each Proxmox network:
    - `GatewayReady` is True;
    - its router port `<vpc>-<subnet>` points at the group `px-<vpc>-<subnet>`, with `external_ids` `owner=proxmox-network` and `network=<ns>/<name>`;
    - the group's chassis are exactly the chassis of the trunk nodes;
    - the switch's `ls_in_arp_rsp` has no priority-105 drop, read from flows that include the switch's localnet port.
  - datapath: pods on a Proxmox subnet reach each other, the router port and the overlay subnet of their VPC, an overlay pod on a node without the trunk, and TCP, DNS (UDP 53) and NTP (UDP 123) destinations through the egress gateway;
  - capi: the control planes reach their datastore before anything waits for a join; then the workers join, and from the VMs: the own gateway, pods on the same subnet and on the overlay subnet, the second NIC, each control-plane LoadBalancer IP, DNS, NTP and the Internet, and the MTU checks of §3.4;
  - resilience: a kube-ovn-cni restart on a trunk node, after which the NIC must be up and steady and carry every VLAN;
  - lifecycle: east-west traffic inside B's VLAN survives the VPC uninstall, and B's egress gateway is gone. A gateway stuck on its finalizer (§7) fails that check.

  The MTU checks send DF pings of the network's MTU. A check towards the Internet, a LoadBalancer or the transit router passes when the packet arrives or the VM learns the smaller path MTU, as §3.4 accepts. A packet of the smaller of the VM's and the overlay pod's MTU must reach the overlay pod.

  Checks whose outcome is an open question of §12 or a known limit are reported without failing the suite: the OVN boundary after each A-to-B L3 deny (question 9), which passes only when A's egress gateway has no conntrack entry for A's packets to B after its positive control left one; traffic to an overlay pod on a node without the trunk (question 7); the transit ACL across chassis while both gateways share a node (question 4); and a full-size packet to an overlay pod (§3.4). No check passes on a failed read: every "nothing changed" or "nothing left" check needs a successful, non-empty baseline.

## 12. Open questions

These were expected to hold from the Kube-OVN and OVN code paths, but only a live datapath answers them, so the E2E was ordered to answer them first. The suite of §11, in the shell form it had before the move to Chainsaw, has run end to end on a test environment of two Proxmox VE 9 hosts with two tenants, with no failures:

- preflight, VPC, control and datapath: 87 of 89 checks passed outright, and the other two are the open checks of questions 7 and 9;
- simulated VMs (network namespaces on a hypervisor, plugged into the zone bridge as access ports of a tenant VLAN, with addresses claimed from the subnet's pool) reached their gateway and everything routed behind it;
- the Proxmox-backed clusters of both tenants: the workers of both joined through the egress gateway, and 34 of 40 checks passed outright. The other six are the open checks of questions 4, 7 and 9 and of the full-size packet to an overlay pod (§3.4), an Internet MTU check skipped because the uplink is smaller, and a LoadBalancer MTU check skipped because the LoadBalancer IP does not answer ping;
- resilience, lifecycle and teardown: 30 of 31 checks passed outright. The one left is the open check of a router deleted with the Vpc while held subnets stay (§7). Nothing was left behind: no VLAN, pool, address, machine, HA chassis group or egress gateway.

The Chainsaw suites have not yet run against such an environment.

1. Answered, with a change to the design. A VM outside OVN got no answer at all for its gateway.

   - **Cause.** Kube-OVN's OVN carries the patch 477695a0, which drops ARP and ND from a localnet port for the addresses of any router port without gateway chassis (§3.5).
   - **Before.** Simulated VMs reached pods on their subnet, but not their gateway and nothing routed.
   - **Fix.** Each router port got an HA chassis group of the two trunk workers, made by hand, which made it a distributed gateway port.
   - **After.** The same checks passed:
     - every echo to the gateway got exactly one reply;
     - an echo request reached only the active worker's trunk NIC;
     - the hypervisor bridge learned the router MAC on that NIC alone;
     - the overlay subnet, the egress gateway and the Internet were reached through it.

     Kube-OVN left the groups alone.

   The controller now keeps these groups (§3.5), and the runs above used the groups it wrote. Three things remain open:

   - **Failover.** The failover of the active chassis has not been measured: no step stops a trunk worker.
   - **Stopgap.** The NB write lasts until Kube-OVN sets the group itself for `logicalGateway` underlay subnets reached through a localnet port. That needs a change in Kube-OVN, which has not been proposed there yet.
   - **Upgrade conflict.** A Kube-OVN that starts writing `ha_chassis_group` on subnet router ports would leave the networks it takes `ForeignGroup` and not Ready (§7), so that upgrade needs management turned off first (§10).
2. Answered: not without a machine-config change. On Talos the default LinkSpec keeps a hot-plugged trunk NIC DOWN while Kube-OVN still reports the node ready. With `ignore: true` and a kube-ovn-cni restart the link comes up and stays steady (§10, step 2). Still to observe: the link after a node reboot with the patch in place, which kube-ovn-cni is expected to bring up when it starts.
3. Answered. A pod on a Proxmox subnet reaches the overlay subnet that admits it through `allowSubnets`, and reaches TCP, DNS and NTP destinations through the egress gateway. VMs reach their control plane, DNS, NTP and the Internet the same way. Both hold for pods on trunk nodes; for overlay pods on other nodes see question 7.
4. Verified in the OVN pipeline. ovn-trace on a live cluster shows gateway A to the router allowed, gateway A to gateway B dropped, and the router to gateway B allowed. The rules are from-lport and run on the sender's chassis. The live cross-chassis check is skipped while both gateways run on one node, as they did in the runs above.
5. Answered: Kube-OVN accepts it. The gateways run with eth0 on the first Proxmox-backed subnet, become Ready, run without port security and carry the egress of question 3. `egress.internalCidr` stays as an option, with the MTU caveat of §3.4.
6. Answered: the networks become Ready, the localnet port carries the VLAN tag, and pods on the subnet reach each other and the router port.
7. Open: traffic from a Proxmox subnet or a VM to an overlay pod on a node without the trunk. It was predicted to fail, because the reply is routed into the VLAN switch on a chassis with no bridge mapping for the provider network. In the runs above it passed, both from a pod and from a VM, but nothing explains yet why, so the E2E still reports it as an open check next to a Geneve control between overlay pods. If it does fail somewhere, there are three ways out: carry the trunk to every node that runs tenant pods, keep a Proxmox-backed VPC's overlay pods (and KubeVirt VMs) on trunk nodes, or record it as a limit.
8. Decided: the zone `mtu` drives the VM NICs and, through the vpc chart, `spec.mtu` of the Proxmox-backed subnets; `transit.mtu` drives the transit subnet. The test environment runs 8996 on a 9000-byte QinQ uplink (§3.4), and a full-size DF packet from a VM reaches the transit router through the gateway.
9. Open, needs a decision: an OVN-side boundary between VPCs. It needs a platform-owned tenant address space, which the vpc chart would render as a 30400 drop (§8). Until then only the transit router stops A -> B at L3. The E2E reports the OVN boundary as an open check after each A-to-B L3 deny (§11).
10. Open: peering with Proxmox-backed subnets. The routes, the 30500 allows and the /30 link are verified from the Kube-OVN source and the chart tests only. No E2E run has a peered VPC yet.
11. Resolved for the NIC it was seen on: the inner 802.1Q tag on the uplink. ConnectX-3 (`mlx4_en`) hardware VLAN insertion overwrote the inner tag of a double-tagged frame, so the far host saw it in VLAN 1. With `txvlan off` on every bond slave the kernel inserts the outer tag and the inner one survives, verified both ways with `tcpdump -e` and the bridge fdb (§10, hypervisor requirements). On `mlx4_en`, switching it off does not reset the port. Other NICs have not been checked.
12. Open: management access. Platform administrators must reach every tenant VM (SSH, the Talos API, the tenant cluster's kube-apiserver) from the management cluster, while a tenant VM reaches nothing of the infrastructure and no other tenant: only its own control plane's kube-apiserver (and konnectivity). Today the transit router also lets VMs out to the Internet (image pulls, DNS, NTP) and lets them reach every address in the Kamaji LoadBalancer range. Meeting the requirement needs (a) a management path into the VPCs, for example a route from the management cluster through each VPC's router, or konnectivity and `talosctl` through the control plane, and (b) per-tenant egress narrowed to that tenant's own LoadBalancer IP, with images, DNS and NTP served from inside the platform (a registry mirror and resolvers the transit network reaches) instead of the Internet.

Proxmox SDN (§13). These come from the code and docs read for §13; each open one needs a test on a Proxmox cluster or a decision before the phase that depends on it (§13.8).

13. Open: are tap devices re-plugged when an SDN apply removes the bridge they sit on and creates it again? It decides the A1 migration and its rename shortcut (§13.3).
14. Open: does PVE disable IPv6 on the bridges it generates? If not, the fix is either a merged stanza in `/etc/network/interfaces` whose `post-up` sets `disable_ipv6=1` (whether ifupdown2 merges it is unverified) or a sysctl or udev rule. Neither A1 nor B starts before this is closed (§13.3): without SDN, the zone bridge's `post-up` sysctl keeps the hosts off the transport (§10, step 1), while `net.ipv6.conf.default.disable_ipv6` is 0 on a default install. A1 deletes that stanza, and the generated `pxt70` and `vmbr0v70` cannot carry a `post-up`.
15. Open, expected yes: the Kube-OVN trunk through a VLAN-aware VNet in a VLAN zone on a bridge that is not VLAN-aware (A1), and through VLAN-in-VXLAN (C). Not tested.
16. Open: the MTU of the implicit `bond0.70` (A1), and of `sv_<zone>` and the zone veths `ln_<zone>`/`pr_<zone>` (A2), on which SDN sets none, and whether the hand-written `bond0.70` stanza merges cleanly with the generated one. For A2 the veths alone cap the trunk path at the veth default of 1500 (§13.3).
17. Answered from code: no. In CAPMOX v0.7.7, `reconcileVirtualMachineConfig` (`internal/service/vmservice/vm.go`) returns early once the VM is running or the ProxmoxMachine is Ready, and the NIC drift check `shouldUpdateNetworkDevices` runs only inside it, so a provisioned VM is never re-pointed. A zone that moves onto SDN needs a worker rollout.
18. Answered for B-dynamic: it needs PVE 9.2 for dry-run [RM]. The lock and rollback endpoints came in `libpve-network-perl` 1.1.3 (2025-07-29) [SRC changelog], before the PVE 9.0 release, whose notes list the rollback endpoint [RM 9.0]; the 9.1 notes fix their return type.
19. Answered from the docs: the nftables proxmox-firewall, which the VNet firewall needs, is still an opt-in tech preview [D-FW], and the roadmap lists its graduation as future work [RM]. The VNet firewall stays out until that changes (§13.7).
20. Open, needs a decision: who writes the per-VLAN or per-VNet ACLs for per-tenant capmox tokens, the admin or a narrow writer with `Permissions.Modify` on `/sdn/zones/<zone>/…`?
21. Answered from code: `changes` on `GET /nodes/{n}/network` is one string beside `data`, the output of `diff -b -N -u /etc/network/interfaces /etc/network/interfaces.new`, and is set only when that diff is not empty [MGR `API2/Network.pm:414-418`; pve-common `INotify.pm:66-83, 285-287`]. The preflight and the B-dynamic applier check that it is absent or empty.
22. Open: the bpg provider seems to have no resources for the VNet firewall or the SDN lock token. That comes from its docs and CHANGELOG only (unverified).

## 13. Proxmox SDN integration

**Status:** proposed, design only. Nothing in this repository creates or reads Proxmox SDN objects, and the test environment of §12 has no SDN zone, VNet, subnet or pending change. This section turns the SDN non-goal of §2 into a phased plan; the claim §2 used to make, that a zone can point at a PVE SDN VLAN zone without an API change, holds for the transport mode only. The SDN questions are §12, questions 13-22.

Sources, cited in brackets:

| Key | Source |
|---|---|
| SRC | `libpve-network-perl` 1.6.7 (pve-manager 9.2.20) |
| MGR | pve-manager `PVE/API2/Network.pm` and `helpers/pve-sdn-commit` |
| QS | qemu-server `PVE/QemuServer.pm` and `PVE/GuestHelpers.pm` |
| IFU | ifupdown2 3.3.0-1+pmx12 |
| D-SDN, D-FW | the [SDN](https://pve.proxmox.com/pve-docs/chapter-pvesdn.html) and [firewall](https://pve.proxmox.com/pve-docs/chapter-pve-firewall.html) chapters of the PVE docs |
| RM | the [PVE roadmap](https://pve.proxmox.com/wiki/Roadmap) and its release notes |
| TF | bpg/proxmox Terraform provider docs and CHANGELOG |
| CAPMOX | CAPMOX v0.7.7 source |
| HOST | read-only checks on the two-host test environment of §12 |

"(unverified)" marks a fact that neither the code nor a live run confirms.

### 13.1 Role

SDN may do one thing: carry tenant VLAN frames, unchanged, between Proxmox nodes and into the trunk NICs of the cluster nodes. That is the job the hand-written vmbr70 does today. SDN never:

- **allocates, records or serves an address.** No SDN subnets, zone `ipam` unset, no DHCP. `cluster-api-ipam-provider-in-cluster` stays the only IP authority for VMs, and Kube-OVN for pods;
- **holds an address or routes on a tenant network.** No subnet gateway, SNAT, anycast gateway, VRF or exit node. The VPC router stays the only router between subnets, and the transit router the only way out;
- **chooses a VLAN.** The controller allocates. A VNet is at most a name for an allocated VLAN: VNet `tag` = `ProxmoxNetwork.status.vlan`;
- **owns VM lifecycle.** capmox still writes `bridge`, `vlan`, `mtu` and `ipv4PoolRef` on the NIC. capmox v0.7.7 has no SDN fields and `bridge` is a free string, so a VNet is attached as `bridge: <vnet>` [CAPMOX `proxmoxmachine_types.go`, `internal/service/vmservice/utils.go`];
- **changes the tenant API.** The VPC subnet stays the source of truth.

The chain in §1 gains one optional link: `ProxmoxNetworkZone -> PVE SDN zone (+ VNets) -> Linux bridges on every node`.

### 13.2 How SDN behaves on a host

Naming and topology:

- Every VNet is a Linux bridge named exactly like the VNet ID: 2 to 8 characters matching `[a-zA-Z][a-zA-Z0-9]*[a-zA-Z0-9]` [SRC `VnetPlugin.pm:19-28`; D-SDN]. `subnet-3f2a9c1e` can never be a VNet ID.
- VLAN zone on a VLAN-aware bridge: the VNet's port is `<bridge>.<tag>`, for example `vmbr70.101`. ifupdown2 adds that vid to the bridge itself, as it does for a hand-written VLAN device such as the transit router's `vmbr70.100` [HOST].
- VLAN zone on a bridge that is not VLAN-aware: SDN writes, in order, a veth pair `ln_<vnet>`/`pr_<vnet>`, a bridge `<bridge>v<tag>` whose `bridge_ports` are `<port>.<tag>` for each physical bridge port (bonds and VLAN devices count) plus `pr_<vnet>`, and the VNet bridge on `ln_<vnet>` [SRC `Zones/VlanPlugin.pm:113-153`; pve-common `IPRoute2.pm:77-91`]. It writes no stanza for `<port>.<tag>`; ifupdown2 creates that VLAN device implicitly (§12, question 16).
- The zone `mtu` goes on the VNet bridge, the veths and `<bridge>v<tag>`, never on a VLAN device.
- A `vlanaware` VNet gets `bridge-vids 2-4094` from the zone plugin [SRC `Zones/VlanPlugin.pm:147-150`, `Zones/QinQPlugin.pm:176-179`, `Zones/VxlanPlugin.pm:144-147`] and cannot have subnets [SRC `VnetPlugin.pm:114-118`]. A NIC with `tag=` or `trunks=` on a VNet that is not VLAN-aware is refused with "vm vlans are not allowed on vnet" [SRC `Zones/Plugin.pm:242-247`]. A NIC attaches to exactly one bridge, so one NIC cannot trunk several VNets.

What an apply does. `PUT /cluster/sdn` needs `SDN.Allocate` on `/sdn`. It commits the running config, then on **every** node of the cluster, whatever the zone's `nodes` [SRC `API2/Network/SDN.pm:285-356`; MGR `API2/Network.pm:882-950`]:

1. moves a staged `/etc/network/interfaces.new` over `/etc/network/interfaces`;
2. rewrites `/etc/network/interfaces.d/sdn`;
3. runs `ifreload -a`.

Consequences:

- `ifreload -a` runs `pre-up`, `up` and `post-up` for every auto interface, changed or not [IFU `ifupdownmain.py:2202-2205, 2433-2440`, `main.py:283`], and removes bridge ports that do not match `bridge-ports-condone-regex`, by default `^(tap|veth|fwpr)` [IFU `addons/bridge.py:443-448`].
- The parent task does not track the per-node tasks (FIXME at `API2/Network/SDN.pm:347`): a failed node produces only a warning. There is no automatic rollback; the running config is already committed.
- The apply starts each node's reload through `pvesh` as root, so `SDN.Allocate` on `/sdn` reloads every node's network, although the direct per-node reload needs `Sys.Modify` on `/nodes/<node>` (from the code).

Pending changes, lock and dry-run:

- `pve-sdn-commit.service`, enabled on a PVE 9 install [HOST], runs once at boot. If anything is pending it commits it cluster-wide and runs `ifreload -a` on the booting node [MGR `helpers/pve-sdn-commit`; RM 9.0]. It does the same, pending or not, whenever a VLAN or QinQ zone sits on a bridge that is not VLAN-aware. A staged change that was never applied is therefore not a safe resting state.
- `POST /cluster/sdn/lock` fails while anything is pending unless `allow-pending=1`. While the lock is held, every SDN write needs its token. An apply that carries the token releases the lock on commit (`release-lock` defaults to 1), and `POST /cluster/sdn/rollback` discards pending edits [SRC `API2/Network/SDN.pm:134-330`].
- `GET /cluster/sdn/dry-run?node=<n>` returns the diffs for `interfaces.d/sdn` and FRR and needs only `Sys.Audit` [SRC `:378-445`; RM 9.2].
- `GET /nodes/<n>/network` returns a `changes` attribute while `interfaces.new` is staged [MGR `API2/Network.pm:414-418`]: one string, the unified diff from `interfaces` to `interfaces.new` (§12, question 21).
- `pve-sdn-commit` calls `commit_config()` without checking the lock token [MGR `helpers/pve-sdn-commit:99-120`; SRC `Network/SDN.pm:240-244`], so the lock does not stop the boot-time commit.

Permissions and node scope:

- A guest NIC needs `SDN.Use` on `/sdn/zones/<zone>/<vnet-or-bridge>` when it is untagged, on `.../<tag>` when it is tagged, and on `.../<tag>` for each VLAN of `trunks=`. A plain bridge sits in the pseudo-zone `localnetwork`; only `root@pam` skips the check [QS `GuestHelpers.pm:397-421`, `QemuServer.pm:6417-6430`]. Per-VLAN ACLs on `/sdn/zones/localnetwork/vmbr70/<vlan>` therefore work today, without SDN.
- A zone with `nodes` set generates nothing on other nodes, and a NIC on its VNet fails there with "vnet X is not allowed on this node" [SRC `Zones.pm:141, 342-343`].
- Deleting a VNet checks only for SDN subnets, not for guests [SRC `VnetPlugin.pm:99-105`].
- No check stops a VNet from taking the name of an existing local bridge (none found in `VnetPlugin.pm` or `API2/Network/SDN/Vnets.pm`).

Host preconditions for any apply. Each is a host change, to be made and checked before the first apply:

- Every node's `/etc/network/interfaces` has a `source /etc/network/interfaces.d/*` line, which D-SDN requires. Without it SDN only warns (`Network/SDN.pm:405`), and VNets never come up on that node.
- Corosync has a redundant link that does not depend on a bridge the apply reloads. Every apply reloads every bridge, `vmbr0` included, so a cluster whose only live corosync link or only management path runs over `vmbr0` risks quorum and access on each apply.
- No stale stanzas: a stanza for an interface that no longer exists, such as a removed NIC, makes every `ifreload -a` fail.
- Every `post-up` hook is idempotent, because `ifreload -a` re-runs them all:
  - firewall rules check before they add: `iptables -C … || iptables -A …` for appended rules and `iptables -C … || iptables -I …` for inserts, with no flush of a chain the transit router depends on. A transit hook that appends or flushes duplicates its rules on a re-run and can briefly let the transit router fail open (§10, step 4);
  - addresses use `ip addr replace`, not `ip addr add`, which fails with EEXIST on every reload. ifupdown2 logs a failed `post-up` as a warning only [IFU `addons/usercmds.py:56-58`].
- Nothing is plugged into a bridge by hand under a name outside `bridge-ports-condone-regex` (default `^(tap|veth|fwpr)`): the apply removes such ports. Test helpers that plug veths into the zone bridge should name them `veth*`.
- A VLAN device on the zone bridge, such as the transit router's `vmbr70.100`, rules out a VNet for that VLAN.

### 13.3 Integration models

| | A: transport only | B: one VNet per network | C: VXLAN transport |
|---|---|---|---|
| PVE objects | 1 VLAN zone, 1 VLAN-aware VNet (A1) | 1 VLAN zone on vmbr70, 1 VNet per ProxmoxNetwork | 1 VXLAN zone, 1 VLAN-aware VNet |
| Created by | admin (Terraform or `pvesh`) | B-pool: admin. B-dynamic: controller | admin |
| SDN applies | install, node add | B-pool: install, range change. B-dynamic: every network create or delete (batched) | install, node add |
| VM NIC | `bridge=<vnet>,tag=<vlan>` | `bridge=<prefix><vlan>`, no tag | `bridge=<vnet>,tag=<vlan>` |
| Trunk NIC | `bridge=<vnet>,trunks=<range>` | `bridge=vmbr70,trunks=<range>` (`localnetwork`) | `bridge=<vnet>,trunks=<range>` |
| Cozystack change | values only; the optional read-only checks need the §13.5 API | API, controller, charts, admission | values, plus a new zone and ProviderNetwork |
| Gain | transport declared in PVE; a new node is a Terraform change | a PVE object per network, per-VNet ACL, VNet firewall, PVE UI | L2 over a routed underlay or across sites |

#### Model A: SDN declares the transport only

**A0: a VLAN zone over the hand-written vmbr70, with no VNets.** It writes only `#version:N` to the sdn file and changes nothing for VMs: NICs on vmbr70 stay in `localnetwork`, because PVE resolves a zone only for VNet names. It still costs one cluster-wide `ifreload -a` to apply, and if left unapplied the next reboot of any node applies it. Its only use is as the base for B; do not create it unless B follows.

**A1, the recommended form of A: a VLAN zone on vmbr0 plus one VLAN-aware transport VNet.**

PVE objects:

- zone `cozy`: type `vlan`, `bridge=vmbr0`, `mtu=9000`, `nodes=` every PVE node that VMs or trunk NICs may run on; no `ipam`, no `dns`;
- VNet `pxt70`: `tag=70`, `vlanaware=1`.

On each node SDN generates `bond0.70`, `vmbr0v70` (ports `bond0.70` and `pr_pxt70`), the veth pair `ln_pxt70`/`pr_pxt70`, and the VNet bridge `pxt70`, VLAN-aware with vids 2-4094 [SRC `Zones/VlanPlugin.pm:108-153`]. That is today's topology, 802.1Q inner tags inside outer VLAN 70 on bond0, under names SDN owns.

The platform admin creates them once with Terraform or `pvesh`; the bpg resources are `proxmox_virtual_environment_sdn_zone_vlan` (0.81.0+), `proxmox_virtual_environment_sdn_vnet` (0.84.0+) and `proxmox_virtual_environment_sdn_applier` (0.83.0+), with the short aliases `proxmox_sdn_*` from 0.100.0 [TF]. The proxmox-network package never writes to PVE.

Mapping to Cozystack:

- `ProxmoxNetworkZone.spec.bridge: pxt70`. `vlanRanges`, `providerNetwork`, `mtu: 8996` and the admission value `uplinkVLAN: 70` are unchanged.
- Admission value `uplinkBridges: [vmbr0, vmbr0v70]`. `vmbr0v70` is new: it is not VLAN-aware and sits on outer VLAN 70, so an untagged NIC on it is a full trunk into the zone.
- ProxmoxNetwork is unchanged: `status.bridge` is `pxt70`, and `status.vlan` is both the NIC tag and the Kube-OVN tag.
- An optional `spec.sdn {zone: cozy, mode: Transport}`, a new API field (§13.5), turns on the read-only checks there.

NICs:

- CAPI NIC: `bridge=pxt70,tag=<vlan>,mtu=8996`, legal because the VNet is VLAN-aware.
- Trunk NIC: `bridge=pxt70,trunks=100-199,mtu=9000,firewall=0`. The guest sees the same NIC, so the Kube-OVN ProviderNetwork is unchanged.

MTU:

- Zone `mtu=9000` is required. Without it the veths default to 1500, and a bridge takes the smallest MTU of its ports, which caps `vmbr0v70` and `pxt70` at 1500 (inferred from the bridge MTU rule).
- `bond0.70` takes 9000 from bond0 by the kernel default for VLAN devices (unverified). Keep the hand-written `bond0.70` stanza with `mtu 9000`; whether it merges cleanly with the implicit device is unverified.
- The tenant MTU stays 8996 (§3.4).

Permissions: the Terraform token needs `SDN.Allocate` on `/sdn` once. Once capmox has per-tenant credentials, each tenant gets `SDN.Use` only on `/sdn/zones/cozy/pxt70/<vlan>` for its own VLANs, and never untagged access to `pxt70`, which lands in its VLAN 1. The admin creates the trunk NICs.

Failure modes:

- An apply that fails on a node leaves the whole transport missing there, so every tenant VLAN on that node is down, while the running config is already committed. `/nodes/<n>/sdn/zones/cozy` shows the VNet as `error` or `pending`.
- Because the zone's bridge is not VLAN-aware, each node runs `ifreload -a` at its own boot and commits any pending SDN config cluster-wide (`pve-sdn-commit`, §13.2). Idempotent `post-up` hooks are a precondition of A1, not an option.
- A hand edit of the VNet tag or the zone bridge re-tags the whole transport on the next apply, whoever triggers it.
- A1 pins vmbr0 as not VLAN-aware. If vmbr0 is later made VLAN-aware, one of the guest-tag mitigations in §10, the next apply silently switches the topology to `vmbr0.70`. Use the ebtables mitigation instead.
- A new PVE node needs a `nodes` change and an apply.

Blast radius: one cluster-wide `ifreload -a` at migration and one for every later SDN change, plus an `ifreload -a` of each node at its own boot, which also commits any pending SDN edit cluster-wide. No tenant action triggers one.

Migration from vmbr70, in a maintenance window during which every tenant VLAN is down:

1. Meet the host preconditions of §13.2.
2. Remove the `vmbr70` stanza and keep `bond0.70`. Move the `txvlan off` `post-up` to the `bond0` or `bond0.70` stanza: SDN stanzas carry no `post-up`, and `ifreload` re-runs it on every apply. vmbr70's other `post-up`, the `disable_ipv6` sysctl, goes with it; the generated `pxt70` and `vmbr0v70` need the replacement of §12, question 14.
3. Move the transit router, if it runs on a hypervisor. Its `vmbr70.100` stanza (the transit gateway address, the zone `mtu`, its firewall and SNAT hooks, the `disable_ipv6` sysctl) sits on vmbr70, so removing vmbr70 cuts egress for every VPC. Recreate it as `pxt70.100`, and rename `vmbr70.100` in every rule (`-i`/`-o`), in the sysctl key and in whatever tooling manages or checks the router. Whether a hand-written VLAN device in `/etc/network/interfaces` on a bridge generated in `interfaces.d/sdn` comes up in the right order is unverified.
4. Create the zone and the VNet, dry-run on each node, then apply.
5. Re-point the NICs. Trunk NICs: `qm set` to `bridge=pxt70`. CAPI machines: change `zone.spec.bridge`, which renders new ProxmoxMachineTemplates and rolls the workers; capmox never re-points a provisioned VM (§12, question 17).

A possible shortcut (unverified): name the VNet `vmbr70`. That is a valid ID, and no check against existing local bridges was found. Every NIC config keeps its meaning and no rollout is needed, and the transit stanza keeps its name `vmbr70.100` (step 3 is not needed), but the ACL paths move from `localnetwork/vmbr70/...` to `cozy/vmbr70/...`. Whether PVE re-plugs tap devices detached when the old bridge went away is open (§12, question 13), so plan to restart the affected VMs.

**A2: QinQ zone variant** (`tag=70`, `bridge=vmbr0`, `vlan-protocol 802.1q`). It creates `sv_cozy` (vlan-raw-device bond0, vid 70) and a VLAN-aware `z_cozy`, and needs the one allowed tag-less `vlanaware` VNet (port `pr_cozy`) as the trunk and access bridge [SRC `Zones/QinQPlugin.pm`]. That VNet's behaviour as a trunk is undocumented and unverified. SDN sets no MTU on `sv_cozy`, nor on the zone veths `ln_cozy`/`pr_cozy` [SRC `QinQPlugin.pm:149-159`]; `pr_cozy` is the only port of the tag-less VNet, so the trunk path stays at the veth default of 1500 whatever the zone `mtu`. Prefer A1. Never set `vlan-protocol` on a QinQ zone whose bridge is VLAN-aware: SDN then rewrites that host bridge's protocol [SRC `QinQPlugin.pm:112-126`].

#### Model B: one VNet per ProxmoxNetwork

PVE objects:

- zone `cozy`: type `vlan`, `bridge=vmbr70` (hand-written or managed by Ansible), `mtu=8996`, `nodes=`, no `ipam`;
- one VNet per network, named `<prefix><vlan>` (for example `cz101`; prefix at most 4 characters), `tag=<vlan>`, `alias=<namespace>.<network>` as the owner marker.

On each node this creates `vmbr70.<vlan>` and a bridge `cz101`; the VLAN's vid is added to vmbr70 itself. B builds on A0, not on A1: a VLAN zone whose bridge is another zone's VNet is unverified, and its first apply fails because that bridge does not exist yet when the config is generated.

Two ways to create the VNets:

- **B-pool:** the admin pre-creates one VNet for each VLAN in `vlanRanges` (Terraform `for_each`) and applies once. The controller only maps `status.bridge = prefix + vlan` and checks it read-only. The cost is two netdevs per VLAN on every node, used or not: 99 VLANs mean about 200.
- **B-dynamic:** the controller creates, deletes and applies VNets through the PVE API (§13.5).

Mapping to Cozystack:

- `ProxmoxNetworkZone.spec.bridge` is the underlay (`vmbr70`) and is denied to every CAPI NIC. The zone gets `spec.sdn {zone, mode, vnetPrefix}`.
- ProxmoxNetwork: `status.vlan` is unchanged and stays the Kube-OVN localnet tag; `status.bridge` becomes `cz101`; new `status.vnet`; `status.nicVLAN` unset; new condition `VNetReady`.

NICs:

- CAPI NIC: `{bridge: cz101, mtu: 8996, ipv4PoolRef}` without `vlan`. capmox omits `tag=` when `vlan` is nil [CAPMOX `utils.go`], and PVE would refuse a tag on this VNet anyway. The charts render `vlan` only when `nicVLAN` is set.
- Trunk: stays `bridge=vmbr70,trunks=100-199` in `localnetwork`. Every VNet's `vmbr70.<tag>` sends into vmbr70 tagged, so Kube-OVN is unchanged. The trunk cannot be a VNet.

MTU: the zone `mtu` (8996) goes on the VNet bridge, and `vmbr70.<tag>` takes 9000 from vmbr70. The MTU now lives in two places, the PVE zone and the ProxmoxNetworkZone, so the controller checks them for drift.

Permissions: a per-tenant capmox token gets `SDN.Use` on `/sdn/zones/cozy/cz<vlan>` for its own networks only. Writing ACLs needs `Permissions.Modify`; keep that out of the controller. Editing a VNet firewall needs `SDN.Allocate` on `/sdn/zones/cozy/<vnet>`.

Extra isolation is limited:

- Per-VNet ACLs matter only once capmox uses privilege-separated tokens per tenant: `root@pam` skips the check, and a shared token reaches every tenant's VNets anyway. The same per-VLAN ACL already works today on `localnetwork/vmbr70/<vlan>`.
- The VNet firewall accepts forward-direction rules only and exists only with the nftables proxmox-firewall, which is still an opt-in tech preview (§12, question 19) [D-FW; RM]. PVE generates its IP sets only from PVE IPAM, which Cozystack does not use, so the controller would have to write them.
- The gain over VLAN plus OVN isolation is small.

New exposure: each VNet puts a host L2 interface on a tenant VLAN. Hosts run with `disable_ipv6=0` by default, the hand-written zone bridge disables IPv6 in a `post-up` (§10, step 1), and SDN stanzas cannot carry one. Unless PVE suppresses IPv6 on the bridges it generates (§12, question 14), the hosts become reachable over IPv6 link-local from tenant VMs. That weakens the Proxmox row of §8 and must be tested before B.

Failure modes:

- **PVE API down:** new networks stay `VNetReady=False`; existing traffic is unaffected.
- **Partial apply:** the VNet is missing on one node, and capmox reports `VMProvisionFailed` there.
- **Foreign pending change:** the lock is refused, and the zone reports `SDNApplyBlocked`.
- **Unrelated host config error:** an `ifreload` error such as a stale stanza fails the node task on every apply. Judge success by per-node VNet status, not by the task.
- **VNet deleted while in use:** PVE does not refuse while a VM created outside CAPI still uses the VNet.
- **Hand-edited tag:** the next apply re-tags the VMs on that VNet.
- **Reboot during staging (B-dynamic):** the lock does not stop `pve-sdn-commit` (§13.2). A node that boots while the applier holds the lock with staged VNets commits them cluster-wide and reloads only itself; the other nodes run them only after the next apply. A `rollback` then has nothing left to discard, so the applier must detect a running config that changed under its lock and finish with the apply (or stage the reverse change), not assume the staged edits are still pending.

Blast radius: B-pool as A. B-dynamic runs one cluster-wide `ifreload -a` per batch of tenant creates or deletes, which also applies any staged `interfaces.new` and re-runs every `post-up`. That is exactly the non-goal of §2.

Migration: the transport does not change. A change of zone mode applies only to new networks. Moving an existing network is an explicit admin action followed by a worker rollout, because templates are immutable. VLAN 100 stays outside SDN.

#### Model C: VXLAN zone as transport

PVE objects:

- zone `cozyvx`: type `vxlan`, `peers=` one underlay IP per node, `mtu=` underlay - 50, `nodes`;
- one VLAN-aware VNet `pxvx`, whose `tag` is the VNI.

On each node this creates `vxlan_pxvx`, with static head-end replication to every peer, and the bridge `pxvx` [SRC `Zones/VxlanPlugin.pm:79-152`].

The mapping is that of A1: `zone.spec.bridge: pxvx`, NIC `bridge=pxvx,tag=<vlan>`, trunk `bridge=pxvx,trunks=<range>`. The trunk is then VLAN-in-VXLAN, which is untested with Kube-OVN.

Use C when a node cannot join VLAN 70 at L2: a routed leaf-spine, another rack or site, or a third host when the first two share a point-to-point link, since that host needs either a switch that carries VLAN 70 or an overlay.

MTU:

- VNet: underlay - 50, so 8950 on a 9000-byte underlay [SRC `VxlanPlugin.pm:120-124`; D-SDN].
- `ProxmoxNetworkZone.mtu`: underlay - 54, so 8946, because the inner 802.1Q tag rides inside the VXLAN packet.
- Trunk NIC: 8950.

Underlay:

- Use a dedicated L3 interface, not the uplink VLAN of the VLAN zone it replaces.
- VXLAN has no encryption [D-SDN]. On untrusted paths use a WireGuard fabric (new in PVE 9.2 [RM]) as the zone's `fabric`, MACsec, or a private link.
- A new node changes the peer list and needs an apply.
- BUM traffic is copied to every other peer (N-1 copies).
- No FRR controller and no EVPN. A fabric (OpenFabric or OSPF) may provide underlay routes only.

Migration: a second ProxmoxNetworkZone with its own ProviderNetwork and a second trunk NIC. Networks move by being recreated in the new zone, with a new VLAN. C coexists with A and B.

### 13.4 Recommendation and phasing

**Phase 0, the current state: no SDN objects and no apply.**

- Keep the hand-written vmbr70. Do not create even an empty zone: a pending zone is applied at the next reboot.
- Add read-only checks to the host tooling and to the E2E preflight: SDN zones, VNets and pending changes as expected (empty today), no SDN lock held, no `changes` attribute on `/nodes/<n>/network`, the `source` line on every node.
- Bring every host to the preconditions of §13.2: the `source` line, a redundant corosync link, no stale stanzas, idempotent `post-up` hooks, and no hand-plugged bridge ports outside `bridge-ports-condone-regex`.
- Independent of SDN, and with the most security value: per-tenant capmox credentials (`ProxmoxCluster.spec.credentialsRef`) plus `SDN.Use` on `/sdn/zones/localnetwork/vmbr70/<vlan>` for each allocated VLAN. No network reload is needed. Who writes these ACLs is open (§12, question 20).

**Phase 1, A1, at the next transport build.** Triggers: a new site, a third host behind a switch, a reinstall, or an existing cluster that meets the Phase 0 preconditions when the operator wants the transport in Terraform. Preconditions: the host preconditions of Phase 0, and §12 questions 13-16 closed on a test node; question 14 (IPv6 on the generated bridges) because A1 drops vmbr70's `disable_ipv6` sysctl. Cozystack changes: values only (`bridge: pxt70`, `vmbr0v70` in `uplinkBridges`). The optional read-only checks (`spec.sdn.mode: Transport`) need the API field and controller code of §13.5. A1 and B are alternative branches: if per-network PVE objects are expected, keep vmbr70 on the host and go A0, then B.

**Phase 2, B-pool, only when all of these hold:**

- per-tenant capmox credentials are in place;
- there is a concrete need for per-network objects in PVE (inventory and UI, per-VNet ACLs, or the VNet firewall once proxmox-firewall leaves tech preview);
- the IPv6 link-local exposure has been tested and closed;
- the range is at most about 100 VLANs.

**Phase 3, B-dynamic, only through a separate design proposal that reverses the §2 non-goal.** Trigger: the range or the number of nodes is too large for a pool. Preconditions: `ifreload -a` is harmless on every host (redundant corosync links, idempotent `post-up` hooks, no stale stanzas); PVE 9.2 or later, for dry-run and the lock; applies are batched.

**Phase 4, C,** when a node cannot share VLAN 70 at L2.

### 13.5 API, controller, admission

API (`api/proxmox/v1alpha1/proxmoxnetworkzone_types.go`):

```go
// +optional
SDN *ZoneSDN `json:"sdn,omitempty"`

type ZoneSDN struct {
	// PVE SDN zone ID.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z][a-zA-Z0-9]{0,6}[a-zA-Z0-9]$`
	Zone string `json:"zone"`
	// Transport: spec.bridge is the zone's VLAN-aware VNet (or a local bridge); NICs are tagged.
	// VNetPool: admin-created VNets <prefix><vlan>; spec.bridge is the underlay.
	// VNetPerNetwork: controller-created VNets; needs --enable-sdn-writes.
	// +kubebuilder:validation:Enum=Transport;VNetPool;VNetPerNetwork
	Mode string `json:"mode"`
	// VNet ID = prefix + decimal VLAN (at most 8 characters).
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9]{0,3}$`
	// +optional
	VNetPrefix string `json:"vnetPrefix,omitempty"`
	// Secret in the controller namespace: url, tokenID, secret, ca.crt.
	// Optional for Transport and VNetPool (read-only checks); required for VNetPerNetwork.
	// +optional
	CredentialsRef *corev1.LocalObjectReference `json:"credentialsRef,omitempty"`
	// VNetPerNetwork only: debounce (default 30s) and minInterval (default 5m).
	// +optional
	Apply *ZoneSDNApply `json:"apply,omitempty"`
}
```

Validation:

- CEL: `vnetPrefix` is required exactly when the mode is not Transport, and `credentialsRef` is required for VNetPerNetwork.
- `zone`, `mode` and `vnetPrefix` cannot change while the zone has networks. CEL cannot count networks, so the controller enforces it with `SDNReady=False/ModeChangeBlocked`.
- The controller flag `--enable-sdn-writes` is off by default.

Zone status: a new `status.sdn {type, nodes, pending, lockHeld, lastApply{time, version, result, message}}` and a new condition `SDNReady`, True when the PVE zone exists with type `vlan`, `vxlan` or `qinq`, has no `ipam` and no subnets, has the expected MTU, and every node in `nodes` reports it `available`. The expected MTU depends on the mode, because in Transport mode the inner tenant tag rides inside the PVE bridge:

- Transport (A1, C): the effective PVE zone MTU (for VXLAN without `mtu`, underlay - 50) = ProxmoxNetworkZone `mtu` + 4, for example 9000/8996 and 8950/8946;
- VNetPool and VNetPerNetwork (B): equal, for example 8996/8996.

ProxmoxNetwork status: new fields `vnet` and `nicVLAN` (`*int32`); `nicVLAN` equals `vlan` in bridge and Transport modes and is nil in the VNet modes. New condition `VNetReady`. `status.vlan` keeps its meaning as the Kube-OVN tag. The namespace annotation triple becomes `<bridge>/<nicVLAN or empty>/<pool>`.

Charts:

- The cozy-lib helper (`_proxmox.tpl:47-55`) returns `vlan = nicVLAN`, and falls back to `status.vlan` when `vnet` is empty, so networks written by an older controller still work.
- `kubernetes-nodes/templates/nodegroup.yaml:194-229` renders `vlan:` only when it is set.
- The vpc chart is unchanged.

Controller, Transport and VNetPool with credentials: read-only. The token has `SDN.Audit` on `/sdn/zones/<zone>` and `Sys.Audit` on `/nodes`. The controller checks the zone type, the absence of `ipam`, each VNet's tag, `vlanaware` flag and alias, per-node status, and that nothing is pending. Without credentials it checks nothing, as today, and failures surface at VM start.

Controller, VNetPerNetwork:

- Client: a thin client for about 10 endpoints. go-proxmox v0.8.1 has SDN wrappers, but its `SDNApply` sends no `lock-token` (`cluster_sdn.go:16-20`). The CA comes from the Secret, insecure TLS is never used, and a NetworkPolicy allows egress to port 8006 only.
- Token `cozy-sdn@pve!proxmox-network`, privsep=1, with a custom role: `SDN.Allocate` and `SDN.Audit` on `/sdn`, `Sys.Audit` on `/nodes`, no `Sys.Modify`, no `Permissions.Modify`. Apply and lock cannot be scoped below `/sdn`, so this token can also edit every zone, controller and fabric and reload every node's network (§13.2). That is the main reason this mode needs its own design proposal.

Network reconcile:

1. After `ensureVLAN` (`internal/proxmoxnetworkcontroller/network.go`), compute the VNet ID `<prefix><vlan>`.
2. Adopt a VNet with that ID and the same tag, zone and alias. An alias that names another owner is `VNetNameTaken`; a different tag is `VNetDrift`, reported and not fixed.
3. Mark the zone dirty so the applier picks the change up.
4. Set `VNetReady` once the VNet is in the running config and `available` on every zone node; only then set `status.bridge = vnet` and Ready. The existing render gate in cozy-lib holds machines back until then.

Zone applier, one apply in flight per zone, with debounce and minInterval:

1. Refuse while any zone node is offline, the cluster is not quorate, or a node reports `changes`.
2. `POST /cluster/sdn/lock` without `allow-pending`. PVE refuses while anything is pending: `SDNApplyBlocked/ForeignPendingChanges`.
3. Stage every queued VNet create and delete with the lock token.
4. Dry-run on every node. The interfaces diff may touch only `<prefix>N` and `<bridge>.N` stanzas, and FRR must stay out of it: no controllers or fabrics in the running or pending config. Otherwise `rollback` with the token.
5. `PUT /cluster/sdn` with the token; the lock is released on commit.
6. Poll `/nodes/<n>/sdn/zones/<zone>/content` until every changed VNet is `available` or gone. On timeout, `SDNApplied=False` with the node and its status.

Never roll back automatically after a commit. Keep the lock token in zone status; after a restart, roll back and release a token the controller still holds.

Delete:

1. Wait for `SwitchDeleted` (§6.3).
2. Stage the VNet delete and apply.
3. Wait until the VNet is gone on every node.
4. Delete the Kube-OVN Vlan, then release the network.

VNet tags in the running or pending config count as used VLANs (in `usedVLANs`, `network.go`). A zone finalizer blocks zone deletion while any `<prefix>*` VNet exists.

Drift and metrics: a resync every 10 minutes counts foreign VNets with tags in the range as used VLANs and exports a metric, and alerts on pending changes older than a set age, because `pve-sdn-commit` applies them at the next boot. New metrics: `cozy_proxmox_sdn_apply_total{zone,result}`, `cozy_proxmox_sdn_apply_duration_seconds`, `cozy_proxmox_sdn_pending{zone}`, `cozy_proxmox_sdn_vnet_drift{zone}`.

Admission (`packages/system/proxmox-network/templates/admission.yaml`):

- Transport mode: the rules do not change, only the values (`bridge: pxt70`, `uplinkBridges: [vmbr0, vmbr0v70]`). Optionally, a new admission-only key `parentBridge` in the package's `zones[]`, next to `uplinkVLAN` and `uplinkBridges`, lets the chart derive `<parentBridge>v<uplinkVLAN>` and deny it itself; neither the key nor the derivation exists today.
- VNet modes:
  - a NIC whose bridge matches `^<prefix>[0-9]{1,4}$` must have no `vlan` and must match a `<bridge>//<pool>` entry of the namespace annotation;
  - the zone's `spec.bridge` (the underlay) goes into `uplinkBridges` instead of the zone bridges, so it is denied to every CAPI NIC, tagged or not;
  - the chart refuses a prefix that starts any listed bridge name.
- Any mode, optionally: `paramKind: ProxmoxNetworkZone`, so bridges and prefixes come from the live zones. This closes the first known limit of §8.

### 13.6 E2E additions

- Preflight: the SDN zone is `available` on every zone node; nothing pending, no lock held, no node reports `changes`; the `source` line on every node; the zone `mtu` as expected; the trunk NIC's bridge is the zone's transport.
- Admission, VNet modes: own VNet without a vlan allowed; own VNet with a tag, another tenant's VNet, and the underlay bridge tagged or untagged denied. Transport mode: `vmbr0v70` denied.
- Datapath: a VM on the VNet reaches a pod on a trunk node and back, across hosts in both directions; a DF packet at the zone MTU arrives; on the far host `bridge fdb` lists the VM's MAC under the right VLAN.
- Security: the host does not answer over IPv6 link-local on any tenant VLAN, on `pxt70` VLAN 1 or on `vmbr0v70`; an 802.1Q frame sent by a guest on a VNet stays inside that VNet's VLAN.
- Lifecycle (B): after a VPC delete its VNet is gone on every node; a PVE API outage during create and during delete; a foreign pending change blocks the apply and nothing is applied; a controller restart in the middle of an apply leaves no lock behind; a node reboot while VNets are staged under the lock (the boot commits them, §13.3 B failure modes) ends with every node on the same config and no lock behind.
- Simulated VMs: in the VNet modes a simulated VM joins the VNet bridge (`status.bridge`) untagged, since `nicVLAN` is unset; in Transport mode it stays an access port of its VLAN, now on the VLAN-aware `pxt70`.

### 13.7 SDN features not to use

| Feature | Why not |
|---|---|
| Subnets, zone `ipam` (pve, NetBox, phpIPAM), `vnets/<id>/ips` | a second IP authority beside in-cluster IPAM; tech preview [D-SDN]; a zone's `ipam` cannot change once subnets exist |
| DHCP (dnsmasq) | Simple zones only; hands out addresses the IPAM provider does not know; dnsmasq is not installed by default |
| Simple zone, subnet `gateway`, `snat` | host-local L3: an address and SNAT on every node, routing outside the VPC router |
| EVPN zone, EVPN/BGP/ISIS controllers, exit nodes, anycast MAC, VRFs, route maps, prefix lists | routing inside PVE and FRR on every node; EVPN also refuses `vlanaware` |
| Fabrics as routing | only as the VXLAN underlay in C, and the WireGuard fabric only for encryption |
| DNS and reverse-DNS plugins | names are not a transport concern |
| `bridge-disable-mac-learning` | only the NIC's own MAC gets a static fdb entry, which breaks the trunk NIC (pod and OVN MACs) |
| `isolate-ports` | isolates guests on the same node only, so isolation becomes inconsistent; cuts same-node VM-to-gateway traffic when the trunk shares the VNet |
| NIC `firewall=1` or `macfilter` on trunk NICs | the trunk carries other MACs |
| VNet firewall / nftables proxmox-firewall | not now: opt-in tech preview, a host-wide switch, and its IP sets need PVE IPAM; revisit in Phase 2 |
| QinQ `vlan-protocol` on a VLAN-aware host bridge | rewrites that host bridge's protocol |
| Faucet zone | not in the docs; treat as experimental |
| Staging without an apply; a lock with `allow-pending` | `pve-sdn-commit` applies pending changes at the next boot, and `allow-pending` applies other people's changes |
| A VNet for VLAN 100 | collides with `vmbr70.100`, the transit router's address |
| Zone `nodes` narrower than capmox's allowed nodes | VM start fails on the missing node |

### 13.8 Open questions

The SDN questions are in §12 as questions 13-22; 17, 18, 19 and 21 are answered. A1 depends on questions 13, 14, 15 and 16; B on 14; the per-tenant ACLs of Phase 0 on 20.
