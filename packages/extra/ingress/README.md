# Ingress-NGINX Controller

## Parameters

### Common parameters

| Name               | Description                                                                                                                             | Type       | Value      |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `replicas`         | Number of ingress-nginx replicas.                                                                                                       | `int`      | `2`        |
| `whitelist`        | List of client networks.                                                                                                                | `[]string` | `[]`       |
| `cloudflareProxy`  | Restoring original visitor IPs when Cloudflare proxied is enabled.                                                                      | `bool`     | `false`    |
| `resources`        | Explicit CPU and memory configuration for each ingress-nginx replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `resources.cpu`    | CPU available to each replica.                                                                                                          | `quantity` | `""`       |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                 | `quantity` | `""`       |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                 | `string`   | `s1.micro` |


## Exposure mode

The ingress Service type is driven by the cluster-wide `publishing.exposure` value in the platform chart, not by any key in this package. Two modes exist:

- `externalIPs` (default) has three rendered shapes:
  - Release namespace matches `publishing.ingressName` AND `publishing.externalIPs` is non-empty → Service is `ClusterIP` with `Service.spec.externalIPs` set from that list and `externalTrafficPolicy: Cluster`.
  - Release namespace matches `publishing.ingressName` but `publishing.externalIPs` is empty → Service falls back to `type: LoadBalancer` with `externalTrafficPolicy: Local`.
  - Release namespace does not match `publishing.ingressName` (non-root tenants) → Service is `type: LoadBalancer` with `externalTrafficPolicy: Local`.
  `Service.spec.externalIPs` is deprecated upstream in Kubernetes v1.36 (KEP-5707); plan migration before v1.40.
- `loadBalancer` — Service is `type: LoadBalancer` with `externalTrafficPolicy: Local`, and a `CiliumLoadBalancerIPPool` makes the addresses in `publishing.externalIPs` allocatable via Cilium LB IPAM. Requires `publishing.externalIPs` to contain at least one non-empty address (render fails otherwise) and assumes the addresses are already routed to a cluster node (floating IP / upstream router). See the inline comment on `publishing.exposure` in the platform chart for full caveats, including the note that switching the value on a running cluster causes the ingress Service to be recreated.

This setting only migrates ingress-nginx away from `Service.spec.externalIPs`. Other cozystack components that use the same deprecated field (e.g. the `vpn` app) must be migrated separately before Kubernetes v1.40 flips the `AllowServiceExternalIPs` feature gate off.

