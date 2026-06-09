# Ingress-NGINX Controller

This package deploys an [ingress-nginx](https://github.com/kubernetes/ingress-nginx)
controller that serves as the HTTP(S) entry point for applications running in a
tenant. It is one of the cluster services a tenant can run on its own or inherit
from its parent, alongside `etcd`, `monitoring`, and `seaweedfs`.

## How ingress works

### One controller per tenant

Ingress is not a single shared component. Each tenant that needs it runs its own
ingress-nginx controller, deployed into the tenant's namespace. A tenant opts in
through the `ingress: true` field on its `Tenant` resource; when it is left at the
default (`false`), the tenant has no controller of its own and inherits its
parent's instead (see [Sharing across tenants](#sharing-across-tenants)).

Every controller is fully isolated: it has its own Deployment (named
`<tenant>-ingress`, e.g. `root-ingress` for `tenant-root`), its own replica count,
and its own resource budget. Defaults are `replicas: 2` and the `t1.micro`
resource preset; both are tunable through the parameters below.

The NGINX admission webhook is enabled only for the root tenant (`tenant-root`).
Child-tenant controllers run with it disabled to avoid the per-namespace webhook
certificate bootstrap and its overhead.

### IngressClasses and routing

Each controller owns a dedicated `IngressClass` named after its namespace. The
root controller registers the `tenant-root` class; a tenant named `tenant-foo`
registers `tenant-foo`, and so on. The class points at a namespace-scoped
controller via the value `k8s.io/ingress-nginx-<namespace>`, so controllers never
fight over the same `Ingress` resources even though they all run ingress-nginx.

An application is routed by a given controller when its `Ingress` sets
`spec.ingressClassName` to that controller's namespace name. Applications do not
hardcode this — they read it from the namespace configuration (see below), so the
correct class is selected automatically whether the tenant runs its own controller
or borrows one.

### Sharing across tenants

When you create a `Tenant`, Cozystack records which ingress controller its
applications should use and exposes it two ways on the tenant namespace:

- the label `namespace.cozystack.io/ingress`, and
- the `_namespace.ingress` value injected into every application in that namespace.

If the tenant enables its own ingress, both resolve to the tenant's own namespace.
If it does not, they resolve to the parent tenant's ingress namespace. This is how
a child tenant transparently routes through its parent's controller without
deploying anything: its applications inherit the parent's `IngressClass` name.

### How applications attach

Managed applications that expose an HTTP UI (Harbor, Grafana, the Kubernetes
dashboard, SeaweedFS, and others) render their own `Ingress` and read the
namespace configuration to wire it up. A typical application `Ingress` sets:

- `spec.ingressClassName` from `_namespace.ingress`, selecting the right
  controller,
- `cert-manager.io/cluster-issuer` and the ACME HTTP-01 ingress-class annotation
  from the cluster certificate settings, and
- a per-application TLS secret and a host derived from the tenant's published host.

You do not configure ingress per application — enabling ingress on the tenant and
publishing it externally is enough for application URLs to start working.

### External exposure

Running a controller does not by itself make it reachable from outside the cluster.
Exactly one controller is published externally: the one whose namespace matches
`publishing.ingressName` in the platform configuration (default `tenant-root`).
Every other tenant controller is rendered as a `LoadBalancer` Service with
`externalTrafficPolicy: Local`, leaving address assignment to the cluster's load
balancer.

How the published controller's Service is shaped depends on
`publishing.exposure` (platform-level), which selects between assigning the
addresses in `publishing.externalIPs` directly to a `ClusterIP` Service and
provisioning a `LoadBalancer` backed by Cilium LB IPAM. The exact rendered shapes,
along with the upstream deprecation of `Service.spec.externalIPs`, are covered in
[Exposure mode](#exposure-mode) below.

### TLS certificates

HTTPS is handled by cert-manager. The platform ships `letsencrypt-prod`,
`letsencrypt-stage`, and a self-signed `ClusterIssuer`; applications request
certificates against `publishing.certificates.issuerName` (default
`letsencrypt-prod`) using the configured solver (`http01` by default, `dns01`
optionally). Certificates are issued into per-application TLS secrets and renewed
automatically. With the HTTP-01 solver, cert-manager validates through the same
tenant controller the application uses, so the published controller must be
reachable on the application's hostname for issuance to succeed.

### Access control

Two parameters on this package adjust how the controller treats incoming traffic:

- `whitelist` — when set, NGINX is configured with `whitelist-source-range`, so
  only the listed client networks (CIDRs) reach the controller; everyone else gets
  a `403`. Leave it empty to accept traffic from anywhere.
- `cloudflareProxy` — when Cloudflare proxying is in front of the cluster, enabling
  this trusts Cloudflare's published IP ranges as `set_real_ip_from`, reads the
  client IP from the `CF-Connecting-IP` header, and turns on forwarded headers, so
  logs, metrics, and `whitelist` rules see the real visitor IP instead of
  Cloudflare's edge.

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
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                 | `string`   | `t1.micro` |


## Exposure mode

The ingress Service type is driven by the cluster-wide `publishing.exposure` value in the platform chart, not by any key in this package. Two modes exist:

- `externalIPs` (default) has three rendered shapes:
  - Release namespace matches `publishing.ingressName` AND `publishing.externalIPs` is non-empty → Service is `ClusterIP` with `Service.spec.externalIPs` set from that list and `externalTrafficPolicy: Cluster`.
  - Release namespace matches `publishing.ingressName` but `publishing.externalIPs` is empty → Service falls back to `type: LoadBalancer` with `externalTrafficPolicy: Local`.
  - Release namespace does not match `publishing.ingressName` (non-root tenants) → Service is `type: LoadBalancer` with `externalTrafficPolicy: Local`.
  `Service.spec.externalIPs` is deprecated upstream in Kubernetes v1.36 (KEP-5707); plan migration before v1.40.
- `loadBalancer` — Service is `type: LoadBalancer` with `externalTrafficPolicy: Local`, and a `CiliumLoadBalancerIPPool` makes the addresses in `publishing.externalIPs` allocatable via Cilium LB IPAM. Requires `publishing.externalIPs` to contain at least one non-empty address (render fails otherwise) and assumes the addresses are already routed to a cluster node (floating IP / upstream router). See the inline comment on `publishing.exposure` in the platform chart for full caveats, including the note that switching the value on a running cluster causes the ingress Service to be recreated.

This setting only migrates ingress-nginx away from `Service.spec.externalIPs`. Other cozystack components that use the same deprecated field (e.g. the `vpn` app) must be migrated separately before Kubernetes v1.40 flips the `AllowServiceExternalIPs` feature gate off.

