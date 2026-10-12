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
- **System** — the apiserver trusts the platform Keycloak realm (`cozy` by default, set via `authentication.oidc.realmName`) via a per-cluster public client. Authenticates the users already in that realm (the one cozystack provisions). Zero-config default.
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

Authenticating a user is only half of the path. A call that lands on an aggregated API server — an `APIService` backed by an extension server rather than by the core apiserver — reaches it over the aggregation layer, which forwards the caller's identity in request headers. The UID travels in `X-Remote-Uid`, which the aggregation layer sends whenever the `RemoteRequestHeaderUID` gate is on, and the extension server trusts that header only when the tenant kube-apiserver has published it under `requestheader-uid-headers` in the `extension-apiserver-authentication` ConfigMap. Without that key the extension server ignores the header, and the call is served as if the caller had no UID, even though `kubectl` against the core apiserver works fine. Anything on the far side that reads the UID, an authorizer or an audit record, sees an empty value.

Carrying the UID across that hop is a property of the aggregation layer, not of OIDC: a caller authenticated by any means loses their UID the same way. The chart nonetheless renders the header flag only when `spec.oidc.mode` is not `None`, which keeps the blast radius to clusters opting into a new feature. A cluster that has never left `mode: None` therefore still loses the UID at the extension server.

Be clear about who this helps. The flag forwards a UID the caller already has; it does not create one. ServiceAccount tokens carry a UID, and so do other identities that set one, and for those the aggregation hop starts working. An OIDC user gets one only from a claim mapping: in `spec.oidc.mode: System` the AuthenticationConfiguration the chart generates maps `uid` to the `sub` claim, the Keycloak account UUID, so those users reach an aggregated API server under a stable identifier. `sub` rather than the username claim, because the platform realm lets a user change the address `email` carries while the subject stays where it is. In `CustomConfig` mode the configuration is yours: map `uid` and the header carries what you mapped; leave it out and upstream resolves an empty UID (`plugin/pkg/authenticator/token/oidc/oidc.go`, `getUID`, read at v0.35.0), with nothing for the header to carry.

On a cluster that was running before the mapping existed, those users gain a UID where they had none. Tenant RBAC does not notice — a role binding matches a username or a group, never a UID — but an audit record on the tenant apiserver now carries `user.uid`, `kubectl auth whoami` shows it, and an admission webhook or extension server inside the tenant that read the empty value now reads the subject.

Where `spec.oidc.mode` is not `None`, and outside the cases further down that stop it, the chart renders the flag that publishes it alongside `--authentication-config`:

| `spec.version` | rendered |
| --- | --- |
| `v1.31` | nothing — the flag does not exist upstream, and an unknown flag stops the apiserver from starting |
| `v1.32` | nothing — `RemoteRequestHeaderUID` is alpha and off, and the apiserver refuses the flag while the gate is off |
| `v1.33` and newer | `--requestheader-uid-headers=X-Remote-Uid` |

The chart never renders `--feature-gates` itself. From `v1.33` `RemoteRequestHeaderUID` is beta and on by default, so the header flag is all the apiserver needs. On `v1.32` the chart does not switch on an alpha gate for you, so aggregated API servers do not see the caller's UID there; if you need them to on `v1.32`, write both `--feature-gates=RemoteRequestHeaderUID=true` and `--requestheader-uid-headers=X-Remote-Uid` into `controlPlane.apiServer.extraArgs` yourself, and the chart passes them through untouched.

The version in that table is the effective one, not the image tag. `--emulated-version` in `controlPlane.apiServer.extraArgs` moves it: the gate resolves through the versioned spec at the emulated version, so `--emulated-version=1.32` puts `RemoteRequestHeaderUID` back to alpha-and-off on a `v1.35` cluster, and the chart follows by rendering nothing, as it would on a `v1.32` image. The chart reads the dot-separated numbers at the start of the value, after an optional `v`, and ignores what follows them, so it reads `v1.32`, `1.32.0` and `1.32-rc` all as `1.32`.

Two values are still not harmless, whatever the chart does with them. An element naming another component, `--emulated-version=wardle=1.2`, says nothing about the tenant kube-apiserver, but the apiserver rejects a component it has not registered, `component not registered: wardle`, and does not start. A value whose minor is above your `spec.version` is ignored too, because emulation only moves the effective version down, and there the apiserver checks against a range rather than a ceiling: it rejects one above its own binary or below the floor it still supports, naming the range it accepts. In both cases nothing the chart renders makes any difference.

Adding the header flag through `controlPlane.apiServer.extraArgs` by hand is no longer necessary from `v1.33`, and an existing cluster that does keeps working as long as its entries would start an apiserver: the chart skips its own copy when your entries already carry `X-Remote-Uid`, and refuses to render the shapes listed below.

