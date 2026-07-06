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
public kubectl one: authentication is wired up by the chart, but
authorization is an operator-supplied `users:` map reconciled into
the target (Grafana orgs here, ClusterRoleBindings in #3044) by a
chart-owned Job. The chart does not own any directory objects.

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
    users:
      - email: alice@acme.example
        role: Admin
      - email: bob@acme.example
        role: Viewer
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
3. A **Secret** `<release>-oidc-client` carrying a random 32-char
   `client-secret`. Generated on first install (`lookup` + random
   fallback, same pattern as `packages/system/dashboard`), preserved
   across upgrades.
4. The Grafana CR's `spec.config.auth.generic_oauth` section wired to
   the cozy realm issuer + the per-instance audience scope, with
   `skip_org_role_sync = true` so a login never overwrites the
   app-side role assignments made by the users-Job, and
   `oauth_allow_insecure_email_lookup = true` so the OIDC identity
   binds to the pre-provisioned local account by email.
5. A `GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET` env on the Grafana
   Deployment sourced from the Secret in step 3.
6. A post-install/post-upgrade **users-Job** — see the "Users and
   RBAC" section — that reconciles `spec.oidc.users` into Grafana's
   Main Org.

No `KeycloakRealmGroup`s and no `role_attribute_path`. Directory
objects (users, groups, group memberships) stay owned by whoever
operates the `cozy` realm; the Monitoring release only requests
authentication wiring, it does not act as a user directory.

Server-level `GrafanaAdmin` promotion (`allow_assign_grafana_admin`)
is out of scope for Phase 1. All Grafana instances — platform and
tenant — cap at org-level `Admin`.

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
        scopes: openid email profile
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

The chart merges two settings on top of the operator's inline map so
the users-Job contract holds in `CustomConfig` mode too:

- `skip_org_role_sync = true` — a login never overwrites the Job's
  org-role assignments;
- `oauth_allow_insecure_email_lookup = true` — the OIDC identity
  binds to the pre-provisioned local account by email.

Both keys are chart-forced (`merge` semantics, chart wins) — setting
them in the operator's map is a no-op, and `role_attribute_path` would
just be dead config since the Job manages roles.

**`secretRef.name` mode disables the users-map.** The chart mounts the
operator's `auth.ini` fragment verbatim via `GF_PATHS_CUSTOM_INI` and
cannot inject either of the two chart-forced settings. Setting
`spec.oidc.users` alongside `customConfig.secretRef.name` fails the
render with an explicit message — the alternatives are: (a) use
`customConfig.config` (inline) and let the chart merge, or (b) leave
`spec.oidc.users` empty and include both `skip_org_role_sync = true`
and `oauth_allow_insecure_email_lookup = true` in your ini fragment
plus manage the OIDC → Grafana user mapping yourself.

Setting both `config` and `secretRef.name` (or neither) fails the
render. In `CustomConfig` mode no Keycloak objects are provisioned in
`cozy` and no chart-owned client-secret Secret is created — the
operator manages their own credentials end-to-end.

## Users and RBAC

Grafana's built-in identity model exposes three org-level roles:
`Admin` / `Editor` / `Viewer`. The chart drives them from an
operator-supplied map on the CR:

```yaml
spec:
  oidc:
    users:
      - email: alice@acme.example
        role: Admin
      - email: bob@acme.example
        role: Editor
      - email: carol@acme.example
        role: Viewer
```

A chart-owned Job runs on every `helm install` / `helm upgrade` and
reconciles that list into Main Org. membership via Grafana's admin
API:

1. Each listed email gets a pre-provisioned local Grafana account
   with a random password. The account is a shell; the operator
   never uses that password. When they sign in with the OIDC flow
   Grafana looks up the pre-provisioned account by email
   (`oauth_allow_insecure_email_lookup = true`) and attaches the
   OIDC identity to it.
2. Each listed email is added to Main Org. with the requested role.
   Re-runs of the Job converge role changes (`Editor` → `Admin`,
   demotions, etc.) via `PATCH /api/orgs/{orgId}/users/{userId}`.
3. Every Main-Org member whose email is neither in the list nor the
   break-glass `grafana-admin-password` login is removed. Removing
   an entry from `users:` and re-reconciling revokes access;
   flipping `mode: None` treats the list as empty and prunes every
   OIDC-provisioned account.

Users not listed in `spec.oidc.users` who log in through OIDC get
nothing — no default `Viewer` role, no cross-tenant read access.
`skip_org_role_sync = true` in the Grafana config makes sure the
Job's assignments outlive the next login.

For `System` mode, the operator provisions the corresponding
Keycloak user in `cozy` in whatever way they already do (Keycloak
UI, a `KeycloakRealmUser` CR, an identity broker). The Monitoring
release does not create Keycloak users and does not manage
`KeycloakRealmGroup`s — group membership curated out-of-band is not
affected by anything this chart does.

## How a user logs in

The user opens `https://grafana.<host>` in a browser and picks the
"Sign in with Keycloak" button under the login form. Grafana runs the
Authorization Code + PKCE flow against the cozy realm, receives a
token whose `aud` claim matches this Monitoring instance's clientId,
and — if the user's email is in `spec.oidc.users` — binds the OIDC
identity to the pre-provisioned local account with the role the Job
already set.

If the user's email is not in `spec.oidc.users` the login succeeds
authentication-wise but they land with no org membership and no role.
Add them to `spec.oidc.users` and re-apply the CR; the Job will run
on the next helm-upgrade and pull them in.

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
- **`mode: System` without the Keycloak operator CRDs registered** —
  chart render hard-fails:
  `spec.oidc.mode: System requires the Keycloak operator CRDs
  (v1.edp.epam.com/v1)`. Symmetric across
  `templates/grafana/oidc-keycloak.yaml` and
  `templates/grafana/grafana.yaml` so Grafana never comes up with an
  `auth.generic_oauth` block pointing at a client that never gets
  provisioned. For the platform `monitoring-system` release the
  `oidc` variant's `dependsOn: cozystack.keycloak-operator` prevents
  the race; for tenant releases (`Monitoring` CR with
  `mode: System`) verify the keycloak-operator package is deployed
  before creating the CR.
- **`CustomConfig` with an unreachable issuer or wrong claim
  mappings** — Grafana rejects the callback and the login screen
  shows an error; the `admin_user`/`admin_password` Secret keeps
  working as break-glass.
- **User successfully logs in but sees no dashboards.** Their email
  is missing from `spec.oidc.users` — no org role was assigned.
  Add the entry and re-apply the CR; the users-Job runs on the
  next helm-upgrade and grants access.
- **users-Job fails.** The Job caps at `activeDeadlineSeconds: 900`
  and `backoffLimit: 6`. Common causes: Grafana never becomes
  ready (check `kubectl -n <ns> get pods -l app=grafana`), or the
  `grafana-admin-password` Secret is missing its `user`/`password`
  keys. The failed hook Job stays around until the next
  helm-upgrade for post-mortem; check its logs with
  `kubectl -n <ns> logs job/<release>-oidc-users`.
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
- **Server-level `GrafanaAdmin` promotion.** All Grafana instances
  cap at org-level `Admin`. Not exposed on the CR.
- **CEL `claimValidationRules` enforcing `email_verified`.** See the
  failure-modes note above.
- **Multi-issuer / BYO alongside `cozy`.** `mode: System` and
  `mode: CustomConfig` are mutually exclusive on a single Monitoring
  release — no composition. Follow the tenant kube-apiserver PR's
  structured-config path if you need this later.
