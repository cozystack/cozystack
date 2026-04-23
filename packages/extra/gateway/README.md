# Cozystack Tenant Gateway

Per-tenant Gateway API Gateway backed by Cilium. Installed automatically when `tenant.spec.gateway=true` on the publishing tenant.

## Parameters

### Common parameters

| Name                     | Description                                                                                                                                                                                                                                                                          | Type       | Value                                    |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ---------------------------------------- |
| `gatewayClassName`       | GatewayClass to attach the tenant Gateway to. Must exist cluster-wide. Default matches the Cilium-managed class.                                                                                                                                                                     | `string`   | `cilium`                                 |
| `tlsPassthroughServices` | Names (from publishing.exposedServices) whose traffic is TLS-passthrough rather than TLS-terminate. For each such service a dedicated HTTPS listener with tls.mode=Passthrough is rendered on the Gateway, and the service is expected to attach a TLSRoute instead of an HTTPRoute. | `[]string` | `[api, vm-exportproxy, cdi-uploadproxy]` |


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

- **Child-tenant ACME HTTP-01** currently relies on the publishing tenant's Gateway; a child tenant that turns on `gateway: true` but still has its issuer pointed at `letsencrypt-prod` (HTTP-01) must either share the parent Gateway or switch to `dns01`. A proper fix is a namespace-scoped `Issuer` per tenant — tracked as a follow-up.
- **Upstream application gaps** — some chart-level features (harbor ACL integrations, bucket upstream limitations) remain on ingress-nginx workflows in upstream docs; cozystack tracks those separately as upstream PRs.

