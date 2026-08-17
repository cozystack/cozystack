# Publishing through a Cloudflare Tunnel

`cloudflare-tunnel-gateway-controller` is an optional system package that registers a second Gateway API implementation alongside the Cilium one. A Gateway on its class is published by Cloudflare: clients reach Cloudflare's edge, and the edge carries the request into the cluster over a Cloudflare Tunnel that the in-cluster data plane opens outbound. The Services the chart renders are `ClusterIP`, so nothing on this path asks the cluster for an address reachable from outside.

## When to reach for it

The Gateway a Cozystack tenant publishes through is backed by Cilium: `packages/extra/gateway` renders a `TenantGateway` whose `gatewayClassName` value defaults to `cilium`, and the cozystack-controller turns that into a `Gateway` plus whatever cert-manager `Issuer` and `Certificate` objects its cert mode calls for. That path wants two things from the outside world — an address clients can open a connection to, and an ACME challenge that completes against the cluster.

The tunnel class drops both for the hostnames it serves. Inbound connections are replaced by an outbound connection from the proxy pods to Cloudflare's edge, and TLS is terminated at the edge rather than on a Gateway listener. It fits a cluster behind NAT or CGNAT, a lab or home cluster with no routable prefix, a site whose firewall will not forward ports, or a deployment that wants Cloudflare's edge in front of a subset of hostnames.

It is the wrong tool when you need TLS passthrough or any non-HTTP protocol, when the certificate a client sees has to be one the cluster controls, or when a third party in the request path is unacceptable. GatewayClasses are cluster-scoped and each Gateway names the one it wants, so enabling this package does not move anything that is already published through Cilium.

## What has to exist on the Cloudflare side

1. A zone whose DNS is served by Cloudflare, covering the hostnames you intend to publish.
2. A Tunnel, created under **Zero Trust → Networks → Tunnels** with the `cloudflared` connector type. Keep both its **Tunnel ID** and its **tunnel token**.
3. An API token with the **Account → Cloudflare Tunnel → Edit** permission.

Give the controller a tunnel of its own. Its upstream documentation states that it assumes exclusive ownership of the tunnel configuration, performs a full synchronization on startup, and removes ingress rules that do not come from routes it manages — so a tunnel that also carries hand-written public hostnames, or one shared with another system, loses them.

The Cloudflare account ID is auto-detected when the API token has access to a single account. When it does not, supply it explicitly, either as an `account-id` key in the Secret below or through the chart's `gatewayClassConfig.accountId` value.

## The credentials Secret

Both planes read one Secret, named `cloudflare-tunnel-credentials`, in the package namespace `cozy-cloudflare-tunnel-gateway-controller`. The package pins the Secret name and the `tunnel-token` key, and a chart test holds those in place across re-vendoring; `api-token` is the controller's own default for the credentials key, which the package leaves unset:

| Key | Read by | Contents |
| --- | --- | --- |
| `api-token` | the controller, through `GatewayClassConfig.spec.cloudflareCredentialsSecretRef` | the Cloudflare API token |
| `tunnel-token` | the proxy, as the `TUNNEL_TOKEN` environment variable | the tunnel's connector token |
| `account-id` | the controller, optional | account ID, when auto-detection cannot pick one |

Create it before or shortly after enabling the package — the proxy pod cannot start without it:

```bash
kubectl create namespace cozy-cloudflare-tunnel-gateway-controller \
  --dry-run=client --output yaml | kubectl apply --filename -

kubectl --namespace cozy-cloudflare-tunnel-gateway-controller \
  create secret generic cloudflare-tunnel-credentials \
  --from-literal=api-token="$CF_API_TOKEN" \
  --from-literal=tunnel-token="$CF_TUNNEL_TOKEN"
```

## Enabling the package

The package is not in any bundle by default, because it cannot become Ready without operator input. Enabling it takes two edits, and the second one lands on an object that exists only after the first.

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