Every entry you write on one of these three flags reaches the tenant kube-apiserver. None of them is among the flags the tenant control plane sets itself and drops from `extraArgs`; the chart moves or refuses an entry naming one of those, and the description of `controlPlane.apiServer.extraArgs` lists them. Where your entry does not already satisfy the requirement the chart therefore adds its own beside it rather than failing. `--requestheader-uid-headers` is a repeatable flag whose occurrences append into one list, so a list of your own that omits `X-Remote-Uid` gets a second entry carrying it and the union is what the apiserver validates; your header keeps working alongside the standard one.

The chart does not check how you spell kube gate terms. The apiserver collects `--feature-gates` terms per component across all your entries and refuses a configuration that names the kube component both bare and `kube:`-prefixed; the chart passes that through like any other gate value the apiserver rejects on its own terms.

If you turned the gate off on purpose, with `RemoteRequestHeaderUID=false` or a blanket `AllBeta=false`, the chart does not render the header flag and the render is byte-identical to the one before this feature existed. You keep losing the UID at the aggregation layer, which is what the opt-out asks for. Adding the opt-out or removing it again needs no second edit on Kamaji 26.9.4-edge: the tenant control plane compares a hash of `extraArgs` against the one it stored last time and rebuilds the argument list from scratch whenever they differ, so nothing is left behind from the previous shape. A rollback of Kamaji to an earlier build loses that. The older code expects a number in the `kube-apiserver.kamaji.clastix.io/args` annotation and gives up when it finds the hash. From then on, a flag removed from `extraArgs` stays on the running control plane until someone sets the annotation back to a number by hand.

The render fails where entries of your own describe a kube-apiserver that already refuses to start: a uid-headers list with an element that is empty once trimmed, which a trailing comma leaves behind, and a uid-headers list alongside a gate setting that turns `RemoteRequestHeaderUID` off. The chart cannot rescue either, because the value the apiserver rejects is yours. The error message names the fix, which beats a crash-looping control plane. More refusals sit outside that group and outside the conditions below: the `--oidc-*` and `--authentication-config` collisions further down, each on its own terms; an unquoted `--emulated-version=value` entry whose kube version has a non-zero patch component, such as `1.32.4`, which the apiserver refuses with `patch version not allowed`; an entry naming a flag the tenant control plane sets itself, refused in every `spec.oidc.mode` unless the chart moves it into a field, as the description of `controlPlane.apiServer.extraArgs` explains; and the list ending in one of these three flags, where the apiserver may well start — it is the silence that earns that one.

Those first two checks run only where the chart renders something. Three things switch them off with the header flag: an effective version of `v1.32` or below, a quoted `--emulated-version`, and two `--emulated-version` entries that both name kube. Each of those leaves the chart either with nothing to render or unable to say what the apiserver will read, so it renders nothing and judges nothing, and a shape that would otherwise be refused here passes silently through to a control plane that does not start. Take the unreadable entry out if you want the checks back. `--feature-gates` or `--emulated-version` written as a flag and a value in separate entries hides a value too, but a narrower one, and the chart keeps what holds whatever that value is. The empty element is still refused. An explicit `RemoteRequestHeaderUID=false` is still refused beside a header entry behind a hidden `--emulated-version`, because it turns the gate off at every version; a blanket `AllBeta=false` is not, because at a lower emulated version the gate can be alpha, where `AllBeta` does not reach it. Neither is refused behind a hidden `--feature-gates` value, which can turn the gate back on. A uid-headers list of yours without `X-Remote-Uid` still gets the chart's element beside it, which the apiserver needs with the gate on and which changes nothing with the gate off, since your list alone is refused then. Behind a hidden value and without a list of yours, the chart renders no `X-Remote-Uid` entry at all.

Otherwise nothing stops rendering: every other `extraArgs` shape reaching this code either renders as it did or gains the header flag.

The gate check reads your entries the way the apiserver does.

- An element may name a component, because `--feature-gates` is a colon-separated multimap: `kube:RemoteRequestHeaderUID=false` is the same opinion as `RemoteRequestHeaderUID=false`, while an element naming another component is not about the tenant kube-apiserver, and the chart ignores it when it works out the state of the UID gate. That is the chart's reading only. Whether the apiserver accepts the component is up to its own component registry, as it is for `--emulated-version` above.
- Past that it is one `Name=value` element at a time, spaces trimmed on both sides, and the value resolved the way `strconv.ParseBool` resolves it, so `1`, `t` and `T` mean the same as `true`, and `0`, `f` and `F` the same as `false`.
- Blanket settings count, because the apiserver applies them to every gate you have not named explicitly. `AllBeta=false` switches `RemoteRequestHeaderUID` off, so the chart reads it as your opt-out; `AllAlpha` covers alpha gates only, and from `v1.33` this gate is beta, so the chart does not read it.
- Naming the gate wins over a blanket: `AllBeta=false,RemoteRequestHeaderUID=true` leaves the gate on, because an explicit entry beats a blanket one whichever order they appear in.
- Gate names are compared exactly, as the apiserver looks them up, so `remoterequestheaderuid=false` is not an opinion about `RemoteRequestHeaderUID`; the apiserver would reject that spelling as an unrecognized gate anyway.
- Several `--feature-gates` entries settle a gate between them, last term wins, exactly as the apiserver settles them: an earlier entry naming the gate still counts when a later entry says nothing about it.
- An entry with an empty value carries no term, so it settles nothing, and it does not cancel what an earlier entry said. Drop the entry you do not want instead of emptying it.

