# Publishing through a Cloudflare Tunnel

`cloudflare-tunnel-gateway-controller` is an optional system package that registers a second Gateway API implementation alongside the Cilium one. A Gateway on its class is published by Cloudflare: clients reach Cloudflare's edge, and the edge carries the request into the cluster over a Cloudflare Tunnel that the in-cluster data plane opens outbound. The Services the chart renders are `ClusterIP`, so nothing on this path asks the cluster for an address reachable from outside.

## When to reach for it

By default the Gateway a Cozystack tenant publishes through is backed by Cilium: `packages/extra/gateway` renders a `TenantGateway` on the platform's `gateway.className`, which defaults to `cilium`, and the cozystack-controller turns that into a `Gateway` plus whatever cert-manager `Issuer` and `Certificate` objects its cert mode calls for. That path wants two things from the outside world — an address clients can open a connection to, and an ACME challenge that completes against the cluster.

The tunnel class drops both for the hostnames it serves. Inbound connections are replaced by an outbound connection from the proxy pods to Cloudflare's edge, and TLS is terminated at the edge rather than on a Gateway listener. It fits a cluster behind NAT or CGNAT, a lab or home cluster with no routable prefix, a site whose firewall will not forward ports, or a deployment that wants Cloudflare's edge in front of a subset of hostnames.

It is the wrong tool when you need TLS passthrough or any non-HTTP protocol, when the certificate a client sees has to be one the cluster controls, or when a third party in the request path is unacceptable. GatewayClasses are cluster-scoped and each Gateway names the one it wants, so enabling this package does not move anything that is already published through Cilium.

## What has to exist on the Cloudflare side

1. A zone whose DNS is served by Cloudflare, covering the hostnames you intend to publish.
2. A Tunnel, created under **Zero Trust → Networks → Tunnels** with the `cloudflared` connector type. Keep both its **Tunnel ID** and its **tunnel token**.
3. An API token with the **Account → Cloudflare Tunnel → Edit** permission. That single scope is everything the controller asks for and everything the token can do.

That permission is account-scoped, and Cloudflare's token editor offers no per-tunnel resource scope to narrow it with, so the token reaches every tunnel in the account rather than the one this cluster uses. The controller spends it on two things: listing the accounts the token can see, to auto-detect the account ID, and reading and rewriting the ingress configuration of its own tunnel. Whoever else holds the token can rewrite any other tunnel in the same account exactly as easily, repointing a published hostname at an origin of their choosing or blackholing it, and revocation is per token rather than per tunnel. Keep the account free of tunnels you would not hand to this cluster.

Give the controller a tunnel of its own. Its upstream documentation states that it assumes exclusive ownership of the tunnel configuration, performs a full synchronization on startup, and removes ingress rules that do not come from routes it manages — so a tunnel that also carries hand-written public hostnames, or one shared with another system, loses them. Two clusters count as two systems, and the ordinary way to end up with two is not deliberate sharing: the tunnel ID lives on a Package CR, so a cluster restored from backup, a staging clone, or a rebuild that reuses the ID gives the tunnel a second owner, and each controller then deletes the other's ingress rules on every full sync. That reads as a public outage that flaps rather than one that stays down. Mint a new tunnel whenever the Package is copied.

The Cloudflare account ID is auto-detected when the API token has access to a single account. When it does not, supply it explicitly, either as an `account-id` key in the Secret below or through the chart's `gatewayClassConfig.accountId` value.

## The credentials Secret

Both planes read one Secret, named `cloudflare-tunnel-credentials`, in the package namespace `cozy-cloudflare-tunnel-gateway-controller`. The package pins the Secret name; the `tunnel-token` key is the chart's own default, and a chart test holds both in place across re-vendoring. `api-token` is likewise the controller's default for the credentials key, which the package leaves unset:

