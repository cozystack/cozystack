# Cozystack Roadmap

**Status:** Living document — updated quarterly by the maintainers.
**Horizon:** May 2026 – May 2028 (two-year forward window).
**Last updated:** 2026-05-18.

This document describes where Cozystack is heading. It is the authoritative
public roadmap. The granular issue-level view lives in
[Project V2 #1 (Cozystack Roadmap)](https://github.com/orgs/cozystack/projects/1);
this file explains the *why* and the *how they fit together*.

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
- `v1.4` (rc) — backup strategies, Flux 2.8, new `cozystack-ui`, cozy-tls,
  Redis TLS, app scheduling, CI/e2e hardening.

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
- API Gateway support (#1265) — rescheduled to Q3 2026.
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
- **security-scanner** — automated CVE pipeline (Trivy) across the whole
  organization with maintainer triage workflow.
- **standalone-trustd** — Talos `trustd` extracted as a standalone
  certificate-signing service.
- **cozystack-scheduler** — custom scheduler hooks for workload-aware
  scheduling (GPU, NUMA, locality).
- **cnai-landscape** — CNAI landscape submission preparation.

The two-year plan brings these tracks into the public roadmap.

## 4. Two-Year Plan at a Glance

| Half | Primary Goal | Headline Outcomes |
|---|---|---|
| H2 2026 (Jun–Dec) | Incubation push + enterprise foundations | CNCF Incubation, OSPS Baseline L2, OpenSSF Passing badge, public release-train policy, Marketplace alpha, host-OS contract documented. |
| H1 2027 (Jan–Jun) | AI platform v1 + multi-cluster GA | Cozystack 2.0, blockstor alpha, Fleet beta, AI Platform MVP, SOC 2 Type I readiness, OSPS Baseline L3, OpenSSF Silver. |
| H2 2027 (Jul–Dec) | Enterprise scale + Marketplace mature | Public scale envelope, blockstor beta, AI Platform GA, certified-apps program, SLSA Build L3, SOC 2 Type II evidence collection, OpenSSF Gold. |
| H1 2028 (Jan–May) | Graduated-tier prep + standard play | Cozystack Conformance Program, certified providers/apps/storage/OS, third-party security audit completed, EU CRA compliance posture, Graduated application drafted. |

## 5. Tracks

The roadmap is organized into eleven tracks. Each track lists deliverables by
quarter and an indicative owning SIG (see §7 for SIG formation timeline).

### Track 1 — Platform Core (SIG-Platform)

**Goal.** Stable APIs, predictable upgrades, formal backwards-compatibility
contract.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Close in-flight items #1246 #1247 #1262 #1265. Cut `v1.5.0`. Publish `API Stability Policy` (alpha/beta/stable lanes, deprecation window). |
| 2026 Q4 | Distroless images (#1261). Automated platform updates (#1266). Public **Release Trains** policy: `stable`, `fast`, `LTS`. Public support matrix for Kubernetes / host OS / Cilium / KubeVirt / storage backends. |
| 2027 Q1 | **Cozystack 2.0** — backwards-compatibility contract published; major API revision based on production feedback; HA control-plane improvements (stretched control plane, multi-AZ, etcd backup automation). |
| 2027 Q2 | `Platform Health API` covering operator, packages, Flux, storage, networking, backups, ingress, auth. Explicit lifecycle states for apps, backups, restores, VMs, tenant clusters. |
| 2027 Q3 | Performance optimizations based on H1 2027 benchmarks. Per-tenant resource isolation hardening (full cgroup v2 adoption, memory.high pressure). |
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

Cozystack's security work is structured around eight external frameworks plus
one internal program. Each is treated as a sub-project with concrete tasks.

#### 3.1 OpenSSF Best Practices Badge (Metal series)

The legacy and still-required "Passing / Silver / Gold" badge program.
CNCF expects Passing as a minimum for Incubation and Gold for Graduation.

| Quarter | Target | Concrete tasks |
|---|---|---|
| 2026 Q3 | **Passing** (67 criteria) | Enroll project on [bestpractices.dev](https://www.bestpractices.dev/). Address all `MUST` items: public version-controlled source ✓, distinct contributing/security/code-of-conduct docs ✓ (already present), public bug tracker ✓, OSS license (Apache-2.0) ✓, build reproducibility documentation, automated test suite documented, public release notes per version, secure communication for vulnerability reports (private maintainer mailbox), warn on common cryptographic mistakes, vulnerability response policy with SLA, no public exposure of sensitive data in commits/issues. |
| 2026 Q4 | **Silver** (55 additional criteria) | DCO or CLA documented and enforced, two-person review for substantive changes (already required), branch protection on all release branches, automated `golangci-lint` + `gosec` on every PR (extend coverage), documented coding standards, documented release-signing process, signed Git tags for releases, security policy with vulnerability disclosure timeline, README references security policy, contribution metadata documented (size, scope, review). |
| 2027 Q2 | **Gold** (23 additional criteria) | Documented secure-design / threat model, fuzzing for critical components (blockstor, custom-scheduler, controllers), public SAST findings management, dynamic analysis (`go vet -race`, kube-conformance), CI-driven dependency-license check (FOSSA/ScanCode), reproducible builds for container images, branch protection includes required signed commits where supported. |

#### 3.2 OSPS Baseline (Open Source Project Security Baseline)

The new (Feb 2026) maturity-model framework that is replacing "Gold-only"
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

#### 3.3 OpenSSF Scorecard

Automated weekly checks producing a numeric score. CNCF expects ≥ 7.0 for
Incubation and ≥ 8.0 for Graduated.

| Quarter | Target | Concrete tasks |
|---|---|---|
| 2026 Q3 | **≥ 6.0** | Enable Scorecard GitHub Action across all org repositories. Publish badge in README. Address quick wins: pinned dependencies in CI, branch-protection metadata, signed releases, no binary artifacts in source tree. |
| 2026 Q4 | **≥ 7.0** | Token-permissions least-privilege in every workflow, dependency-update-tool (Dependabot/Renovate enforced), code-review for every merge, signed commits where practical. |
| 2027 Q2 | **≥ 8.0** | SAST integration, fuzzing presence, CII Best Practices score reflection. |
| 2027 Q4 | **≥ 9.0** | All applicable checks pass; only systemic exceptions remain documented. |

#### 3.4 SLSA — Supply-chain Levels for Software Artifacts

Build-side integrity. CNCF Graduation expects SLSA Build L3.

| Quarter | Target | Concrete tasks |
|---|---|---|
| 2026 Q3 | **Build L1** | Provenance generation for every release artifact (container images, Helm charts, binaries). Publish as `.intoto.jsonl` next to releases. |
| 2026 Q4 | **Build L2** | Hosted-build platform (GitHub Actions OIDC + slsa-github-generator). Authenticated provenance with signed `_provenance` files. |
| 2027 Q3 | **Build L3** | Hardened build platform: isolated build steps, non-falsifiable provenance, no human override of build pipeline; reusable workflows audited; secrets handled via OIDC short-lived tokens only. |

#### 3.5 CNCF CLOMonitor

CNCF-wide automated check dashboard. Must be all-green for Incubation review.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Verify all CLOMonitor checks. Address gaps: project metadata, governance, security policy, adopters file completeness, contributor ladder, license, code of conduct, OpenSSF badge, artifact signing, SBOM. |

#### 3.6 CNCF Security Self-Assessment + Third-Party Audit

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Complete the **CNCF TAG Security self-assessment** ([template](https://github.com/cncf/tag-security/blob/main/community/assessments/guide/self-assessment.md)). Publish as `SECURITY-SELF-ASSESSMENT.md` in repo. Include attack-surface diagram, trust boundaries, threat model, and known weaknesses with mitigation status. |
| 2027 Q3 | Engage a **third-party audit** (e.g. Ada Logics with OSTIF funding, or NCC Group). Scope: core controllers, blockstor, custom scheduler, multi-tenancy boundaries, supply chain. Required for Graduated tier. |
| 2027 Q4 | Publish audit results and remediation status. Triage findings into milestones. |

#### 3.7 EU Cyber Resilience Act (CRA)

The CRA's reporting obligations apply from **11 September 2026**; full
obligations from **11 December 2027**. Cozystack itself, as a non-commercial
OSS project, is technically a *contributor* under CRA Article 23, not a
*manufacturer*. But downstream commercial vendors who package Cozystack are
manufacturers. We document the project so that downstream compliance is
tractable.

| Quarter | Deliverables |
|---|---|
| 2026 Q3 | Publish a **`CRA-COMPLIANCE.md`** describing: (a) the project's status as an OSS contributor under Article 23, (b) the CVE coordinated-disclosure process aligning with CRA timelines (24h initial / 72h info / 14d final after fix or workaround for actively exploited issues), (c) SBOM availability per release. |
| 2026 Q3 | Adopt **CSAF VEX** for vulnerability advisories. Publish each advisory as a CSAF JSON file alongside the GHSA. |
| 2026 Q4 | SBOM in **SPDX** and **CycloneDX** formats published with every release; signed with cosign. |
| 2027 Q1 | Document the **shared-responsibility matrix** between Cozystack maintainers and downstream commercial vendors for incident response. |
| 2027 Q4 | Verify the disclosure pipeline against CRA timeline obligations with a tabletop exercise. |

#### 3.8 SOC 2 (Process Readiness, not Certification)

An OSS project does not get SOC 2 itself — services do. We deliver a **process
evidence pack** that any commercial Cozystack distributor can use in their
own SOC 2 audit.

| Quarter | Deliverables |
|---|---|
| 2027 Q1 | **SOC 2 Type I evidence pack** — `SOC2-EVIDENCE/` directory with: change-management policy, access-control policy for the GitHub org, incident-response runbook, vendor/dependency-management policy, backup/DR documentation, monitoring/audit-log policy. |
| 2027 Q3 | **Type II evidence-collection automation** — GitHub Actions snapshot monthly evidence (permission grants, branch-protection state, release-signing logs, CVE triage decisions) to `SOC2-EVIDENCE/`. |
| 2027 Q4 | Tabletop exercise with downstream Cozystack distributors to validate the evidence pack supports their audits. |

#### 3.9 PCI DSS v4.0.1 (Reference Architecture)

A `PCI-DSS.md` reference architecture for deploying Cozystack as a CDE
(Cardholder Data Environment).

| Quarter | Deliverables |
|---|---|
| 2027 Q2 | Publish `reference-architectures/pci-dss-v4/` containing: network segmentation (Cilium NetworkPolicy + Kubernetes NetworkPolicy), MFA enforcement (OIDC config), audit-log architecture (Cozystack audit log + Loki + immutable retention), vulnerability-management workflow, secure-defaults documentation, key-management (External Secrets Operator + HashiCorp Vault / OpenBao), responsibility matrix. |
| 2027 Q3 | Validate the reference architecture with a friendly PCI QSA review. |

#### 3.10 ISO/IEC 5230 (OpenChain) — License Compliance

| Quarter | Deliverables |
|---|---|
| 2026 Q4 | **OpenChain self-certification** — `LICENSE-COMPLIANCE.md` documenting the project's SPDX-correct license metadata, third-party license inventory (auto-generated from `go.mod` and Helm chart dependencies), and license-clearing workflow for new dependencies. |
| 2027 Q1 | Automated license-policy gate in CI (`fossa`/`scancode`). |

#### 3.11 Internal Security Programs

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

#### 3.12 Certifications Roll-up

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
| 2026 Q3 | Reference architectures published as `reference-architectures/`: GPU cloud provider, hosting provider, telco (5G CNF), fintech (PCI-aligned), research/HPC. |
| 2026 Q4 | **`cozystack` CLI 2.0** — single entry point replacing `make` + `kubectl` + `helm` patchwork. |
| 2027 Q1 | Backstage plugin for Cozystack. Crossplane Provider for Cozystack. |
| 2027 Q2 | OpenTofu/Terraform provider. DORA metrics dashboard for tenants. |
| 2027 Q3 | Sustainability/carbon metrics (Kepler integration). |
| 2027 Q4 | Migration playbooks: from OpenStack, from VMware, from legacy KVM. |

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
  SIG-Storage. These are mandatory for Incubation review.
- **Wave 2 (2026 Q4):** SIG-Network, SIG-AI, SIG-Apps, SIG-DX, SIG-OS,
  SIG-Governance.

Each SIG owns an area of the roadmap, holds a public weekly or biweekly
meeting, maintains a charter in `community/sigs/<sig-name>/charter.md`, and
reports to the maintainer group quarterly.

## 8. Risks and Anti-Goals

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

## 9. How This Roadmap Is Maintained

- Each track has an owning SIG (see §7).
- Each quarter, the maintainers conduct a public roadmap review and update
  this document. The current Project V2 board remains the granular
  issue-level source of truth for in-quarter work; this document gives the
  multi-quarter view.
- Material changes to the roadmap are proposed via a CzEP (Cozystack
  Enhancement Proposal) in
  [cozystack/community/design-proposals](https://github.com/cozystack/community/tree/main/design-proposals).
- The document is versioned in `git` history; major revisions are tagged.

## 10. References

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
