# Managed Site-to-Site Router

Site Router connects a Cozystack tenant network to a remote network over an IPsec site-to-site tunnel. It runs a VyOS-based router VM in the tenant namespace and programs the routed data path (static routes, optional BGP) so that the configured remote networks become reachable from the tenant workloads and vice versa.

## What it is

Site Router is **routed (L3) site-to-site connectivity**: decrypted traffic is L3-forwarded onto the tenant pod network with the **remote source IP preserved** — a workload sees the real client address, not a translated one — and whole subnets are reachable in both directions for TCP, UDP, ICMP and SCTP. The tunnel is IKEv2 IPsec terminated inside a VyOS gateway VM that the app's Helm chart materializes in the tenant namespace (a KubeVirt `VirtualMachine`, its boot disk, the tunnel `LoadBalancer` Service and the credential Secrets); a platform controller then mediates the pieces the chart cannot express — it validates the remote networks, programs the kube-ovn return route, relaxes the gateway port's anti-spoofing after the guest source filter is up, and pushes the live VyOS configuration over the management API.

ESP is always encapsulated in UDP (forced NAT-T): native ESP is dropped pod-to-pod by the CNI conntrack, so the tunnel forces UDP encapsulation unconditionally. A TCP MSS clamp is applied by default (derived from the overlay MTU) so large flows do not black-hole on the reduced tunnel path.

## When to use it

Use `site-router` when you want a remote network and your tenant workloads to reach each other **by their real addresses** — routed, symmetric, source-IP-preserving connectivity, the productized replacement for a hand-rolled `port_security` relaxation plus namespace-route recipe. It is not a NAT gateway: it does not masquerade tenant traffic behind a single address and does not port-forward inbound connections to specific services. A future `site-gateway` app (Phase 2) will cover the NAT / port-forward case; until it lands, reach for `site-router` when whole-subnet, source-preserving reachability is what you need, and do not use it where you specifically want address translation.

## Model

- **Responder only.** The gateway is the IPsec responder: the remote peer dials in to the public tunnel endpoint (a native `Service type: LoadBalancer` on IKE UDP 500 and NAT-T UDP 4500). The gateway does not initiate the tunnel; an initiator model is a later-phase follow-up.
- **One peer per instance.** Each instance builds exactly one tunnel to one remote peer. Reach a second site by deploying a second `site-router` instance in the same tenant.
- **LoadBalancer pool / quota consequence.** Because each instance exposes its own tunnel endpoint, each instance claims one address from the tenant's LoadBalancer pool. The number of concurrent sites is therefore bounded by the tenant's LB-pool size and quota — plan pool capacity for the number of remote sites you intend to connect.

## Example

Minimal instance — a single tunnel to a remote peer with one reachable remote subnet and an explicit pre-shared key (omit `peer.auth.psk` to have one auto-generated and stored in a Secret):

```yaml
peer:
  address: 203.0.113.10        # public address the remote peer dials in from
  auth:
    psk: "replace-with-a-strong-pre-shared-key"
remoteCIDRs:
  - 192.168.50.0/24            # remote network to make reachable (must not overlap cluster networks)
```

`remoteCIDRs` must be disjoint from the cluster pod/service/join networks; an overlapping value is rejected synchronously at apply time (and again by the controller), naming the offending CIDR and the network it collides with.

## Parameters

### Tunnel configuration

| Name                       | Description                                                                                                                      | Type     | Value   |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `tunnel`                   | Site-to-site tunnel configuration.                                                                                               | `object` | `{}`    |
| `tunnel.type`              | Tunnel protocol used to connect to the remote peer.                                                                              | `string` | `ipsec` |
| `peer`                     | Remote peer this router builds a tunnel to.                                                                                      | `object` | `{}`    |
| `peer.address`             | Public address (IP or hostname) of the remote peer. Responder model: the remote peer dials in.                                   | `string` | `""`    |
| `peer.auth`                | IPsec authentication material for the tunnel.                                                                                    | `object` | `{}`    |
| `peer.auth.psk`            | IPsec pre-shared key. Autogenerated and stored in a Secret when omitted. Mutually exclusive with `existingSecret`.               | `string` | `""`    |
| `peer.auth.existingSecret` | Name of an existing Secret in the tenant namespace holding the pre-shared key (key `psk`). Takes precedence over `psk` when set. | `string` | `""`    |


### Routing configuration

