# Managed Site-to-Site Router

Site Router connects a tenant namespace to a remote network over an IPsec site-to-site tunnel. Workloads in the namespace and hosts in the remote networks reach each other by their real addresses: traffic is routed, not translated, so a workload sees the actual client address. Whole subnets are reachable in both directions for TCP, UDP, ICMP and SCTP. The router runs as a VM ([barerouter](https://github.com/aenix-io/barerouter)) in the tenant namespace.

Use it when a remote site and your workloads need to reach each other by their own addresses. It is not a NAT gateway: it does not hide tenant traffic behind one address and does not forward inbound connections to particular services.

## How it works

Each instance runs a gateway VM in the tenant namespace and exposes it through a `LoadBalancer` Service named `site-router-<name>-tunnel`, which takes one address from the tenant's LoadBalancer pool and listens on UDP 500 (IKE) and UDP 4500 (NAT-T). The remote peer starts the tunnel to that address; the gateway only answers. ESP always travels encapsulated in UDP, and TCP MSS is clamped to fit the tunnel.

The platform adds a route for every network in `remoteCIDRs` to the namespace, pointing at the gateway. Pods pick that route up only when they are created, so pods created before the route was added do not have it: restart them. The `PendingRoutes` event names the pods that still miss the route.

## Example

A tunnel to one remote peer with one remote network and an explicit pre-shared key. Leave out `peer.auth.psk` to have a key generated and stored in the Secret `site-router-<name>-psk` under the key `psk`.

```yaml
peer:
  address: 203.0.113.10        # public address the remote peer connects from
  auth:
    psk: "replace-with-a-strong-pre-shared-key"
remoteCIDRs:
  - 192.168.50.0/24            # remote network to make reachable
```

`remoteCIDRs` must not overlap the cluster pod, service or join networks, node addresses or allocated LoadBalancer and external service addresses. An overlapping value is rejected when the application is created or updated, and the error names the network it collides with.

## Configuring the remote peer

| Setting | Value |
| --- | --- |
| IKE version | IKEv2 |
| Authentication | Pre-shared key |
| IKE proposal | AES-256, SHA-256, DH group 14, lifetime 28800 s |
| ESP proposal | AES-256, SHA-256, PFS with DH group 14, lifetime 3600 s |
| Encapsulation | ESP in UDP (NAT-T), UDP 4500 |
| Gateway address | external IP of the Service `site-router-<name>-tunnel` |
| Local identity | the peer's own address, the one set in `peer.address` |
| Remote identity | external IP of the Service `site-router-<name>-tunnel` |
| Local traffic selector | the networks listed in `remoteCIDRs` |
| Remote traffic selector | `0.0.0.0/0` |

The peer has to initiate the tunnel. To find the gateway address, run `kubectl -n <namespace> get service site-router-<name>-tunnel`.

## Events

The application reports what it is waiting for or why it failed through Kubernetes events:

| Reason | Meaning |
| --- | --- |
| `ConfigApplied` | The gateway configuration was applied. |
| `TunnelAddressPending` | The tunnel Service has no external address yet. |
| `PSKSecretPending` | The Secret named in `peer.auth.existingSecret` does not exist yet. |
| `ConfigureFailed` | Applying the configuration to the gateway failed; the message says why. |
| `InvalidRemoteCIDR` | A network in `remoteCIDRs` overlaps a cluster network or address. |
| `RouteConflict` | Another instance in the namespace already routes the same remote network. |
| `PendingRoutes` | Pods created before the route existed still miss it and need a restart. |

## Limitations

- One remote peer per instance. Reach another site with another instance.
- A remote network can be routed by only one instance in a namespace.
- The gateway only answers; the remote peer must start the tunnel.
- Each instance takes one address from the tenant's LoadBalancer pool, so the pool size and quota bound the number of sites.
- The gateway is a KubeVirt VM, so the application is available only where virtualization is enabled.

## Parameters

### Tunnel configuration

| Name                       | Description                                                                                                                      | Type     | Value   |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `tunnel`                   | Site-to-site tunnel configuration.                                                                                               | `object` | `{}`    |
| `tunnel.type`              | Tunnel protocol used to connect to the remote peer.                                                                              | `string` | `ipsec` |
| `peer`                     | Remote peer of the tunnel.                                                                                                       | `object` | `{}`    |
| `peer.address`             | Public address (IP or hostname) the remote peer connects from. The remote peer starts the tunnel.                                | `string` | `""`    |
| `peer.auth`                | IPsec authentication material for the tunnel.                                                                                    | `object` | `{}`    |
| `peer.auth.psk`            | IPsec pre-shared key. Autogenerated and stored in a Secret when omitted. Mutually exclusive with `existingSecret`.               | `string` | `""`    |
| `peer.auth.existingSecret` | Name of an existing Secret in the tenant namespace holding the pre-shared key (key `psk`). Takes precedence over `psk` when set. | `string` | `""`    |


### Routing configuration

| Name                          | Description                                                                                                                                                                                                                        | Type       | Value   |
| ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `remoteCIDRs`                 | Remote networks reachable over the tunnel, at most 16. They must not overlap the cluster pod, service or join networks, node addresses or allocated LoadBalancer and external service addresses; an overlapping value is rejected. | `[]string` | `[]`    |
| `staticRoutes`                | Optional extra static routes programmed on the router.                                                                                                                                                                             | `[]object` | `[]`    |
| `staticRoutes[i].destination` | Destination network in CIDR notation.                                                                                                                                                                                              | `string`   | `""`    |
| `staticRoutes[i].nextHop`     | Next-hop IP address for the destination.                                                                                                                                                                                           | `string`   | `""`    |
| `bgp`                         | Optional BGP peering over the tunnel. Disabled by default.                                                                                                                                                                         | `object`   | `{}`    |
| `bgp.enabled`                 | Enable BGP peering.                                                                                                                                                                                                                | `bool`     | `false` |
| `bgp.localASN`                | Local autonomous system number. Required when `enabled` is true; must be a valid ASN (1..4294967295).                                                                                                                              | `int`      | `0`     |
| `bgp.neighbors`               | BGP neighbors to peer with.                                                                                                                                                                                                        | `[]object` | `[]`    |
| `bgp.neighbors[i].address`    | Neighbor IP address.                                                                                                                                                                                                               | `string`   | `""`    |
| `bgp.neighbors[i].remoteASN`  | Remote autonomous system number of the neighbor. Must be a valid ASN (1..4294967295).                                                                                                                                              | `int`      | `0`     |


### Common parameters

| Name               | Description                                                                                                                                                                                                                                                                                  | Type       | Value        |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------------ |
| `resources`        | Explicit CPU and memory sizing for the router VM.                                                                                                                                                                                                                                            | `object`   | `{}`         |
| `resources.cpu`    | CPU cores allocated to the router VM, whole cores only and at least one. A fractional value such as "1500m" is rejected.                                                                                                                                                                     | `int`      | `2`          |
| `resources.memory` | Memory (RAM) allocated to the router VM.                                                                                                                                                                                                                                                     | `quantity` | `2Gi`        |
| `storageClass`     | StorageClass for the router boot disk. Keep the `replicated` default where DRBD is available: only a DRBD-backed disk can live-migrate, and without live migration draining or losing the node drops the tunnel until the VM is rescheduled. Empty selects the cluster default StorageClass. | `string`   | `replicated` |


### Advanced parameters

| Name            | Description                                                                                                                                                         | Type     | Value |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `cloudInitSeed` | Opaque value mixed into the VM firmware UUID. Change it to make the router apply its first-boot configuration again; leave it empty to keep the existing VM's UUID. | `string` | `""`  |

