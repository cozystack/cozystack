# OIDC for tenant Kubernetes clusters (Phase 1)

Cozystack tenant Kubernetes clusters opt in to OIDC authentication on their
kube-apiserver through a flat selector on the `Kubernetes` CR. This document
covers the operator-facing surface: what the modes mean, what the chart
provisions on either end, and how to give a user kubectl access.

The architectural rationale lives in
[cozystack/community#24](https://github.com/cozystack/community/pull/24) —
in particular why per-tenant Keycloak realms are deliberately deferred to
Phase 2.

## Selector

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: prod
  namespace: tenant-acme
spec:
  oidc:
    mode: System        # System | CustomConfig | None (default)
    users:
      - email: alice@acme.example
        role: admin     # admin → cluster-admin
      - email: bob@acme.example
        role: view      # view → view
```

Three modes:

- **None** — the only user-facing path is the static
  `<release>-admin-kubeconfig` Secret (the Kamaji-minted `super-admin.svc`
  kubeconfig). This is the default; existing clusters render byte-identical
  to before.
- **System** — the apiserver trusts the platform `cozy` Keycloak realm via a
  per-cluster public client. Authenticates the users already in `cozy` (the
  realm cozystack ships with). Zero-config default.
- **CustomConfig** — the apiserver trusts a tenant-supplied OIDC issuer
  directly. `cozy` is not in the path. Use for BYO IdPs (Okta, Auth0, a
  customer's own Keycloak).

The `users[]` map is independent of the mode and drives per-user
`ClusterRoleBinding`s inside the tenant cluster.

## System mode

What the chart provisions, in the management cluster:

1. **KeycloakClient** named `<namespace>-<release>` in the `cozy` realm —
   `public: true`, PKCE required, redirect URIs locked to `localhost:8000`
   and `localhost:18000` (the kubelogin / `kubectl oidc-login` defaults).
2. **KeycloakClientScope** named `<namespace>-<release>-audience` carrying
   an `oidc-audience-mapper` that pins `id_token.aud` to the per-cluster
   clientId. This is the per-cluster isolation primitive: a token minted
   for cluster A is rejected by cluster B's apiserver.
3. A **Secret** `<release>-oidc-authn-config` carrying a structured
   `apiserver.config.k8s.io/v1beta1` `AuthenticationConfiguration` with
   the cozy realm issuer and the per-cluster audience.
4. A `--authentication-config=` flag, mount, and volume on the
   `KamajiControlPlane` referencing the Secret above.
5. A **bootstrap Job** (Helm `post-install,post-upgrade` hook) that
   applies one `ClusterRoleBinding` per `users[]` entry inside the
   tenant cluster and writes a ready-to-use OIDC kubeconfig Secret on
   the management side (see below).
6. A `<release>-oidc-kubeconfig` Secret in the tenant namespace carrying
   a kubeconfig with a `kubectl oidc-login` exec block, exposed to the
   dashboard via `packages/system/kubernetes-rd`.

The structured authentication-config form (rather than the legacy
`--oidc-*` flags) is intentional: it accepts multiple issuers in a list,
inline private-CA PEM, and future Phase-2 issuers extend the same Secret
instead of fighting the chart shape.

## CustomConfig mode

The tenant supplies the entire `AuthenticationConfiguration`. Two paths:

```yaml
spec:
  oidc:
    mode: CustomConfig
    customConfig:
      config: |
        apiVersion: apiserver.config.k8s.io/v1beta1
        kind: AuthenticationConfiguration
        jwt:
        - issuer:
            url: https://idp.acme.example
            certificateAuthority: |
              -----BEGIN CERTIFICATE-----
              ...
              -----END CERTIFICATE-----
            audiences:
            - cozystack-prod
          claimMappings:
            username:
              claim: email
              prefix: ""
            groups:
              claim: groups
              prefix: ""
    users:
      - email: alice@acme.example
        role: admin
```

…or via an out-of-band Secret the operator has already created in the
tenant namespace:

```yaml
spec:
  oidc:
    mode: CustomConfig
    customConfig:
      secretRef:
        name: acme-byo-authn-config       # has key config.yaml
```

`config` and `secretRef.name` are mutually exclusive; the chart fails the
render if both — or neither — are set.

No Keycloak objects land in `cozy`; the chart writes only the
AuthenticationConfiguration Secret (or mounts the operator's) and the
RBAC Job. The `<release>-oidc-kubeconfig` helper Secret is NOT written
in CustomConfig mode: the issuer and clientId are inside the
operator-supplied config and are not knowable to the chart. Distribute
the OIDC kubeconfig out-of-band.

## Aggregated API servers

Authenticating a user is only half of the path. A call that lands on an aggregated API server — an `APIService` backed by an extension server rather than by the core apiserver — reaches it over the aggregation layer, which forwards the caller's identity in request headers. The UID travels in `X-Remote-Uid`, and the extension server trusts that header only when the tenant kube-apiserver has published it under `requestheader-uid-headers` in the `extension-apiserver-authentication` ConfigMap. Without it the UID is dropped in transit and the call fails on an empty user UID, even though `kubectl` against the core apiserver works fine.

Carrying the UID across that hop is a property of the aggregation layer, not of OIDC — a user authenticated by any means loses their UID the same way. The chart nonetheless renders these flags only when `spec.oidc.mode` is not `None`, which keeps the blast radius to clusters opting into a new feature. A cluster left on `mode: None` therefore still drops UIDs at the aggregation layer, including for ServiceAccount tokens, which carry a UID regardless of OIDC.

Whenever `spec.oidc.mode` is not `None`, the chart renders the flags that publish it, alongside `--authentication-config`:

| `spec.version` | rendered |
| --- | --- |
| `v1.31` | nothing — the flag does not exist upstream, and an unknown flag stops the apiserver from starting |
| `v1.32` | `--requestheader-uid-headers=X-Remote-Uid` plus `--feature-gates=RemoteRequestHeaderUID=true` |
| `v1.33` and newer | `--requestheader-uid-headers=X-Remote-Uid` |

The gate is rendered on `v1.32` only, where `RemoteRequestHeaderUID` is still alpha and the apiserver rejects the flag while the gate is off. From `v1.33` the gate is beta and on by default, so rendering it would buy nothing and would collide with a gate list of your own.

Adding either flag through `controlPlane.apiServer.extraArgs` by hand is no longer necessary, and an existing cluster that does keeps working: the tenant control plane holds a single entry per flag name, so the chart skips its own copy when your entry already carries `X-Remote-Uid` or already enables the gate.

Where your entry and the chart cannot both be satisfied, the chart refuses to render and the message names the fix. That covers a uid-headers list of your own that omits `X-Remote-Uid`, a `v1.32` gate entry that leaves `RemoteRequestHeaderUID` out — the chart needs that flag for itself there, and the control plane keeps only one entry per flag — and a `v1.33`-and-newer gate entry that sets `RemoteRequestHeaderUID=false`. The gate is beta-and-on from `v1.33` but never locked to its default, so an explicit `=false` genuinely turns it off and genuinely makes the header flag fatal.

Failing is deliberate rather than quietly skipping the flag. Skipping would hand you back the exact problem this feature removes: OIDC on, no error anywhere, and `user UID is empty` the first time something calls an aggregated API. A `HelmRelease` that goes red with an actionable message is the better failure.

This is a breaking change on every version the chart renders the flag on, and worth stating plainly. On `v1.32` a cluster that already sets `--feature-gates` for something unrelated rendered and booted before, and will now refuse to render until you merge `RemoteRequestHeaderUID=true` into that entry. From `v1.33` the same applies more narrowly: a cluster that switches the gate off, with `RemoteRequestHeaderUID=false` or a blanket `AllBeta=false`, also rendered and booted before — the chart added no header flag, so a disabled gate was harmless — and now fails to render. In both cases the error message is the migration instruction. Setups whose gate is genuinely already on are left alone, whether you named it explicitly or switched it on with a blanket `AllAlpha=true`.

The gate check reads your entry the way the apiserver does: one `Name=value` element at a time, spaces trimmed on both sides, and the value resolved the way `strconv.ParseBool` resolves it, so `1`, `t` and `T` mean the same as `true` and `0`, `f` and `F` the same as `false`. Blanket settings count too, because the apiserver applies them to every gate you have not named explicitly. On `v1.32` a `--feature-gates=AllAlpha=true` genuinely switches `RemoteRequestHeaderUID` on — it is an alpha gate there — so the chart treats it as yours and leaves it alone. From `v1.33` the mirror image applies: `AllBeta=false` genuinely switches it off, and is rejected. Naming the gate wins either way — `AllAlpha=true,RemoteRequestHeaderUID=false` is still rejected on `v1.32`, and `AllBeta=false,RemoteRequestHeaderUID=true` is still accepted on `v1.33` — because an explicit entry beats a blanket one whichever order they appear in.

The uid-headers list is read without trimming, because the apiserver does not trim it either: `--requestheader-uid-headers=X-Remote-Uid , Other` does not contain `X-Remote-Uid` as far as the apiserver is concerned, and the chart treats it the same way rather than accepting a value the apiserver will reject.

On `v1.31` the chart renders neither flag, and the two UID guards above do not run, so a uid-headers or `--feature-gates` entry of yours passes through untouched. The unrelated `--oidc-*` and `--authentication-config` collision guards still apply there as everywhere else. Aggregated API servers cannot see the caller's UID on `v1.31`; move the cluster to `v1.32` or newer if you need them to. Writing `--requestheader-uid-headers` yourself does not work around it — the flag does not exist on `v1.31`, and an unknown flag stops the apiserver from starting.

## Users and RBAC

`users[]` is a flat list. Each entry produces a single
`ClusterRoleBinding` inside the tenant cluster, labelled
`app.kubernetes.io/managed-by=cozystack-oidc` and
`app.kubernetes.io/instance=<release>`. The CRB name is a
deterministic hash of `<release>-<email>`, so the same email always
maps to the same binding.

| `role:` | `ClusterRole` bound |
| --- | --- |
| `admin` | `cluster-admin` |
| `view` | `view` |

The CRB `User:` subject is the literal `email` value; it must match the
`email` claim emitted by the issuer. The chart-generated OIDC
kubeconfig requests `--oidc-extra-scope=email` in the
`kubectl oidc-login` exec block, so the token includes `email`
regardless of whether the per-cluster client lists `email` in its
default client scopes. The `email` scope itself is a built-in OIDC
scope available in every conformant issuer (Keycloak included), so no
extra realm-side configuration is required for `System`. For
`CustomConfig`, verify your BYO issuer emits the `email` claim when
the client requests the `email` scope; every conformant OIDC provider
does.

Toggling a user out of `users[]` revokes their access on the next chart
reconcile — the bootstrap Job prunes any CRBs labelled by the release
that are no longer in the desired list.

The static admin kubeconfig stays as the documented break-glass path
regardless of mode.

## How a user logs in (System mode)

The user installs `kubectl oidc-login` once:

```bash
kubectl krew install oidc-login
```

The operator hands them the OIDC kubeconfig. The Secret name follows
the Helm release pattern `kubernetes-<cluster>-oidc-kubeconfig` (the
cozystack-api prefixes the cluster name with `kubernetes-` when it
materialises the `HelmRelease`):

```bash
kubectl --namespace tenant-acme get secret kubernetes-prod-oidc-kubeconfig \
  --output=jsonpath='{.data.kubeconfig}' | base64 -d > prod.kubeconfig
```

First `kubectl --kubeconfig prod.kubeconfig get …` call triggers the
Keycloak browser flow on localhost:8000 (then 18000 as fallback) and
caches the token locally. Subsequent calls are silent until the token
expires.

## Failure modes

- **`mode: System` without the platform-level OIDC feature** — chart
  render hard-fails: `spec.oidc.mode: System requires the platform-level
  OIDC feature (authentication.oidc.enabled) — enable it in
  cozystack-values, or use mode: CustomConfig for a tenant-supplied
  issuer.`
- **`CustomConfig` with an unreachable issuer or wrong claim mappings** —
  the apiserver rejects tokens at request time; the admin kubeconfig
  keeps working as break-glass.
- **`emailVerified` on Keycloak users is a prescriptive requirement,
  not a chart-enforced one.** The chart emits a
  `claimValidationRules` entry keyed on `groups` (see next bullet),
  but not one keyed on `email_verified`. The layered guarantees you
  rely on instead:
  1. Provision users with `emailVerified: true` (via `KeycloakRealmUser`
     or the Keycloak UI's email-verify flow) so no unverified identity
     ever holds the email you name in `users[].email`.
  2. The `cozy` realm keeps Keycloak's default `duplicateEmails: false`,
     so a second account cannot claim an already-registered address to
     impersonate an existing operator.
  3. As a k8s side-effect, if the issuer explicitly emits
     `email_verified: false` on a token the apiserver rejects it with
     `oidc: email not verified` — but a *missing* claim is treated as
     verified.
  Adding a CEL `claimValidationRules` entry
  (`!has(claims.email_verified) || claims.email_verified == true`) to
  the rendered System-mode config would elevate item 3 to a hard gate;
  that is a reasonable follow-up hardening but is out of scope for
  Phase 1.

- **Cross-tenant isolation gate.** The rendered
  `AuthenticationConfiguration` carries a CEL `claimValidationRules`
  entry on the `groups` claim that rejects any token whose caller is
  not a member of at least one of this tenant's four Keycloak groups:

  ```
  <namespace>-view <namespace>-use <namespace>-admin <namespace>-super-admin
  ```

  The tenant chart (`packages/apps/tenant/templates/keycloakgroups.yaml`)
  provisions these groups in the shared `cozy` realm. Without this
  gate, any authenticated cozy-realm user could point `kubectl
  oidc-login` at another tenant's cluster, land at
  `system:authenticated`, and enumerate the discovery surface
  (`kubectl api-resources`, OpenAPI schemata) — RBAC would default-deny
  named-resource access, but discovery still leaks. Symmetric with the
  Grafana-side `allowed_groups` gate on the Monitoring chart's
  `auth.generic_oauth` block (see `docs/oidc-grafana.md`). CustomConfig
  mode does NOT get this gate injected — the tenant's own
  AuthenticationConfiguration is authoritative and the tenant is
  responsible for their own claim-side guards.
- **`kubectl` without the `oidc-login` plugin** — the exec block errors
  out client-side; install the plugin.

## What is NOT in Phase 1

- **Per-tenant Keycloak realms.** Phase 2 candidate; tracked in
  cozystack/community#24 (Option B — retained as the strongest isolation
  answer; Option A is Keycloak Organizations).
- **Federating an external IdP into `cozy`.** Out of scope.
- **Cross-cluster SSO inside one tenant.** Each cluster has its own
  audience — that is the per-cluster isolation primitive.
- **Custom credential plugin / RFC 8693 token exchange.** Possible future
  optimisation; not required for the per-cluster client + audience model.