`--emulated-version` accumulates rather than replaces, and the apiserver refuses two elements naming the same component with `duplicate version flag`. Two entries of yours that both name kube, explicitly or by naming nothing, stop the control plane from starting; the chart does not render the header flag there, because there is no effective version left to decide against. Entries naming different registered components are fine and all of them apply, so an emulated kube version stays in force when a later entry names something else.

Quotes stop the chart from reading the entry they are on. `--requestheader-uid-headers` and `--emulated-version` are both CSV-parsed by the apiserver, which strips a surrounding pair of double quotes before it sees the value, and the chart does not reproduce that. On `--emulated-version` that hides the version the whole decision rests on, so the chart does not render the header flag, exactly as it did before that flag existed. On `--requestheader-uid-headers` it only means the chart cannot tell whether that entry already carries `X-Remote-Uid` or an empty element: it renders its own element, which is safe whatever your value turns out to be, and does not refuse the render over an element it may be misreading. `--feature-gates` is not CSV-parsed, so quotes there are neither stripped nor understood: the apiserver rejects the term as an unrecognized gate name and the chart, reading the same text, does not take it for an opinion about `RemoteRequestHeaderUID` either. Drop the quotes if you want the chart to read the entry.

Writing a flag and its value as two list entries has the same effect, for the same reason. The apiserver pairs them, but the value sits in an entry the chart reads separately, so it cannot be attached to the flag. On `--feature-gates` and `--emulated-version` the chart therefore renders its own `X-Remote-Uid` element only beside a uid-headers list of yours that lacks it, as described above. On `--requestheader-uid-headers` it counts the flag as present, which is what the opt-out check needs, and renders its own `X-Remote-Uid` entry beside it. Write `--flag=value` in one entry if you want the chart to read the value.

One arrangement of that shape the chart refuses outright: one of those three flags as the last entry in the list. The apiserver pairs a valueless flag with whatever argument follows it, and what follows yours is the `--authentication-config` the chart appends, so the flag would swallow it. The AuthenticationConfiguration is then never applied, and what that costs depends on which flag took it: `--requestheader-uid-headers` accepts the path as a header name, so the control plane starts and fails every login with nothing to show for it, while `--feature-gates` and `--emulated-version` reject it and the apiserver exits instead. Give the flag its value in the same entry. Moving it earlier works too: the entry after it is then one of yours, and it reaches the apiserver right behind the flag, since the chart refuses every entry the control plane would drop or moves it out of the list it renders.

The uid-headers list is read without trimming, because the apiserver does not trim it either: `--requestheader-uid-headers=X-Remote-Uid , Other` does not contain `X-Remote-Uid` as far as the apiserver is concerned, so the chart does not read it as one and renders its own element, which is what makes that list start. The emptiness check is the one that trims, again because the apiserver's does: in `X-Remote-Uid, ` the second element is empty once trimmed, and the apiserver refuses the whole list with `empty value in "requestheader-uid-headers"` although `X-Remote-Uid` is in it, which is why that shape fails the render instead.

On `v1.31` and `v1.32` the chart renders no UID flag, and the UID guards above do not run, so a uid-headers or `--feature-gates` entry of yours passes through untouched. These checks still apply there as everywhere else, because none of them depends on the effective version: the `--oidc-*` and `--authentication-config` collisions, the refusal of a patch-level `--emulated-version`, the move or refusal of an entry naming a flag the tenant control plane sets itself, which the description of `controlPlane.apiServer.extraArgs` explains, and the refusal of a list ending in one of the three flags, which is about the `--authentication-config` the chart appends on every version rather than about the UID flags. Aggregated API servers cannot see the caller's UID on `v1.31`; on `v1.32` they can only through the alpha gate you enable yourself, as described above. Move the cluster to `v1.33` or newer if you need them to without it. Writing `--requestheader-uid-headers` yourself does not work around it on `v1.31` — the flag does not exist there, and an unknown flag stops the apiserver from starting.

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
