# Cozystack Tenant Gateway

Per-tenant Gateway API Gateway backed by Cilium. Installed automatically when `tenant.spec.gateway=true` on the publishing tenant.

## Parameters

### Common parameters

| Name               | Description                                                                                                      | Type     | Value    |
| ------------------ | ---------------------------------------------------------------------------------------------------------------- | -------- | -------- |
| `gatewayClassName` | GatewayClass to attach the tenant Gateway to. Must exist cluster-wide. Default matches the Cilium-managed class. | `string` | `cilium` |


## Security model

Two layers protect cross-tenant isolation:

1. **Namespace whitelist on listeners.** The Gateway's `allowedRoutes.namespaces.from: Selector` only accepts HTTPRoutes from the publishing tenant's namespace and from `publishing.gateway.attachedNamespaces` in the platform chart (default: cozy-cert-manager, cozy-dashboard, cozy-keycloak, cozy-system, cozy-harbor, cozy-bucket, cozy-kubevirt, cozy-kubevirt-cdi, cozy-monitoring, cozy-linstor-gui). A tenant namespace that is not on this list cannot attach an HTTPRoute to this Gateway at all.
2. **Hostname ownership via ValidatingAdmissionPolicy.** `cozystack-gateway-hostname-policy` (installed by cozystack-basics when `gateway.enabled` is true) rejects any Gateway whose listener hostnames fall outside the tenant's own domain suffix. For `tenant-root` the allowed suffix is `publishing.host`; for any `tenant-<name>` it is `<name>.publishing.host`. Other namespaces are rejected outright.

## Rate limits

cert-manager issues one `Certificate` per Gateway release. With `issuerName: letsencrypt-prod` (the default), the `Certificate` for a tenant Gateway counts against the [Let's Encrypt rate limits](https://letsencrypt.org/docs/rate-limits/):

- 50 new certificates per registered domain per week.
- 5 duplicate certificates per week for the same set of hostnames.
- 300 new orders per account per 3 hours.

A cluster where many tenants share the same apex domain can exhaust these quickly. Mitigations:

- Use `publishing.certificates.issuerName: letsencrypt-stage` for non-production clusters (staging does not count against prod quotas).
- Limit the number of simultaneous tenant Gateways per cluster via the platform's package quota, or cap it via `tenant.spec.resourceQuotas` with `count/certificates.cert-manager.io` to limit how many `Certificate` objects a tenant may create.
- For bare-metal or air-gapped deployments consider an internal ACME server or the self-signed `ClusterIssuer` (`selfsigned-cluster-issuer`) that ships alongside the Let's Encrypt issuers.

## Known limitations

- **TLS passthrough services** (`cozystack-api`, `vm-exportproxy`, `cdi-uploadproxy`) are not migrated to the Gateway. They keep rendering their existing Ingress regardless of `gateway.enabled`. A follow-up PR will add a Passthrough listener + `TLSRoute` per passthrough service.
- **Tenant-scoped apps** (`harbor`, `bucket`) are not yet wired to a tenant's own Gateway — they still use ingress-nginx even when `gateway.enabled=true`. Follow-up work needs to plumb `gateway` through `_namespace` in `packages/apps/tenant/templates/namespace.yaml` so the apps know which Gateway to attach to.
- **Child-tenant ACME HTTP-01** currently relies on the publishing tenant's Gateway; a child tenant that turns on `gateway: true` but still has its issuer pointed at `letsencrypt-prod` (HTTP-01) must either share the parent Gateway or switch to `dns01`. A proper fix is a namespace-scoped `Issuer` per tenant — tracked as a follow-up.

