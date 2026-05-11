# Cozystack Tenant Gateway

Per-tenant Gateway API Gateway backed by Cilium. Installed automatically when `tenant.spec.gateway=true` on the publishing tenant.

The chart renders one `gateway.cozystack.io/v1alpha1 TenantGateway` CR per tenant. The cozystack-controller reconciles the actual `Gateway`, per-tenant `Issuer`, and per-listener `Certificate` resources from there. Helm does not render `Gateway` or `Certificate` directly — that prevents the Helm-vs-controller race on `Gateway.spec.listeners` that ad-hoc HTTPRoute additions would cause.

## Cert mode: HTTP-01 (default) vs DNS-01 (opt-in)

The platform-wide `publishing.certificates.solver` value selects how the controller sources TLS certificates for the tenant Gateway.

### Default — HTTP-01

Out of the box, no extra config required. The controller:

- Renders an ACME `Issuer` in the tenant namespace with an `http01.gatewayHTTPRoute` solver pointing at the tenant's own Gateway / `http` listener.
- Watches HTTPRoutes / TLSRoutes attached to the Gateway (parentRefs pointing at it). For each unique hostname seen, it adds a per-app HTTPS listener and a per-app `Certificate` (dnsNames containing exactly that hostname).
- Per-app listener naming: `https-<first-label-of-hostname>` (e.g. `https-harbor`).
- Per-app cert naming: `<tgw-name>-<first-label>-tls`.

Adding a new published app is purely a matter of deploying its HTTPRoute — no edits to `_cluster.expose-services` needed.

### Opt-in — DNS-01

Set `publishing.certificates.solver: dns01` and configure the provider under `publishing.certificates.dns01.*` in the platform chart values. Each provider reads its own sub-block; others are ignored.

| Provider     | `publishing.certificates.dns01.provider` | Required `publishing.certificates.dns01.<provider>` keys                                  |
| ------------ | ---------------------------------------- | ----------------------------------------------------------------------------------------- |
| Cloudflare   | `cloudflare` (default)                   | `cloudflare.secretName`, `cloudflare.secretKey`                                           |
| AWS Route53  | `route53`                                | `route53.region`, `route53.secretName` (and `route53.accessKeyID` if not using IRSA)      |
| DigitalOcean | `digitalocean`                           | `digitalocean.secretName`                                                                 |
| RFC 2136     | `rfc2136`                                | `rfc2136.nameserver`, `rfc2136.tsigKeyName`, `rfc2136.secretName`                         |

The platform chart writes those values into `_cluster.dns01-*` keys consumed by the per-tenant gateway chart, which renders them onto the `TenantGateway` CR. Each provider sub-block carries safe defaults for secret-key field names (`api-token`, `secret-access-key`, `access-token`, `tsig-secret-key`) so the typical opt-in path is `solver: dns01` plus the provider-specific `secretName` (and `region` for route53 / `nameserver`+`tsigKeyName` for rfc2136).

DNS-01 mode renders a single wildcard `Certificate` covering `<apex>` and `*.<apex>`, plus the corresponding `https` (`*.<apex>`) and `https-apex` (`<apex>`) listeners. New apps published under the apex pick up the existing wildcard cert without per-listener provisioning.

Pick DNS-01 when you specifically want a wildcard cert (e.g. a long-lived staging cluster with many short-lived apps and tight LE rate limits). Otherwise stay on HTTP-01.

> **Listener-cap considerations.** Gateway API caps `Gateway.spec.listeners` at 64. In HTTP-01 mode, every published hostname adds one HTTPS listener, plus the mandatory `http` listener and one extra per TLS-passthrough service — so a tenant approaching 60+ published apps on HTTP-01 hits the spec cap and the rendered `Gateway` fails admission. DNS-01 mode collapses every hostname under the apex into one wildcard listener and is the right choice for high-fanout single-tenant deployments.

## External IP allocation

The per-tenant Gateway's auto-created `LoadBalancer` Service draws its IP from whatever LB allocator the cluster admin has configured at the platform layer — same shape as ingress-nginx today. Cozystack itself ships MetalLB installed but does not render any `IPAddressPool` / `L2Advertisement` / `BGPAdvertisement` from this chart; admins set up the allocator that suits their environment (MetalLB pool with L2 / BGP, Cilium LB-IPAM with announcer, robotlb against a cloud provider, or `Service.spec.externalIPs` pinning).

The tenant API stays mechanism-agnostic — no `gatewayIP` field, no allocator-specific manifest in the tenant chart. If a tenant needs a specific address (DNS already pinned, firewall rule, etc.), the operator pre-allocates it on the admin side: either pre-create the Service with `loadBalancerIP` set, or hand the tenant a reference to a named admin-managed pool. Per-Service IP uniqueness is the allocator's responsibility and works the same way as for any other LoadBalancer Service in the cluster.
## Parameters

