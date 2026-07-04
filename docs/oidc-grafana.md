# OIDC for the Grafana instance (Phase 1)

Cozystack ships one Grafana codebase deployed twice: as
`monitoring-system` in the platform's `cozy-monitoring` namespace and as
`monitoring` in every tenant namespace. Both instances opt in to OIDC
authentication through the same flat selector on the `Monitoring` CR.
This document covers the operator-facing surface: what the modes mean,
what the chart provisions, and how a user signs in.

The architectural rationale lives in
[cozystack/community#24](https://github.com/cozystack/community/pull/24)
— in particular why per-tenant Keycloak realms are deliberately
deferred to Phase 2. This Grafana integration follows the same shape
as the tenant kube-apiserver's Phase 1
([cozystack/cozystack#3044](https://github.com/cozystack/cozystack/pull/3044)),
adapted for a server-side (confidential) OAuth client instead of a
public kubectl one.

## Selector

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Monitoring
metadata:
  name: monitoring
  namespace: tenant-acme
spec:
  oidc:
    mode: System        # System | CustomConfig | None (default)
```

Three modes:

- **None** — the only user-facing path is the `grafana-admin-password`
  Secret, exposed to the tenant through
  `packages/system/monitoring-rd`. This is the default; existing
  instances render byte-identical to before.
- **System** — the Grafana instance trusts the platform `cozy` Keycloak
  realm via a per-instance confidential client. Users are the ones a
  platform admin already provisioned in `cozy`; the tenant does not
  manage a directory of its own.
- **CustomConfig** — Grafana trusts a tenant-supplied OIDC issuer
  directly. `cozy` is not in the path. Use for BYO IdPs (Okta, Auth0,
  a customer's own Keycloak).

The `grafana-admin-password` Secret stays a documented break-glass
path in every mode; `disable_login_form` is not flipped by this
selector. Hardening that further (e.g. locking the form off in
`System` mode) is a follow-up.

## System mode

What the chart provisions, in the release namespace of the Monitoring
CR:

1. **KeycloakClient** named `<namespace>-<release>` in the `cozy`
   realm — `public: false`, `directAccess: false`. The `secret` field
   points at a chart-owned Kubernetes Secret via the EDP-Keycloak
   `$<secret>:<key>` syntax so the operator provisions the same
   confidential secret to Keycloak that Grafana reads at runtime.
   `redirectUris` is locked to
   `https://grafana.<host>/login/generic_oauth`.
2. **KeycloakClientScope** named `<namespace>-<release>-audience`
   carrying an `oidc-audience-mapper` that pins `id_token.aud` to the
   per-instance clientId. This is the per-instance isolation
   primitive: a token minted for one Monitoring release is rejected
   by another's Grafana.
3. **Three `KeycloakRealmGroup`s** named
   `<namespace>-<release>-{admin,editor,viewer}`. The groups
   themselves are chart-owned; their membership is not — a platform
   operator adds users to them through the Keycloak UI or a
   `KeycloakRealmUser` CR. See the "Users and RBAC" section.
4. A **Secret** `<release>-oidc-client` carrying a random 32-char
   `client-secret`. Generated on first install (`lookup` + random
   fallback, same pattern as `packages/system/dashboard`), preserved
   across upgrades.
5. The Grafana CR's `spec.config.auth.generic_oauth` section wired to
   the cozy realm issuer + the per-instance audience scope +
   `role_attribute_path` mapping the three groups above to Grafana's
   `Admin` / `Editor` / `Viewer` roles.
6. A `GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET` env on the Grafana
   Deployment sourced from the Secret in step 4.

Server-level `GrafanaAdmin` promotion is opt-in via
`spec.oidc.grafanaAdmin: true`. When set, an `<clientId>-admin` group
member is auto-promoted to `GrafanaAdmin` (via
`allow_assign_grafana_admin: true` in the rendered `[auth.generic_oauth]`
block). The platform bundle
(`packages/core/platform/templates/bundles/system.yaml`) flips it on for
the platform Grafana release via
`Package.spec.components["monitoring-system"].values.oidc.grafanaAdmin: true`;
tenant Grafana releases inherit the chart default `false` and stay at
org-level Admin.

## CustomConfig mode

The tenant supplies the entire `[auth.generic_oauth]` payload. Two
paths:

```yaml
spec:
  oidc:
    mode: CustomConfig
    customConfig:
      config:
        client_id: my-grafana
        client_secret: xxxxxxxx
        auth_url: https://idp.acme.example/protocol/openid-connect/auth
        token_url: https://idp.acme.example/protocol/openid-connect/token
        api_url: https://idp.acme.example/protocol/openid-connect/userinfo
        scopes: openid email profile groups
        role_attribute_path: "contains(groups[*], 'grafana-admins') && 'Admin' || 'Viewer'"
```

…or via an existing Secret in the tenant namespace whose `auth.ini`
key holds a ready-made `[auth.generic_oauth]` fragment:

```yaml
spec:
  oidc:
    mode: CustomConfig
    customConfig:
      secretRef:
        name: acme-byo-grafana-auth
```

Setting both `config` and `secretRef.name` (or neither) fails the
render. In `CustomConfig` mode no Keycloak objects are provisioned in
`cozy` and no chart-owned client-secret Secret is created — the
operator manages their own credentials end-to-end.

## Users and RBAC

Grafana's built-in identity model exposes three org-level roles:
`Admin` / `Editor` / `Viewer`. The chart maps them to Keycloak groups
via `role_attribute_path`, evaluated on the `groups` claim at login:

```text
contains(groups[*], '<ns>-<release>-admin')  && 'Admin'  ||
contains(groups[*], '<ns>-<release>-editor') && 'Editor' ||
contains(groups[*], '<ns>-<release>-viewer') && 'Viewer' ||
'Viewer'
```

Authenticated identities with none of the three groups default to
`Viewer`. To give a user a role, add them to the corresponding
`KeycloakRealmGroup` in `cozy`:

```yaml
apiVersion: v1.edp.epam.com/v1
kind: KeycloakRealmUser
metadata:
  name: alice-acme
  namespace: cozy-keycloak
spec:
  realm: cozy
  username: alice@acme.example
  email: alice@acme.example
  emailVerified: true
  password: "…"
  groups:
    - tenant-acme-monitoring-admin
```

Removing a user from the group demotes them (Grafana re-evaluates
`role_attribute_path` on every login). Deleting them from `cozy`
revokes access outright.

## How a user logs in

The user opens `https://grafana.<host>` in a browser and picks the
"Sign in with Keycloak" button under the login form. Grafana runs the
Authorization Code + PKCE flow against the cozy realm, receives a
token whose `aud` claim matches this Monitoring instance's clientId,
and creates or updates the local Grafana user on the first
successful login with the role from `role_attribute_path`.

The break-glass `admin_user` / `admin_password` field on the form
stays wired to the `grafana-admin-password` Secret and continues to
work — useful when Keycloak is down or misconfigured.

## Failure modes

- **`mode: System` without the platform-level OIDC feature** — chart
  render hard-fails:
  `spec.oidc.mode: System requires the platform-level OIDC feature`.
  Flip `authentication.oidc.enabled: true` in the platform values, or
  switch to `CustomConfig`. On the platform side that flag also
  swings the `cozystack.monitoring-application` PackageSource from
  variant `default` (baseline dependsOn only) to variant `oidc`
  (additionally waits for `cozystack.keycloak-operator` so the
  `v1.edp.epam.com` CRDs consumed by the Keycloak-side of System
  mode are registered before the monitoring chart reconciles).
- **`CustomConfig` with an unreachable issuer or wrong claim
  mappings** — Grafana rejects the callback and the login screen
  shows an error; the `admin_user`/`admin_password` Secret keeps
  working as break-glass.
- **Mode toggle destroys chart-owned `KeycloakRealmGroup`
  membership.** The three groups (`<clientId>-{admin,editor,viewer}`)
  are release-owned. Flipping `spec.oidc.mode` from `System` to
  `CustomConfig` or `None` runs a `helm upgrade` that deletes the
  three `KeycloakRealmGroup` objects — and, with them, every user's
  membership. Flipping back to `System` recreates the groups empty;
  the platform operator has to re-populate them by hand (via the
  Keycloak UI or new `KeycloakRealmUser` CRs). Not a bug, but a
  one-way UX cost on the toggle; treat it like rotating an admin
  Secret.
- **`emailVerified` on Keycloak users is a prescriptive requirement,
  not a chart-enforced one.** The chart does not emit any
  `claimValidationRules` — the layered guarantees you rely on
  instead are:
  1. Provision users with `emailVerified: true` (via
     `KeycloakRealmUser` or the Keycloak UI's email-verify flow) so
     no unverified identity ever holds a given email.
  2. The `cozy` realm keeps Keycloak's default
     `duplicateEmails: false`, so a second account cannot claim an
     already-registered address to impersonate an existing operator.
  3. Grafana's own login flow rejects tokens the apiserver rejects,
     so on the wire the same guarantee still holds through the JWT
     signature and audience checks.
  Adding a CEL `claimValidationRules` entry is a reasonable
  follow-up hardening; it is out of scope for Phase 1.
- **`CustomConfig` with both or neither payload set** — chart render
  hard-fails: `set exactly one of 'config' (inline) or
  'secretRef.name' — they are mutually exclusive`, or `CustomConfig
  requires either spec.oidc.customConfig.config … or …secretRef.name`.

## What is NOT in Phase 1

- **Per-tenant Keycloak realms.** Phase 2 candidate; tracked in
  cozystack/community#24.
- **Full-logout through Keycloak's end-session endpoint.**
  `auth.generic_oauth` native to Grafana does the OAuth part; wiring
  `--backend-logout-url` and the corresponding Keycloak client
  attribute is a subsequent hardening.
- **`disable_login_form: true` under `mode: System`.** Kept off so
  the `admin_user`/`admin_password` Secret remains a documented
  break-glass path; hardening it is a follow-up.
- **CEL `claimValidationRules` enforcing `email_verified`.** See the
  failure-modes note above.
- **Multi-issuer / BYO alongside `cozy`.** `mode: System` and
  `mode: CustomConfig` are mutually exclusive on a single Monitoring
  release — no composition. Follow the tenant kube-apiserver PR's
  structured-config path if you need this later.
