# Cozystack Security Groups — Design

**Status:** Implemented (v1alpha1, aggregated-API projection)

## 1. Summary

A **SecurityGroup** is a namespace-scoped, tenant-facing firewall object in the `sdn.cozystack.io/v1alpha1` API group. It is served by the Cozystack aggregated API server (`cozystack-api`) as a **1:1 projection of a single CiliumNetworkPolicy** in the same namespace. Tenants manage SecurityGroups; the API server translates each one, synchronously, into a CiliumNetworkPolicy on write and back on read. There is no separate controller and no extra reconcile loop.

The product value is the **RBAC boundary plus a Cozystack-native surface**: tenants get full self-service control over their own network policy without being granted any access to the `cilium.io` API group, and without the platform exposing Cilium's full, version-coupled CRD surface to them.

## 2. Goals / Non-Goals

**Goals**

- Let tenants declare allow-list ingress/egress for their own pods, scoped to their namespace.
- Keep policy count equal to the number of SecurityGroups (1 SecurityGroup ↔ 1 CiliumNetworkPolicy).
- Require no direct tenant access to `cilium.io` resources.
- Avoid disrupting clusters or namespaces that do not create SecurityGroups.

**Non-Goals**

- We do not redesign tenant isolation; existing platform isolation policies remain and are untouched (they carry no SecurityGroup marker label, so they are invisible to this API).
- We do not introduce an AWS-style attach/detach abstraction, reusable CIDR/FQDN groups, or per-attachment policy logic. Those are future work (§8).

## 3. Architecture

1. A tenant creates a **SecurityGroup** (`sdn.cozystack.io/v1alpha1`) with an `endpointSelector` and `ingress`/`egress` rules.
2. The `cozystack-api` REST storage translates it into a **CiliumNetworkPolicy** of the same name in the same namespace, carrying the marker label `sdn.cozystack.io/securitygroup: "true"`.
3. Reads (`get`/`list`/`watch`) project marked CiliumNetworkPolicies back into SecurityGroups. The marker label is hidden from the SecurityGroup view.
4. The translation is synchronous and stateless — the aggregated API server owns it, so there is no controller, no status reconciliation, and no eventual-consistency window.

### 3.1 Marker-label scoping

Only CiliumNetworkPolicies labelled `sdn.cozystack.io/securitygroup: "true"` are visible through the SecurityGroup API. Policies created by other means (platform tenant-isolation policies, hand-written CiliumNetworkPolicies) are never surfaced and never mutated. `get`/`update`/`patch`/`delete` of an unmarked policy returns `NotFound`. The storage owns the marker label and always re-asserts it on every write (`create`, `update`, `patch`) — the marker is set after any tenant-supplied labels are merged, so a tenant cannot orphan an enforced policy by overwriting the marker through spec labels.

### 3.2 Why an in-tree CiliumNetworkPolicy mirror

`cozystack-api` does not import the `github.com/cilium/cilium` Go module: the current Cilium release pins a Kubernetes minor version newer than this project's `k8s.io/apimachinery` fork supports. Instead the storage uses a minimal in-tree mirror type (`CiliumNetworkPolicy` with a spec equal to `SecurityGroupSpec`) registered with the controller-runtime client at GroupVersion `cilium.io/v2`. Because the SecurityGroup field names and JSON tags match the CiliumNetworkPolicy CRD exactly, marshalling the mirror produces wire-compatible objects and the projection is a near-identity translation in both directions.

## 4. API

```yaml
apiVersion: sdn.cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: sg-db
  namespace: tenant-a
spec:
  endpointSelector:
    matchLabels:
      app: db
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

`SecurityGroupSpec` exposes the subset of the CiliumNetworkPolicy rule that the abstraction supports: `endpointSelector`, `ingress[]` (`fromEndpoints`, `fromCIDR`, `toPorts`) and `egress[]` (`toEndpoints`, `toCIDR`, `toFQDNs`, `toPorts`). `endpointSelector` is **required and must select at least one pod** — an empty selector matches every pod in the namespace in Cilium, which would silently turn a single-pod rule into a namespace-wide one, so the storage rejects it. An empty `ingress`/`egress` list with a set `endpointSelector` denies all traffic in that direction for the selected pods, matching Cilium semantics.

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
    matchLabels:
      app: db
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

- Policies only apply to pods matched by the SecurityGroup's `endpointSelector`; other pods are unaffected.
- CiliumNetworkPolicy is namespaced, so a SecurityGroup can only affect its own namespace and cannot grant cross-tenant access (network policies are allow-lists for the selected pods' own traffic).
- The marker label keeps SecurityGroups and platform-managed policies strictly separate.
- SecurityGroup requires the `ciliumnetworkpolicies.cilium.io` CRD (Cilium ships in the standard Cozystack networking stack). On a cluster without it, every SecurityGroup operation fails loudly with a no-matches-for-kind error rather than silently — there is no fallback.

## 8. Future Work

The aggregated-API model is intentionally minimal. The following can be layered on without changing the core projection:

- **Cluster-scope** SecurityGroups projecting to CiliumClusterwideNetworkPolicy.
- An **attachment abstraction** (attach/detach a SecurityGroup to a managed application by reference, with controller-driven pod labelling) for AWS-like ergonomics.
- Reusable **CIDR/FQDN groups** and references between SecurityGroups.
- Exposing more of the CiliumNetworkPolicy rule surface (ICMP, `toServices`, `fromRequires`, `fromCIDRSet`/`toCIDRSet` exceptions) as demand appears.
- **Richer spec validation.** Create and update already validate the highest-risk fields synchronously — CIDR syntax, port range (1–65535 or a valid named port) and the protocol enum (TCP/UDP/SCTP/ANY) — and reject a bad value with `Invalid` instead of writing a policy that Cilium would silently discard. Still deferred: FQDN `matchName`/`matchPattern` syntax, endpoint-selector key conventions, and cross-rule consistency checks, which currently pass through to Cilium and surface (if at all) asynchronously in the agent.
