# Cozystack Self-assessment

This document is a security self-assessment for [Cozystack](https://github.com/cozystack/cozystack), prepared by the Cozystack maintainers as part of the project's application for CNCF Incubation.

## Metadata

| | |
| --- | --- |
| Assessment Stage | Completed |
| Software | <https://github.com/cozystack/cozystack> |
| Security Provider | No. Cozystack is a cloud platform, not a security product; security is a supporting property, not its primary function. |
| Languages | Go, Helm, shell |
| SBOM | Not published at this time. Image builds can emit a CycloneDX SBOM attestation (`SBOM=1` in `hack/common-envs.mk`), but this is disabled by default; SBOM generation on release is a planned improvement. |

### Security links

| Document | URL |
| --- | --- |
| Security policy | <https://github.com/cozystack/cozystack/blob/main/SECURITY.md> |
| Threat model | <https://github.com/cozystack/cozystack/blob/main/docs/security/threat-model.md> |
| Governance | <https://github.com/cozystack/cozystack/blob/main/GOVERNANCE.md> |
| Maintainers | <https://github.com/cozystack/cozystack/blob/main/MAINTAINERS.md> |
| Contributor ladder | <https://github.com/cozystack/cozystack/blob/main/CONTRIBUTOR_LADDER.md> |
| Code of Conduct | <https://github.com/cozystack/cozystack/blob/main/CODE_OF_CONDUCT.md> |
| Adopters | <https://github.com/cozystack/cozystack/blob/main/ADOPTERS.md> |
| OpenSSF Best Practices | <https://www.bestpractices.dev/projects/10177> |
| OpenSSF Scorecard | <https://scorecard.dev/viewer/?uri=github.com/cozystack/cozystack> |

## Overview

Cozystack is an open-source Kubernetes-based platform for building cloud infrastructure and offering managed services (databases, virtual machines, tenant Kubernetes clusters) with multi-tenancy and GitOps delivery. This overview summarizes the security-relevant architecture; the full design-level threat model lives in the repository at [`docs/security/threat-model.md`](https://github.com/cozystack/cozystack/blob/main/docs/security/threat-model.md).

### Background

Cozystack is delivered as a **management (root) Kubernetes cluster** onto which tenants, managed services, virtual machines, and tenant Kubernetes clusters are layered. The centre of the security model is the aggregated API server **`cozystack-api`**. Tenants never write privileged Kubernetes objects (HelmRelease, Deployment, Secret, RBAC) directly. Instead they write thin, virtual `apps.cozystack.io/*` **Application** custom resources, and `cozystack-api` translates each one into a Flux `HelmRelease` whose chart reference is fixed server-side. The set of Application kinds is registered dynamically from `ApplicationDefinition` custom resources, so the platform's catalogue of managed services is data-driven rather than hard-coded.

When OIDC is enabled (opt-in; `authentication.oidc.enabled` is off by default), authentication is centralized through Keycloak/OIDC; in the default OIDC-disabled mode the dashboard is fronted by a token-proxy. Authorization uses Kubernetes RBAC (with roles aggregated per tenant) plus ValidatingAdmissionPolicy. Networking isolation is provided by Cilium. Platform PKI (including the aggregated-API serving certificate) is issued by cert-manager. Tenant Kubernetes clusters run on dedicated Kamaji-hosted control planes with Talos worker nodes running as KubeVirt virtual machines.

**Usage modes.** Cozystack is deployed in two ways. As a **managed-services backend** (roughly half of deployments), an external provisioning system authenticates with its own ServiceAccount and creates the tenant resources it needs — no OIDC involved. Used **directly**, a human is granted OIDC access to the dashboard and the Cozystack API and operates it, constrained by RBAC to the `apps.cozystack.io` group within their tenant.

### Multitenancy model

Cozystack combines hard and soft multitenancy:

- **Hard multitenancy for tenant-controlled code.** Anything in which a tenant can run arbitrary code — VMs and tenant Kubernetes clusters — is isolated at the hardware-virtualization / dedicated-control-plane boundary, and the tenant has full access inside it. Those VMs and tenant-cluster nodes are themselves ordinary, unprivileged pods in the tenant namespace: governed by Pod Security Standards, denied access to the management-cluster API, and subject to the tenant network and admission policies.
- **Soft multitenancy for managed data services.** Databases and queues run in the management cluster but expose only a fixed, validated set of parameters and never let a tenant execute arbitrary code (unsafe options are disabled). A tenant therefore cannot compromise the management cluster by swapping a container image or enabling a dangerous setting.

### Actors

Actors are decomposed by **trust boundary**: two components are distinct actors only when compromise of one does not automatically compromise the other. Cozystack has five trust tiers (T0 highest trust, T4 untrusted).

| Actor | Trust tier | Privileges | Isolation mechanism |
| --- | --- | --- | --- |
| Platform / cluster administrator (`system:masters`) | T0 — root of trust | Full control of the management cluster; owns GitOps, Talos config, and the Keycloak realm. | Trusted by design; nothing isolates it. |
| GitOps / Flux delivery layer | T1 — platform control plane | Reconciles HelmReleases/Kustomizations into arbitrary privileged manifests; effectively cluster-admin. | Tenants cannot create HelmReleases (RBAC); not tenant-network-reachable. |
| `cozystack-api` aggregated API server | T1 — platform control plane | Converts tenant CRs into HelmReleases; can CRUD HelmReleases, Secrets, and Cilium policies cluster-wide. Acts as the policy-enforcing gateway. | Constrains tenants via fixed chart reference, name/quota/host validation, and admission. |
| System operators / controllers | T1 — platform control plane | Domain-scoped cluster privileges (provision control planes, mint certs/secrets, program CNI). | Distinct ServiceAccounts; isolated from tenants by RBAC and network policy. |
| Tenant administrator (OIDC `<tenant>-admin` / `<tenant>-super-admin`) | T2 — tenant boundary | CRUD on `apps.cozystack.io` application kinds and `sdn.cozystack.io/securitygroups` in its namespace subtree; read on pods/services/ingress plus `delete` on a fixed set (pods/services/endpoints/events); no create/update of arbitrary core resources. (The ClusterRole also lists `persistentvolumes`, but that grant is inert under the namespace-scoped RoleBinding, since PVs are cluster-scoped.) `super-admin` holds the `apps.cozystack.io/*` wildcard (and can create child `Tenant`s); `admin` has an enumerated allowlist excluding some kinds (e.g. `tenants`, `monitoring`, `etcd`, `ingress`). | Namespace-scoped RBAC + admission policies + quota; no HelmRelease or cross-tenant access. |
| Tenant user (OIDC `<tenant>-view` / `<tenant>-use`) | T2 — tenant boundary | Read-only view plus operational verbs (VM console/vnc/portforward, start/stop/restart). Cannot create Application CRs. | Strictly dominated by tenant-admin; cannot escalate. |
| Parent vs child (nested) tenant | T2 — tenant boundary | Parent ServiceAccounts bind into descendant namespaces; children do not bind upward. Quota hierarchically capped. | Ancestor→descendant trust is one-way. |
| Tenant workloads / managed services | T3 — workload boundary | Run tenant-controlled code; no cluster-API rights beyond their ServiceAccount. | Namespace-scoped RBAC/identity isolate API objects; on the network, Cilium egress confines each tenant pod to its own subtree + platform system services + `world`, so unrelated tenants cannot reach each other over the pod network (enforced on egress; ingress is open to `cluster` — see Non-goals). |
| Tenant Kubernetes clusters | T3 — workload boundary | Own kube-apiserver (Kamaji hosted control plane, running as pods on the management cluster) and Talos worker nodes as KubeVirt VMs. | Worker nodes carry a VM/kernel boundary; the Kamaji control-plane pods are isolated by namespace/RBAC/NetworkPolicy, not by virtualization. |
| External end user / unauthenticated internet | T4 — untrusted | Reaches published gateway hostnames, the Keycloak login and dashboard oauth2-proxy, and any raw LoadBalancer L4 endpoints a tenant opts to expose (`external: true`, e.g. VPN/MariaDB). | OIDC authentication and hostname policies for the gateway path; Cilium `world` rules. Tenant-published LoadBalancer services are reachable directly, outside the hostname policies and OIDC. |

### Actions

| Action | Actors (from → to) | Data crossing boundary | Checks | Result |
| --- | --- | --- | --- | --- |
| Authenticate | End user (T4) → Keycloak → kube-apiserver | OIDC token (`groups`, `preferred_username`) | When OIDC is enabled (off by default): kube-apiserver OIDC verification; UIs fronted by oauth2-proxy (the `gatekeeper` Deployment). In the default OIDC-disabled mode the dashboard uses a token-proxy and the LINSTOR GUI is not externally exposed. | In OIDC mode, an identity whose `groups` map to the `<tenant>-view/use/admin/super-admin` roles. In the default mode the token-proxy forwards a Kubernetes-issued ServiceAccount JWT as the bearer token, so authorization follows that ServiceAccount's RBAC. The tenant ServiceAccount is bound into the tenant's view/use/admin **and super-admin** RoleBindings, so a holder of the tenant SA token has super-admin-level access over the tenant subtree (equivalent to the `<tenant>-super-admin` group), not merely the base `cozy:tenant` role. |
| Create an application | Tenant admin (T2) → `cozystack-api` (T1) → Flux (T1) → workload (T3) | Application CR `spec` (Helm values only) | RBAC (delegated to kube-apiserver via `system:auth-delegator`); name and reserved-key validation; admission chain; **server-fixed chart reference**. | Managed service provisioned from a vetted chart; tenant never touched a HelmRelease. |
| Create a tenant / nested tenant | Tenant super-admin (T2) → `cozystack-api` → Flux → new namespace (T2) | Tenant CR (`spec.host`, quotas) | Name checks; **best-effort hierarchical quota cap** (declaration-time snapshot; concurrent writes can briefly overshoot, with a runtime controller backstop); `cozystack-tenant-host-policy` restricts `spec.host` to trusted callers. | New tenant namespace with RBAC, NetworkPolicies, Keycloak groups, quota. |
| Publish a hostname (Gateway/HTTPRoute/TLSRoute/Ingress) | Tenant Application spec (T2) → Flux/controllers (T1) → core kube-apiserver | Rendered Gateway/Route/Ingress hostnames; namespace host label | Tenants have **no** RBAC to write Gateways, Routes, Ingresses, or namespace labels — Flux/controllers render them from the tenant's Application spec. The hostname VAPs (Gateway/Route policies, and `cozystack-ingress-hostname-policy` for the default legacy-Ingress path) validate rendered hostnames against the namespace host label (defense-in-depth). The apex is platform-set (`cozystack-tenant-host-policy` restricts `Tenant.spec.host` to trusted callers; the namespace label is immutable). Route/namespace/Ingress policies fail-closed; the Gateway-listener policy is fail-open when the label is absent. | A tenant cannot claim or hijack another tenant's platform-apex hostname, provided its namespace carries the host label. |
| Create a network policy | Tenant admin (T2) → `cozystack-api` | `sdn.cozystack.io/SecurityGroup` spec | RBAC; projected to a `CiliumNetworkPolicy` under the API server's ServiceAccount; tenants cannot write raw `cilium.io` objects. | CNI policy programmed on the tenant's behalf. |
| Delete a platform-critical object | Any actor → core kube-apiserver | DELETE request | `cozystack-no-delete-guardrail` denies DELETE on `platform.cozystack.io/no-delete=true` objects. Operational guidance, not adversarial defense: an actor able to update the object can remove the label first (tenants cannot reach these roots; only T0/T1). | Accidental teardown of platform roots blocked. |
| East-west traffic | Workload (T3) → workload/system | Packets | Cilium enforces policy at both endpoints. Each tenant pod's egress is confined to its own subtree, platform system services, and `world` (out-of-cluster); ingress is open to `world`+`cluster`. A flow needs the source's egress to allow it, so an unrelated tenant has no egress path to another tenant's pod. | Cross-tenant pod-to-pod traffic is **denied** (by source egress). Within a subtree, parent→descendant is allowed; child→parent is denied except for specific ancestor services (vminsert, etcd, ingress). Caveats (see Non-goals): open ingress and LoadBalancer hairpin. |

### Goals

- **Tenant isolation (API / identity layer).** A tenant cannot read, modify, or delete an *unrelated* tenant's Application CRs, workloads, or secrets, and a descendant cannot reach its ancestors. Parent→child access is intentional (ancestor ServiceAccounts and OIDC groups are bound one-way into descendant namespaces).
- **Network isolation between unrelated tenants.** Unrelated tenants cannot reach each other over the pod network: each tenant pod's Cilium egress is confined to its own subtree, platform system services, and `world`, and Cilium requires the source's egress to allow a flow — so there is no egress path between unrelated subtrees. Enforced on egress; ingress is permissively open (see Non-goals for caveats).
- **Constrained self-service provisioning.** Tenants may instantiate only vetted `ApplicationDefinition` kinds with server-fixed chart references; they cannot run arbitrary Helm charts or write raw privileged manifests.
- **Hostname and tenancy integrity.** Tenants cannot claim or hijack a platform-apex hostname belonging to another tenant, on both dataplanes. Gateway API path: HTTPRoute/TLSRoute/Gateway-listener hostnames must be within the tenant's own apex. Default legacy-Ingress path (`cozystack-ingress-hostname-policy`): a hostname within the tenant's own apex or entirely outside the platform root apex (an external custom domain, e.g. a tenant Kubernetes cluster's Proxied ingress) is allowed, while a hostname under the platform root apex but outside the tenant's own sub-apex — plus hostless rules and `defaultBackend` catch-alls — is denied. Fail-closed (the Gateway-listener policy relies on the platform-set, immutable `namespace.cozystack.io/host` label).
- **Hierarchical resource-quota enforcement (best-effort).** At declaration time a child tenant's declared quota is checked against its parent's remaining budget; the check is a non-transactional admission snapshot (concurrent writes can briefly overshoot) with the tenant-quota controller as the runtime backstop.
- **Strong isolation for tenant Kubernetes/VM workloads** via Talos worker nodes as KubeVirt VMs (hardware virtualization); the tenant control plane is a Kamaji hosted control plane running as pods on the management cluster, isolated by namespace/RBAC/network rather than by virtualization.
- **Central, token-based authentication** when OIDC is enabled (opt-in; off by default): Keycloak/OIDC with group-to-role mapping into RBAC. The default OIDC-disabled mode fronts the dashboard with a token-proxy instead.
- **Defense in depth at admission**, enforced at both the core kube-apiserver and the aggregated `cozystack-api`, with mostly fail-closed tenancy policies (the Gateway-listener policy is the documented exception noted above).
- **Accidental-deletion protection** for platform-critical roots via the no-delete guardrail (operational guidance, not adversarial defense — a privileged T0/T1 actor can remove the label and delete).

### Non-goals

- Defending against a compromised **cluster administrator, node root, or infrastructure administrator** — trusted by design and out of scope (`SECURITY.md`).
- Treating the **T1 platform control plane** (`cozystack-api` ServiceAccount, Flux, system operators) as a sandbox; these hold cluster-admin-equivalent power.
- **Upstream-only vulnerabilities** not introduced or worsened by Cozystack packaging or defaults.
- **Ingress-side isolation of tenant pods.** Tenant-pod ingress is open to `world` and `cluster`, so a tenant pod is reachable by any in-cluster peer whose own egress permits it (platform components) or by a workload not governed by Cozystack's tenant egress policies. Cross-tenant isolation is enforced on the *egress* side, not tenant-pod ingress.
- **Blocking cross-tenant reachability through externally-exposed services.** If a tenant publishes a LoadBalancer external IP, another tenant can reach it via `world` egress.
- **Noisy-neighbour / side-channel** guarantees for co-located tenant pods beyond namespace scoping (and, for tenant Kubernetes/VMs, virtualization).
- Acting as an **egress firewall**: tenant workloads may egress to `world` by default.
- Supporting **pre-release builds and end-of-life versions**.

## Self-assessment use

This self-assessment is created by the Cozystack team to perform an internal analysis of the project's security. It is not intended to provide a security audit of Cozystack, or function as an independent assessment or attestation of Cozystack's security health.

This document serves to provide Cozystack users with an initial understanding of Cozystack's security, where to find existing security documentation, Cozystack's plans for security, and a general overview of Cozystack security practices, both for the development of Cozystack as well as the security of Cozystack.

This document provides the CNCF TAG-Security with an initial understanding of Cozystack to assist in a joint assessment, necessary for projects under incubation. Taken together, this document and the joint assessment serve as a cornerstone for if and when Cozystack seeks graduation and is preparing for a security audit.

## Security functions and features

### Critical

These components are load-bearing: a failure or compromise directly defeats a tenant-isolation or platform-integrity guarantee.

- **`cozystack-api` (aggregated API server).** Converts tenant Application CRs into Flux HelmReleases with a server-fixed chart reference, and enforces name/quota/host validation plus in-process admission. The primary control that constrains what tenants can provision.
- **Core kube-apiserver + RBAC + ValidatingAdmissionPolicy.** Per-tenant aggregated ClusterRoles grant no HelmRelease or arbitrary core writes; VAPs enforce hostname/tenancy integrity (mostly fail-closed — see Overview) and deletion protection.
- **Flux (source/helm/kustomize controllers).** Executes releases with cluster-admin-equivalent power; the delivery mechanism for all platform and tenant workloads.
- **Cilium.** The sole enforcement of network isolation between tenants; also the backend for tenant `SecurityGroup` policies.
- **Keycloak** (when OIDC is enabled; opt-in, off by default). The identity source whose OIDC group claims drive RBAC.
- **cert-manager.** Mints the aggregated-API serving CA (the trust anchor for the `apps.cozystack.io` API) and tenant TLS.
- **Kamaji + KubeVirt + Talos** (for tenant Kubernetes clusters). Provide hardware-virtualized isolation for tenant worker nodes; the Kamaji control plane runs as pods on the management cluster.

### Security relevant

These support security but are more contained; a compromise is less likely to escalate on its own.

- **Cozystack controllers/reconcilers** (`ApplicationDefinition`, tenant gateway/ReferenceGrant, quota, workload monitors).
- **oauth2-proxy front doors** (the `gatekeeper` Deployment) for the dashboard and LINSTOR GUI.
- **external-secrets-operator, clustersecret-operator, OpenBao** for secret sync, replication, and vaulting.
- **Curated tenant registries** (`tenantsecret`, `tenantmodule`, `tenantnamespace`) that expose only labelled resources and strip internal labels.
- **reloader, flux-plunger, kube-ovn webhook, lineage-controller webhook.**

The primary trust-boundary invariant is that tenants cannot influence the chart reference, `valuesFrom`, or write HelmReleases directly. Any change to tenant RBAC or the Application REST layer that weakened this would collapse T2→T1 isolation and should be reviewed accordingly.

## Project compliance

Cozystack does not currently claim compliance with, or certification against, any specific security standard or regulatory framework (for example PCI-DSS, ISO 27001, SOC 2, or GDPR). Downstream adopters may operate Cozystack within their own compliance programs, but the project itself makes no formal attestation.

## Secure development practices

### Development Pipeline

- **Source control and review.** All changes land through GitHub pull requests. `.github/CODEOWNERS` requires maintainer review; the project deliberately requires a single approving review rather than two, to keep contribution velocity acceptable (branch protection requiring multiple reviews was considered and intentionally not adopted).
- **Commit sign-off (DCO).** Contributor guidance requires `--signoff` on every commit (`docs/agents/contributing.md`, `.gemini/styleguide.md`). Note honestly: DCO is documented but **not yet enforced by an automated check**, and the sign-off requirement is not yet reflected in the top-level `CONTRIBUTING.md`. Adding a DCO check is a planned improvement.
- **Automated checks on pull requests.** A pre-commit workflow validates generated-code/schema freshness; unit and end-to-end tests run in CI.
- **Static analysis (SAST).** CodeQL runs on pull requests, on push to `main`, and weekly (Go, manual build mode), intended as a required check that blocks merge on new error-severity alerts. Additional Go linters (`gosec`, `govulncheck`, `golangci-lint`) are not yet wired into CI and are a planned improvement.
- **Dependency updates.** Renovate manages Go modules, Dockerfiles, and GitHub Actions, with OpenSSF OSV vulnerability alerts enabled and automerge disabled. There is no Dependabot configuration; Renovate is the dependency-update tool.
- **Supply-chain posture.** OpenSSF Scorecard runs weekly with published results. Most GitHub Actions are pinned by commit SHA; a small number of recently added workflows still use floating tags and are being brought into line. Renovate is configured to pin digests; applying digest pins across container base images is in progress.
- **Signed / reproducible builds.** Honestly noted as gaps: release images are **not** signed (no cosign/sigstore), build provenance is currently disabled (`--provenance=false`), SBOM generation is plumbed but off by default, and reproducible builds are not yet established. These are recognized improvement areas for the incubation timeframe.

### Communication Channels

- **Internal.** Maintainers coordinate over Slack, Telegram, and project email (`cncf-cozystack-*` addresses), and in community meetings.
- **Inbound.** Users reach the project through GitHub issues and Discussions; security reports follow the private routing in `SECURITY.md` (see below).
- **Outbound.** The project announces releases and security fixes through GitHub Releases, in-repository changelogs, the documentation site at <https://cozystack.io>, and GitHub Security Advisories (GHSA) when applicable.

### Ecosystem

Cozystack is a CNCF Sandbox project. It is built on and integrates a broad set of cloud-native components — Kubernetes, Flux, Cilium, KubeVirt, Kamaji, Talos, cert-manager, external-secrets, and Keycloak — and positions itself as a full platform layer for building managed-service clouds on top of Kubernetes.

## Security issue resolution

### Responsible Disclosures Process

The vulnerability reporting process is documented in [`SECURITY.md`](https://github.com/cozystack/cozystack/blob/main/SECURITY.md). Reporters are asked not to use public issues, discussions, pull requests, or chat channels for vulnerabilities.

Honest note on the current reporting channel: the repository **does not yet publish a dedicated private security mailbox in-tree**, and GitHub Private Vulnerability Reporting is not wired as the intake channel. Instead, reporters are asked to contact a maintainer listed in `CODEOWNERS` through an existing private channel, or to use a public channel only to request a private contact path without disclosing details. Standing up a dedicated private intake (a security mailbox and/or GitHub Private Vulnerability Reporting) is a recognized improvement. There is also a minor inconsistency to reconcile: `SECURITY.md` routes reports to "maintainers listed in `CODEOWNERS`", but the `CODEOWNERS` and `MAINTAINERS.md` lists have diverged.

#### Vulnerability Response Process

Per `SECURITY.md`, the maintainers commit to:

- Acknowledge receipt within **3 business days**.
- Perform initial triage and severity assessment within **7 business days**.
- Keep the reporter informed as a fix and disclosure plan are developed.

Resolution follows a coordinated-disclosure model: reporters are asked to keep details private until a fix or mitigation is available and users have had a reasonable opportunity to upgrade. The project may request or publish a GHSA and/or CVE. Reporters are credited unless anonymity is requested. A specific embargo window / maximum-disclosure timeline is not currently committed to and could be added.

### Incident Response

Cozystack does not maintain a standalone incident-response runbook, and this is a gap to close. Partial coverage exists today: the release process supports patch releases and backports with priority handling of security fixes, and `CONTRIBUTOR_LADDER.md` defines a concrete offboarding procedure (completed within five business days) that removes GitHub team access, downgrades org roles, updates `CODEOWNERS`, and audits and rotates GitHub Actions secrets, registry tokens, and App keys — the access-revocation and credential-rotation half of incident response. Public disclosure of a resolved incident would follow the outbound channels above (release notes, changelog, GHSA).

## Appendix

### Known Issues Over Time

Cozystack has not yet published a history of security issues or advisories. As the project formalizes GHSA usage and (once available) a dedicated intake channel, resolved vulnerabilities will be tracked as GitHub Security Advisories on the repository.

### OpenSSF Best Practices

Cozystack participates in the OpenSSF Best Practices Badge program as project [#10177](https://www.bestpractices.dev/projects/10177) (the badge is shown in the repository README). The project is working through the passing-level criteria; the live status is reflected on the badge page. Cozystack also runs OpenSSF Scorecard weekly with published results.

### Case Studies

Cozystack is used in production by the organizations listed in [`ADOPTERS.md`](https://github.com/cozystack/cozystack/blob/main/ADOPTERS.md), including Ænix (managed services for cloud providers), Mediatech (Kubernetes distribution for hosted software), Bootstack (cloud-infrastructure operator), gohost (bare-metal Kubernetes), Urmanac (hosting, including WASM/SpinKube), Hidora / Hikube (sovereign multi-datacenter cloud), QOSI (sovereign GPU clouds for a national AI program), and Cloupard (public IaaS/PaaS on bare metal). These adopters exercise the multi-tenancy and managed-service model described above across several regions.

### Related Projects / Vendors

Cozystack occupies a space adjacent to platforms such as Deckhouse, Rancher/RKE, Red Hat OpenShift, and KubeVirt-based virtualization platforms. Its distinguishing security-relevant characteristics are: an aggregated API server that exposes managed services as virtual `apps.cozystack.io` resources (so tenants provision from vetted charts rather than writing Helm/manifests directly), tenant isolation enforced through RBAC plus native ValidatingAdmissionPolicy, and hardware-virtualized tenant Kubernetes clusters built on Talos and KubeVirt.
