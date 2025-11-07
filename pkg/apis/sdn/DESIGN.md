# Cozystack Security Groups (Model A) — Design v0.1

**Status:** Draft (decisions captured; open questions intentionally deferred)

## 1) Summary

Cozystack introduces **Security Groups (SGs)** that map 1:1 to Cilium (Clusterwide)NetworkPolicies. Users attach SGs to managed applications; under the hood, the controller labels the application’s pods with a presence label `securitygroups.cozystack.io/sg-<id>: ""`. Each SG is reconciled into a **single** Cilium policy whose `endpointSelector` matches that label. This yields AWS-like semantics (attach/detach) with minimal object fan‑out.

## 2) Goals / Non‑Goals

**Goals**

- Provide a stable, AWS‑style abstractions for traffic control (attach/detach SGs to managed apps).
- Keep policy count ≈ number of SGs (not attachments).
- Avoid disruptive changes to clusters that do not opt into SGs.

**Non‑Goals**

- We do not redesign tenant isolation here; existing isolation policies remain.
- We do not introduce per‑attachment bespoke policy logic (that would be Model B).

## 3) Terminology

- **SG** — SecurityGroup (Cozystack CRD), user‑facing construct.
- **CNP/CCNP** — CiliumNetworkPolicy / CiliumClusterwideNetworkPolicy (backend object).
- **Attachment** — association between a managed application and one or more SGs; implemented via pod labels.

## 4) High‑Level Architecture

1. User creates **SecurityGroup** (`cozystack.io/v1alpha1`), defining ingress/egress rules.
2. SG controller reconciles SG → a **single** CNP/CCNP named `cozy-sg-<id>` with:
   - `endpointSelector`: `matchExpressions: [{ key: securitygroups.cozystack.io/sg-<id>, operator: Exists }]`.
   - Rules translated from SG spec.
3. User attaches SG(s) to a managed application via either:
   - Application subresource: `/<apigroup>/<kind>/<name>/securitygroupattachments`, or
   - SG subresource: `/securitygroups/<name>/attach`.
4. Attachment controller resolves the target → applies/maintains pod template labels so new pods inherit; best‑effort patches existing pods; removes labels on detach.

## 5) CRDs (user‑facing)

### 5.1 SecurityGroup

```yaml
apiVersion: cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: sg-db
  namespace: tenant-a           # optional; cluster scope supported (see spec.scope)
spec:
  scope: Cluster                 # Cluster | Namespace
  description: "DB access group"
  selectorLabelKey: securitygroups.cozystack.io/sg-db  # controller defaults to this if unset
  egress:
    - toSecurityGroups: [ sg-web ]
      toPorts:
        - protocol: TCP
          port: 5432
    - toFQDNs: [ "*.apt.example.org" ]
    - toCIDRs: [ "10.0.0.0/8" ]
  ingress:
    - fromSecurityGroups: [ sg-web ]
      toPorts:
        - protocol: TCP
          port: 5432
```

**Notes**

- `scope: Cluster` → controller renders a **CiliumClusterwideNetworkPolicy**; otherwise **namespaced CNP**.
- `selectorLabelKey` is the authoritative label for attachments. Presence semantics (`""` value) are used.

### 5.2 SecurityGroupAttachment

```yaml
apiVersion: cozystack.io/v1alpha1
kind: SecurityGroupAttachment
metadata:
  name: sga-postgres-foo
  namespace: tenant-a
spec:
  subjectRef:                    # ONE of subjectRef | selectorRef (subjectRef preferred for managed apps)
    group: postgresql.cnpg.io
    kind: Cluster
    name: foo
  securityGroups:
    - sg-db
    - sg-monitoring
```

**Notes**

- This is the **source of truth** the controller watches; subresources (below) create/update these objects.

## 6) Subresources (user entry points)

Subresources provide ergonomic, RBAC‑friendly ways to manage attachments without exposing internal wiring.

### 6.1 Managed application subresource — `/securitygroupattachments`

- **Path (example):** `/apis/postgresql.cnpg.io/v1/namespaces/<ns>/clusters/<name>/securitygroupattachments`
- **POST/PATCH body:** `{ securityGroups: ["sg-db", "sg-monitoring"] }`
- **Semantics:** Create or upsert a `SecurityGroupAttachment` bound to this subject.
- **RBAC intent:** Tenant admins with rights on the app can manage its attachments.

### 6.2 Security group subresource — `/attach`

- **Path:** `/apis/cozystack.io/v1/namespaces/<ns>/securitygroups/<name>/attach`
- **POST body:**

```yaml
subjects:
  - group: postgresql.cnpg.io
    kind: Cluster
    name: foo
    namespace: tenant-a
```

- **Semantics:** Append or reconcile `SecurityGroupAttachment` objects for the listed subjects.
- **RBAC intent:** Platform admins (or delegated roles) can attach SGs to many resources.