The name also has to stay out of `bundles.disabledPackages`: the platform helper that emits it checks that list first, so a name present in both is not emitted. That list is a veto on rendering, not an uninstall — see [Removing the package](#removing-the-package).

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

The controller writes the tunnel's CNAME target, `<tunnel-id>.cfargotunnel.com`, into the Gateway's `status.addresses`. Turning that into a DNS record needs an external-dns that watches Gateway API, which is not how Cozystack ships either of them: the system package `cozystack.external-dns` overrides no `sources` and so runs the upstream chart's defaults, `service` and `ingress`, with no value exposed to add more. The per-tenant `external-dns` application does have the switch — `gatewayAPI: true` adds the `gateway-httproute` and `gateway-tlsroute` sources — and it is `false` by default. Neither path adds `gateway-grpcroute`, so a hostname published only by a GRPCRoute gets no record from either. Failing all that, create the CNAME in the Cloudflare zone yourself, proxied.

Routes are `HTTPRoute` and `GRPCRoute`. A backend is normally a Service; the chart also installs an `ExternalBackend` CRD for pointing a route at an out-of-cluster HTTP(S) origin, which a Service of type `ExternalName` cannot express because it carries no scheme.

## Hostname ownership between tenants

By default every Gateway of this class shares one tunnel and one proxy pool, so the data plane picks a backend by hostname across all of them — a route claiming another tenant's hostname would be answered rather than ignored, which is not the case behind a per-tenant Cilium address.

Cozystack's own admission policy does not close that on its own. `cozystack-route-hostname-policy` binds route hostnames to the namespace's `namespace.cozystack.io/host` label, but it matches only `HTTPRoute` and `TLSRoute` in namespaces whose name starts with `tenant-`, it does not see `GRPCRoute`, and it admits a route that declares no hostnames at all.

So the package turns on the chart's own hostname-ownership layer, keyed on the same `namespace.cozystack.io/host` label and scoped to every namespace that carries it. On this class, a route in such a namespace is rejected by the controller unless it declares hostnames explicitly and each one equals the label value or is a subdomain of it. The chart's fail-fast admission half of that feature stays off on purpose: admission cannot resolve `parentRefs`, so it would also police routes bound to the Cilium Gateway in those same namespaces.

## Giving one Gateway its own tunnel

A Gateway can opt out of the shared data plane with a `GatewayConfig` (`cf.k8s.lex.la/v1alpha1`) referenced from its `spec.infrastructure.parametersRef`, in the same namespace. The controller then runs a proxy Deployment and a Cloudflare Tunnel dedicated to that Gateway. `spec.tunnelTokenSecretRef` is the only required field — the tunnel ID and account are parsed out of the connector token rather than configured separately. Reach for it when two sets of hostnames must not share a connector, or when one Gateway needs its own data-plane sizing.

Keep it an operator resource. A `GatewayConfig` also names the image for the Deployment the controller creates on its behalf, and the controller creates that Deployment with the cluster-wide grant described under Limitations. The tenant roles in `cozystack-basics` carry no access to the `cf.k8s.lex.la` group today, which is what keeps that out of tenant reach — widening a tenant's Gateway API access later should not quietly include this group.

## Limitations

- **HTTP and gRPC only.** A Cloudflare Tunnel carries HTTP to the origin and this controller implements `HTTPRoute` and `GRPCRoute`. There is no TLS passthrough and no raw TCP or UDP.
- **The tenant publishing Gateway is not a drop-in for this class.** `packages/extra/gateway` defaults `tlsPassthroughServices` to `api`, `vm-exportproxy` and `cdi-uploadproxy`, and each becomes a `protocol: TLS`, `mode: Passthrough` listener on the rendered Gateway — a shape this controller has nothing to map onto, since a Cloudflare Tunnel carries HTTP to the origin. One listener the controller will not accept holds the whole object at `Ready=False`, because cozystack-controller marks a TenantGateway Ready only when every listener reports both `Accepted` and `Programmed`. Emptying that list is necessary but not sufficient. The remaining port-443 listeners are still rendered `mode: Terminate` with a cert-manager `certificateRefs` entry, which is exactly the shape this class does not use, because the edge already terminates. And the port-443 `allowedRoutes.kinds` are pinned to `HTTPRoute` and `TLSRoute`, so a `GRPCRoute` cannot attach to a TenantGateway at all, on any class. Both `gatewayClassName` and `tlsPassthroughServices` are values on the `gateway` chart, and this package adds no platform-level surface for setting either, so the standalone Gateway shown above is the path it supports.
- **The HTTP-to-HTTPS redirect does not survive the ownership layer.** The redirect `HTTPRoute` that cozystack-controller renders next to a TenantGateway deliberately carries no hostnames, so it matches every host on the port-80 listener. Routes without explicit hostnames are exactly what the hostname-ownership layer rejects. Enforce HTTPS at the Cloudflare zone level instead.
- **Certificates are Cloudflare's.** The edge terminates TLS, so the certificate a client sees is the one Cloudflare serves for the zone, not one cert-manager issued in the cluster. Cloudflare's Universal SSL covers the zone apex and one label below it, so a two-label hostname such as `app.tenant.example.com` needs Advanced Certificate Manager or a separate zone per tenant apex.
- **The controller holds a cluster-wide write grant.** Its ClusterRole is attached by a ClusterRoleBinding, so every rule in it applies in every namespace. It reads Secrets everywhere (`get`, `list`, `watch`, plus `create` for the config-API token it generates), because it resolves credential, TLS and CA-bundle references wherever a Gateway or route points at them. It can also write Deployments, Services, NetworkPolicies and HorizontalPodAutoscalers in any namespace — `create`, `update` and `delete` on all four, plus `patch` on Deployments — which is how it materialises a dedicated data plane next to a Gateway that asks for one. And it holds `update` and `patch` on `gateways` and `gatewayclasses` themselves, not only on their `/status` subresources — it writes a finalizer onto a class in use — which reaches the spec of any Gateway in the cluster, including ones belonging to the Cilium class. Cluster-wide Deployment creation is the strongest of these — it places a pod in any namespace, under any ServiceAccount that already exists there — so weigh that, not only the Secret read, when deciding to enable the package.
- **One controller replica, no leader election.** The chart defaults are `replicaCount: 1` with leader election disabled and the package does not override them, so configuration pushes pause while the controller pod restarts. The proxy runs two replicas.
- **Tunnel dial is deferred on `protocol: auto`.** The proxy defaults to negotiating QUIC with an HTTP/2 fallback and waits for the controller's first config push before dialing, bounded at roughly 30 seconds — so a proxy on a route-less cluster can take that long to connect on each start. gRPC needs HTTP/2, since cloudflared drops HTTP trailers over QUIC and `grpc-status` is lost with them; `auto` upgrades at startup when a GRPCRoute already exists, so a GRPCRoute added later needs a proxy restart.
- **A namespace without the ownership label is out of scope, not denied.** The layer is scoped by `namespace.cozystack.io/host` existing, so a namespace that does not carry it is not policed rather than refused. Cozystack's own policy still covers `HTTPRoute` and `TLSRoute` in `tenant-`-prefixed namespaces and is fail-closed on a missing label there, but it does not see `GRPCRoute` — so a tenant namespace caught without its label, mid-creation or after the label is scrubbed, leaves a GRPCRoute free to claim any hostname on the shared tunnel.
- **Nothing is scraped by default.** Both ServiceMonitors the chart carries are gated on `serviceMonitor.enabled`, which is `false` and which the package does not change, so neither plane reaches the platform's Prometheus. Turning it on is not sufficient on its own: the proxy serves `/metrics` on its config API port, and the proxy's NetworkPolicy — which is on by default — admits that port only from the package namespace, so the monitoring namespace has to be added to `proxy.networkPolicy.ingress.from` in the same change.
- **Leave the controller's own NetworkPolicy off.** `networkPolicy.enabled` is `false` in the chart and the package does not change it. Turning it on confines controller egress to DNS, TCP 443 and 6443, and the Cloudflare address ranges — none of which covers the proxy's config API port, `8081` by default. Where the CNI enforces NetworkPolicy, the controller can then no longer push configuration to its data plane. The proxy's own policy is a separate value, on by default, and is not affected.
- **Gateway API bundle skew shows up as `SupportedVersion=False`.** The controller is built against Gateway API v1.6.1 and checks the `gateway.networking.k8s.io/bundle-version` annotation on the installed `gatewayclasses` CRD for an exact major-and-minor match: a patch difference within the same minor is accepted, a different minor is not. Cozystack ships the bundle at v1.5.1 today, so on a stock install the class comes up `Accepted=True` next to `SupportedVersion=False`, reason `UnsupportedVersion`, with a message naming the minor it wants — `Gateway API CRD bundle version v1.5.1 is not supported; controller requires 1.6.x`. The two conditions are set independently and nothing gates serving on the version one, so read `Accepted` for whether the class is usable — but treat the skew as a real signal rather than cosmetic noise: the Gateway API CRDs carry structural schemas, so any field the controller writes that exists only in the newer bundle is pruned by the older CRD on write, with no error anywhere. When the bundle and the controller do agree on a minor the condition turns `True`, but only on that GatewayClass's next reconcile — the controller deliberately does not watch the CRDs, so a spec change, a periodic resync or a controller restart is what recomputes it.
- **Images are not mirrored into the Cozystack registry.** The controller and proxy images come from `ghcr.io/lexfrei`, pinned by tag and digest. Air-gapped installs mirror them; the chart itself is vendored into the repository, so nothing fetches it at runtime.
- **Coverage is template-level.** The package ships chart tests that pin its wiring; there is no end-to-end test in this repository that exercises a live tunnel.

## Upgrades

The vendored chart ships its CRDs in `crds/`, which Helm installs once and never touches again on upgrade. The PackageSource sets `upgradeCRDs: CreateReplace` for that reason: the controller has added kinds across minor releases, and without it a chart bump would land a new controller binary against the old CRD set.

## Removing the package

Taking the name back out of `bundles.enabledPackages` does not remove anything on its own, and deleting the Package on its own does not stick. Both are needed, and the order matters twice over.

The platform annotates every package it emits with `helm.sh/resource-policy: keep`, so dropping the name from `enabledPackages` stops the platform rendering the Package but deletes nothing: the Package stays, the HelmRelease it owns stays, and so do the controller and proxy Deployments, the cluster-wide ClusterRole and its binding, and the GatewayClass. That is deliberate — the platform leaves package deletion to an administrator rather than reaping workloads on a values edit — but it means a cluster that reads as disabled is still running a controller that can read Secrets in every namespace and write Deployments in every namespace.

Deleting the Package while the name is still listed does not stick either. `keep` blocks deletion, not creation, so the next `helm upgrade` of the platform — any Cozystack version bump, any platform values edit — renders the Package again. What comes back carries only what the platform renders, `controller.clusterDomain`; the `gatewayClassConfig.tunnelID` you set by hand died with the object you deleted. The chart then refuses to render, that HelmRelease fails permanently, and since cluster readiness sweeps every HelmRelease, the cluster reads not-ready until someone traces it back.

So, in this order:

1. Delete every Gateway on the `cloudflare-tunnel` class, and the routes attached to them.
2. Remove `cozystack.cloudflare-tunnel-gateway-controller` from `bundles.enabledPackages`, so the platform stops rendering the Package.
3. Wait for the `cozystack-platform` HelmRelease to finish reconciling step 2, then delete the Package: `kubectl delete package.cozystack.io cozystack.cloudflare-tunnel-gateway-controller`. The HelmRelease carries an ownerReference back to the Package, so this garbage-collects the release and with it both Deployments, the Services, the RBAC and the GatewayClass. Deleting it while the platform is still emitting it lands in the state described above.

Step 1 has to precede step 3 for a second, unrelated reason. While any Gateway uses the class the controller keeps the `gateway-exists-finalizer.gateway.networking.k8s.io` finalizer on the GatewayClass, and the controller is the only thing that removes it. Delete the Package first and the GatewayClass is left in `Terminating` behind a finalizer nothing will clear — recovering from that means editing the finalizer off by hand.

Three things outlive the uninstall by design. Helm does not remove CRDs it installed from a chart's `crds/` directory, so the `GatewayClassConfig`, `GatewayConfig` and `ExternalBackend` kinds stay registered along with any objects of those kinds; delete them separately if you want the API surface gone. The namespace `cozy-cloudflare-tunnel-gateway-controller` carries the same `keep` annotation and stays. And the `cloudflare-tunnel-credentials` Secret in it was created by hand rather than by the release, so nothing in the teardown touches it — revoke the Cloudflare API token and delete the tunnel on Cloudflare's side too, or the credentials outlive the cluster that used them.

## See also

- [`packages/extra/gateway/README.md`](../packages/extra/gateway/README.md) — the Cilium-backed per-tenant Gateway, its cert modes, and the layered hostname security model.
- [Cloudflare Tunnel documentation](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) — creating a tunnel, connector tokens, and edge behaviour.
- [Gateway API](https://gateway-api.sigs.k8s.io/) — GatewayClass, Gateway and route semantics.
