# Cozystack Security Groups — Design

**Status:** Implemented (v1alpha1, aggregated-API projection + membership controller)

## 1. Summary

A **SecurityGroup** is a namespace-scoped, tenant-facing firewall object in the `sdn.cozystack.io/v1alpha1` API group. It is served by the Cozystack aggregated API server (`cozystack-api`) as a **1:1 projection of a single CiliumNetworkPolicy** in the same namespace: tenants manage SecurityGroups while the API server translates each one, synchronously, into a CiliumNetworkPolicy on write and back on read.

A SecurityGroup is a **membership group**. It owns a stable identity label — `securitygroup.sdn.cozystack.io/<name>` — and the backing CiliumNetworkPolicy's `endpointSelector` matches that label rather than any one application's labels. A SecurityGroup **attaches to managed applications by reference** (`spec.attachments`, a list of `{apiGroup, kind, name}`); a dedicated controller stamps the membership label onto the attached applications' pods and removes it when they are detached. Because the selector is the group's own machine-assigned label and the attachments are resolved through the lineage labels the platform already stamps, a SecurityGroup can only ever apply to its attached applications' own pods in the tenant's namespace — never to arbitrary or platform-owned pods — and one SecurityGroup can attach to several applications at once.

Rule peers are expressed in the same vocabulary: `fromApp`/`toApp` reference managed applications (resolved to their lineage labels) and `fromSG`/`toSG` reference other SecurityGroups (resolved to the *other* group's membership label). The `fromSG`/`toSG` reference is **live**: it projects to a label the Cilium dataplane resolves dynamically, so re-attaching a group to different applications updates every rule that references it without rewriting those rules.

The product value is the **RBAC boundary plus a Cozystack-native surface**: tenants get self-service control over the network policy of their own applications without any access to the `cilium.io` API group, and without the platform exposing Cilium's full, version-coupled CRD surface to them.

## 2. Goals / Non-Goals

**Goals**

- Let tenants declare allow-list ingress/egress for their own applications, scoped to their namespace, with one SecurityGroup able to cover several applications.
- Derive the enforced pod set from authorized references (attachments and app/SG peers), never from free-form tenant input.
- Support live group-to-group references (`fromSG`/`toSG`) that follow membership as attachments change.
- Keep policy count equal to the number of SecurityGroups (1 SecurityGroup ↔ 1 CiliumNetworkPolicy).
- Require no direct tenant access to `cilium.io` resources, and keep the membership label writable only by the platform.

**Non-Goals**

- **We do not flip the tenant baseline to default-deny in this change.** Today the per-tenant baseline (`packages/apps/tenant/templates/networkpolicy.yaml`) blanket-allows intra-namespace and outbound traffic, and Cilium allow-rules are additive, so a SecurityGroup can only *widen* — it cannot yet restrict (`ingress: []` does not deny). This API is designed to be the right one in the default-deny world, but actually shrinking the baseline touches every tenant and is its own change (§8).
- **We do not add a membership admission webhook in this change.** The controller closes the membership labels asynchronously; a pod-admission webhook to make new pods members at creation time only matters under default-deny and is deferred with the baseline flip (§7, §8).
- We do not redesign tenant isolation; existing platform isolation policies carry no SecurityGroup marker label, so they are invisible to this API and untouched.
- We do not let a SecurityGroup target free-form label selectors or raw (non-application) pods, name reserved Cilium entities, or reference SecurityGroups in another namespace.

## 3. Architecture

The design has two cleanly-split halves.

1. A tenant creates a **SecurityGroup** (`sdn.cozystack.io/v1alpha1`) with `spec.attachments` (managed applications) and `ingress`/`egress` rules.
2. The `cozystack-api` REST storage translates it into a **CiliumNetworkPolicy** of the same name and namespace, carrying the marker label `sdn.cozystack.io/securitygroup: "true"`. The CiliumNetworkPolicy's `endpointSelector` is the SecurityGroup's own membership label `securitygroup.sdn.cozystack.io/<name>`. The attachments are stored in a storage-owned annotation (`sdn.cozystack.io/attachments`); peers project into endpoint selectors. This translation is synchronous and stateless.
3. The **securitygroup-controller** watches the marked CiliumNetworkPolicies and managed-app pods. For each policy it stamps the membership label onto the pods of every attached application and removes it on detach or deletion. This is the only stateful piece, and the only writer of membership labels.
4. Reads (`get`/`list`/`watch`) project marked CiliumNetworkPolicies back into SecurityGroups, rebuilding `attachments` from the annotation and `fromApp`/`fromSG` (and `toApp`/`toSG`) from the rule endpoint selectors. The marker label and the attachments annotation are hidden from the SecurityGroup view.

### 3.1 Marker-label scoping

Only CiliumNetworkPolicies labelled `sdn.cozystack.io/securitygroup: "true"` are visible through the SecurityGroup API. Policies created by other means (platform tenant-isolation policies, hand-written CiliumNetworkPolicies) are never surfaced and never mutated. `get`/`update`/`patch`/`delete` of an unmarked policy returns `NotFound`. The storage owns the marker label and always re-asserts it on every write after any tenant-supplied labels are merged, so a tenant cannot orphan an enforced policy by overwriting the marker through spec labels.

### 3.2 Membership labels and attachments

A SecurityGroup's membership label key is `securitygroup.sdn.cozystack.io/<name>` (value always empty). The backing policy's `endpointSelector` matches it. The membership relationship — which applications the group attaches to — is stored as a JSON array of `ApplicationReference` in the storage-owned annotation `sdn.cozystack.io/attachments` on the backing policy. The storage re-asserts that annotation from `spec.attachments` on every write (canonicalizing an empty `apiGroup` to `apps.cozystack.io`) and hides it from the SecurityGroup view, exactly as it does the marker label. There is no home for the attachment list in the CiliumNetworkPolicy spec — the spec's selector is the group's own identity, decoupled from any single application — so the annotation is where the controller reads it.

### 3.3 The membership controller

The `securitygroup-controller` (`internal/securitygroupcontroller`, `cmd/securitygroup-controller`) reconciles membership labels. For each marked policy it computes the desired member set — the union, over the policy's attachments, of pods in the policy's namespace carrying that application's lineage labels — and brings the actual set of pods labelled `securitygroup.sdn.cozystack.io/<name>` to match, adding and removing the label with single-key merge patches. It carries a finalizer on the backing policy so the membership labels are stripped before the policy is deleted. It watches managed-app pods so a freshly-created pod of an attached application is labelled promptly.

The controller is cheap because the identity it needs already lands on every managed-app pod: the lineage mutating webhook (`internal/lineagecontrollerwebhook`) stamps `apps.cozystack.io/application.{group,kind,name}` and `internal.cozystack.io/managed-by-cozystack: "true"` at admission. The controller only consumes those labels; it adds no responsibility to the webhook.

### 3.4 Why an in-tree CiliumNetworkPolicy mirror

Neither `cozystack-api` nor the controller imports the `github.com/cilium/cilium` Go module: the current Cilium release pins a Kubernetes minor version newer than this project's `k8s.io/apimachinery` fork supports. The storage uses a minimal in-tree mirror (`CiliumNetworkPolicy` with a concrete `endpointSelector` and Cilium-shaped ingress/egress rules) registered at GroupVersion `cilium.io/v2`; the controller uses an even smaller metadata-only mirror (it never reads or writes the spec, so its mirror omits it and changes finalizers through merge patches that never carry a spec). Because the field names and JSON tags match the CiliumNetworkPolicy CRD exactly, marshalling produces wire-compatible objects.

### 3.5 Liveness of `fromSG`/`toSG`

`fromSG: other` projects to `fromEndpoints: [{matchLabels: {securitygroup.sdn.cozystack.io/other: ""}}]` — the *other* group's membership label. Cilium resolves that label against live pods at enforcement time, so when `other` re-attaches to different applications its membership label moves with it and every rule referencing `other` follows automatically. This is the payoff of the membership model over a frozen reference: a `targetRef`-style model would have to dereference `other` to its applications and freeze those labels into the rule at write time, going stale the moment `other` re-targeted.

## 4. API

```yaml
apiVersion: sdn.cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: db
  namespace: tenant-a
spec:
  attachments:
    - kind: Postgres        # apiGroup optional, defaults to apps.cozystack.io
      name: db
  ingress:
    - fromApp:
        - kind: Kubernetes
          name: web
      fromSG:
        - frontend
      toPorts:
        - ports:
            - port: "5432"
              protocol: TCP
  egress:
    - toFQDNs:
        - matchPattern: "*.apt.example.org"
    - toCIDR:
        - "10.0.0.0/8"
```

`SecurityGroupSpec` carries `attachments` plus `ingress[]` (`fromApp`, `fromSG`, `fromCIDR`, `toPorts`) and `egress[]` (`toApp`, `toSG`, `toCIDR`, `toFQDNs`, `toPorts`). Each `ApplicationReference` requires `kind` and `name`; `apiGroup` defaults to `apps.cozystack.io`. Every reference component must be a valid label value, and each `fromSG`/`toSG` name must project to a valid label key (`securitygroup.sdn.cozystack.io/<name>`); names that collide with reserved Cilium entities (`world`, `cluster`, `kube-apiserver`, `host`, …) are rejected, since external reach is expressed through CIDR/FQDN, not entities. An empty `attachments` list is valid — the group simply selects no pods until something is attached. An empty `ingress`/`egress` list adds no allow rules in that direction; because Cilium policies are additive over the tenant's blanket-allow baseline it does not isolate the member pods — see §7 for why the membership API is inert as a restriction until the baseline becomes default-deny.

## 5. Backing CiliumNetworkPolicy

The SecurityGroup above projects to:

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: db
  namespace: tenant-a
  labels:
    sdn.cozystack.io/securitygroup: "true"
  annotations:
    sdn.cozystack.io/attachments: '[{"apiGroup":"apps.cozystack.io","kind":"Postgres","name":"db"}]'
spec:
  endpointSelector:
    matchLabels:                                         # the group's own membership label
      securitygroup.sdn.cozystack.io/db: ""
  ingress:
    - fromEndpoints:
        - matchLabels:                                   # fromApp -> lineage labels
            apps.cozystack.io/application.group: apps.cozystack.io
            apps.cozystack.io/application.kind: Kubernetes
            apps.cozystack.io/application.name: web
        - matchLabels:                                   # fromSG -> the other group's membership label
            securitygroup.sdn.cozystack.io/frontend: ""
      toPorts:
        - ports:
            - port: "5432"
              protocol: TCP
  egress:
    - toFQDNs:
        - matchPattern: "*.apt.example.org"
    - toCIDR:
        - "10.0.0.0/8"
```

The securitygroup-controller stamps `securitygroup.sdn.cozystack.io/db: ""` onto the pods of the attached `Postgres/db` application, which is what makes the policy's `endpointSelector` select them.

A **membership-only SecurityGroup** — one with `attachments` but no `ingress`/`egress` rules — still projects to a backing CiliumNetworkPolicy, because the controller reconciles the policy (not the SecurityGroup) and the attachments live on it. The CiliumNetworkPolicy CRD requires at least one rule section to be present (its `spec` `anyOf` is `ingress | ingressDeny | egress | egressDeny`), so the projection always emits the `ingress` section, an empty list `ingress: []` when the group has no ingress rules. An empty list is the CRD's documented no-op ("if omitted or empty, this rule does not apply at ingress") and `enableDefaultDeny` defaults to `false` for a direction with no rules, so the policy is schema-valid yet datapath-inert: it neither allows nor denies anything, consistent with §7. The empty section is hidden from the SecurityGroup view on read, so a membership-only group round-trips with no `ingress`/`egress`.

## 6. RBAC

- **`cozystack-api` ServiceAccount** — full CRUD on `ciliumnetworkpolicies.cilium.io`, since the storage CRUDs these objects on behalf of tenants.
- **`securitygroup-controller` ServiceAccount** — cluster-wide `get`/`list`/`watch`/`patch` on `pods` (it stamps the membership label across dynamically-created tenant namespaces) and `get`/`list`/`watch`/`patch` on `ciliumnetworkpolicies.cilium.io` (to watch the backing policies and manage its finalizer via merge patches). This is the platform's first tenant-driven, cluster-wide pod-label writer; §7 covers how the controller is constrained so the grant is safe.
- **Tenants** — `securitygroups.sdn.cozystack.io` is granted across the tenant ClusterRole tiers exactly like `apps.cozystack.io`: the tenant ServiceAccount role gets full access, human `view` read-only, human `admin`/`super-admin` write. Tenants never receive any `cilium.io` permission, and never write the membership label.

## 7. Safety & Interactions

**The membership label is written exclusively by the platform.** Tenants address SecurityGroups, never the `cilium.io` objects or pod labels. The selector a policy enforces is the group's own membership label, and that label is placed on pods only by the securitygroup-controller.

**The controller is a privileged pod-label writer, and is constrained to stay safe.** A cluster-wide `pods: patch` grant driven by tenant-authored objects is a real new surface (the rest of the tenant-facing platform is read-only or namespace-scoped). The controller upholds three invariants so it cannot reach pods a tenant could not otherwise address: it resolves each attachment through the lineage labels the webhook stamps (never the attachment list directly), it only ever labels pods in the SecurityGroup's own namespace, and it patches a single label key so it can neither clobber another group's label nor a pod's lineage labels. The membership label key is namespace-unique only (two tenants can each have a group named `db`), so the namespace-equality invariant — not the key — is what keeps one tenant's group off another tenant's pods; the namespaced backing policy independently scopes *enforcement* to its own namespace.

**Eventual-consistency window.** The controller labels pods asynchronously, so a newly-created pod of an attached application is briefly unlabelled. Under the current allow-all baseline this is harmless: a SecurityGroup only adds allowances, so an unlabelled pod is simply "not yet additionally allowed," never wrongly denied. Under a future default-deny baseline this window would wrongly deny a fresh pod until the controller catches up, which is exactly why a pod-admission webhook is paired with the baseline flip (§8) rather than shipped now.

**A tenant cannot deny platform-managed traffic — and, today, cannot deny anything.** Cilium policies are additive: when several policies select an endpoint the allowed set is the union of their allow rules, and SecurityGroup exposes only allow rules. The per-tenant baseline blanket-allows intra-namespace and outbound traffic, so a SecurityGroup can only *widen* it; `ingress: []` does not actually deny. The membership API is therefore inert as a restriction until the baseline becomes default-deny. It is shipped now to settle the contract (membership identity, live group references, no free-form selectors) before tenants depend on it.

**The membership model does not, by itself, solve "a tenant firewalls its own managed application."** Once the baseline is default-deny, a tenant could attach a SecurityGroup with `ingress: []` to their own managed Postgres and starve its platform traffic (backups, metrics scrape, operator reconcile). The fix for that is platform-traffic carve-outs in the baseline, orthogonal to membership and out of scope here (§8).

**Caveats.**

- Attachments and app peers can only reference managed applications. Raw, tenant-created pods carry no lineage labels and cannot be members or peers — a deliberate trade for the structural boundary above.
- A reference to a non-existent application or SecurityGroup is not rejected: it resolves to no pods, so it has no effect. The storage does not verify existence (a SubjectAccessReview/existence check is possible future UX, not a security requirement).
- If the controller is uninstalled, membership labels it stamped remain on pods and the backing policies keep enforcing against them; pods created afterwards are not labelled. This is acceptable for an opt-in, additive feature and revisited with the default-deny work.

**Other interactions.**

- SecurityGroup requires the `ciliumnetworkpolicies.cilium.io` CRD (Cilium ships in the standard Cozystack networking stack). On a cluster without it, every SecurityGroup operation fails loudly rather than silently.

## 8. Future Work

The aggregated-API + controller model is intentionally minimal. The following can be layered on without changing the core projection:

- **Default-deny tenant baseline.** Shrink the per-tenant baseline to the minimum the platform needs (DNS, apiserver, monitoring, each app's own flows) and let tenants open the rest with SecurityGroups. This is what gives the feature its restrictive power; it touches every tenant, needs the membership admission webhook below, and is its own change.
- **Membership admission webhook.** Stamp the membership label at pod-create time to close the eventual-consistency window under default-deny. It must be a *separate* webhook: the lineage webhook is gated by `objectSelector: managed-by-cozystack DoesNotExist`, so it never re-fires on already-managed pods and cannot be extended to do this, nor can it retro-label running pods — a backfill controller (this one) is still required.
- **Platform-traffic carve-outs** in the default-deny baseline so a tenant cannot starve their own managed application's management plane.
- **Cluster-scope** SecurityGroups projecting to CiliumClusterwideNetworkPolicy.
- Reusable **CIDR/FQDN groups**, and exposing more of the CiliumNetworkPolicy rule surface (ICMP, `toServices`, CIDR-set exceptions) as demand appears.
- An optional **existence/authorization check** on attachments and SG peers (SubjectAccessReview) to fail fast on a typo instead of silently matching zero pods.
- **Richer spec validation.** Create and update already validate the highest-risk fields synchronously — CIDR syntax, port range, the protocol enum, attachment/peer label validity, reserved-entity names — and reject a bad value with `Invalid` instead of writing a policy Cilium would silently discard. Still deferred: FQDN `matchName`/`matchPattern` syntax and cross-rule consistency, which pass through to Cilium.
