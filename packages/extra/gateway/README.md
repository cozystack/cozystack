# Cozystack Tenant Gateway

Per-tenant Gateway API Gateway backed by Cilium. Installed automatically when `tenant.spec.gateway=true` on the publishing tenant.

## Parameters

### Common parameters

| Name                     | Description                                                                                                                                                                                                                                                                          | Type       | Value                                    |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------- | ---------------------------------------- |
| `gatewayClassName`       | GatewayClass to attach the tenant Gateway to. Must exist cluster-wide. Default matches the Cilium-managed class.                                                                                                                                                                     | `string`   | `cilium`                                 |
| `tlsPassthroughServices` | Names (from publishing.exposedServices) whose traffic is TLS-passthrough rather than TLS-terminate. For each such service a dedicated HTTPS listener with tls.mode=Passthrough is rendered on the Gateway, and the service is expected to attach a TLSRoute instead of an HTTPRoute. | `[]string` | `[api, vm-exportproxy, cdi-uploadproxy]` |


## Security model

Six independent layers protect cross-tenant isolation. Compromising one of them does not bypass the others; admission-time checks (layers 2–5) fail closed (`failurePolicy: Fail`, `validationActions: [Deny]`).

1. **Namespace whitelist on listeners.** The Gateway's `allowedRoutes.namespaces.from: Selector` matches the built-in `kubernetes.io/metadata.name` label (written by kube-apiserver, unspoofable). It accepts routes from the publishing tenant's namespace plus `publishing.gateway.attachedNamespaces` in the platform chart (default includes the `cozy-*` namespaces for platform services and `default` for the Kubernetes API TLSRoute). A namespace outside the list literally cannot attach any `HTTPRoute` or `TLSRoute` to this Gateway.
2. **`cozystack-gateway-hostname-policy`** — `ValidatingAdmissionPolicy` on `gateway.networking.k8s.io/v1 Gateway` CREATE/UPDATE. Reads `namespaceObject.metadata.labels["namespace.cozystack.io/host"]` and rejects any listener hostname that is not equal to that value or a subdomain of it. `matchConditions` gate the VAP to cozystack-managed namespaces only — Gateways in unrelated namespaces (e.g. `kube-system`) are not touched.
3. **`cozystack-gateway-attached-namespaces-policy`** — VAP on `cozystack.io/v1alpha1 Package` CREATE/UPDATE. Rejects any `tenant-*` entry in `spec.components.platform.values.gateway.attachedNamespaces`. Catches direct `kubectl edit packages.cozystack.io` that would bypass the helm render-time guard in layer 6.
4. **`cozystack-tenant-host-policy`** — VAP on `apps.cozystack.io/v1alpha1 Tenant` CREATE/UPDATE. Rejects setting or changing `spec.host` unless the caller's groups contain `system:masters`, `system:serviceaccounts:cozy-system`, `system:serviceaccounts:cozy-cert-manager`, `system:serviceaccounts:cozy-fluxcd` or `system:serviceaccounts:kube-system`. Closes the path where a tenant user sets `spec.host=dashboard.example.org` on their own tenant to have the tenant chart write a hijacked label into the namespace.
5. **`cozystack-namespace-host-label-policy`** — VAP on core `v1 Namespace` CREATE/UPDATE. Rejects any set or change of the `namespace.cozystack.io/host` label, except by the same trusted-caller whitelist as layer 4. This closes both first-time label writes on CREATE and first-time adds on UPDATE — only cozystack/Flux service accounts (which apply the tenant chart) can stamp the label.
6. **Render-time `fail` in cozystack-basics.** The cozystack-basics chart fails the helm render if `_cluster.gateway-attached-namespaces` contains any `tenant-*` entry. Triggers on the helm-install path before the cluster ever sees the values — complements layer 3 which triggers at `kubectl apply` time.

For `tenant-root` the allowed host suffix is `publishing.host`; for any `tenant-<name>` that inherits from its parent the suffix is `<name>.<parent apex>`. A child tenant with an independent apex (`customer1.io` instead of a subdomain) is handled correctly because the VAP reads the per-namespace label rather than assuming a subdomain hierarchy.

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

- **Upstream application gaps** — some chart-level features (harbor ACL integrations, bucket upstream limitations) remain on ingress-nginx workflows in upstream docs; cozystack tracks those separately as upstream PRs.
- **Supported ACME issuers** — `publishing.certificates.issuerName` for Gateway-based tenants must be `letsencrypt-prod` or `letsencrypt-stage` (the chart maps those names to concrete ACME server URLs). To support another ACME provider, extend `templates/issuer.yaml` with an additional branch.