| Key | Read by | Contents |
| --- | --- | --- |
| `api-token` | the controller, through `GatewayClassConfig.spec.cloudflareCredentialsSecretRef` | the Cloudflare API token |
| `tunnel-token` | the proxy, as the `TUNNEL_TOKEN` environment variable | the tunnel's connector token |
| `account-id` | the controller, optional | account ID, when auto-detection cannot pick one |

Create it before or shortly after enabling the package — the proxy pod cannot start without it. Both tokens go in through files rather than `--from-literal`, because a literal ends up in the `kubectl` process arguments, which any local user can read out of `/proc` while the command runs:

```bash
umask 077
tokens="$(mktemp -d)"
trap 'rm -rf -- "$tokens"' EXIT
printf '%s' "$CF_API_TOKEN" > "$tokens/api-token"
printf '%s' "$CF_TUNNEL_TOKEN" > "$tokens/tunnel-token"

kubectl create namespace cozy-cloudflare-tunnel-gateway-controller \
  --dry-run=client --output yaml | kubectl apply --filename -

kubectl --namespace cozy-cloudflare-tunnel-gateway-controller \
  create secret generic cloudflare-tunnel-credentials \
  --from-file=api-token="$tokens/api-token" \
  --from-file=tunnel-token="$tokens/tunnel-token"
```

## Enabling the package

The package is not in any bundle by default, because it cannot become Ready without operator input. Enabling it takes two edits, and the second one lands on an object that exists only after the first.

Check the Gateway API bundle first, because the controller refuses to work against any minor but 1.6, and what it reads is the cluster's CRDs rather than anything this package installs:

```bash
kubectl get crd gatewayclasses.gateway.networking.k8s.io \
  --output jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}'
```

Anything other than `v1.6.<patch>` means the GatewayClass will come up `SupportedVersion=False` and silently lose any field the older CRD does not know. That is not fixable from the cluster: the bundle is baked into the `gateway-api-crds` package's own templates by a pinned `kubectl kustomize` ref, not exposed as a value to override, so the only route to 1.6.x is a Cozystack version whose `gateway-api-crds` carries it. The rest of this section assumes the lookup came back 1.6.x.

First, list it in `bundles.enabledPackages` on the platform values — the `platform` component of the `cozystack.cozystack-platform` Package CR:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  components:
    platform:
      values:
        bundles:
          enabledPackages:
          - cozystack.cloudflare-tunnel-gateway-controller
```

The name also has to stay out of `bundles.disabledPackages`: the platform helper that emits it checks that list first, so a name present in both is not emitted. That list is a veto on rendering, not an uninstall, and once the Package exists the platform refuses to render with the name vetoed — see [Removing the package](#removing-the-package).

That render creates a `Package` named `cozystack.cloudflare-tunnel-gateway-controller`. Second, set the tunnel ID on it:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cloudflare-tunnel-gateway-controller
spec:
  components:
    cloudflare-tunnel-gateway-controller:
      values:
        cloudflare-tunnel-gateway-controller:
          gatewayClassConfig:
            tunnelID: "00000000-0000-4000-8000-000000000000"
```

Until that value is set the chart refuses to render, and the HelmRelease reports `gatewayClassConfig.tunnelID is required`. Because the Package appears only once the name is in `enabledPackages`, the first reconcile after enabling fails this way; setting the tunnel ID changes the values and the install is retried. The failure is not confined to this release while the window is open: cluster readiness sweeps every HelmRelease, so the cluster reads not-ready until the tunnel ID is in place. Have it ready before enabling if anything gates on that.

The platform writes exactly one key of its own inside that same component values block — `controller.clusterDomain`, taken from `networking.clusterDomain` — because the vendored chart bakes the cluster domain into the proxy's config-endpoint URL and falls back to `cluster.local` when it is empty, which is wrong on a Cozystack default of `cozy.local`. The `gatewayClassConfig` block you add is a different key from the one the platform renders. If a future reconcile ever did drop it, the failure is the loud one above rather than a silently mis-rendered release.

