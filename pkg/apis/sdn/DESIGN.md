# Cozystack Security Groups — Design

**Status:** Implemented (v1alpha1, aggregated-API projection)

## 1. Summary

A **SecurityGroup** is a namespace-scoped, tenant-facing firewall object in the `sdn.cozystack.io/v1alpha1` API group. It is served by the Cozystack aggregated API server (`cozystack-api`) as a **1:1 projection of a single CiliumNetworkPolicy** in the same namespace. Tenants manage SecurityGroups; the API server translates each one, synchronously, into a CiliumNetworkPolicy on write and back on read. There is no separate controller and no extra reconcile loop.

A SecurityGroup **attaches to a managed application by reference** (`targetRef: {apiGroup, kind, name}`) rather than carrying a tenant-authored pod selector. The backing CiliumNetworkPolicy's `endpointSelector` is **derived** by the API server from the referenced application's lineage labels (`apps.cozystack.io/application.{group,kind,name}`), so the selector is machine-generated and can only ever point at the referenced application's own pods in the tenant's namespace.

The product value is the **RBAC boundary plus a Cozystack-native surface**: tenants get self-service control over the network policy of their own applications without being granted any access to the `cilium.io` API group, and without the platform exposing Cilium's full, version-coupled CRD surface to them. Because the selector is derived from an authorized reference instead of free-form tenant input, a tenant cannot express network policy against arbitrary or platform-owned pods.

## 2. Goals / Non-Goals

**Goals**

- Let tenants declare allow-list ingress/egress for one of their own applications, scoped to their namespace.
- Derive the enforced pod selector from an authorized application reference, never from free-form tenant input.
- Keep policy count equal to the number of SecurityGroups (1 SecurityGroup ↔ 1 CiliumNetworkPolicy).
- Require no direct tenant access to `cilium.io` resources.
- Avoid disrupting clusters or namespaces that do not create SecurityGroups.

**Non-Goals**

- We do not redesign tenant isolation; existing platform isolation policies remain and are untouched (they carry no SecurityGroup marker label, so they are invisible to this API).
- We do not let a SecurityGroup target arbitrary, free-form label selectors or raw (non-application) pods. A SecurityGroup only ever attaches to a managed application by reference (§7).
- We do not introduce reusable CIDR/FQDN groups, cluster-scope policies, or references between SecurityGroups. Those are future work (§8).

## 3. Architecture

1. A tenant creates a **SecurityGroup** (`sdn.cozystack.io/v1alpha1`) with a `targetRef` referencing one of their applications and `ingress`/`egress` rules.
2. The `cozystack-api` REST storage translates it into a **CiliumNetworkPolicy** of the same name in the same namespace, carrying the marker label `sdn.cozystack.io/securitygroup: "true"`. The CiliumNetworkPolicy's `endpointSelector` is **derived** from the `targetRef` — `matchLabels` on `apps.cozystack.io/application.{group,kind,name}` — not copied from tenant input.
3. Reads (`get`/`list`/`watch`) project marked CiliumNetworkPolicies back into SecurityGroups, reconstructing the `targetRef` from the derived `endpointSelector` matchLabels. The marker label is hidden from the SecurityGroup view.
4. The translation is synchronous and stateless — the aggregated API server owns it, so there is no controller, no status reconciliation, and no eventual-consistency window.

### 3.1 Marker-label scoping

Only CiliumNetworkPolicies labelled `sdn.cozystack.io/securitygroup: "true"` are visible through the SecurityGroup API. Policies created by other means (platform tenant-isolation policies, hand-written CiliumNetworkPolicies) are never surfaced and never mutated. `get`/`update`/`patch`/`delete` of an unmarked policy returns `NotFound`. The storage owns the marker label and always re-asserts it on every write (`create`, `update`, `patch`) — the marker is set after any tenant-supplied labels are merged, so a tenant cannot orphan an enforced policy by overwriting the marker through spec labels.

### 3.2 Selector derivation from lineage labels

The lineage mutating webhook (`internal/lineagecontrollerwebhook`) stamps every managed-application pod, at admission time, with `apps.cozystack.io/application.{group,kind,name}` (and `internal.cozystack.io/managed-by-cozystack: "true"`), derived by walking the pod's ownership graph up to its application. The SecurityGroup storage reuses those labels: a `targetRef: {apiGroup, kind, name}` projects to an `endpointSelector` with `matchLabels` on exactly those three keys (an empty `apiGroup` defaults to `apps.cozystack.io`). The reverse projection on read reads the three matchLabels back into the `targetRef`. The mapping is lossless because validation rejects any component that is not a valid label value (≤63 characters), and the webhook truncates only groups longer than 63 characters — so a valid value is stamped on pods unchanged and equals the value the tenant submitted.

### 3.3 Why an in-tree CiliumNetworkPolicy mirror

`cozystack-api` does not import the `github.com/cilium/cilium` Go module: the current Cilium release pins a Kubernetes minor version newer than this project's `k8s.io/apimachinery` fork supports. Instead the storage uses a minimal in-tree mirror type (`CiliumNetworkPolicy`, with a `CiliumNetworkPolicySpec` carrying a concrete `endpointSelector` plus the shared ingress/egress rule types) registered with the controller-runtime client at GroupVersion `cilium.io/v2`. Because the field names and JSON tags match the CiliumNetworkPolicy CRD exactly, marshalling the mirror produces wire-compatible objects. The ingress/egress rules project 1:1; only the selector is translated (`targetRef` → derived `endpointSelector` on write, and back on read).

## 4. API

