# GatewayClass selection and TLS termination for tenant Gateways

A tenant that owns a Gateway (`tenant.spec.gateway: true`) gets one `Gateway` object bound to one `GatewayClass`. Which class that is decides who programs the dataplane and, with it, whether TLS is terminated by the Gateway itself or by something in front of it. This page covers the operator-facing surface: the platform values that bound the choice, the tenant field that makes it, what each resulting certificate mode implies, what a move between classes does to certificates and Secrets, and how the mechanism fails when the configured names and the installed classes disagree.

The chart-level reference for the per-tenant Gateway — inheritance, listener shapes, the security model, ACME rate limits — is [`packages/extra/gateway/README.md`](../packages/extra/gateway/README.md).

## Platform values

Three keys under `gateway` in the platform values bound which class a tenant Gateway can land on:

```yaml
gateway:
  enabled: true
  className: cilium
  tenantSelectableClasses: []
  edgeTerminatedClasses: []
```

- `className` — the class every tenant Gateway uses unless the tenant names another one. Defaults to the Cilium class the platform ships.
- `tenantSelectableClasses` — the additional class names a tenant may name for itself. Empty (the default) means tenants cannot choose at all.
- `edgeTerminatedClasses` — the class names whose provider terminates TLS upstream of the Gateway. Membership in this list is the only thing that puts a tenant into edge mode.

All three reach the per-tenant gateway chart through the platform values channel, as `_cluster.gateway-class-name`, `_cluster.gateway-tenant-classes` and `_cluster.gateway-edge-classes`. The two lists are joined with commas on the way, so a name containing a comma is rejected at platform render time rather than silently split into two names.

`gateway.enabled` is a separate switch and not a prerequisite for any of this: it moves the platform's own published services from Ingress onto Gateway API and points the ACME `ClusterIssuer`s' HTTP-01 solver at the publishing tenant's Gateway. A tenant Gateway is created from `tenant.spec.gateway` alone.

## How a tenant picks a class

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: acme
  namespace: tenant-root
spec:
  gateway: true
  gatewayClass: edge-tunnel