## The GatewayClass, and attaching a Gateway

The package creates one `GatewayClass` named `cloudflare-tunnel`, with `spec.controllerName: cf.k8s.lex.la/tunnel-controller` and a `parametersRef` to the cluster-scoped `GatewayClassConfig` named `cloudflare-tunnel-gateway-controller`, which is where the tunnel ID and the credentials reference land. The class name is cosmetic; the controller binds its GatewayClasses by `controllerName`.

A Gateway joins the class by naming it, and routes attach to that Gateway the usual way:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: tunnel
  namespace: tenant-example
spec:
  gatewayClassName: cloudflare-tunnel
  listeners:
  - name: https
    protocol: HTTPS
    port: 443
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: app
  namespace: tenant-example
spec:
  parentRefs:
  - name: tunnel
  hostnames:
  - app.example.com
  rules:
  - backendRefs:
    - name: app
      port: 80
```

The listener carries no `tls` block: TLS ends at Cloudflare's edge, so there is no certificate for the Gateway to present. This is the listener shape the chart's own installation notes use.

The hostname is the one part of that example not to copy. This package turns the hostname-ownership layer on (see below), and in any namespace carrying `namespace.cozystack.io/host` that layer rejects a route unless every hostname equals the label or sits under it; in a `tenant-`-prefixed namespace Cozystack's own route policy applies the same bound. A Cozystack tenant's label holds `<tenant>.<parent apex>`, so `app.example.com` in `tenant-example` is refused unless the tenant's apex really is `example.com`. Substitute a hostname under the apex the namespace actually carries.

The controller writes the tunnel's CNAME target, `<tunnel-id>.cfargotunnel.com`, into the Gateway's `status.addresses`. Turning that into a DNS record needs an external-dns that watches Gateway API, which is not how Cozystack ships either of them: the system package `cozystack.external-dns` overrides no `sources` and so runs the upstream chart's defaults, `service` and `ingress`. Its wrapper exposes no value for them, but the Package's component values are assigned to the HelmRelease wholesale, so an operator can set `sources` there and reach the chart directly. The per-tenant `external-dns` application does have the switch — `gatewayAPI: true` adds the `gateway-httproute` and `gateway-tlsroute` sources — and it is `false` by default. Neither path adds `gateway-grpcroute`, so a hostname published only by a GRPCRoute gets no record from either. Failing all that, create the CNAME in the Cloudflare zone yourself, proxied.

Routes are `HTTPRoute` and `GRPCRoute`. A backend is normally a Service; the chart also installs an `ExternalBackend` CRD for pointing a route at an out-of-cluster HTTP(S) origin, which a Service of type `ExternalName` cannot express because it carries no scheme.

## Putting a tenant's own Gateway on the class

A tenant that owns a Gateway (`gateway: true` on its Tenant) can move it onto this class instead of running a standalone one. The platform decides which classes a tenant may pick and how each class terminates TLS, so this takes two platform values and one Tenant field. The package itself declares neither list: both are claims an operator makes about a class, and nothing checks them against the GatewayClass.

On the platform, in the same `platform` component values as `bundles.enabledPackages`:

```yaml
gateway:
  tenantSelectableClasses:
  - cloudflare-tunnel
  edgeTerminatedClasses:
  - cloudflare-tunnel
```

`tenantSelectableClasses` is the allowlist. A tenant naming a class outside it plus `gateway.className` fails its own `gateway` release with a message naming the allowed set, and no other tenant is affected. `edgeTerminatedClasses` declares that the class's provider ends TLS upstream of the Gateway, which is what Cloudflare's edge does here. Leave it out and the tenant Gateway is rendered for in-cluster termination: cert-manager certificates, `mode: Terminate` listeners, and the TLS-passthrough listeners this class has nothing to map onto.

On the Tenant:

```yaml
spec:
  gateway: true
  gatewayClass: cloudflare-tunnel
