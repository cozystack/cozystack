# Cozystack Roadmap

**Status:** Living document — updated quarterly by the maintainers.
**Horizon:** May 2026 – May 2028 (two-year forward window).
**Last updated:** 2026-07-16.

This document describes where Cozystack is heading. It is the authoritative
public roadmap. The granular issue-level view lives in
[Project V2 #1 (Cozystack Roadmap)](https://github.com/orgs/cozystack/projects/1);
this file explains the *why* and the *how they fit together*.

---

## Executive Summary

By **May 2028**, Cozystack should be a credible candidate for the open standard
platform layer for building clouds — recognized inside the CNCF ecosystem,
adopted in production by organizations across multiple geographies and verticals,
and governed openly by a community wider than any single company. Linux
illustrates what "open standard for a category" looks like over decades; the
two-year window covered here is the *starting condition* for that long arc, not
the destination.

Concretely, success by the end of the horizon means all of the following are
true at once:

- **CNCF maturity.** Project is **Incubating** (achieved 2026 Q3) with a
  Graduated application drafted (2028 Q1). Third-party security audit complete
  and findings remediated.
- **Open specifications.** **Eight Cozystack-authored specifications** —
  CP-API (Package), CT-API (Tenant), Host OS Contract, GitOps Engine
  Interface, Fleet API, Cloud Native AI Inference Gateway, Block Replication
  CSI Extension, Tenant FinOps API — are in or through the relevant standards
  body process. At least one is ratified, and at least four have a second
  independent implementation.
- **Security posture.** OpenSSF Best Practices **Gold**, OSPS Baseline **L3**,
  OpenSSF Scorecard **≥ 9.0**, **SLSA Build L3**, CSAF VEX disclosure
  pipeline, EU CRA-compliant reporting, all-green CLOMonitor checks.
- **Storage independence.** **blockstor** as the production-default replicated
  block storage in Cozystack 3.0, with LINSTOR maintained as a legacy opt-in.
  blockstor itself a CNCF-hosted independent project.
- **AI/ML platform.** **Inference Gateway GA**, GPU FinOps tenant view,
  managed model registry, multi-tenant GPU sharing (MIG/vGPU/time-slicing),
  agentic and multi-modal workload support — all production-grade and
  documented as reference for NVIDIA NCP-aligned deployments.
- **Multi-cluster.** **Fleet API v1.0 GA**. Federation across ≥ 100 clusters
  in publicly attested production deployments.
- **Marketplace.** **≥ 100 certified applications** across sub-categories
  (AI/ML, Databases, Messaging, Observability, Security, Networking, DevTools).
  Third-party publisher program operating. Signed packages, SBOM-attested
  artifacts, vulnerability gates.
- **Conformance Program.** **Certified Cozystack Provider / App / Storage
  Backend / Host OS** all operating, with multiple certified entities per
  category.
- **Governance and community.** ≥ 10 active SIGs · ≥ 10 maintainers from
  ≥ 5 different organizations · ≥ 30 public production adopters · public
  vendor-neutrality dashboard live and maintained.
- **Adoption channels.** "Tested on Cozystack" hardware program with ≥ 20
  certified configurations across ≥ 3 vendors · co-published host OS profiles
  with ≥ 4 Linux distributions · managed Cozystack offerings from European
  service providers · ≥ 12 conference talks per calendar year · **Cozystack
  Admin Certification (CCA) GA** with measurable labor-market presence.
- **Documentation and materials.** Versioned docs per supported release line ·
  auto-generated API reference per release · 11 published reference
  architectures · technical whitepaper · security whitepaper · TCO calculator
  · vendor lock-in analysis · ≥ 5 published case studies · migration
  playbooks from VMware / OpenStack / Proxmox / KVM · Cozystack Academy with
  four courses live.
- **Compliance enablers for downstream.** PCI DSS v4.0.1 reference
  architecture · SOC 2 Type II evidence pack · ISO/IEC 5230 (OpenChain)
  self-certified · EU CRA shared-responsibility matrix · CIS Kubernetes
  Benchmark profile · Threat Model published.

None of these outcomes is achievable by code alone. Each requires sustained,
coordinated work across **twelve strategic tracks** (§5), the **Standardization
Strategy** (§8), and the **Conformance Program** (§6). The remainder of this
document is the breakdown of how each commitment above is delivered.

The single fastest way to derail this plan is to remain a single-vendor
project. Maintainer diversity (§5 Track 11) is the highest-priority risk
mitigation in the entire roadmap.

---

## 1. Vision

Cozystack aims to become the open standard platform layer for building clouds —
the same way Linux became the open standard for operating systems. Concretely,
that means a platform that:

- Stays **vendor-neutral** and is governed openly under the CNCF.
- Defines a **stable Core API** with explicit backwards-compatibility
  guarantees.
- Ships a **Conformance Program** so that multiple distributions can exist
  while remaining interoperable.
- Treats **enterprise reliability, security, and compliance** as first-class
  concerns, not bolt-ons.
- Is a **first-class AI/ML platform** without giving up its general-purpose
  PaaS roots.

The two-year goal is to graduate from CNCF Sandbox to Incubating in 2026 and
prepare the conditions for a Graduated application by the end of the horizon.

## 2. Strategic Layers

Cozystack is shaped as a layered offering:

- **Cozystack Core** — stable APIs, operator, package model, tenant model,
  auth, backup framework, observability primitives.
- **Cozystack Conformance** — public test suites for platforms, applications,
  storage backends, host OS profiles, and providers.
- **Cozystack Marketplace** — certified applications with version channels,
  signed artifacts, SBOM, and compatibility metadata.
- **Cozystack Enterprise Profile** — LTS release lines, upgrade policy,
  auditability, public scale envelope, compliance-ready architecture.
- **Cozystack AI Platform** — GPU lifecycle, datasets, model serving, model
  registry, vector databases, tenant isolation, FinOps for tokens/GPU-time.
- **Cozystack Fleet** — multi-cluster, multi-region, unified policy and
  identity, federated networking and lifecycle.

## 3. Current State (May 2026)

### 3.1 What is shipped

- `v1.0` — package-based architecture, `Package` / `PackageSource`,
  `cozystack-operator`, backup framework, VM backup, RWX for AI/ML workloads,
  non-Talos installs.
- `v1.2` — OpenSearch, VPC peering, `SchedulingClass`, clustered VictoriaLogs,
  production stabilization.
- `v1.3` — storage-aware scheduling via LINSTOR extender, LINSTOR GUI, VM
  default images, app-level observability, S3 metering, cross-namespace VM
  restore.
- `v1.4` — backup strategies, Flux 2.8, new `cozystack-ui`, cozy-tls,
  Redis TLS, app scheduling, CI/e2e hardening.
- `v1.5` — shipped (latest stable line).
- `v1.6` — in release-candidate.

### 3.2 Roadmap items already completed

The following items from
[Project V2 #1](https://github.com/orgs/cozystack/projects/1) have been
delivered:

- Public access for end users (#1257)
- OIDC integration (#1258)
- Tenant isolation improvements (#1250)
- GPU support in VMs and tenant Kubernetes (#1244)
- Topology and affinity settings (#1242)
- NUMA support for VMs (#1245)
- Air-gap installation support (#1243)
- New Cozystack UI foundation (#1252)
- Plugin system (#1259)
- Audit log (#1263)
- Backup and recovery system (#1248)
- Decomposition and extensibility (#1249)
- API stabilization and `v1.0.0` release (#1251)
- Installation on multiple Linux distributions (#1260)

### 3.3 In flight or rescheduled

- Internal Development Platform bundle (#1247) — Q3 2026.
- Selecting managed application versions (#1246) — rescheduled to Q3 2026.
- Grafana dashboard with SLA for each service (#1262) — rescheduled to
  Q3 2026.
- API Gateway support (#1265) — closed.
- Distroless images (#1261) — Q3 2026.
- Automated platform updates (#1266) — Q3 2026.

### 3.4 Tracks in flight not yet captured in Project V2

- **blockstor** — Go-native Kubernetes block-storage control plane (LINSTOR
  REST-compatible). Active development in
  [cozystack/blockstor](https://github.com/cozystack/blockstor).
- **kilo-clustermesh-operator** — WireGuard-based multi-cluster mesh control
  plane.
- **cozystack-ui** — pure SPA Console/Marketplace UI talking directly to the
  Kubernetes API with `ApplicationDefinition`-based dynamic discovery.
- **ccp** — Cozystack Claude Plugins marketplace for AI-assisted platform
  operations.
- **security-scanner** *(private, maintainer-only — kept private for security
  reasons)* — automated CVE pipeline (Trivy) across the whole organization
  with maintainer triage workflow.
- **standalone-trustd** — Talos `trustd` extracted as a standalone
  certificate-signing service.
- **cozystack-scheduler** — custom scheduler hooks for workload-aware
  scheduling (GPU, NUMA, locality).
- **cnai-landscape** — CNAI landscape submission preparation.

The two-year plan brings these tracks into the public roadmap.

## 4. Two-Year Plan at a Glance

| Half | Primary Goal | Headline Outcomes |
|---|---|---|
| H2 2026 (Jun–Dec) | Incubation push + enterprise foundations | CNCF Incubation, OSPS Baseline L2, OpenSSF Passing badge, public release-train policy, Marketplace alpha, host-OS contract documented, Argo CD experimental, GitLab integration alpha, refreshed website + brand kit + technical whitepaper v1, first five reference architectures published. |
| H1 2027 (Jan–Jun) | AI platform v1 + multi-cluster GA | Cozystack 2.0, blockstor alpha, Fleet beta, AI Platform MVP, SOC 2 Type I readiness, OSPS Baseline L3, OpenSSF Silver, Argo CD beta, GitLab CI integration, Academy fundamentals + administrator courses, first five published case studies. |
| H2 2027 (Jul–Dec) | Enterprise scale + Marketplace mature | Public scale envelope, blockstor beta, AI Platform GA, certified-apps program, SLSA Build L3, SOC 2 Type II evidence collection, OpenSSF Gold, Argo CD GA, GitLab integration GA, CCA certification beta, threat model and third-party audit published. |
| H1 2028 (Jan–May) | Graduated-tier prep + standard play | Cozystack Conformance Program, certified providers/apps/storage/OS, third-party security audit completed, EU CRA compliance posture, Graduated application drafted, full Academy + CCA GA, distribution program landing live. |

## 5. Tracks

The roadmap is organized into twelve tracks. Each track lists deliverables by
quarter and an indicative owning SIG (see §7 for SIG formation timeline).

### Track 1 — Platform Core (SIG-Platform)

**Goal.** Stable APIs, predictable upgrades, formal backwards-compatibility
contract.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Close in-flight items #1246 #1247 #1262. Cut `v1.5.0`. Publish `API Stability Policy` (alpha/beta/stable lanes, deprecation window). Scope the **GitOps Engine Abstraction** CzEP — decouple package delivery from the underlying engine so Flux remains the default while a second engine becomes feasible. |
| 2026 Q4 | Distroless images (#1261). Automated platform updates (#1266). Public **Release Trains** policy: `stable`, `fast`, `LTS`. Public support matrix for Kubernetes / host OS / Cilium / KubeVirt / storage backends / **GitOps engines**. **Argo CD experimental support** as an alternative GitOps engine — package model adapts `Package` / `PackageSource` to `Application` / `ApplicationSet` semantics; documented as `experimental` and opt-in at install time. |
| 2027 Q1 | **Cozystack 2.0** — backwards-compatibility contract published; major API revision based on production feedback; HA control-plane improvements (stretched control plane, multi-AZ, etcd backup automation). **Argo CD support reaches alpha** — supported install path for greenfield clusters; not yet a migration target for existing Flux-based deployments. |
| 2027 Q2 | `Platform Health API` covering operator, packages, GitOps engine (Flux or Argo CD), storage, networking, backups, ingress, auth. Explicit lifecycle states for apps, backups, restores, VMs, tenant clusters. **Argo CD support reaches beta** — feature parity with Flux for tenant app delivery, including `ApplicationSet` patterns for multi-tenant fan-out. |
| 2027 Q3 | Performance optimizations based on H1 2027 benchmarks. Per-tenant resource isolation hardening (full cgroup v2 adoption, memory.high pressure). **Argo CD support reaches GA** as a fully-supported alternative engine; Flux→Argo CD migration tooling enters alpha. |
| 2027 Q4 | Documented Flux↔Argo CD migration paths for existing deployments. Both engines remain first-class. |
| 2028 Q1 | **Cozystack 3.0** — second major API revision. Deprecation of legacy `v1` surfaces on a documented timeline. |

### Track 2 — Testing & Conformance (SIG-Testing)

**Goal.** Testing as a first-class deliverable. A public **Cozystack
Conformance Suite** that third parties can certify against.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Close current e2e push (parallel post-install, event-driven backstops, pre-pull caching). Document the test architecture. |
| 2026 Q4 | **Cozystack Conformance Suite v0.1** — public, runnable test suite covering install, tenant lifecycle, app lifecycle, backup/restore, networking, storage. Chaos-Mesh integration in e2e (node failure, network partition, DRBD split-brain scenarios). |
| 2027 Q1 | AI-augmented test generation: an LLM-based assistant proposes test cases for new behaviors on each PR; humans review and commit. Performance regression CI — benchmark suite gates merge. |
| 2027 Q2 | Long-running soak environment — a permanent cluster that runs reliability tests for weeks. Upgrade matrix — every RC passes the full N-2 → N upgrade matrix. |
| 2027 Q3 | **Public scale tests** — automated runs against 100/500/1000 nodes; published scale reports. |
| 2027 Q4 | **Cozystack Conformance Suite v1.0** — stable test contract for the Conformance Program. |
| 2028 Q1–Q2 | Third-party distributions can earn a "Cozystack Certified" badge by passing the suite. |

### Track 3 — Security, Certifications & Compliance (SIG-Security)

**Goal.** Bring the project to a security posture that supports CNCF
Incubation, then Graduated, and supports downstream commercial users in
audited environments. **This is the largest single track in the roadmap.**

Cozystack's security work is structured around ten external frameworks plus
one internal program. Each is treated as a sub-project with concrete tasks.

#### Track 3.1 — OpenSSF Best Practices Badge (Metal series)

The legacy and still-required "Passing / Silver / Gold" badge program.
CNCF Graduation requires only the Passing badge; Silver and Gold are suggested,
not required, and are self-chosen targets here.

| Quarter | Target | Concrete tasks |
|---|---|---|
| 2026 Q3 | **Passing** (67 criteria) | Enroll project on [bestpractices.dev](https://www.bestpractices.dev/). Address all `MUST` items: public version-controlled source ✓, distinct contributing/security/code-of-conduct docs ✓ (already present), public bug tracker ✓, OSS license (Apache-2.0) ✓, build reproducibility documentation, automated test suite documented, public release notes per version, secure communication for vulnerability reports (private maintainer mailbox), warn on common cryptographic mistakes, vulnerability response policy with SLA, no public exposure of sensitive data in commits/issues. |
| 2026 Q4 | **Silver** (55 additional criteria) | DCO or CLA documented and enforced, two-person review for substantive changes (already required), branch protection on all release branches, automated `golangci-lint` + `gosec` on every PR (extend coverage), documented coding standards, documented release-signing process, signed Git tags for releases, security policy with vulnerability disclosure timeline, README references security policy, contribution metadata documented (size, scope, review). |
| 2027 Q2 | **Gold** (23 additional criteria) | Documented secure-design / threat model, fuzzing for critical components (blockstor, custom-scheduler, controllers), public SAST findings management, dynamic analysis (`go vet -race`, kube-conformance), CI-driven dependency-license check (FOSSA/ScanCode), reproducible builds for container images, branch protection includes required signed commits where supported. |

#### Track 3.2 — OSPS Baseline (Open Source Project Security Baseline)

The maturity-model framework — launched Feb 2025, current version dated
2026-02 — that is replacing "Gold-only"
thinking. Has three levels organized by project maturity. Cozystack already
qualifies for L3 by user-base; the work is bringing controls up to spec.

The eight control families (each ID `OSPS-XX-YY`):

- **AC** — Access Control
- **BR** — Build & Release
- **DO** — Documentation
- **GV** — Governance
- **LE** — Legal
- **QA** — Quality
- **SA** — Security Assessment
- **VM** — Vulnerability Management

| Quarter | Target level | Concrete tasks |
|---|---|---|
| 2026 Q3 | **L1 — Basic Hygiene** | All L1 controls. The project has most already; gap closure focus: documented release verification instructions (`OSPS-DO-03.*`), documented support scope (`OSPS-DO-04.01`), documented secret-management policy for CI (`OSPS-BR-07.02`). Self-assessment published in repo. |
| 2026 Q4 | **L2 — Standardized** | Two-person review required for primary branch (`OSPS-QA-07.01` — extend to enforcement), test execution documentation (`OSPS-QA-06.02`), tests required for major changes (`OSPS-QA-06.03`), threat-model artifacts (`OSPS-SA-03.02`), VEX documents for component-level vulnerabilities (`OSPS-VM-04.02`). |
| 2027 Q2 | **L3 — High Assurance** | Minimum-privilege CI/CD job assignments (`OSPS-AC-04.02`), formal review before granting escalated permissions (`OSPS-GV-04.01`), trusted-collaborator input sanitization in pipelines (`OSPS-BR-01.04`), unique-identifier release-asset association (`OSPS-BR-02.02`), publish verification instructions for release integrity AND authorship (`OSPS-DO-03.01` + `03.02`), document EOL timelines per release line (`OSPS-DO-05.01`), SBOM with compiled assets (`OSPS-QA-02.02`), equal-or-stricter security on multi-repo releases (`OSPS-QA-04.02`), dependency remediation thresholds + automated blocking (`OSPS-VM-05.01–03`), code-weakness remediation thresholds + automated blocking (`OSPS-VM-06.01–02`). |

The repository ships an `OSPS-BASELINE.md` self-assessment that maps every
control to evidence and is regenerated on each release.

#### Track 3.3 — OpenSSF Scorecard

Automated weekly checks producing a numeric score. CNCF sets no numeric
Scorecard threshold for either tier; the levels below are self-chosen targets
(around ≥ 7.0 by Incubation, ≥ 8.0 toward Graduated).

| Quarter | Target | Concrete tasks |
|---|---|---|
| 2026 Q3 | **≥ 6.0** | Enable Scorecard GitHub Action across all org repositories. Publish badge in README. Address quick wins: pinned dependencies in CI, branch-protection metadata, signed releases, no binary artifacts in source tree. |
| 2026 Q4 | **≥ 7.0** | Token-permissions least-privilege in every workflow, dependency-update-tool (Dependabot/Renovate enforced), code-review for every merge, signed commits where practical. |
| 2027 Q2 | **≥ 8.0** | SAST integration, fuzzing presence, CII Best Practices score reflection. |
| 2027 Q4 | **≥ 9.0** | All applicable checks pass; only systemic exceptions remain documented. |

#### Track 3.4 — SLSA — Supply-chain Levels for Software Artifacts

Build-side integrity, tracked against the current SLSA v1.2 specification
(Build track). SLSA is not a stated CNCF graduation requirement; Build L3 is a
self-chosen aspirational target.

| Quarter | Target | Concrete tasks |
|---|---|---|
| 2026 Q3 | **Build L1** | Provenance generation for every release artifact (container images, Helm charts, binaries). Publish as `.intoto.jsonl` next to releases. |
| 2026 Q4 | **Build L2** | Hosted-build platform (GitHub Actions OIDC + slsa-github-generator). Authenticated provenance with signed `_provenance` files. |
| 2027 Q3 | **Build L3** | Hardened build platform: isolated build steps, non-falsifiable provenance, no human override of build pipeline; reusable workflows audited; secrets handled via OIDC short-lived tokens only. |

#### Track 3.5 — CNCF CLOMonitor

CNCF-wide automated check dashboard. Must be all-green for Incubation review.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Verify all CLOMonitor checks. Address gaps: project metadata, governance, security policy, adopters file completeness, contributor ladder, license, code of conduct, OpenSSF badge, artifact signing, SBOM. |

#### Track 3.6 — CNCF Security Self-Assessment + Third-Party Audit

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Complete the **CNCF TAG Security self-assessment** ([template](https://github.com/cncf/tag-security/blob/main/community/assessments/guide/self-assessment.md)). Publish as `SECURITY-SELF-ASSESSMENT.md` in repo. Include attack-surface diagram, trust boundaries, threat model, and known weaknesses with mitigation status. |
| 2027 Q3 | Engage a **third-party audit** (e.g. Ada Logics with OSTIF funding, or NCC Group). Scope: core controllers, blockstor, custom scheduler, multi-tenancy boundaries, supply chain. Required for Graduated tier. |
| 2027 Q4 | Publish audit results and remediation status. Triage findings into milestones. |

#### Track 3.7 — EU Cyber Resilience Act (CRA)

The CRA's reporting obligations apply from **11 September 2026**; full
obligations from **11 December 2027**. Cozystack itself, as an OSS project,
falls under the *open-source software steward* regime of CRA Article 24
("Obligations of open-source software stewards"), not the *manufacturer*
obligations. But downstream commercial vendors who package Cozystack are
manufacturers. We document the project so that downstream compliance is
tractable.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Publish a **`CRA-COMPLIANCE.md`** describing: (a) the project's status as an open-source software steward under Article 24, (b) the CVE coordinated-disclosure process aligning with CRA timelines (24h initial / 72h info / 14d final after fix or workaround for actively exploited issues), (c) SBOM availability per release. |
| 2026 Q3 | Adopt **CSAF VEX** for vulnerability advisories. Publish each advisory as a CSAF JSON file alongside the GHSA. |
| 2026 Q4 | SBOM in **SPDX** and **CycloneDX** formats published with every release; signed with cosign. |
| 2027 Q1 | Document the **shared-responsibility matrix** between Cozystack maintainers and downstream commercial vendors for incident response. |
| 2027 Q4 | Verify the disclosure pipeline against CRA timeline obligations with a tabletop exercise. |

#### Track 3.8 — SOC 2 (Process Readiness, not Certification)

An OSS project does not get SOC 2 itself — services do. We deliver a **process
evidence pack** that any commercial Cozystack distributor can use in their
own SOC 2 audit.

| Quarter | Deliverables |
|---|---|
| 2027 Q1 | **SOC 2 Type I evidence pack** — `SOC2-EVIDENCE/` directory with: change-management policy, access-control policy for the GitHub org, incident-response runbook, vendor/dependency-management policy, backup/DR documentation, monitoring/audit-log policy. |
| 2027 Q3 | **Type II evidence-collection automation** — GitHub Actions snapshot monthly evidence (permission grants, branch-protection state, release-signing logs, CVE triage decisions) to `SOC2-EVIDENCE/`. |
| 2027 Q4 | Tabletop exercise with downstream Cozystack distributors to validate the evidence pack supports their audits. |

#### Track 3.9 — PCI DSS v4.0.1 (Reference Architecture)

A `PCI-DSS.md` reference architecture for deploying Cozystack as a CDE
(Cardholder Data Environment).

| Quarter | Deliverables |
|---|---|
| 2027 Q2 | Publish `reference-architectures/pci-dss-v4/` containing: network segmentation (Cilium NetworkPolicy + Kubernetes NetworkPolicy), MFA enforcement (OIDC config), audit-log architecture (Cozystack audit log + Loki + immutable retention), vulnerability-management workflow, secure-defaults documentation, key-management (External Secrets Operator + HashiCorp Vault / OpenBao), responsibility matrix. |
| 2027 Q3 | Validate the reference architecture with a friendly PCI QSA review. |

#### Track 3.10 — ISO/IEC 5230 (OpenChain) — License Compliance

| Quarter | Deliverables |
|---|---|
| 2026 Q4 | **OpenChain self-certification** — `LICENSE-COMPLIANCE.md` documenting the project's SPDX-correct license metadata, third-party license inventory (auto-generated from `go.mod` and Helm chart dependencies), and license-clearing workflow for new dependencies. |
| 2027 Q1 | Automated license-policy gate in CI (`fossa`/`scancode`). |

#### Track 3.11 — Internal Security Programs

**Vulnerability response.** Formalize the **Cozystack Security Response
Team** with rotating on-call.

- 2026 Q3: Publish private security mailbox (`security@cozystack.io`).
  Document SRT membership and rotation in `SECURITY.md`.
- 2026 Q3: Publish **Vulnerability SLA**: triage in 3 business days,
  severity classification in 7, fix targets — critical 7d, high 30d,
  medium 90d, low 180d.
- 2026 Q4: Embargo-policy document. Coordinated disclosure timeline default
  90 days (subject to active exploitation override).
- 2026 Q4: GitHub Security Advisories (GHSA) workflow documented and
  enforced. Each advisory mirrored to CVE and CSAF VEX.
- 2027 Q1: Bug-bounty program scoped (low budget initially; community-funded
  via OSTIF / GitHub Sponsors).

**Hardening defaults.**

- 2026 Q4: All shipped images **distroless** or **scratch-based** where
  practical (#1261).
- 2026 Q4: All shipped images run **non-root** and **read-only root FS**.
- 2027 Q1: All shipped pods ship with **seccomp profiles** (RuntimeDefault
  minimum, custom restricted where feasible) and **AppArmor** policies.
- 2027 Q2: All shipped controllers use **least-privilege ServiceAccounts**
  with explicit audit.
- 2027 Q2: Cluster-wide default **NetworkPolicies** for the platform layer.
- 2027 Q3: **mTLS** enforced between Cozystack components (Linkerd or Cilium
  service mesh, mode TBD).

**Supply chain.**

- 2026 Q3: All releases signed with **cosign** keyless OIDC.
- 2026 Q4: All releases ship **SBOM** in SPDX and CycloneDX.
- 2027 Q1: **Sigstore Rekor transparency log** entries for every release.
- 2027 Q2: **Helm chart signing** for all charts shipped from cozystack org.
- 2027 Q3: **Fuzzing** for security-sensitive components: REST parsers,
  CRD validators, controller reconcilers (oss-fuzz integration).

#### Track 3.12 — Certifications Roll-up

| Cert / Badge | 2026 Q3 | 2026 Q4 | 2027 Q1 | 2027 Q2 | 2027 Q3 | 2027 Q4 | 2028 Q1+ |
|---|---|---|---|---|---|---|---|
| OpenSSF Best Practices | Passing | — | — | Silver | — | — | Gold |
| OSPS Baseline | L1 | L2 | — | L3 | maintain | maintain | maintain |
| OpenSSF Scorecard | ≥ 6.0 | ≥ 7.0 | — | ≥ 8.0 | — | ≥ 9.0 | maintain |
| SLSA Build | L1 | L2 | — | — | L3 | maintain | maintain |
| CLOMonitor | all-green | maintain | maintain | maintain | maintain | maintain | maintain |
| CNCF Self-Assessment | published | — | — | refresh | refresh | refresh | refresh |
| CNCF Third-Party Audit | — | — | — | — | engage | publish | remediate |
| EU CRA posture | docs+CSAF | SBOM live | shared-resp matrix | — | — | tabletop | maintain |
| SOC 2 evidence | — | — | Type I pack | — | Type II auto | tabletop | maintain |
| PCI DSS ref-arch | — | — | — | published | QSA review | — | — |
| ISO 5230 (OpenChain) | — | self-cert | CI gate | — | — | — | — |

### Track 4 — Storage Independence: blockstor (SIG-Storage)

**Goal.** A Kubernetes-native, Go-native, community-governed block storage
control plane. Long-term: Cozystack's default replicated block storage;
near-term: a CNCF Sandbox candidate in its own right.

Positioning: **not** "anti-LINSTOR". LINSTOR solved a real problem; the
ecosystem needs a Go-native, Kubernetes-native, community-governed
alternative. blockstor is built with LINSTOR REST API compatibility so
existing clients (`linstor-csi`, `piraeus-operator`, `ha-controller`,
`golinstor`) continue to work.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | blockstor governance pack: `GOVERNANCE.md`, `CONTRIBUTING.md`, `MAINTAINERS.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, release process, contributor ladder. Feature-parity audit against LINSTOR. Conformance tests against `linstor-csi` and friends. Vanilla Kubernetes demo. |
| 2026 Q4 | **CNCF Sandbox application** for blockstor as an independent project. Pilot deployments on 3 production clusters in shadow mode (alongside LINSTOR, not replacing it). `cozystack-migrate-storage` tooling. |
| 2027 Q1 | blockstor **alpha** in Cozystack 2.0 as an opt-in storage backend. LINSTOR marked deprecated in `v2.0` release notes. New blockstor capabilities: VDUSE backend, shared-LUN production mode, BYOK key rotation, snapshot shipping to S3-compatible storage. |
| 2027 Q2 | Engagement with LINBIT: contribute back relevant fixes; align long-term coexistence story. **Required for CNCF vendor-neutrality posture.** |
| 2027 Q3 | blockstor **beta** in Cozystack 2.1: dual-stack mode default, blockstor used for new installs unless opted out. Migration tooling tested at scale. Production-scale tests: ≥ 1000 PVs with DRBD replication, ≥ 100 TB shared storage. |
| 2027 Q4 | Performance benchmarks vs LINSTOR published. DR documentation. Storage Conformance Suite v1. |
| 2028 Q1–Q2 | blockstor **GA** in Cozystack 3.0. LINSTOR support enters legacy mode (security backports only). |

### Track 5 — Host OS Strategy (SIG-OS)

**Goal.** Reduce single-source dependency on Talos Linux without forking it
into a standalone distribution prematurely. A full Linux distribution fork
is a multi-year, multi-engineer investment that risks turning Cozystack into
a distro vendor at the expense of being a platform standard.

The correct sequence:

1. Document the **`Host OS Contract`** — what Cozystack expects from any
   host OS (kernel modules, container runtime, kubelet, sysctl, networking,
   storage drivers, GPU drivers, secure-boot story, AppArmor/SELinux
   posture).
2. Build a **Host Conformance Suite** so host profiles can be certified.
3. Bring multiple host profiles (Talos, Ubuntu, Debian, kubeadm, k3s, RKE2)
   to production-quality.
4. Talos stays a **first-class** profile; Sidero stays a partner.
5. Fork Talos only if upstream blocks critical storage / GPU / kernel /
   security changes — and even then, fork *minimally*, upstream-first.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Publish `HOST-OS-CONTRACT.md`. OS Support Matrix (Tier-1 Talos production-supported; Tier-2 Ubuntu 24.04 / Debian 12 / Flatcar community-supported; Tier-3 RHEL 9 / Rocky 9 experimental). |
| 2026 Q4 | Extract Talos-dependent services as standalone (continuing what `standalone-trustd` established). Targets: `standalone-machined-bootstrap`, `standalone-cluster-config`. |
| 2027 Q1 | Partnership statement with Sidero on long-term collaboration (vendor-neutrality posture). |
| 2027 Q2 | If partnership posture is not achievable, scope a minimal **cozyOS** image (Talos-derived, kept upstream-first, customized only via Talos extensions). |
| 2027 Q4 | Public case studies for multi-OS production deployments. |
| 2028 Q1+ | Decide whether cozyOS becomes a standalone effort. Only if all higher-priority tracks are stable. |

### Track 6 — AI/ML Platform (SIG-AI)

**Goal.** Cozystack as the open standard for running AI/ML workloads on
private cloud and on-prem GPU clusters.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Submit Cozystack to the **CNAI Landscape** (Cloud Native AI). AI workload presets: `kubeflow-bundle`, `inference-bundle` (vLLM / SGLang / TensorRT-LLM with NVIDIA Dynamo), `rag-bundle`, `agentic-bundle`. |
| 2026 Q4 | Managed vector databases as first-class apps: Qdrant, Milvus, Weaviate, pgvector. GPU sharing improvements (MIG, vGPU, time-slicing) in multi-tenant with per-tenant quotas. JupyterHub, Argo Workflows, MLflow as managed apps. |
| 2027 Q1 | **AI Platform MVP**: GPU inventory + GPU quotas, NVIDIA GPU Operator integration, topology-aware GPU scheduling, GPU billing metrics, model serving packages, model registry, S3/RWX dataset workflows. |
| 2027 Q2 | **Inference Gateway** — OpenAI-compatible endpoint routing to Dynamo / vLLM / TRT-LLM under the hood, with token-level billing, rate limiting, model registry integration. Tenant-side GPU FinOps dashboard (token-time, GPU-time, KV-cache hits, prefill/decode split). |
| 2027 Q3 | Agentic workload orchestration managed apps (Letta, AutoGen-class). Temporal as a managed app. Approved-model registry with signed-model provenance. |
| 2027 Q4 | **AI Platform GA**: inference gateway, autoscaling, model lifecycle, private model registry, GPU pools, cost allocation, dataset versioning, secure model import, audit trails. Multi-modal support (video/audio generation workloads with scheduling profiles). |
| 2028 Q1+ | Reference platform for NCP-aligned GPU cloud providers. |

### Track 7 — AI Inside Cozystack (SIG-AI, AI-Ops sub-group)

**Goal.** AI assists in operating the platform itself — read-only and
human-approved first; gradually expand to safe auto-remediation.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Expand the `ccp` (Cozystack Claude Plugins) marketplace to 20+ plugins: `cozy-debug`, `cozy-cve-triage`, `cozy-perf-investigate`, `cozy-cost-optimize`, `cozy-capacity-plan`. |
| 2026 Q4 | `cozyhr` enhancement: LLM-powered Helm-value generation from natural language. |
| 2027 Q1 | **`cozydoctor`** — built-in diagnostics service. Inputs: events, logs, metrics, HelmRelease states, WorkloadMonitor, cozyreport. Output: probable root cause + suggested next step. Read-only mode by default. |
| 2027 Q2 | AI-powered alert routing and root-cause analysis: correlates Prometheus + Loki + Tempo into structured findings. Upgrade impact analyzer for risky packages, immutable fields, breaking changes. |
| 2027 Q3 | Self-healing automation for documented patterns (DRBD split-brain, etcd disk pressure, image-pull backoff) — human-approval gate on every write action; audit log on every action. |
| 2027 Q4 | Capacity planner: forecasts 3–6 months ahead, generates IaC PRs for capacity changes. |

Guardrail: any write-action by AI assistance requires **explicit user
approval** and is **audit-logged**.

### Track 8 — Marketplace & Application Ecosystem (SIG-Apps)

**Goal.** A controlled supply-chain layer, not a YAML storefront.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | `ApplicationDefinition` schema v1: metadata, dependencies, compatibility matrix, version channels. Private OCI registry credentials support. UI catalog in `cozystack-ui`. **Marketplace alpha**. |
| 2026 Q4 | App certification test framework (basic conformance). Signed application packages (cosign). Publisher onboarding documentation. |
| 2027 Q1 | **External Apps Program** — third-party orgs may publish to the marketplace via formalized submission and vetting. Target: 30 apps in marketplace. |
| 2027 Q2 | Paid-app support for commercial publishers. Marketplace federation for private/enterprise instances. |
| 2027 Q3 | Vulnerability scanning gates and SBOM publication for every marketplace app. App lifecycle hooks and upgrade policies. **Marketplace beta**. |
| 2027 Q4 | Target: 100 apps. Sub-categories (AI/ML, Databases, Messaging, Observability, Security, Networking, DevTools). **Marketplace GA**. |
| 2028 Q1–Q2 | Marketplace analytics (success rates, popular apps). **Certified Apps program** GA. |

### Track 9 — Multi-Cluster & Federation (SIG-Network)

**Goal.** Cozystack federation as a first-class concept — operating 10+
clusters as one platform.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | `kilo-clustermesh-operator` GA. Documented use cases. |
| 2026 Q4 | Multi-cluster `ApplicationDefinitions` — deploy an app to N clusters from one control point. |
| 2027 Q1 | **Fleet API alpha**. Tenant-to-tenant allowlisted peering. Per-tenant podCIDR / serviceCIDR planning. Identity federation. |
| 2027 Q2 | **Fleet beta**: cross-cluster mesh, global DNS, centralized policy, multi-cluster observability. Cross-cluster failover automation for stateless workloads. |
| 2027 Q3 | Geo-distributed managed services: PostgreSQL multi-region active-passive; MongoDB sharded cross-cluster. Public reference architectures (multi-region). |
| 2027 Q4 | **Fleet GA**. |
| 2028 Q1+ | 100-cluster federation production case studies. |

### Track 10 — Developer Experience & Reference Architectures (SIG-DX)

**Goal.** Golden paths. Cozystack works "out of the box" for each named
vertical.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Reference architectures published as `reference-architectures/`: GPU cloud provider, hosting provider, telco (5G CNF), fintech (PCI-aligned), research/HPC. **GitLab integration alpha** — GitLab as a first-class repository source for `Package` / `PackageSource` and tenant Helm releases (private repos, deploy tokens, group-level credentials). |
| 2026 Q4 | **`cozystack` CLI 2.0** — single entry point replacing `make` + `kubectl` + `helm` patchwork. **GitLab OAuth handshake** at tenant level for self-service onboarding alongside the existing OIDC flow. |
| 2027 Q1 | Backstage plugin for Cozystack. Crossplane Provider for Cozystack. **GitLab CI integration**: a managed-app **GitLab Runner** that tenants can deploy into their namespace for build pipelines, with per-tenant runner registration tokens and quota integration. |
| 2027 Q2 | OpenTofu/Terraform provider. DORA metrics dashboard for tenants. **GitLab Container Registry and GitLab Helm Registry** supported as first-class sources for Marketplace `ApplicationDefinitions`, alongside OCI registries. |
| 2027 Q3 | Sustainability/carbon metrics (Kepler integration). **Cozystack GitLab plugin** — GitLab CI components for `cozystack-deploy` and `cozystack-promote`, plus a GitLab-native view of tenant deployments. Symmetric work for GitHub Actions. |
| 2027 Q4 | Migration playbooks: from OpenStack, from VMware, from legacy KVM. GitLab integration reaches GA. |

### Track 11 — Ecosystem Standardization & Community (SIG-Governance)

**Goal.** Turn Cozystack from "one organization makes the platform" into
"ecosystem makes the standard". This is the strategic transformation
without which the "Linux of platforms" goal cannot land.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Form SIGs: SIG-Platform, SIG-Storage, SIG-Network, SIG-AI, SIG-Security, SIG-Testing, SIG-DX, SIG-Apps, SIG-OS, SIG-Governance. Weekly SIG meetings on a public calendar. |
| 2026 Q3 | Onboard 2+ maintainers from organizations other than the founding company. (CNCF Incubation expectation.) |
| 2026 Q3 | **Adopter list ≥ 10** production adopters published in `ADOPTERS.md`. |
| 2026 Q3 | **Submit CNCF Incubation application**. |
| 2026 Q3 | Adopt a **Cozystack Enhancement Proposal (CzEP)** process modeled on KEP — status states, sponsorship, voting. The `cozystack/community/design-proposals/` directory becomes the canonical location. |
| 2026 Q4 | Cozystack Annual Report 2026 published. Public quarterly roadmap reviews. |
| 2027 Q1 | First **Cozystack Conference** (in-person or virtual). |
| 2027 Q2 | **Cozystack Academy** — formal training modules, free tier on the website. |
| 2027 Q3 | **Cozystack Admin Certification (CCA)** beta — testing & certification track. |
| 2027 Q4 | **Cozystack Distribution Program** — third parties may build certified distributions (similar to Kubernetes Certified Distributions). |
| 2028 Q1 | Begin work on **CNCF Graduation application**. |
| 2028 Q2 | First public **vendor-neutrality dashboard** — commits-by-org, maintainers-by-org, releases-by-org transparency. |

### Track 12 — Documentation, Website & Community Materials (SIG-Docs)

**Goal.** A documentation, website, and content portfolio that matches the
ambitions of the platform. Adopters, downstream commercial vendors, TOC
reviewers, and conference attendees must each find what they need in less
than two minutes.

This track consolidates work across the [cozystack/website](https://github.com/cozystack/website)
repository, in-tree documentation, reference architectures, compliance
documents, marketing assets, training content, and event materials.

#### 12.1 Website (cozystack.io)

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Refresh top-level **information architecture**: clear paths for *adopters*, *contributors*, *commercial vendors*, *researchers*. Public `/roadmap` page that renders this `ROADMAP.md` plus a live view of Project V2 status. Project status badges (CNCF lifecycle, OpenSSF, OSPS Baseline, Scorecard, SLSA, CLOMonitor) in README and on site landing page. Lighthouse / Core Web Vitals audit, target ≥ 90 across categories. WCAG 2.2 AA accessibility audit. |
| 2026 Q4 | **Adopters page** rendered from `ADOPTERS.md` with logo permissions tracked. **Resource library** section: whitepapers, reference architectures, case studies, comparison docs. Public **events / talks** calendar. **Search** powered by Algolia DocSearch (free for OSS) or self-hosted Meilisearch. Multi-language scaffolding (EN canonical; RU + DE + ZH-CN as community-driven translations). |
| 2027 Q1 | **Marketplace storefront preview** — public-facing read-only view of the Marketplace catalog with filters, ratings, SBOM/signature badges. **Compliance & Security** dedicated section linking OSPS Baseline self-assessment, CNCF self-assessment, third-party audit results, EU CRA posture, SBOM downloads. |
| 2027 Q2 | **Newsletter** with quarterly project updates. SEO programme — target Tier-1 keyword set (private cloud, Kubernetes-native PaaS, GitOps platform, AI infrastructure). Press / media kit at `/press`. |
| 2027 Q3 | **Cozystack Academy** site (`learn.cozystack.io` or `/academy`) — free training tier launches here (paired with Track 11). |
| 2027 Q4 | **Annual Report 2027** as a website-native experience, not just a PDF. Architecture-rebuild assessment — if Docsy is showing limits, prepare a migration plan. |
| 2028 Q1+ | **Distribution Program** landing page with verified distribution list (paired with Track 2 Conformance). |

#### 12.2 Technical Documentation

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | **Documentation Information Architecture v2** — reorganize around user journeys: *Get Started*, *Operate*, *Develop*, *Administer*, *Reference*, *Tutorials*, *Migrate*. **Versioned docs** per supported release line (current: v1.4, v1.5, v1.6-rc; future: stable / LTS / fast). |
| 2026 Q3 | **Install & upgrade docs** brought to production quality for every supported host OS profile (Talos Tier-1; Ubuntu / Debian / Flatcar Tier-2). |
| 2026 Q4 | **Auto-generated API reference** from CRDs and `cozystack-api` OpenAPI spec, published per release. Tested code samples — every code block in docs runs in CI against a live cluster, broken samples block release. |
| 2026 Q4 | **Operator's Handbook** — production-grade runbook collection for common operations: cluster upgrades, tenant onboarding, backup restore, DR exercises, storage migration, network troubleshooting. |
| 2027 Q1 | **Tutorials track** — minimum 15 narrated tutorials covering: first install, first tenant, deploying Postgres, deploying a VM, GPU workload, multi-cluster setup, OIDC integration, backup & restore drill, upgrading. Each tutorial is owned by an SME and reviewed quarterly. |
| 2027 Q1 | **API Stability Policy** doc (referenced from Track 1) lands in `/docs/contributors/api-stability-policy`. |
| 2027 Q2 | **Developer Guide** for Cozystack contributors: package authoring (`Package` / `PackageSource`), `ApplicationDefinition` authoring for Marketplace, controller-runtime patterns, testing patterns, release engineering. |
| 2027 Q2 | **Troubleshooting Atlas** — visual decision tree mapping symptoms → likely causes → diagnostic commands → resolutions. Integrates with `cozydoctor` AI assistant (Track 7). |
| 2027 Q3 | **Migration playbooks** in `/docs/migrate/`: from OpenStack, from VMware vSphere, from legacy KVM/libvirt, from Proxmox, from bare Kubernetes installations. Each playbook is end-to-end runnable and gated by integration tests. |
| 2027 Q4 | **Docs translation pipeline** — community translation workflow with translation memory, glossary, and freshness tracking. Languages: RU, DE, ZH-CN (initial). |
| 2028 Q1+ | Docs versioning consolidation: stable, LTS, and current; deprecated docs archived but discoverable. |

#### 12.3 Reference Architectures (RAs)

Reference architectures live in `reference-architectures/` and are published
both in-repo and on the website. They are paired with each vertical defined
in Track 10.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | RA: **GPU Cloud Provider** (NCP-aligned). RA: **Hosting Provider** (multi-tenant with billing). RA: **Telco / 5G CNF**. RA: **Fintech / PCI-aligned**. RA: **Research & HPC**. Each RA includes architecture diagram, deployment YAMLs, scale envelope, security posture, total-cost-of-ownership notes. |
| 2026 Q4 | RA: **Edge** (small clusters, intermittent connectivity, lightweight host OS). RA: **Government / Sovereign Cloud** (air-gap, audit, encryption-at-rest mandatory). |
| 2027 Q1 | RA: **AI Inference Service Provider** (GPU pools, Inference Gateway, FinOps tenant view). RA: **Multi-Region Active-Passive**. |
| 2027 Q2 | RA: **PCI DSS v4.0.1 CDE Deployment** (paired with Track 3.9). |
| 2027 Q3 | RA: **Carbon-Optimized Workload Placement** (paired with Sustainability metrics in Track 10). |
| 2027 Q4 | RA gallery refresh — case-study links and adopter testimonials per RA. |

#### 12.4 Compliance & Process Documents

Documents that ship in-repo and are kept current as Track 3 progresses.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | `ROADMAP.md` (this document). `OSPS-BASELINE.md` self-assessment. `CNCF-SELF-ASSESSMENT.md`. `CRA-COMPLIANCE.md`. `HOST-OS-CONTRACT.md` (paired with Track 5). Update `SECURITY.md` with private mailbox and Vulnerability SLA. |
| 2026 Q4 | `LICENSE-COMPLIANCE.md` for OpenChain. `SBOM/` directory with SPDX + CycloneDX per release. **Cozystack Annual Report 2026** as PDF and web-native page. **Documented Release Engineering Handbook** for maintainers. |
| 2027 Q1 | `SOC2-EVIDENCE/` evidence pack (paired with Track 3.8). **Shared Responsibility Matrix** for CRA compliance between maintainers and downstream commercial vendors. |
| 2027 Q2 | `PCI-DSS.md` reference architecture doc with QSA-readability check. **Cozystack Enhancement Proposal (CzEP)** process documented in `community/design-proposals/process.md`. |
| 2027 Q3 | **Threat Model** document published (`THREAT-MODEL.md`) — required for OpenSSF Gold and OSPS L3. |
| 2027 Q4 | Third-party security audit findings published in `audits/` with remediation status. **Annual Report 2027**. |
| 2028 Q1 | CNCF Graduation application drafted (paired with Track 11). |

#### 12.5 Marketing & Sales-Enablement Materials

Materials oriented to adopters, downstream commercial vendors, and TOC
reviewers. Vendor-neutral language throughout.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | **Technical whitepaper v1** — "Cozystack Platform Architecture" (~30 pages). **Comparison briefs**: vs OpenStack, vs VMware vCloud Director, vs OpenShift, vs Rancher, vs vanilla Kubernetes — each ~6 pages, factual and balanced. |
| 2026 Q4 | **Security whitepaper** — "Securing Cozystack Deployments" covering hardening, supply chain, secrets, audit, tenant isolation. **TCO calculator** (interactive web page) with documented assumptions. **Vendor lock-in analysis** briefing. |
| 2027 Q1 | **First case studies** — minimum five published case studies covering distinct verticals. Each ≥ 2,000 words, with adopter approval. **Pitch deck** template that adopters can reuse internally. |
| 2027 Q2 | **AI Platform whitepaper** — "Cozystack as an AI/ML Platform" tied to Track 6 GA. **Multi-Cluster whitepaper** — tied to Track 9 Fleet beta. |
| 2027 Q3 | **Cozystack Solution Guides** per vertical — GPU cloud, hosting, telco, fintech, research; each a polished web/PDF asset. |
| 2027 Q4 | **Migration whitepapers** — "Migrating from VMware to Cozystack", "Migrating from OpenStack to Cozystack". Includes runtime/cost/risk comparisons. |
| 2028 Q1+ | **Annual marketing review** — refresh whitepapers, retire stale comparisons, add Graduated-tier-relevant content. |

#### 12.6 Cozystack Academy & Certification Content (with SIG-Governance)

| Quarter | Deliverables |
|---|---|
| 2027 Q1 | **Academy curriculum design**: course tracks (Fundamentals, Administrator, Developer, Architect). Define learning objectives per track. |
| 2027 Q2 | **Fundamentals course** GA — free tier. Video + text + interactive labs (using something like Killercoda or self-hosted equivalents). |
| 2027 Q3 | **Administrator course** GA. **Cozystack Admin Certification (CCA) beta** — exam blueprint, sample questions, proctoring vendor selection. |
| 2027 Q4 | **Developer course** GA. CCA exam delivery infrastructure live. |
| 2028 Q1 | **Architect course** GA. CCA certification GA. Discussion on a Foundation-recognized credential program (parallel to CKA/CKAD/CKS). |

#### 12.7 Press, Brand & Visual Identity

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | **Brand kit** published at `/press` — logo variants (light/dark/mono), color palette, typography, usage guidelines, downloadable in SVG/PNG/PDF. Approved-use cases for downstream distributions documented. **Trademark policy** clarified in line with CNCF norms. |
| 2026 Q4 | **Press kit** — boilerplate, leadership bios, fact sheet, contact channels. Press release templates for releases and milestones. |
| 2027 Q1 | **Conference materials pack** — booth backdrop, slide decks, one-pagers, demo flow scripts, swag-design guidelines (community-funded swag). |
| 2027 Q2 | **Video assets**: 2-minute project explainer, 5-minute architecture deep-dive, vertical demo videos (one per RA). |
| 2027 Q3 | **Style guide v2** — synchronized brand evolution as the project enters Marketplace GA and AI Platform GA phases. |
| 2027 Q4 | **Annual Report 2027** visuals — infographic-quality summary of project metrics, contributor growth, adopter logos. |

## 6. Cozystack Conformance Program

The capstone deliverable of the two-year plan. Run by SIG-Testing and
SIG-Governance jointly.

Four certifications:

- **Certified Cozystack Provider** — a deployment passes the Conformance
  Suite end-to-end on the provider's environment.
- **Certified Cozystack App** — a marketplace application meets app
  conformance (schema, signed artifacts, SBOM, lifecycle hooks, supported
  upgrade paths).
- **Certified Storage Backend** — a CSI-compatible storage backend passes
  the Storage Conformance Suite.
- **Certified Host OS** — a host OS profile passes the Host Conformance
  Suite.

Initial public versions land in 2028 Q1–Q2.

## 7. SIG Formation Timeline

SIGs are formed in two waves:

- **Wave 1 (2026 Q3):** SIG-Platform, SIG-Security, SIG-Testing,
  SIG-Storage, SIG-Docs. These are mandatory for Incubation review.
- **Wave 2 (2026 Q4):** SIG-Network, SIG-AI, SIG-Apps, SIG-DX, SIG-OS,
  SIG-Governance.

Each SIG owns an area of the roadmap, holds a public weekly or biweekly
meeting, maintains a charter in `community/sigs/<sig-name>/charter.md`, and
reports to the maintainer group quarterly.

## 8. Standardization Strategy

Cozystack's long-term ambition — to become the open standard platform layer
for building clouds — cannot be achieved by writing better software alone.
Categories are defined by **specifications, not implementations**. Linux
became Linux because of POSIX, LSB, FHS, and stable kernel ABIs that allowed
distributions to multiply. Kubernetes became Kubernetes because of OCI,
CRI, CSI, CNI, the Operator pattern, and a Conformance Program that
distinguished compliant distributions from incompatible ones. Docker actually
contributed its reference implementations to the community — containerd to the
CNCF and runc to the OCI — yet Docker Engine still lost the Kubernetes runtime
slot, because it never implemented the CRI interface and dockershim was
eventually removed. The lesson is consistent: own the specification, let the
ecosystem own the implementations.

This section enumerates the specifications Cozystack should define and own,
the specifications Cozystack should adopt and champion, the bodies it should
engage with, the distinguishing artifacts that should become the
"Cozystack way," and the adoption playbook required for any of this to
matter.

The work in this section is **cross-cutting** — it touches every track in
§5. SIG-Governance is the primary owner, with each named SIG taking
co-ownership of the spec relevant to its area.

### 8.1 Specifications Cozystack Should Define and Own

These are interfaces where no widely adopted standard exists today and where
Cozystack is well positioned to draft, publish, and shepherd one through a
standards body. Each spec is intended to follow this lifecycle: **CzEP
draft → published spec in `cozystack/community/specs/` → submitted to a
standards body → adopted by at least two independent implementations →
ratified**.

#### 8.1.1 Cozystack Package API (`CP-API`)

**What.** A formal, vendor-neutral specification for cloud-native managed
applications, formalizing the `Package`, `PackageSource`, and
`ApplicationDefinition` constructs that already exist in Cozystack core.
Covers schema, lifecycle hooks, compatibility metadata, SBOM references,
signature requirements, version channels, dependency declarations, and
upgrade semantics.

**Why a standard is needed.** Helm Charts solve the templating problem but
not the lifecycle problem; Operators solve the lifecycle problem but not
the catalog problem. The Marketplace category is fragmented across
ApplicationDefinitions, OperatorHub, Helm Hub, and proprietary catalogs.
A unified, signed, conformance-testable application manifest is a missing
layer.

**Reference implementation.** Cozystack core (`cozystack-operator`,
`cozystack-controller`, `cozyhr`).

**Target venue.** CNCF TAG App Delivery as a draft spec; long-term, a
CNCF-hosted sub-project (sibling to OCI / SLSA).

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2026 Q4 | First public CzEP draft of CP-API v0.1. |
| 2027 Q1 | Submit to CNCF TAG App Delivery for community review. |
| 2027 Q2 | Conformance test suite for CP-API published. |
| 2027 Q4 | Second independent implementation lands (target: a Marketplace partner). |
| 2028 Q2 | CP-API v1.0 ratified. |

#### 8.1.2 Cloud Tenant API (`CT-API`)

**What.** A specification for what it means to be an isolated tenant on a
Kubernetes-based platform. Defines the composition of namespace,
NetworkPolicy, RBAC, ResourceQuota, scheduling boundaries, audit context,
and identity binding required to constitute a "tenant" with explicit
isolation guarantees.

**Why a standard is needed.** Every multi-tenant platform — vCluster,
Kamaji, Capsule, HNC, Rancher Projects, OpenShift Projects — defines
tenancy slightly differently. There is no portable answer to "is workload
X isolated from workload Y in tenant Z." Cross-platform tenancy assurances
matter for compliance auditors, for regulated industries, and for service
providers who need to express tenant boundaries in contracts.

**Reference implementation.** Cozystack `tenant` core package plus
Cilium-based network isolation.

**Target venue.** Kubernetes SIG Multi-Tenancy and CNCF TAG App Delivery
co-sponsorship.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2027 Q1 | First public CzEP draft of CT-API v0.1. |
| 2027 Q2 | Engagement with SIG Multi-Tenancy upstream. |
| 2027 Q3 | Conformance assertions integrated into the Cozystack Conformance Suite. |
| 2028 Q1 | Second independent implementation lands. |

#### 8.1.3 Host OS Contract (`HOC`)

**What.** A specification of what a platform requires from its host
operating system: kernel modules, container runtime, kubelet configuration,
sysctl set, secure-boot expectations, AppArmor/SELinux/seccomp posture,
networking stack (eBPF features, kernel version floor), storage drivers
(DRBD, ZFS, LVM), GPU drivers and CDI compliance, observability surface
(`/proc`, `/sys` access), and bootstrapping interface.

**Why a standard is needed.** Today every Kubernetes platform implicitly
ties to one or two host OS distributions. A formal contract enables
**multi-OS support** as a first-class property, not a porting project. It
also allows host OS distributions (Talos, Flatcar, Bottlerocket, Ubuntu
Server, openSUSE MicroOS) to validate themselves against the contract.

**Reference implementation.** Cozystack host OS profiles (Talos Tier-1;
Ubuntu/Debian/Flatcar Tier-2).

**Target venue.** CNCF TAG Runtime; engagement with Sidero (Talos),
Kinvolk (Flatcar), Amazon (Bottlerocket), and Canonical (Ubuntu) for
co-authorship.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2026 Q3 | `HOST-OS-CONTRACT.md` v0.1 published in-repo (paired with Track 5). |
| 2026 Q4 | Host Conformance Suite v0.1. |
| 2027 Q1 | Co-author engagement with at least two host OS vendor projects. |
| 2027 Q3 | Submit as a draft CNCF specification. |
| 2028 Q1 | First non-Cozystack platform adopts HOC. |

#### 8.1.4 GitOps Engine Interface (`GEI`)

**What.** An abstraction layer above specific GitOps engines (Flux, Argo
CD), modeled on how CRI abstracted Kubernetes from container runtimes.
Defines the contract a platform expects from its GitOps engine: package
reconciliation, drift detection, source authentication, signed-manifest
verification, dependency ordering, status reporting, and upgrade semantics.

**Why a standard is needed.** Enterprises standardize on one GitOps engine
and rarely switch. A platform locked to one engine forces a binary
adoption decision. GEI allows platforms (Cozystack, OpenShift GitOps,
Rancher Fleet, kubectl-only deployments) to express the same intent across
engines, and lets engines evolve independently.

**Reference implementation.** Cozystack's planned GitOps Engine Abstraction
CzEP (Track 1, 2026 Q3).

**Target venue.** OpenGitOps Working Group (CNCF App Delivery), with
co-authorship from Flux and Argo maintainers.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2026 Q3 | CzEP draft of GEI v0.1 (paired with Track 1). |
| 2026 Q4 | Engagement with Flux and Argo maintainer groups. |
| 2027 Q2 | GEI v0.1 supports both Flux and Argo CD as Cozystack engines. |
| 2027 Q4 | Submit to OpenGitOps WG as a community spec. |

#### 8.1.5 Fleet API (Multi-Cluster Federation)

**What.** A federation API that abstracts multi-cluster lifecycle, identity,
networking, policy, and observability into a single declarative surface.
Tenant boundaries traverse clusters; applications declare cluster placement
constraints; policies federate; observability federates.

**Why a standard is needed.** Multi-cluster solutions today (Karmada, Open
Cluster Management, Liqo, KubeFed legacy, Rancher Fleet, ArgoCD
ApplicationSets) overlap in scope but do not interoperate. A federation
landscape with no shared interface forces enterprises to commit to one
vendor's multi-cluster vision.

**Reference implementation.** Cozystack `Fleet API` planned for Track 9
(2027 Q1 alpha → 2027 Q4 GA).

**Target venue.** Kubernetes SIG Multicluster co-sponsored with CNCF TAG
App Delivery.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2027 Q1 | Fleet API v0.1 alpha published; CzEP draft. |
| 2027 Q2 | Engagement with Karmada, OCM, and Liqo maintainer groups for harmonization. |
| 2027 Q3 | Submit to Kubernetes SIG Multicluster. |
| 2028 Q1 | Fleet API v1.0 — Cozystack GA implementation plus one other independent implementation. |

#### 8.1.6 Cloud-Native AI Inference Gateway (`CN-AI-Inference`)

**What.** A specification for an AI inference gateway abstracting model
serving engines (vLLM, SGLang, TensorRT-LLM, Dynamo, Triton) behind a
unified API surface. Includes OpenAI-compatible endpoints, token-level
metering, multi-model routing, model-registry binding, and FinOps metric
emission.

**Why a standard is needed.** Every AI inference service today
re-implements the same gateway: rate limiting, token counting, model
selection, routing to the appropriate engine. There is no portable
contract for what "an inference endpoint" means at platform level.

**Reference implementation.** Cozystack Inference Gateway (Track 6, planned
2027 Q2).

**Target venue.** CNCF CNAI Working Group, MLCommons engagement.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2026 Q4 | Engagement with CNAI WG; submit Cozystack to CNAI Landscape (Track 6 2026 Q3). |
| 2027 Q2 | CN-AI-Inference v0.1 alpha spec published. |
| 2027 Q3 | Working group draft submitted to CNCF CNAI WG. |
| 2027 Q4 | Reference implementation GA in Cozystack. |
| 2028 Q2 | Second independent implementation lands. |

#### 8.1.7 Block Replication CSI Extension (`BR-CSI`)

**What.** An extension to the CSI specification covering replicated block
storage semantics: replication topology, peer discovery, failover, quorum,
split-brain resolution, snapshot replication, and disaster recovery
coordination. blockstor's LINSTOR-compatible REST API is a starting point;
a portable CSI extension is the destination.

**Why a standard is needed.** Replicated block storage today is
vendor-specific (LINSTOR/DRBD, Portworx, OpenEBS Mayastor, Ceph RBD with
mirroring). Migration between storage vendors requires re-architecting
the application's storage assumptions. A CSI extension lets platforms
express replication requirements portably.

**Reference implementation.** Cozystack `blockstor` (paired with Track 4).

**Target venue.** Kubernetes SIG Storage, CSI specification sub-project.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2027 Q2 | First public BR-CSI v0.1 draft. |
| 2027 Q3 | Engagement with Ceph CSI, Portworx, OpenEBS, LINBIT maintainer groups. |
| 2027 Q4 | Submit to SIG Storage as a CSI extension proposal. |
| 2028 Q2 | First non-blockstor implementation lands. |

#### 8.1.8 Tenant FinOps API

**What.** A specification for per-tenant resource accounting and chargeback,
including CPU-seconds, memory-byte-seconds, storage-IOPS-seconds,
network-bytes, GPU-time, AI token-time, KV-cache hits, image-pull bytes,
and other dimensions relevant for multi-tenant platforms. Schema is
emit-friendly (OpenTelemetry-compatible) and consumption-friendly
(OpenCost-compatible).

**Why a standard is needed.** FinOps in multi-tenant Kubernetes is
fragmented: OpenCost handles workload-level cost, Kubecost overlays UI,
but per-tenant attribution at the AI-inference and GPU-time level is
unspecified. Service providers each invent a metering schema.

**Reference implementation.** Cozystack S3 metering (1.3) plus planned
Tenant FinOps view (Track 6 2027 Q2).

**Target venue.** FinOps Foundation Open Source Working Group, OpenCost
sub-project.

**Timeline.**

| Quarter | Milestone |
|---|---|
| 2027 Q2 | First public Tenant FinOps API v0.1 draft. |
| 2027 Q3 | Engagement with OpenCost, Kubecost, and FinOps Foundation. |
| 2027 Q4 | Submit as a working draft to FinOps Foundation OSS WG. |
| 2028 Q1 | Reference implementation in Cozystack GA. |

### 8.2 Specifications Cozystack Adopts and Champions

These are existing standards where Cozystack's role is to be a visible,
high-fidelity adopter and a vocal advocate. Adoption signals that the
standards are production-ready; advocacy ensures the standards continue to
evolve in a direction Cozystack benefits from.

| Standard | Domain | Status in Cozystack | Engagement Action |
|---|---|---|---|
| **OCI (Image, Distribution, Runtime, Artifacts)** | Container packaging | Adopted via container ecosystem | Use OCI Artifacts for `ApplicationDefinition` packaging. |
| **CSI** (Container Storage Interface) | Storage | Adopted; extended via BR-CSI (§8.1.7) | Active SIG Storage participation. |
| **CNI** (Container Network Interface) | Networking | Adopted via Cilium | Cilium maintainer engagement. |
| **CDI** (Container Device Interface) | Device sharing | Adopted via Talos 1.13 default | Use for first-class GPU device sharing. |
| **Kubernetes Gateway API** | Ingress next-gen | `gateway-api-crds` in `packages/system`; needs roll-out | Move tenant ingress to Gateway API as default by 2027 Q2. |
| **MCS-API** (Multi-Cluster Services) | Cross-cluster services | Beta in Cilium since ~1.17–1.18 | Adopt once the platform's Cilium baseline reaches a release with stable MCS-API support. |
| **OpenAPI 3.1** | API specs | Adopted for `cozystack-api` | Auto-publish per release (paired with Track 12.2). |
| **OpenTelemetry** | Observability | Partially adopted (metrics) | Extend to traces and logs by 2027 Q1. |
| **CloudEvents** | Event-driven workflows | Not adopted | Adopt for Marketplace lifecycle hooks by 2027 Q1. |
| **PROXY protocol v2** | Reverse-proxy passthrough | Adopted via `ouroboros` | Maintain. |
| **SLSA** (Build Levels) | Supply-chain integrity | Roadmap Track 3.4 | Target Build L3 by 2027 Q3. |
| **OSPS Baseline** | Security maturity | Roadmap Track 3.2 | Target L3 by 2027 Q2. |
| **CSAF VEX** | Vulnerability disclosure | Roadmap Track 3.7 | Adopt by 2026 Q3. |
| **SPDX + CycloneDX** | SBOM formats | Roadmap Track 3.11 | Publish both per release. |
| **Sigstore (cosign, Rekor, Fulcio)** | Signing & transparency | Adopted for releases | Extend to all `ApplicationDefinitions`. |
| **OpenSSF Best Practices Badge** | Security hygiene | Roadmap Track 3.1 | Target Gold by 2027 Q2. |
| **OpenSSF Scorecard** | Automated security | Roadmap Track 3.3 | Target ≥ 9.0 by 2027 Q4. |
| **OpenChain ISO/IEC 5230** | License compliance | Roadmap Track 3.10 | Self-certify 2026 Q4. |
| **DCO** (Developers Certificate of Origin) | Contribution licensing | Adopted | Maintain. |
| **OpenAI API compatibility** | AI inference endpoints | Roadmap Track 6 | Inference Gateway target compatibility level Q2 2027. |
| **MLCommons Inference benchmarks** | AI workload measurement | Not adopted | Publish Cozystack benchmark profiles by 2027 Q4. |
| **CIS Kubernetes Benchmark** | Hardening | Partial via Talos defaults | Publish Cozystack-specific CIS profile by 2027 Q1. |
| **CRA reporting timelines** | Vulnerability response | Roadmap Track 3.7 | Operate by 2026 Q3. |

### 8.3 Industry Bodies, Working Groups, and Engagement Plan

Standards engagement is people work. Each body listed below needs a
designated Cozystack representative who attends meetings, contributes
proposals, and reports back to the maintainer group.

#### Tier 1 — Engage immediately (H2 2026)

| Body / Group | Why | Cozystack role |
|---|---|---|
| **CNCF TAG App Delivery** | Owns App Delivery WG and OpenGitOps WG; CP-API, GEI, CT-API venues. | Submit proposals; nominate a Cozystack representative. |
| **CNCF TAG Security** | Owns self-assessments, third-party audit framework. | Run self-assessment; engage on threat-model reviews. |
| **CNCF TAG Storage** | Owns CSI sub-project evolution. | Engage on BR-CSI (§8.1.7) and blockstor sub-project. |
| **CNCF TAG Network** | Owns CNI, Gateway API, multi-cluster networking. | Engage via Cilium and kilo-clustermesh-operator. |
| **CNCF CNAI Working Group** | Owns Cloud Native AI landscape and standards. | CN-AI-Inference (§8.1.6); submit to CNAI landscape (Track 6 Q3 2026). |
| **CNCF Multicluster SIG** | Owns multi-cluster harmonization. | Fleet API (§8.1.5); harmonization with Karmada/OCM/Liqo. |
| **OpenSSF** | Owns Best Practices, Scorecard, OSPS Baseline. | Cozystack as OpenSSF-member project; quarterly status reports. |
| **OpenChain Project** | Owns ISO/IEC 5230 license compliance. | Self-certify (Track 3.10 2026 Q4). |
| **Kubernetes SIG Multi-Tenancy** | Owns tenancy patterns. | CT-API (§8.1.2) engagement. |

#### Tier 2 — Engage from H1 2027

| Body / Group | Why | Cozystack role |
|---|---|---|
| **Kubernetes SIG Storage** | Owns CSI specification evolution. | BR-CSI extension proposal. |
| **Kubernetes SIG Release** | Owns Kubernetes release / deprecation cadence. | Track upstream deprecations affecting Cozystack support matrix. |
| **OpenGitOps Working Group** | Owns GitOps principles and patterns. | GEI (§8.1.4); engage with Flux and Argo maintainers. |
| **FinOps Foundation OSS WG** | Owns OpenCost; vendor-neutral FinOps. | Tenant FinOps API (§8.1.8). |
| **MLCommons** | Owns AI/ML benchmarks. | Inference benchmark publication. |
| **OASIS CSAF Technical Committee** | Owns CSAF / VEX format. | Adopt and contribute back. |
| **Sigstore Community / Cosign WG** | Owns signing primitives. | Cozystack as visible adopter; SLSA Build L3 work. |
| **CNCF TAG Sustainability** | Owns green-cloud-native posture. | Carbon metrics (Track 10 2027 Q3); Kepler integration. |

#### Tier 3 — Engage from H2 2027

| Body / Group | Why | Cozystack role |
|---|---|---|
| **IETF** | Owns network protocols (if BR-CSI or Fleet API touch network protocols). | Conditional on standards needing protocol drafts. |
| **NIST** (U.S.) | Cloud security frameworks (SP 800-53, 800-190). | Cozystack-specific guidance documents. |
| **ENISA / BSI (EU and Germany)** | EU CRA, sovereign cloud, C5. | Engagement around EU CRA compliance posture (Track 3.7). |
| **DMTF** | Data center management standards (Redfish, etc.). | Conditional on hardware integration depth. |

### 8.4 Distinguishing Artifacts — The "Cozystack Way"

A category is also defined by a few visible artifacts that become
synonymous with the category. Linux has `/etc`, `ELF`, `Makefile`,
`grep | awk | sed`. Kubernetes has `kubectl apply -f`, `Pod`, `Service`,
`Operator`. Docker had `Dockerfile`. Cozystack needs three to five such
recognizable artifacts.

Candidate artifacts:

| Artifact | What it stands for | Why it differentiates |
|---|---|---|
| **`ApplicationDefinition`** | The Cozystack equivalent of "what a managed cloud-native app is." | If CP-API succeeds, "ApplicationDefinition" enters industry vocabulary as the signed, conformance-tested app manifest. |
| **`Package` / `PackageSource`** | The Cozystack package model, decoupled from Helm rendering. | Eliminates the `helm template lookup` race-condition class. Recognizable as "the Cozystack package model." |
| **`Tenant`** as a first-class CRD | The Cozystack tenancy model with explicit isolation guarantees. | If CT-API succeeds, "tenant" gains a portable meaning. |
| **`cozystack` CLI** | The primary user surface — single entry point. | Replaces `kubectl + helm + make` patchwork; recognizable Cozystack-ism. |
| **`cozydoctor`** | Built-in AI-assisted diagnostics. | Distinctive UX; pairs Cozystack with AI-assisted operations narrative. |
| **Cozystack Certified badges** | Visible certification marks: Certified Provider / App / Storage / OS. | Forces ecosystem to express "Cozystack-compatible" as a verifiable claim. |
| **Cozystack Federation** | Single control plane across many clusters, via Fleet API. | If Fleet API succeeds, "federation" gains a portable meaning. |

Each artifact should be referenced consistently across:

- The website (`cozystack.io`).
- The documentation site.
- All conference talks.
- All whitepapers, briefs, and case studies.
- The brand kit (Track 12.7) — visual treatment of `cozystack` typography
  and badge marks.

### 8.5 Adoption Playbook

Specifications without adopters are paper. Without an adoption playbook,
the work in §8.1–8.4 produces well-written documents that no one
implements. This playbook covers the marketing, partner, and ecosystem
work required for the specifications to be picked up.

#### 8.5.1 Conference and event circuit

| Event | Cadence | Target Cozystack presence |
|---|---|---|
| KubeCon + CloudNativeCon North America | Annual (Nov) | Minimum 2 accepted talks; sponsor booth from 2027. |
| KubeCon + CloudNativeCon Europe | Annual (Mar) | Minimum 2 accepted talks. |
| CloudFest (Europe) | Annual (Mar) | Hosting/service-provider audience; minimum 1 talk. |
| CloudFest Americas | Annual (Nov) | Service-provider audience; minimum 1 talk. |
| Open Source Summit Europe / NA | Annual | Standards-track engagement. |
| FOSDEM | Annual (Feb) | Maintainer-track talks. |
| SREcon | Annual | Operations-narrative engagement. |
| KubeVirt Summit | Annual | VM workload narrative. |
| AI Infra Summit / NeurIPS systems track | Annual | AI Platform narrative; AI inference benchmarks. |
| Cozystack Conference | Annual (first event 2027 Q1, Track 11) | Project's own flagship event. |

Target: **a minimum of 12 accepted talks per calendar year** across the
above events, across diverse contributor organizations.

#### 8.5.2 Hardware vendor program

A "Tested on Cozystack" certification program for hardware platforms.
Modeled on the Linux Foundation's "Tested with Linux" certifications and
the Red Hat Certified Hardware program.

| Quarter | Deliverables |
|---|---|
| 2027 Q1 | Hardware Compatibility Lab established (initially a Cozystack-hosted lab; eventually federated). Test profiles for bare-metal servers, network switches, GPUs. |
| 2027 Q2 | Initial vendor engagements: Dell PowerEdge, HPE ProLiant, Supermicro, Lenovo ThinkSystem, GIGABYTE. NVIDIA NCP Reference Platform validation pipeline. |
| 2027 Q3 | Public Hardware Compatibility List (HCL). |
| 2027 Q4 | At least 20 certified hardware configurations across 3+ vendors. |
| 2028 Q1+ | Storage vendor certifications (VAST, WEKA, DDN, Pure) and network vendor certifications (Arista, Cisco Nexus, Broadcom, Mellanox/NVIDIA Networking). |

#### 8.5.3 Host OS distribution program

Co-publish Host OS profiles with major Linux distributions, validating
against the Host OS Contract (§8.1.3).

| Quarter | Deliverables |
|---|---|
| 2026 Q4 | Talos profile co-signed with Sidero. Initial engagement with Flatcar (Kinvolk / Microsoft), Ubuntu (Canonical), openSUSE MicroOS, Bottlerocket (Amazon). |
| 2027 Q2 | Ubuntu 26.04 LTS profile co-published with Canonical. |
| 2027 Q3 | Flatcar profile co-published. openSUSE MicroOS profile. |
| 2027 Q4 | Bottlerocket profile (if engagement with Amazon succeeds). |

#### 8.5.4 Hyperscaler and provider engagement

Long-term, "Linux of platforms" requires managed Cozystack instances from
multiple service providers — analogous to managed Kubernetes from every
cloud.

| Quarter | Deliverables |
|---|---|
| 2027 Q3 | Engagement with European service providers (Hetzner, OVH, Scaleway, IONOS) for managed Cozystack offerings. |
| 2027 Q4 | Hyperscaler engagement: AWS Marketplace listing; GCP Marketplace listing; Azure Marketplace listing. |
| 2028 Q1+ | First managed Cozystack from a hyperscaler (target: a sovereign-cloud focused European provider). |

#### 8.5.5 Certification and labor market

Certified Kubernetes Administrator (CKA) created the K8s skill labor
market. Cozystack Admin Certification (CCA) does the same for Cozystack.

| Quarter | Deliverables |
|---|---|
| 2027 Q3 | CCA beta (paired with Track 11 and Track 12.6). |
| 2027 Q4 | CCA GA. Recruiter outreach: include "Cozystack" in keyword sets used by major IT recruiters. |
| 2028 Q1+ | Cozystack Certified Developer (CCD) and Cozystack Certified Architect (CCAr) exam tracks. |
| 2028 Q2 | Job-board partnerships: LinkedIn / Indeed / Stack Overflow Talent recognized skill tags. |

#### 8.5.6 Academic and research engagement

| Quarter | Deliverables |
|---|---|
| 2027 Q1 | Academic partnership: at least one university course incorporates Cozystack. |
| 2027 Q3 | First peer-reviewed paper using Cozystack as research substrate (private cloud, sovereign cloud, or AI infrastructure research). |
| 2028 Q1+ | Cozystack Research Grants — small grants (community-funded) for academic projects on Cozystack. |

#### 8.5.7 Regulatory and government engagement

| Quarter | Deliverables |
|---|---|
| 2027 Q2 | EU CRA dialogue engagement via OpenSSF policy WG. |
| 2027 Q3 | GAIA-X / IPCEI-CIS alignment statement — Cozystack as candidate sovereign-cloud platform. |
| 2027 Q4 | US FedRAMP pre-readiness assessment for downstream commercial offerings. |
| 2028 Q1+ | Engage with national clouds: Spain ENS, Germany BSI C5, France SecNumCloud, Italy AGID, India MeitY empanelment criteria. |

#### 8.5.8 Vendor-neutrality transparency

Standards adoption is undermined by perceived single-vendor concentration.
The vendor-neutrality dashboard (Track 11 2028 Q2) must launch as a
**public** artifact, not an internal metric.

| Metric | Source | Cadence |
|---|---|---|
| Commits by organization | GitHub API | Monthly |
| Maintainers by organization | `MAINTAINERS.md` | Per change |
| Releases by organization (release lead) | Release tagging | Per release |
| Working group chairs by organization | SIG charters | Per change |
| Conference talks by organization | Talk tracker | Quarterly |
| External adopters in production | `ADOPTERS.md` | Per change |

### 8.6 Annual Standardization Review

Each Q1, the maintainer group conducts a public **Standardization Review**:

- For each spec in §8.1: status (draft / submitted / under review /
  adopted / second-implementation), blockers, owner SIG.
- For each Tier 1/2/3 body in §8.3: engagement state, designated
  representative, last activity, action items.
- For each distinguishing artifact in §8.4: visibility check (does it
  appear in talks, docs, brand assets?).
- For the adoption playbook in §8.5: progress on hardware program, OS
  program, hyperscaler engagement, certification numbers, academic and
  regulatory traction.

Output: a public **Standardization Review YYYY** document published
alongside the Annual Report (Track 12.4). The first review lands in
**2027 Q1**.

### 8.7 Summary: What Determines Whether Cozystack Becomes a Standard

The single hardest question in this document. The honest answer is that
no list of activities guarantees standardhood. What the evidence from
Linux, Kubernetes, and the OCI/CRI transition does suggest:

1. **Specifications are owned by neutral bodies, not by the project.**
   Cozystack must be willing to relinquish the specs in §8.1 to CNCF or
   another standards body, even at the cost of slower iteration.
2. **Multiple independent implementations are required.** A spec with one
   implementation is documentation; a spec with two is a standard. The
   roadmap targets a second implementation for each major spec by 2028.
3. **Conformance distinguishes "compatible" from "claimed-compatible."**
   The Conformance Program (§6) is the enforcement mechanism.
4. **The labor market is the deepest moat.** A pool of CCA-certified
   engineers, university courses using Cozystack, and recruiters listing
   "Cozystack" skills create demand pressure on enterprises.
5. **Vendor neutrality is non-negotiable.** Apparent single-vendor
   concentration kills standardhood faster than technical flaws. The
   transparency artifacts in §8.5.8 must be public, frequent, and
   maintained.

The two-year roadmap is a starting condition, not the destination. By the
end of the horizon (May 2028), Cozystack should be **plausible** as a
category standard. Actual category-standard status will be earned, if at
all, over the following five to ten years, through sustained execution of
the principles above.

## 9. Risks and Anti-Goals

Acknowledging what could derail the plan or push the project away from its
goal:

- **Storage rewrite scope creep.** blockstor must hit a clear feature parity
  bar before broadening scope. A 12–18-month rewrite that grows new features
  faster than it ships is a known failure mode.
- **Host OS distro adventure.** Forking Talos into a full Linux distribution
  is enormous work and risks turning Cozystack into a distro vendor instead
  of a platform standard. The conservative path in §5 (Track 5) is
  deliberate.
- **Marketplace without certification pipeline.** Opening external app
  submissions before SBOM, signing, and conformance gates exist creates
  supply-chain risk for adopters.
- **AI layer without GPU accounting and data governance** will be perceived
  as a demo, not a platform.
- **Multi-cluster without stable identity, networking, and policy** becomes
  a collection of integrations rather than a federation product.
- **SOC 2 / PCI scope drift.** These standards apply to *deployments*, not
  to an OSS project. We deliver evidence packs and reference architectures,
  not a stamp on the project itself.
- **blockstor without independent governance** will be perceived as a
  Cozystack-internal component rather than a CNCF-grade independent
  project — undermining storage choice across the ecosystem.
- **Vendor-neutrality concentration.** The single largest CNCF Graduation
  risk is contributor concentration in one organization. Track 11 directly
  addresses this; the maintainer-diversity work must succeed.

## 10. How This Roadmap Is Maintained

- Each track has an owning SIG (see §7).
- Each quarter, the maintainers conduct a public roadmap review and update
  this document. The current Project V2 board remains the granular
  issue-level source of truth for in-quarter work; this document gives the
  multi-quarter view.
- The initial roadmap (this document) is ratified via a maintainer roadmap
  review on this PR; thereafter, material changes to the roadmap are proposed
  via a CzEP (Cozystack Enhancement Proposal) in
  [cozystack/community/design-proposals](https://github.com/cozystack/community/tree/main/design-proposals).
- The document is versioned in `git` history; major revisions are tagged.

## 11. References

- [Cozystack Project V2 Roadmap board](https://github.com/orgs/cozystack/projects/1)
- [Cozystack Governance](./GOVERNANCE.md)
- [Cozystack Maintainers](./MAINTAINERS.md)
- [Cozystack Security Policy](./SECURITY.md)
- [Cozystack Contributor Ladder](./CONTRIBUTOR_LADDER.md)
- [CNCF Project Lifecycle](https://contribute.cncf.io/projects/lifecycle/)
- [OSPS Baseline](https://baseline.openssf.org/)
- [OpenSSF Best Practices Badge](https://www.bestpractices.dev/)
- [OpenSSF Scorecard](https://github.com/ossf/scorecard)
- [SLSA Framework](https://slsa.dev/)
- [CNCF TAG Security Self-Assessment Guide](https://tag-security.cncf.io/community/assessments/guide/self-assessment/)
- [EU Cyber Resilience Act](https://digital-strategy.ec.europa.eu/en/policies/cyber-resilience-act)
- [PCI DSS v4.0.1](https://www.pcisecuritystandards.org/document_library/)
- [ISO/IEC 5230 (OpenChain)](https://www.openchainproject.org/)
- [CSAF VEX](https://docs.oasis-open.org/csaf/csaf/v2.0/csaf-v2.0.html)
- [Argo CD](https://argo-cd.readthedocs.io/)
- [Flux CD](https://fluxcd.io/)
- [GitLab Runner](https://docs.gitlab.com/runner/)
- [GitLab Container Registry](https://docs.gitlab.com/ee/user/packages/container_registry/)