```

`edge-tunnel` throughout this page stands for the name of a GatewayClass whose controller terminates TLS outside the cluster and forwards cleartext to the Gateway. Substitute the name your own edge-terminating controller registers.

The field is read only for a tenant that owns a Gateway. A tenant that inherits — `gateway` left unset or set to `false` — publishes through the nearest ancestor that owns one and runs on that ancestor's class, so its own `gatewayClass` has no effect. Leaving `gatewayClass` empty selects `gateway.className`.

The set a tenant may name is `gateway.tenantSelectableClasses` plus the current `gateway.className`, so a tenant may always name the default explicitly. Anything else fails that tenant's own gateway release at render time, with a message naming the class and the allowed set, and reaches no other tenant. The allowlist exists because `Tenant` is a tenant-writable object while a `GatewayClass` is cluster-scoped: without it a tenant could attach its Gateway to any class the cluster registers, including one an admin keeps for internal-only traffic.

## What the class decides: the certificate mode

`TenantGateway.spec.certMode` is derived, never written by the tenant. The gateway chart resolves it in this order, first match winning:

| Condition | `certMode` | What the controller issues |
| --- | --- | --- |
| the resolved class is in `gateway.edgeTerminatedClasses` | `edge` | nothing |
| `publishing.certificates.wildcardSecretName` is set | `existingSecret` | nothing; the operator's Secret is referenced |
| `publishing.certificates.solver: dns01` | `dns01` | one wildcard `Certificate` per tenant |
| otherwise | `http01` | one `Certificate` per published hostname |

Edge therefore wins over an operator-supplied wildcard Secret, which wins over the solver. In edge mode the solver, DNS-01 provider and issuer-name inputs are not read at all, so an unsupported value in any of them does not fail the tenant's release. `existingSecret` skips the same validations; only `http01` and `dns01` reject a bad solver, provider or issuer name.

## What edge mode changes on the Gateway

- The Gateway serves port 80 only. Its listeners are `edge` for `*.<apex>`, `edge-apex` for `<apex>`, and one `edge-child-*` per inheriting child tenant carrying that child's `*.<child-apex>`. There is no port-443 listener, and no hostname-less port-80 listener either.
- No per-tenant ACME `Issuer` and no `Certificate` are created.
- No http-to-https redirect `HTTPRoute` is created. The edge is expected to perform that redirect.
- `tlsPassthroughServices` is not written onto the `TenantGateway` and no passthrough listener is rendered, so the tenant Kubernetes API, VM export and CDI upload endpoints are not published through such a Gateway. The packages that own those endpoints still create their `TLSRoute` objects, which then match no listener on that Gateway.
- Application `HTTPRoute`s attach by hostname exactly as in the other modes — the edge listeners carry the same namespace selector the HTTPS listeners carry elsewhere.

That last point has a blast radius worth stating on its own. Setting `gateway.className` to an edge-terminated class puts the publishing tenant on it too, and then the endpoints that go unpublished are the platform's own: the `TLSRoute` for `api.<root-host>` is still rendered and still pinned to a `tls-api` listener, and an edge Gateway has no such listener, so the tenant Kubernetes API stops being served — VM export and CDI upload go the same way. Nothing fails at render time and nothing reports it. Putting only child tenants on the edge class avoids this, because the platform's routes keep pointing at the publishing tenant's Gateway.

Listing a class in `gateway.edgeTerminatedClasses` is an assertion about that class, and nothing verifies it. Put the bundled `cilium` class in the list and its Gateways serve every application hostname over plain HTTP on a public address, with no redirect and no certificate. Only list a class whose provider really does terminate TLS in front of the Gateway.

## Moving a tenant between classes

Onto an edge-terminated class, on the next reconcile:

| Object | What happens |
| --- | --- |
| the per-tenant ACME `Issuer` | deleted |
| per-listener and wildcard `Certificate` objects | deleted |
| the TLS Secrets those `Certificate`s issued | left in the namespace, unreferenced |
| the http-to-https redirect `HTTPRoute` | deleted |
| the replica of `publishing.certificates.wildcardSecretName` | deleted from that tenant's namespace |
| route status conditions the controller wrote earlier | left as they are |

Every deletion is guarded by ownership: an object of the same name that the controller did not create is left in place.

The TLS Secrets survive because cert-manager ships here with `enableCertificateOwnerRef: false`, so a `Certificate` does not own the Secret it issued into. That is how every mode transition behaves and is not something edge introduces. Removing the leftovers is an operator action.

The wildcard replica is withdrawn because the namespace stops counting as a TLS termination point. Three things keep it anyway: a namespace that also owns an ingress controller still terminates locally; a namespace holding more than one `TenantGateway` keeps the replica while any of them is not on an edge-terminated class; and a namespace whose `TenantGateway` objects cannot be listed, or which carries the Gateway owner label but has no `TenantGateway` yet, keeps it as well, so that a transient read failure never withdraws a key a Gateway may still be serving.

Moving back off an edge-terminated class restores whatever the new mode calls for: the ACME `Issuer` and its `Certificate`s under `http01` or `dns01`, the redirect route under any non-edge mode, and the wildcard replica whenever `publishing.certificates.wildcardSecretName` is set. `existingSecret` mints no `Issuer` and no `Certificate` of its own, so a move from `edge` into it brings back the replica and the redirect route and nothing else.

The two halves of the move are not ordered against each other. The wildcard-secret controller watches the `TenantGateway` and prunes the replica as soon as `certMode` changes, while the `Gateway` itself is re-rendered when the tenant's HelmRelease reconciles, and the class controller reprograms after that. A tenant leaving `existingSecret` can therefore be briefly serving HTTPS listeners whose Secret has already gone.

## When the configured names and the installed classes disagree

Nothing in this mechanism checks that a class name corresponds to a `GatewayClass` the cluster actually has. Run `kubectl get gatewayclass` before putting a name into any of the three values.

- **A name in `className` or `tenantSelectableClasses` that matches no installed class.** The tenant's release renders and the `Gateway` object is created, but no controller claims it, so it gets no address and no listener status. Nothing fails at render time; the symptom is the `TenantGateway` holding `Ready=False` with reason `GatewayNotAccepted`.
- **A misspelled name in `edgeTerminatedClasses`.** It matches no class, so edge mode never engages for it and nothing says so. A tenant whose TLS really is terminated upstream stays in whichever mode the solver and wildcard-secret settings select, and its Gateway keeps terminating TLS itself.
- **A class removed from `tenantSelectableClasses` while a tenant still names it.** That tenant's gateway release starts failing to render. Other tenants are unaffected.
- **`className` changed while a tenant pins the outgoing name.** The allowed set is the *current* default plus `tenantSelectableClasses`, so the pin stops being valid and that tenant's gateway release fails. Add the outgoing class to `tenantSelectableClasses` before changing the default, or update the tenant first.
- **The publishing tenant selecting a class that terminates TLS differently from `className`.** Refused at render time, in both directions. The cluster-wide ACME `ClusterIssuer`s point their HTTP-01 solver at the publishing tenant's Gateway, and whether that solver pins `sectionName: http` is decided from `gateway.className` alone — a tenant's own choice does not travel on that channel. A publishing tenant that moved itself onto an edge class would keep a solver pinned to the `http` listener its Gateway no longer renders, and every HTTP-01 certificate in the cluster would stop issuing with nothing in any status naming the cause. Change `gateway.className` instead. The publishing tenant is the namespace named by `publishing.ingressName`, `tenant-root` by default.
- **A comma or a non-string entry in either list.** The platform chart fails to render, naming the entry.

## Checking what a tenant landed on

```bash
kubectl get gatewayclass

kubectl --namespace tenant-acme get tenantgateway cozystack \
  --output jsonpath='{.spec.gatewayClassName}{"\t"}{.spec.certMode}{"\n"}'

kubectl --namespace tenant-acme get tenantgateway cozystack \
  --output jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{"\t"}{.reason}{"\n"}{end}'

kubectl --namespace tenant-acme get gateway cozystack \
  --output jsonpath='{range .spec.listeners[*]}{.name}{"\t"}{.port}{"\t"}{.hostname}{"\n"}{end}'

kubectl --namespace tenant-acme get certificate,issuer
```

A tenant on an edge-terminated class reports `certMode: edge`, shows port-80 listeners only, and has no `Certificate` or `Issuer` that this controller created. Leftover TLS Secrets from an earlier mode are not shown by the last command — list Secrets of type `kubernetes.io/tls` in the namespace to find those.