> Both subresources are symmetric and operate by creating/updating `SecurityGroupAttachment` objects. They are idempotent.

## 7) Labeling Conventions (authoritative)

- **Attachment label (presence):** `securitygroups.cozystack.io/<sg-id>: ""`
- **Optional marker label:** `securitygroups.cozystack.io/enabled: "true"` (used to exclude SG‑managed pods from legacy namespace policies during migration).
- Controllers ensure pod **templates** carry the labels; existing pods are patched best‑effort.

## 8) Controller Responsibilities

### 8.1 SG Controller

- Reconcile SG → CNP/CCNP `cozy-sg-<id>` with the `endpointSelector` above.
- Translate SG `ingress/egress` to Cilium spec (toEndpoints/fromEndpoints using other SGs’ label keys; toFQDNs; toCIDRs; toPorts).
- Ensure ownerRefs/labels for traceability; emit Events; expose Prometheus metrics.

### 8.2 Attachment Controller

- Resolve `subjectRef` to the managed application → identify pod template(s) (e.g., StatefulSet, Deployment, KubeVirt VM pods).
- Apply/remove attachment labels on templates; patch live pods where feasible.
- Maintain a projection status on `SecurityGroupAttachment.status` (attached pod count, last reconcile time).
- Garbage‑collect labels when attachments are deleted; use finalizers for cleanup.

## 9) Backend Cilium Objects (rendered)

### 9.1 Example generated CCNP for `sg-db`

```yaml
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: cozy-sg-db
spec:
  endpointSelector:
    matchExpressions:
      - key: securitygroups.cozystack.io/sg-db
        operator: Exists
  ingress:
    - fromEndpoints:
        - matchExpressions:
            - key: securitygroups.cozystack.io/sg-web
              operator: Exists
      toPorts:
        - ports:
            - port: "5432"
              protocol: TCP
  egress:
    - toFQDNs:
        - matchPattern: "*.apt.example.org"
```

## 10) RBAC Model (concise)

- \*\*ClusterRole: \*\*\`\` — full CRUD on `SecurityGroup` and `/attach`, cluster‑wide.
- \*\*Role: \*\*\`\` — can use app `/securitygroupattachments` within their namespaces; read SGs cluster‑wide.
- Controllers need write access to: CNP/CCNP, workload templates in target namespaces, and pods (patch labels best‑effort).

## 11) Safety & Interactions (decisions baked in)

- **Scoping:** Policies only apply to pods carrying the SG label; other pods remain unaffected.
- **Union of allows:** SG rules are additive with existing policies; to avoid accidental widening, legacy namespace policies should be updated to **exclude** pods with `securitygroups.cozystack.io/enabled=true`.
- **No host‑policy changes:** Host networking (nodeSelector) is out of scope for SGs.

## 12) Migration (minimal, non‑disruptive)

1. Deploy controllers (no attachments yet).
2. Create tenant‑specific **baseline SG(s)** if desired (DNS, metrics, etc.).
3. Update legacy namespace policies to **not select** SG‑managed pods (e.g., `DoesNotExist` match on `securitygroups.cozystack.io/enabled`).
4. Attach SGs to a canary app; verify datapath; roll out.
5. Optionally deprecate legacy namespace egress‑only policies after adoption.

## 13) Observability

- `SecurityGroupAttachment.status` summarizes effective attachments.
- Pods receive an annotation `securitygroups.cozystack.io/attached: "sg-a,sg-b,..."` for quick inspection.
- Events on SG and Attachment objects for attach/detach and reconciliation failures.

## 14) Examples

### 14.1 User defines an SG and attaches it to a Postgres cluster

```yaml
apiVersion: cozystack.io/v1alpha1
kind: SecurityGroup
metadata:
  name: sg-db
spec:
  scope: Cluster
  ingress:
    - fromSecurityGroups: [ sg-web ]
      toPorts:
        - protocol: TCP
          port: 5432
```

```yaml
apiVersion: cozystack.io/v1alpha1
kind: SecurityGroupAttachment
metadata:
  name: sga-postgres-foo
  namespace: tenant-a
spec:
  subjectRef:
    group: postgresql.cnpg.io
    kind: Cluster
    name: foo
  securityGroups:
    - sg-db
```

### 14.2 Resulting pod labels (applied by controller)

```yaml
metadata:
  labels:
    securitygroups.cozystack.io/enabled: "true"
    securitygroups.cozystack.io/sg-db: ""
```

---

**This document intentionally focuses on the decided mechanics (Model A, label‑based attachments, SG→Cilium one‑to‑one). Extensions (e.g., reusable CIDR/FQDN groups, per‑attachment overrides, policy linting) can be added later without changing the core model.**