```

With the class listed as edge-terminated, the `gateway` chart writes `certMode: edge` on the `TenantGateway` and leaves `tlsPassthroughServices` out of it, and cozystack-controller then renders a Gateway this class can serve:

- plain HTTP listeners on port 80 for the apex, its wildcard and the wildcard of every inheriting child tenant's apex, with no `certificateRefs`, and no hostname-less port-80 listener;
- no TLS-passthrough listeners, so the tenant Kubernetes API, VM export and CDI upload endpoints are not published through this Gateway;
- no cert-manager `Issuer` or `Certificate`, and no HTTP-to-HTTPS redirect route, because the edge does both.

Three things follow from that:

- **HTTPRoute only.** The edge listeners admit `HTTPRoute` and nothing else, so a `GRPCRoute` cannot attach to a tenant Gateway on this class. gRPC still needs the standalone Gateway shown above.
- **The publishing tenant stays on the default class.** The cluster-wide ACME issuers are configured from `gateway.className`, so the `gateway` chart refuses to let the publishing tenant (`tenant-root` by default) select a class that terminates TLS differently from that default.
- **The operator wildcard is withdrawn.** A tenant on an edge-terminated class no longer counts as a TLS termination point, so the replica of `publishing.certificates.wildcardSecretName` in its namespace is deleted on the next reconcile.

An inheriting tenant does not choose: it publishes through its ancestor's Gateway, on whatever class that Gateway uses.

## Hostname ownership between tenants

By default every Gateway of this class shares one tunnel and one proxy pool, so the data plane picks a backend by hostname across all of them — a route claiming another tenant's hostname would be answered rather than ignored, which is not the case behind a per-tenant Cilium address.

Cozystack's own admission policy covers most of that. `cozystack-route-hostname-policy` requires every `HTTPRoute`, `TLSRoute` and `GRPCRoute` to declare its hostnames and binds each one to the namespace's `namespace.cozystack.io/host` label, failing closed when the label is missing. It matches only namespaces whose name starts with `tenant-`, though, and it decides once, at admission.

So the package also turns on the chart's own hostname-ownership layer, keyed on the same `namespace.cozystack.io/host` label and scoped to every namespace that carries it, whatever its name. It is enforced by the controller rather than at admission. On this class, a route in such a namespace is rejected by the controller unless it declares hostnames explicitly and each one equals the label value or is a subdomain of it. The chart's fail-fast admission half of that feature stays off on purpose: admission cannot resolve `parentRefs`, so it would also police routes bound to the Cilium Gateway in those same namespaces.

## Giving one Gateway its own tunnel

A Gateway can opt out of the shared data plane with a `GatewayConfig` (`cf.k8s.lex.la/v1alpha1`) referenced from its `spec.infrastructure.parametersRef`, in the same namespace. The controller then runs a proxy Deployment and a Cloudflare Tunnel dedicated to that Gateway. `spec.tunnelTokenSecretRef` is the only required field — the tunnel ID and account are parsed out of the connector token rather than configured separately. Reach for it when two sets of hostnames must not share a connector, or when one Gateway needs its own data-plane sizing.

Keep it an operator resource. A `GatewayConfig` also names the image for the Deployment the controller creates on its behalf, and the controller creates that Deployment with the cluster-wide grant described under Limitations. The tenant roles in `cozystack-basics` carry no access to the `cf.k8s.lex.la` group today, which is what keeps that out of tenant reach — widening a tenant's Gateway API access later should not quietly include this group.

## Limitations

- **HTTP and gRPC only.** A Cloudflare Tunnel carries HTTP to the origin and this controller implements `HTTPRoute` and `GRPCRoute`. There is no TLS passthrough and no raw TCP or UDP.
- **A tenant Gateway on this class carries HTTPRoute only.** The edge-mode listeners cozystack-controller renders admit `HTTPRoute` alone and no TLS-passthrough listener, so the tenant Kubernetes API, VM export and CDI upload endpoints, and every `GRPCRoute`, need another path. A tenant Gateway reaches this class through the platform allowlist and edge declaration described under [Putting a tenant's own Gateway on the class](#putting-a-tenants-own-gateway-on-the-class). Allowlisted without the edge declaration, it is rendered with `mode: Terminate` and passthrough listeners, and one listener the controller will not accept holds the `TenantGateway` at `Ready=False`, because cozystack-controller marks it Ready only when every listener reports both `Accepted` and `Programmed`.
- **No HTTP-to-HTTPS redirect in the cluster.** cozystack-controller renders no redirect route for an edge-terminated `TenantGateway`, and a standalone Gateway on this class gets none either. The redirect route it renders in the other modes carries no hostnames, which the hostname-ownership layer would reject on this class anyway. Enforce HTTPS at the Cloudflare zone level.
- **Certificates are Cloudflare's.** The edge terminates TLS, so the certificate a client sees is the one Cloudflare serves for the zone, not one cert-manager issued in the cluster. Cloudflare's Universal SSL covers the zone apex and one label below it, so a two-label hostname such as `app.tenant.example.com` needs Advanced Certificate Manager or a separate zone per tenant apex.
- **The controller holds a cluster-wide write grant.** Its ClusterRole is attached by a ClusterRoleBinding, so every rule in it applies in every namespace. It reads Secrets everywhere (`get`, `list`, `watch`, plus `create` for the config-API token it generates), because it resolves credential, TLS and CA-bundle references wherever a Gateway or route points at them. It can also write Deployments, Services, NetworkPolicies and HorizontalPodAutoscalers in any namespace — `create`, `update` and `delete` on all four, plus `patch` on Deployments — which is how it materialises a dedicated data plane next to a Gateway that asks for one. And it holds `update` and `patch` on `gateways` and `gatewayclasses` themselves, not only on their `/status` subresources — it writes a finalizer onto a class in use — which reaches the spec of any Gateway in the cluster, including ones belonging to the Cilium class. Cluster-wide Deployment creation is the strongest of these — it places a pod in any namespace, under any ServiceAccount that already exists there — so weigh that, not only the Secret read, when deciding to enable the package.
- **One controller replica, no leader election.** The chart defaults are `replicaCount: 1` with leader election disabled and the package does not override them, so configuration pushes pause while the controller pod restarts. The proxy runs two replicas, which the package spreads across nodes with a `topologySpreadConstraints` entry — without one both can land on the same node and a single drain takes every hostname published through the class down at once. The spread is `ScheduleAnyway`, so a single-node cluster still schedules both.
- **The proxy runs on the namespace's `default` ServiceAccount.** The proxy Deployment the chart renders sets no `serviceAccountName` and no `automountServiceAccountToken`, so the pod that terminates every request arriving from the Cloudflare edge carries a mounted `default` token. The vendored v3.4.1 chart exposes no value for either field, so the package cannot correct it; upstream v3.5.0 sets `automountServiceAccountToken: false` on the proxy, and this package does not vendor that release yet. Meanwhile `default` holds no RBAC of its own in this namespace, which bounds it to whatever a cluster grants that subject globally.
- **Tunnel dial is deferred on `protocol: auto`.** The proxy defaults to negotiating QUIC with an HTTP/2 fallback and waits for the controller's first config push before dialing, bounded at roughly 30 seconds — so a proxy on a route-less cluster can take that long to connect on each start. gRPC needs HTTP/2, since cloudflared drops HTTP trailers over QUIC and `grpc-status` is lost with them; `auto` upgrades at startup when a GRPCRoute already exists, so a GRPCRoute added later needs a proxy restart.
- **A namespace without the ownership label is out of scope, not denied.** The layer is scoped by `namespace.cozystack.io/host` existing, so a namespace that does not carry it is not policed rather than refused. In `tenant-`-prefixed namespaces Cozystack's own policy still covers `HTTPRoute`, `TLSRoute` and `GRPCRoute` at admission and fails closed on a missing label; a route on this class in any other namespace without that label is checked by neither layer.
- **Nothing is scraped by default.** Both ServiceMonitors the chart carries are gated on `serviceMonitor.enabled`, which is `false` and which the package does not change, so neither plane reaches the platform's Prometheus. That toggle alone is enough for the controller and not for the proxy: the proxy's ServiceMonitor is gated on `proxy.metrics.enabled` as well, and the proxy serves `/metrics` on its config API port, whose NetworkPolicy — on by default — admits that port only from the package namespace, so the monitoring namespace has to be added to `proxy.networkPolicy.ingress.from` in the same change.
- **Leave the controller's own NetworkPolicy off.** `networkPolicy.enabled` is `false` in the chart and the package does not change it. Turning it on confines controller egress to DNS, TCP 443 and 6443, and the Cloudflare address ranges — none of which covers the proxy's config API port, `8081` by default. Where the CNI enforces NetworkPolicy, the controller can then no longer push configuration to its data plane. The proxy's own policy is a separate value, on by default, and is not affected.
- **The Gateway API bundle has to be 1.6.x.** The controller reads the `gateway.networking.k8s.io/bundle-version` annotation off the installed `gatewayclasses` CRD and requires an exact major-and-minor match with the bundle it was built against: a patch difference inside 1.6 is accepted, any other minor is not. That bundle is supplied cluster-wide by the `gateway-api-crds` package, so it is a platform-wide version rather than anything this package can carry on its own: this package works only where `gateway-api-crds` ships 1.6.x, and nothing in the package's own wiring enforces that. Its `dependsOn: cozystack.gateway-api-crds` orders the install and carries no version constraint, so a satisfied dependency is not evidence of a satisfied requirement. Read the bundle off the cluster before enabling. On a cluster whose bundle is a different minor the class comes up `Accepted=True` next to `SupportedVersion=False`, reason `UnsupportedVersion`, with a message naming the minor it wants. Nothing gates serving on that condition, so such an install reads as usable and is not: the Gateway API CRDs carry structural schemas, so any field the controller writes that exists only in the newer bundle is pruned on write with no error recorded anywhere. Once bundle and controller agree the condition turns `True`, but only on that GatewayClass's next reconcile — the controller deliberately does not watch the CRDs, so a spec change, a periodic resync or a controller restart is what recomputes it.
- **Images are not mirrored into the Cozystack registry.** The controller and proxy images come from `ghcr.io/lexfrei`, pinned by tag and digest. Air-gapped installs mirror them; the chart itself is vendored into the repository, so nothing fetches it at runtime.
- **Coverage stops at the chart contract.** The package ships chart tests that pin what these values do to the vendored chart, and no end-to-end test here exercises a live tunnel. The hostname-ownership decision in particular cannot be pinned in this repository: it lives in an `internal/` package of the upstream module, which Go will not let another module import, and the controller resolves its Cloudflare account before it evaluates any route, so there is no credential-free path that reaches a verdict. Upstream drives that decision from one shared vector table through both of its enforcement layers; what the tests here pin is that these values are what select the layer, and that they still select it after a re-vendor.

## Upgrades

The vendored chart ships its CRDs in `crds/`, which Helm installs once and never touches again on upgrade. The PackageSource sets `upgradeCRDs: CreateReplace` for that reason: the controller has added kinds across minor releases, and without it a chart bump would land a new controller binary against the old CRD set.

## Removing the package

Taking the name back out of `bundles.enabledPackages` does not remove anything on its own, and deleting the Package on its own does not stick. Both are needed, and the order matters twice over.

The platform annotates every package it emits with `helm.sh/resource-policy: keep`, so dropping the name from `enabledPackages` stops the platform rendering the Package but deletes nothing: the Package stays, the HelmRelease it owns stays, and so do the controller and proxy Deployments, the cluster-wide ClusterRole and its binding, and the GatewayClass. That is deliberate — the platform leaves package deletion to an administrator rather than reaping workloads on a values edit. Left at that, a cluster that reads as disabled would still be running a controller that can read Secrets in every namespace and write Deployments in every namespace, so the platform refuses that state: while the Package exists on the cluster and the name is missing from `enabledPackages` or present in `disabledPackages`, the platform render fails with a message naming the Package. The `cozystack-platform` HelmRelease stays failed until the name is listed again or the Package is gone. The check runs a `lookup`, which returns nothing under `helm template` and dry-run, so it guards a live reconcile only.

Deleting the Package while the name is still listed does not stick either. `keep` blocks deletion, not creation, so the next `helm upgrade` of the platform — any Cozystack version bump, any platform values edit — renders the Package again. What comes back carries only what the platform renders, `controller.clusterDomain`; the `gatewayClassConfig.tunnelID` you set by hand died with the object you deleted. The chart then refuses to render, that HelmRelease fails permanently, and since cluster readiness sweeps every HelmRelease, the cluster reads not-ready until someone traces it back.

So, in this order:

1. Delete every Gateway on the `cloudflare-tunnel` class, and the routes attached to them. A tenant Gateway on the class goes by clearing `gatewayClass` on that Tenant (or setting `gateway: false`); then take `cloudflare-tunnel` out of `gateway.tenantSelectableClasses` and `gateway.edgeTerminatedClasses`, so no tenant can select it again.
2. Remove `cozystack.cloudflare-tunnel-gateway-controller` from `bundles.enabledPackages`. The platform HelmRelease now fails on the guard described above, and that is expected: the controller is still running and nothing has been deleted.
3. Delete the Package: `kubectl delete package.cozystack.io cozystack.cloudflare-tunnel-gateway-controller`. The HelmRelease carries an ownerReference back to the Package, so this garbage-collects the release and with it both Deployments, the Services, the RBAC, the GatewayClass and the cluster-scoped `GatewayClassConfig` the chart renders. The next platform reconcile finds no Package and renders cleanly. Deleting the Package before step 2, while the platform still emits it, lands in the state described above.

Step 1 has to precede step 3 for a second, unrelated reason. While any Gateway uses the class the controller keeps the `gateway-exists-finalizer.gateway.networking.k8s.io` finalizer on the GatewayClass, and the controller is the only thing that removes it. Delete the Package first and the GatewayClass is left in `Terminating` behind a finalizer nothing will clear — recovering from that means editing the finalizer off by hand.

Four things outlive the uninstall by design. Helm does not remove CRDs it installed from a chart's `crds/` directory, so the `GatewayClassConfig`, `GatewayConfig` and `ExternalBackend` kinds stay registered, and any objects of them that the release did not render — the per-Gateway `GatewayConfig`s and `ExternalBackend`s you wrote — stay with them; delete them separately if you want the API surface gone. The namespace `cozy-cloudflare-tunnel-gateway-controller` carries the same `keep` annotation and stays. The `cloudflare-tunnel-credentials` Secret in it was created by hand rather than by the release, so nothing in the teardown touches it — revoke the Cloudflare API token and delete the tunnel on Cloudflare's side too, or the credentials outlive the cluster that used them. And the config-API bearer token the controller mints for itself on first start, in a Secret named after the release with a `-proxy-auth-token` suffix, has no owning object at all, so it survives as well.

## See also

- [`packages/extra/gateway/README.md`](../packages/extra/gateway/README.md) — the Cilium-backed per-tenant Gateway, its cert modes, and the layered hostname security model.
- [Cloudflare Tunnel documentation](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) — creating a tunnel, connector tokens, and edge behaviour.
- [Gateway API](https://gateway-api.sigs.k8s.io/) — GatewayClass, Gateway and route semantics.