```yaml
apiVersion: sdn.cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: sg-db
  namespace: tenant-a
spec:
  targetRef:
    apiGroup: apps.cozystack.io   # optional, defaults to apps.cozystack.io
    kind: Postgres
    name: db
  ingress:
    - fromEndpoints:
        - matchLabels:
            app: web
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

`SecurityGroupSpec` carries a `targetRef` plus the subset of the CiliumNetworkPolicy rule the abstraction supports: `ingress[]` (`fromEndpoints`, `fromCIDR`, `toPorts`) and `egress[]` (`toEndpoints`, `toCIDR`, `toFQDNs`, `toPorts`). `targetRef.kind` and `targetRef.name` are **required**; `apiGroup` defaults to `apps.cozystack.io`. Each component must be a valid label value (≤63 characters), since the storage projects them into the backing policy's `endpointSelector` matchLabels. An empty `ingress`/`egress` list denies all traffic in that direction for the targeted application's pods, matching Cilium semantics.

## 5. Backing CiliumNetworkPolicy

The SecurityGroup above projects to:

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: sg-db
  namespace: tenant-a
  labels:
    sdn.cozystack.io/securitygroup: "true"
spec:
  endpointSelector:
    matchLabels:                            # derived from targetRef, not tenant input
      apps.cozystack.io/application.group: apps.cozystack.io
      apps.cozystack.io/application.kind: Postgres
      apps.cozystack.io/application.name: db
  ingress:
    - fromEndpoints:
        - matchLabels:
            app: web
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

## 6. RBAC

- **`cozystack-api` ServiceAccount** — full CRUD on `ciliumnetworkpolicies.cilium.io`, since the storage CRUDs these objects on behalf of tenants.
- **Tenants** — `securitygroups.sdn.cozystack.io` is granted across the tenant ClusterRole tiers exactly like `apps.cozystack.io`: the tenant ServiceAccount role (`cozy:tenant:base`) gets full access; human `view` gets read-only; human `admin`/`super-admin` get write (create/update/patch/delete) via `cozy:tenant:admin:base`. Tenants never receive any `cilium.io` permission.

## 7. Safety & Interactions

**The RBAC boundary is structural, not selector-validated.** The backing `endpointSelector` is derived from `targetRef`, matching only `apps.cozystack.io/application.{group,kind,name}` labels. Those labels are stamped by the lineage webhook **only** on pods whose ownership chain resolves to a managed Cozystack application, and the backing CiliumNetworkPolicy is namespaced — Cilium scopes a namespaced `endpointSelector` to the policy's own namespace. So a `targetRef` can only ever select the referenced application's own pods in the tenant's namespace. Cross-namespace attachment is structurally impossible, and a tenant cannot author a selector that reaches arbitrary or platform-owned pods. No SubjectAccessReview is required for this boundary; the namespace scope and the derived selector deliver it on their own.

**A tenant cannot deny platform-managed traffic.** Cilium policies are additive: when several policies select an endpoint, the allowed set is the union of their allow rules, and SecurityGroup exposes only allow rules (no deny). The platform already blankets every tenant pod with selecting allow-policies (e.g. the per-tenant `*-ingress`/`*-egress` CiliumClusterwideNetworkPolicies and `allow-internal-communication`) that permit management traffic — monitoring scrape, operator reconcile, intra-namespace replica traffic. A SecurityGroup attached to a managed application can only **add** allowances on top of those; it cannot subtract the platform's, so it cannot sever a managed application's management plane. By the same token a SecurityGroup cannot tighten traffic below the platform baseline — it widens, it does not restrict. Tightening below the baseline would require the platform to stop blanket-allowing, which is out of scope here.

**Caveats.**

- A `targetRef` can only attach to a managed application. Raw, tenant-created pods (a bare `Deployment` not owned by an `apps.cozystack.io` application) carry no lineage labels and cannot be targeted. This is a deliberate reduction from a free-form selector, in exchange for the structural boundary above.
- A `targetRef` to a non-existent application is not rejected: it projects to a CiliumNetworkPolicy whose selector matches zero pods, so it has no effect. The storage does not verify the referenced application exists (a SubjectAccessReview/existence check is possible future UX, not a security requirement).

**Other interactions.**

- The marker label keeps SecurityGroups and platform-managed policies strictly separate; the storage never surfaces or mutates an unmarked policy.
- SecurityGroup requires the `ciliumnetworkpolicies.cilium.io` CRD (Cilium ships in the standard Cozystack networking stack). On a cluster without it, every SecurityGroup operation fails loudly with a no-matches-for-kind error rather than silently — there is no fallback.

## 8. Future Work

The aggregated-API model is intentionally minimal. The following can be layered on without changing the core projection:

- **Cluster-scope** SecurityGroups projecting to CiliumClusterwideNetworkPolicy.
- **Targeting raw pods** a tenant owns (a derived "tenant-owned, non-managed" label) so SecurityGroups can apply beyond managed applications without reopening free-form selectors.
- **Multiple targets per SecurityGroup**, or a `targetRef` list, if attaching one policy to several applications becomes common.
- An optional **existence/authorization check** on `targetRef` (SubjectAccessReview) to fail fast on a typo instead of silently matching zero pods.
- Reusable **CIDR/FQDN groups** and references between SecurityGroups.
- Exposing more of the CiliumNetworkPolicy rule surface (ICMP, `toServices`, `fromRequires`, `fromCIDRSet`/`toCIDRSet` exceptions) as demand appears.
- **Richer spec validation.** Create and update already validate the highest-risk fields synchronously — CIDR syntax, port range (1–65535 or a valid named port) and the protocol enum (TCP/UDP/SCTP/ANY) — and reject a bad value with `Invalid` instead of writing a policy that Cilium would silently discard. Still deferred: FQDN `matchName`/`matchPattern` syntax, endpoint-selector key conventions, and cross-rule consistency checks, which currently pass through to Cilium and surface (if at all) asynchronously in the agent.