### Common parameters

| Name                     | Description                                                                                                                                                                                                                                                                          | Type       | Value                                    |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ---------------------------------------- |
| `gatewayClassName`       | GatewayClass to attach the tenant Gateway to. Must exist cluster-wide. Default matches the Cilium-managed class.                                                                                                                                                                     | `string`   | `cilium`                                 |
| `tlsPassthroughServices` | Names (from publishing.exposedServices) whose traffic is TLS-passthrough rather than TLS-terminate. For each such service a dedicated HTTPS listener with tls.mode=Passthrough is rendered on the Gateway, and the service is expected to attach a TLSRoute instead of an HTTPRoute. | `[]string` | `[api, vm-exportproxy, cdi-uploadproxy]` |


## Security model

Tenants in Cozystack interact with the platform exclusively through `apps.cozystack.io/*` resources (Tenant, Bucket, Kubernetes, …) served by `cozystack-api`. Tenant RBAC (`cozy:tenant:*` aggregated to a RoleBinding in the tenant's own namespace) does not grant write access to `gateway.networking.k8s.io/*`, core `Namespaces`, or `cozystack.io/Package`. Every layer below is shaped by that constraint — the security model is built around the `apps.cozystack.io/*` API surface, not around Gateway API admission.

The protections below split into three groups by who they defend against:

- **Tenant-user-input gates** — Layer 4 (`cozystack-tenant-host-policy`). `Tenant.spec.host` is the user-supplied field that surfaces as a security boundary at the hostname layer; it is gated on every Create / Update via `cozystack-api`'s admission chain (`pkg/registry/apps/application/rest.go`).
- **Defense-in-depth** — Layers 1, 2, 5, 6, 7. These do not protect against tenant-user input (tenants don't hold the relevant RBAC). They guard against bugs in cozystack-controller / Flux, supply-chain compromise of an app chart that emits Gateway API resources, and confused-deputy mistakes by a cluster admin. Fail-closed via `failurePolicy: Fail` + `validationActions: [Deny]`.
- **Admin-against-themselves** — Layer 3 (`cozystack-gateway-attached-namespaces-policy`). Rejects a `kubectl edit packages.cozystack.io` that would slip a `tenant-*` entry into the platform Package's `gateway.attachedNamespaces`. Layer 6 catches the same misconfiguration at helm render time.

1. **Namespace whitelist on listeners.** Every listener carries an `allowedRoutes.namespaces.from: Selector` matching the built-in `kubernetes.io/metadata.name` label (written by kube-apiserver, unspoofable). HTTPS / TLS-passthrough listeners accept routes from the publishing tenant's namespace plus `gateway.attachedNamespaces` in the platform chart (default includes the `cozy-*` namespaces for platform services and `default` for the Kubernetes API TLSRoute). A namespace outside the list literally cannot attach any `HTTPRoute` or `TLSRoute` to those listeners. The plain-HTTP listener (port 80) carries a strictly narrower selector — only the tenant namespace itself (where the controller-owned http→https redirect HTTPRoute lives) and `cozy-cert-manager` (HTTP-01 ACME challenge HTTPRoutes) — so app HTTPRoutes attaching by hostname cannot bind to port 80 and serve plaintext. HTTPS listeners additionally restrict `allowedRoutes.kinds` to `HTTPRoute` (and TLS-passthrough listeners to `TLSRoute`), preventing GRPCRoute / TCPRoute / UDPRoute from attaching outside the route-hostname VAP's coverage.
2. **`cozystack-gateway-hostname-policy`** — `ValidatingAdmissionPolicy` on `gateway.networking.k8s.io/v1 Gateway` CREATE/UPDATE. Reads `namespaceObject.metadata.labels["namespace.cozystack.io/host"]` and rejects any listener hostname that is not equal to that value or a subdomain of it. `matchConditions` gate the VAP to cozystack-managed namespaces only — Gateways in unrelated namespaces (e.g. `kube-system`) are not touched.
3. **`cozystack-gateway-attached-namespaces-policy`** — VAP on `cozystack.io/v1alpha1 Package` CREATE/UPDATE. Rejects any `tenant-*` entry in `spec.components.platform.values.gateway.attachedNamespaces`. Catches direct `kubectl edit packages.cozystack.io` that would bypass the helm render-time guard in layer 6.
4. **`cozystack-tenant-host-policy`** — VAP on `apps.cozystack.io/v1alpha1 Tenant` CREATE/UPDATE. Rejects setting or changing `spec.host` unless the caller's groups contain `system:masters`, `system:serviceaccounts:cozy-system`, `system:serviceaccounts:cozy-cert-manager`, `system:serviceaccounts:cozy-fluxcd` or `system:serviceaccounts:kube-system`. Closes the path where a tenant user sets `spec.host=dashboard.example.org` on their own tenant to have the tenant chart write a hijacked label into the namespace.
5. **`cozystack-namespace-host-label-policy`** — VAP on core `v1 Namespace` CREATE/UPDATE. Rejects any set or change of the `namespace.cozystack.io/host` label, except by the same trusted-caller whitelist as layer 4. This closes both first-time label writes on CREATE and first-time adds on UPDATE — only cozystack/Flux service accounts (which apply the tenant chart) can stamp the label.
6. **Render-time `fail` in cozystack-basics.** The cozystack-basics chart fails the helm render if `_cluster.gateway-attached-namespaces` contains any `tenant-*` entry. Triggers on the helm-install path before the cluster ever sees the values — complements layer 3 which triggers at `kubectl apply` time.
7. **`cozystack-route-hostname-policy`** — VAP on `gateway.networking.k8s.io/v1 HTTPRoute` and `v1alpha2 TLSRoute` CREATE/UPDATE. Scoped to `tenant-*` namespaces (cozy-* are cluster-admin-managed and trusted to publish under any apex). Rejects any `spec.hostnames` entry that is not equal to the namespace's `namespace.cozystack.io/host` label or a subdomain of it. Defense-in-depth against an app chart bug or supply-chain compromise that emits Gateway API resources outside the tenant's apex — tenants in Cozystack do not hold `gateway.networking.k8s.io/*` RBAC by design, so this is not a tenant-user defense. The within-apex cross-namespace case (a tenant chart claiming a hostname that is published by a `cozy-*` app) is handled by the controller at reconciliation time: when two routes from different namespaces claim the same hostname, the `cozy-*` namespace wins and the loser receives a `HostnameConflict` condition under the controller's name in `Status.Parents`.

For `tenant-root` the allowed host suffix is `publishing.host`; for any `tenant-<name>` that inherits from its parent the suffix is `<name>.<parent apex>`. A child tenant with an independent apex (`customer1.io` instead of a subdomain) is handled correctly because the VAP reads the per-namespace label rather than assuming a subdomain hierarchy.

## Rate limits

cert-manager issues per-listener `Certificate` resources in HTTP-01 mode (one per published app), or a single wildcard `Certificate` per tenant in DNS-01 mode. With `issuerName: letsencrypt-prod` (the default), every certificate counts against the [Let's Encrypt rate limits](https://letsencrypt.org/docs/rate-limits/):

- 50 new certificates per registered domain per week.
- 5 duplicate certificates per week for the same set of hostnames.
- 300 new orders per account per 3 hours.

A cluster where many tenants share the same apex domain can exhaust these quickly, especially in HTTP-01 mode where each published app contributes one certificate. Mitigations:

- Use `publishing.certificates.issuerName: letsencrypt-stage` for non-production clusters (staging does not count against prod quotas).
- Limit the number of simultaneous tenant Gateways per cluster via the platform's package quota, or cap it via `tenant.spec.resourceQuotas` with `count/certificates.cert-manager.io` to limit how many `Certificate` objects a tenant may create.
- Switch to DNS-01 to consolidate every tenant's apps under one wildcard cert (cuts cert count from N apps to 1).
- For bare-metal or air-gapped deployments consider an internal ACME server or the self-signed `ClusterIssuer` (`selfsigned-cluster-issuer`) that ships alongside the Let's Encrypt issuers.

Recommended tenant-level quota to contain a misbehaving tenant:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
spec:
  gateway: true
  resourceQuotas:
    count/certificates.cert-manager.io: "10"
```

The default for a fresh tenant is unlimited; operators running shared-apex multi-tenant clusters should set this explicitly (or stage it via the tenant-application default values) before opening `gateway: true` to non-trusted tenants.

## Known limitations

- **Upstream application gaps** — some chart-level features (harbor ACL integrations, bucket upstream limitations) remain on ingress-nginx workflows in upstream docs; cozystack tracks those separately as upstream PRs.
- **Supported ACME issuers** — `publishing.certificates.issuerName` for Gateway-based tenants must be `letsencrypt-prod` or `letsencrypt-stage` (the controller maps those names to concrete ACME server URLs). To support another ACME provider, extend the controller's renderer with an additional branch.
- **Inheritance from parent Gateway** — child tenants currently must opt into their own Gateway via `tenant.spec.gateway=true`. There is no "share the parent's Gateway" mode. Two reasons that compound: (a) the Gateway API spec hard-caps `Gateway.spec.listeners` at 64, and HTTP-01 ACME mints one listener per Certificate — inheriting bottom-tenant routes onto the parent Gateway exhausts that cap quickly; (b) at the Cilium layer every tenant Gateway claims `443/TCP`, so sharing-key on a single parent Gateway is inactive until [Cilium ListenerSet support](https://github.com/cilium/cilium/issues/42756) lands. Until ListenerSet ships, per-tenant Gateway is the only scalable shape. Inheritance may revisit once the upstream constraint clears.
