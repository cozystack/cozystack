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
              claim: preferred_username
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

## Users and RBAC

`users[]` is a flat list. Each entry produces a single
`ClusterRoleBinding` inside the tenant cluster, labelled
`app.kubernetes.io/managed-by=cozystack-oidc` and
`app.kubernetes.io/instance=<release>`. The CRB name is a
deterministic hash of `<release>:<email>`, so the same email always
maps to the same binding.

| `role:` | `ClusterRole` bound |
| --- | --- |
| `admin` | `cluster-admin` |
| `view` | `view` |

The CRB `User:` subject is the literal `email` value; it must match the
`preferred_username` claim emitted by the issuer. In the platform `cozy`
realm `preferred_username` defaults to the user's email, so the typical
case "just works". For `CustomConfig`, ensure your issuer's
`preferred_username` claim matches the value you list here.

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

The operator hands them the OIDC kubeconfig:

```bash
kubectl --namespace tenant-acme get secret prod-oidc-kubeconfig \
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