| Name                          | Description                                                                                                                           | Type       | Value   |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `remoteCIDRs`                 | Remote networks reachable over the tunnel. Must be disjoint from the cluster pod/service/join networks (validated by the controller). | `[]string` | `[]`    |
| `staticRoutes`                | Optional extra static routes programmed on the router.                                                                                | `[]object` | `[]`    |
| `staticRoutes[i].destination` | Destination network in CIDR notation.                                                                                                 | `string`   | `""`    |
| `staticRoutes[i].nextHop`     | Next-hop IP address for the destination.                                                                                              | `string`   | `""`    |
| `bgp`                         | Optional BGP peering over the tunnel. Disabled by default.                                                                            | `object`   | `{}`    |
| `bgp.enabled`                 | Enable BGP peering.                                                                                                                   | `bool`     | `false` |
| `bgp.localASN`                | Local autonomous system number. Required when `enabled` is true; must be a valid ASN (1..4294967295).                                 | `int`      | `0`     |
| `bgp.neighbors`               | BGP neighbors to peer with.                                                                                                           | `[]object` | `[]`    |
| `bgp.neighbors[i].address`    | Neighbor IP address.                                                                                                                  | `string`   | `""`    |
| `bgp.neighbors[i].remoteASN`  | Remote autonomous system number of the neighbor. Must be a valid ASN (1..4294967295).                                                 | `int`      | `0`     |


### Security

| Name                       | Description                                                                                                                                                                                                                                                                                                                                                                            | Type       | Value |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ----- |
| `security`                 | Platform-owned network security guards for the gateway VM.                                                                                                                                                                                                                                                                                                                             | `object`   | `{}`  |
| `security.egressDenyCIDRs` | Extra destination CIDRs the gateway is denied egress to, on top of the built-in link-local `169.254.0.0/16` deny (which always applies and covers the cloud metadata endpoint `169.254.169.254`). Use for management or node ranges that do not overlap tenant workloads; do NOT list the cluster pod/service/join ranges (the gateway must reach tenant workloads). Empty by default. | `[]string` | `[]`  |


### Common parameters

| Name               | Description                                                                                                                                                                   | Type       | Value |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ----- |
| `resources`        | Explicit CPU and memory sizing for the router VM.                                                                                                                             | `object`   | `{}`  |
| `resources.cpu`    | CPU topology cores allocated to the router VM. Whole cores only; a fractional quantity (e.g. "1500m", "0.5") is rejected at admission rather than silently truncated to zero. | `int`      | `2`   |
| `resources.memory` | Memory (RAM) allocated to the router VM.                                                                                                                                      | `quantity` | `2Gi` |


### Image and low-level materialization

| Name                  | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Type     | Value           |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | --------------- |
| `image`               | Boot-disk image source for the router VM.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `object` | `{}`            |
| `image.name`          | Golden image name in cozy-public. Cloned as the boot disk from PVC vm-default-images-<name> unless `enabled` is true.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `string` | `vyos-router`   |
| `image.enabled`       | Import the boot disk over HTTP from `url` instead of cloning the golden-image PVC.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `bool`   | `false`         |
| `image.url`           | HTTP(S) URL of a VyOS qcow2/raw disk image. Used only when `enabled` is true.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | `string` | `""`            |
| `managementCIDR`      | Source CIDR allowed to reach the VyOS management API (HTTPS 443) through the first-boot firewall. This value and the controller's --management-cidr flag (T05/T06) must agree: both default to the cluster pod CIDR (10.244.0.0/16, the kube-ovn default) and must be kept consistent. On a cluster with a non-default `networking.podCIDR`, set this (and the controller's managementCidr) to that pod CIDR, or the firewall will reject the real controller source. An empty value requires `allowOpenManagement=true` (fail-closed). Constrained to a strict IPv4 CIDR (or empty) so a tenant value cannot inject arbitrary text into the VyOS first-boot config. | `string` | `10.244.0.0/16` |
| `allowOpenManagement` | Permit an empty `managementCIDR`, leaving the VyOS management API unrestricted (no first-boot firewall). Fail-closed by default: an empty `managementCIDR` with this false aborts rendering.                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | `bool`   | `false`         |
| `cloudInitSeed`       | Opaque seed mixed into the VM firmware UUID. Change it to force a first-boot cloud-init re-run; clear it to preserve an existing VM's UUID across re-renders.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | `string` | `""`            |

