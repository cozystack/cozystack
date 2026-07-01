<!--
https://github.com/cozystack/cozystack/releases/tag/v1.3.0-rc.1
-->

# Cozystack v1.3.0-rc.1

Cozystack v1.3.0-rc.1 is the first release candidate for v1.3.0, bringing **storage-aware scheduling** via the LINSTOR scheduler extender, a managed **LINSTOR GUI** web UI with Keycloak SSO, a **VM Default Images** catalog for out-of-the-box virtual machine provisioning, **WorkloadsReady conditions** with a real-time Events tab in the dashboard, and **cross-namespace VM backup restore** capabilities. Additional highlights include stricter tenant name validation, VM network selector improvements, Keycloak theme injection and SMTP configuration, and a comprehensive host runtime preflight check.

> **Note:** Fixes marked with *(backported to v1.2.x)* were also included in v1.2.1 or v1.2.2 patch releases.

## Feature Highlights

### Storage-Aware Scheduling via LINSTOR Extender

The `cozystack-scheduler` now calls the **LINSTOR scheduler extender** for storage-locality-aware pod placement. When a pod declares both a `SchedulingClass` and LINSTOR-backed PVCs, the scheduler consults LINSTOR to prefer nodes where volume replicas already exist — reducing cross-node replication traffic and improving I/O latency for storage-heavy workloads ([**@lllamnyp**](https://github.com/lllamnyp) in #2330).

### LINSTOR GUI: Managed Web UI for Storage Administration

A new opt-in `linstor-gui` system package deploys **LINBIT's linstor-gui web UI** alongside the LINSTOR controller with mTLS client authentication, non-root security context, and ClusterIP-only service. An optional **Keycloak-protected Ingress** (via oauth2-proxy) can be enabled for SSO-authenticated browser access when OIDC is configured on the platform ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2382, #2390).

### VM Default Images: Out-of-the-Box VM Provisioning

The new `vm-default-images` package provides a curated set of **cluster-wide virtual machine images** (Ubuntu, Debian, CentOS Stream, and others) as pre-populated DataVolumes. The package is opt-in via the `iaas` bundle and defaults to replicated storage for high availability. A companion migration (migration 38) renames legacy `vm-image-*` DataVolumes to the new `vm-default-images-*` naming scheme. The `vm-disk` chart also gains a new "disk" source type for cloning from existing vm-disks in the same namespace ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2258).

### WorkloadsReady Condition and Events Tab

Applications now expose a **WorkloadsReady** condition on their status by querying associated WorkloadMonitor resources, giving operators a single place to check whether all underlying workloads (Deployments, StatefulSets, DaemonSets) are healthy. The dashboard gains a new **Events tab** showing namespace-scoped Kubernetes events for each application, with fallback to `.firstTimestamp` when `.eventTime` is absent. A bug where WorkloadMonitor's `Operational` status was never persisted is also fixed ([**@lexfrei**](https://github.com/lexfrei) in #2356).

### Cross-Namespace VM Backup Restore

The backup system now supports **restoring VMInstance backups into a different namespace** (cross-namespace copy restores), with IP/MAC preservation and safe rename semantics. In-place backup/restores for VMDisk and VMInstance are improved: HelmReleases and DataVolumes are properly handled, and Velero failure messages are propagated to the Application status. The backup status structure has been refactored to store underlying resources as a generic opaque JSON object, enabling arbitrary application-specific metadata ([**@androndo**](https://github.com/androndo) in #2251, #2329, #2319).

## Major Features and Improvements

* **[api] Reject tenant names with dashes at Create time**: Enforces alphanumeric-only naming for Tenants at the API level, preventing names with hyphens that would silently fail during Helm reconciliation. A corresponding regex tightening and regression test suite hardens the validation ([**@lexfrei**](https://github.com/lexfrei) in #2380).

* **[platform] Validate computed tenant namespace length**: Rejects Tenant creation when the computed ancestor-chain namespace would exceed the 63-character Kubernetes namespace limit, preventing opaque HelmRelease reconcile errors downstream ([**@lexfrei**](https://github.com/lexfrei) in #2376).

* **[vm-instance] Rename subnets to networks and add dropdown selector**: Renames the misleading `subnets` field to `networks` in VMInstance for clarity, adds a dropdown selector for available networks in the dashboard form, and includes a migration to copy existing `subnets` values. The old field remains supported for backward compatibility ([**@sircthulhu**](https://github.com/sircthulhu) in #2263).

* **[keycloak] Enable injecting themes**: Cozystack administrators can now inject custom Keycloak themes via `initContainers` for UI white-labeling and customization ([**@lllamnyp**](https://github.com/lllamnyp) in #2142).

* **[keycloak-configure] Add email verification and SMTP configuration**: Adds configurable Keycloak settings for user self-registration, email verification, and SMTP server configuration, enabling automated user onboarding flows ([**@BROngineer**](https://github.com/BROngineer) in #2318).

* **[postgres] Hardcode PostgreSQL 17 for monitoring databases**: Pins PostgreSQL 17.7 images for system databases (Grafana, Alerta, Harbor, Keycloak, SeaweedFS) and adds migration 37 to backfill `spec.version=v17` for existing PostgreSQL resources, preventing CNPG from defaulting to PostgreSQL 18 *(backported to v1.2.1)* ([**@IvanHunters**](https://github.com/IvanHunters) in #2304).

* **[hack] Add host runtime preflight check**: New `check-host-runtime.sh` script and `make preflight` target that warns operators when a standalone containerd or docker runtime is running alongside the embedded k3s runtime, helping diagnose container runtime conflicts ([**@lexfrei**](https://github.com/lexfrei) in #2371).

* **[hack] Add check-readiness.sh diagnostic script**: A new diagnostic script for tracking platform reconciliation by checking readiness of Packages, ArtifactGenerators, ExternalArtifacts, and HelmReleases, with support for watch mode and continuous monitoring ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2294).

* **[mariadb] Always enable replication for consistent service naming**: MariaDB now always enables replication, creating `-primary`/`-secondary` services even for single-replica instances. This fixes dashboard visibility and backup functionality for single-replica setups ([**@sircthulhu**](https://github.com/sircthulhu) in #2279).

* **[platform] Prevent installed packages deletion**: Adds `helm.sh/resource-policy: keep` annotation to packages, preventing automatic deletion when packages are disabled and restoring documented behavior *(backported to v1.2.1)* ([**@kvaps**](https://github.com/kvaps) in #2273).

## Bug Fixes

* **[cilium] Opt-out of cri-containerd.apparmor.d for nsenter init containers**: Opts cilium-agent init containers out of the `cri-containerd.apparmor.d` AppArmor profile on non-Talos variants, fixing `Init:CrashLoopBackOff` on Ubuntu 22.04+ and Debian *(backported to v1.2.2)* ([**@lexfrei**](https://github.com/lexfrei) in #2370).

* **[virtual-machine] Exclude external VM services from Cilium BPF LB**: Adds `service-proxy-name: cozy-proxy` label to VM LoadBalancer services, telling Cilium to skip BPF processing. Fixes inter-tenant connectivity via public LB IPs and WholeIP functionality on Cilium 1.19+ *(backported to v1.2.2)* ([**@mattia-eleuteri**](https://github.com/mattia-eleuteri) in #2357).

* **[monitoring] Fix infra dashboards missing in default variant**: Includes `cozy-monitoring` namespace in the dashboard rendering condition, fixing infrastructure Grafana dashboards not rendering in the default platform variant *(backported to v1.2.2)* ([**@mattia-eleuteri**](https://github.com/mattia-eleuteri) in #2365).

* **[postgres] Fix system PostgreSQL images to 17.7-standard-trixie**: Normalizes system PostgreSQL image tags to use `17.7-standard-trixie` variant with migration logic for existing CNPG clusters *(backported to v1.2.2)* ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2364).

* **[build] Filter git describe to match only v\* tags**: Adds `--match 'v*'` to `git describe` calls, preventing API subtags from being picked up instead of release tags and producing invalid Docker image tags *(backported to v1.2.2)* ([**@kvaps**](https://github.com/kvaps) in #2386).

* **[platform] Fix resource allocation ratios not propagated to packages**: Restores propagation of CPU, memory, and ephemeral-storage allocation ratios to managed applications and KubeVirt, which were silently ignored since the bundle restructure *(backported to v1.2.1)* ([**@sircthulhu**](https://github.com/sircthulhu) in #2296).

* **[kubernetes] Set explicit ephemeral-storage on virt-launcher pods**: Sets explicit `domain.resources` with ephemeral-storage on VirtualMachine spec to prevent virt-launcher pods from being evicted due to LimitRange defaults being too low for actual emptyDisk capacity ([**@kvaps**](https://github.com/kvaps) in #2317).

* **[multus] Pin master CNI to 05-cilium.conflist**: Prevents a boot-time race condition where multus could auto-detect kube-ovn's conflist instead of Cilium's *(backported to v1.2.1)* ([**@kvaps**](https://github.com/kvaps) in #2315).

* **[multus] Build custom image with DEL cache fix**: Fixes sandbox cleanup deadlock when CNI ADD never completes, preventing stale sandbox name reservations from permanently blocking pod creation *(backported to v1.2.1)* ([**@kvaps**](https://github.com/kvaps) in #2313).

* **[linstor] Set verify-alg to crc32c**: Prevents DRBD connection failures on kernels where `crct10dif` is unavailable (e.g., Talos v1.12.6 with kernel 6.18.18) *(backported to v1.2.1)* ([**@kvaps**](https://github.com/kvaps) in #2303).

* **[linstor] Preserve TCP ports during toggle-disk operations**: Fixes TCP port mismatches after toggle-disk operations that could cause DRBD resources to enter StandAlone state *(backported to v1.2.1)* ([**@kvaps**](https://github.com/kvaps) in #2292).

## Dependencies & Version Updates

* **[linstor] Update piraeus-server to v1.33.2 with selected backports**: Bumps LINSTOR server from v1.33.1 to v1.33.2 with backported patches for stale bitmap adjust retry, LUKS2 header sizing, and optimal I/O size detection *(backported to v1.2.2)* ([**@kvaps**](https://github.com/kvaps) in #2331).

* **[kamaji] Update to 26.3.5-edge, drop upstreamed patches**: Updates Kamaji from edge-26.2.4 to 26.3.5-edge and removes two patches accepted upstream. Adds configurable probe tuning and DataStore readiness conditions ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2260).

* **[talm] Release v0.23.0, v0.23.1, v0.24.0** (github.com/cozystack/talm): Migrates to the Talos v1.12 multi-document machine config format ([**@lexfrei**](https://github.com/lexfrei) in cozystack/talm#116); fixes template rendering in `apply` command to resolve lookups ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in cozystack/talm#119); bumps dependencies and modernizes codebase ([**@lexfrei**](https://github.com/lexfrei) in cozystack/talm#124).

* **[ansible-cozystack] Release v1.2.1, v1.2.2** (github.com/cozystack/ansible-cozystack): Exposes `publishing.externalIPs` and tenant-root ingress via role variables ([**@lexfrei**](https://github.com/lexfrei) in cozystack/ansible-cozystack#30); adds comprehensive node prerequisites audit ([**@lexfrei**](https://github.com/lexfrei) in cozystack/ansible-cozystack#27); replaces `ansible.utils.ipaddr` with a stdlib-based test plugin ([**@lexfrei**](https://github.com/lexfrei) in cozystack/ansible-cozystack#24).

## Security

* **docs: add SECURITY.md**: Adds vulnerability reporting procedures, disclosure expectations, and supported release lines ([**@kvaps**](https://github.com/kvaps) in #2230).

* **docs: add OpenSSF Best Practices badge to README**: Adds the OpenSSF Best Practices passing badge to the project README ([**@lexfrei**](https://github.com/lexfrei) in #2320).

## Development, Testing, and CI/CD

* **[ci] Replace cozystack-bot PAT with cozystack-ci GitHub App**: Replaces the long-lived cozystack-bot personal access token with short-lived, scoped tokens from the cozystack-ci GitHub App across all CI release workflows ([**@tym83**](https://github.com/tym83) in #2351; [**@kvaps**](https://github.com/kvaps) in #2383, #2392).

* **[ci] Add Gemini Code Assist and CodeRabbit configuration**: Adds repository-level configuration for AI code reviewers with ignore patterns for vendored/generated code and incremental review settings ([**@lexfrei**](https://github.com/lexfrei) in #2385).

* **[ci] Make tags workflow idempotent on re-runs**: Fixes CI to force-update API subtags and handle re-runs gracefully ([**@kvaps**](https://github.com/kvaps)).

* **[tests] Fix Kafka E2E test timeout and retry race condition**: Increases Kafka E2E test timeout from 60s to 300s and fixes a retry race condition where `kubectl apply` could hit a still-deleting resource ([**@lexfrei**](https://github.com/lexfrei) in #2358).

* **docs: adopt Conventional Commits for commit and PR titles**: Standardizes commit and PR title format to `type(scope): description` across all contributing docs and the PR template ([**@lexfrei**](https://github.com/lexfrei) in #2395).

* **docs(ci): require screenshots for UI changes in PR template**: Adds a mandatory screenshots section to the PR template for UI-related changes ([**@kitsunoff**](https://github.com/kitsunoff) in #2407).

## Documentation

* **[website] Add ApplicationDefinition naming convention reference**: Documents how `cozystack-api` resolves kinds to their backing definitions ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#478).

* **[website] Document Talos / talosctl / Cozystack version pairing**: Adds version compatibility matrix for installation ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#484).

* **[website] Document namespace layout and parent/child derivation**: Explains tenant namespace hierarchy and parent/child namespace derivation rules ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#479).

* **[website] Document the checkbox-then-edit-CR customization pattern for tenants**: Describes the workflow for customizing tenant settings via the CR after initial checkbox-based creation ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#485).

* **[website] Add custom Keycloak themes documentation**: Covers the theme image contract, configuration, `imagePullSecrets`, and theme activation in the Keycloak admin console ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#463).

* **[website] Add bonding (LACP) configuration how-to guide**: Covers network bonding configuration for Cozystack installations ([**@sircthulhu**](https://github.com/sircthulhu) in cozystack/website#459).

* **[website] Improve registry mirrors for tenant Kubernetes in air-gapped guide**: Improved documentation for configuring registry mirrors in air-gapped environments ([**@sircthulhu**](https://github.com/sircthulhu) in cozystack/website#461).

* **[website] Rewrite guide for ApplicationDefinition API (external-apps)**: Comprehensive rewrite of the external apps guide using the ApplicationDefinition API ([**@kitsunoff**](https://github.com/kitsunoff) in cozystack/website#488).

* **[website] Add documentation for Go types usage**: Guide for using generated Go types for Cozystack managed applications as a Go module ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in cozystack/website#465).

* **[website] Update backup/restore documentation for VMI/VMDisk**: Updated backup documentation with VM instance and VM disk restore improvements ([**@androndo**](https://github.com/androndo) in cozystack/website#466).

* **[website] Add OSS Health pages and OpenSSF badge**: Added OSS Health section with OpenSSF Scorecard and Best Practices badge to the website ([**@tym83**](https://github.com/tym83) in cozystack/website#470).

* **[website] Add CozySummit Virtual 2026 program announcement**: Published the CozySummit Virtual 2026 program announcement blog post ([**@tym83**](https://github.com/tym83) in cozystack/website#472).

* **[website] Add missing release announcements for v0.1–v0.41**: Backfilled missing release announcement blog posts for historical Cozystack versions ([**@tym83**](https://github.com/tym83) in cozystack/website#468).

* **[website] Fix broken links and stale anchors across v1 docs**: Fixes 14 broken links and stale talm anchors ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#486).

* **[website] Prefix bundle package names with cozystack. in v1 examples**: Corrects package naming in documentation examples ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#482).

* **[website] Finish isolated-field removal and document opt-in policy labels**: Removes obsolete `isolated` field from tenant documentation and documents the new approach ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#481).

* **[website] Add --take-ownership flag and describe networking.* fields**: Documents the `--take-ownership` flag and `networking.*` fields in the installation guide ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#480).

* **[website] Fix KubeOVN MASTER_NODES example path and key in troubleshooting**: Corrects the MASTER_NODES example path ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#483).

* **[external-apps-example] Replace MongoDB example with Minecraft apps**: Refactors the external apps example to use ApplicationDefinition API with Minecraft server applications ([**@lexfrei**](https://github.com/lexfrei) in cozystack/external-apps-example#2).

## Governance

* **Add Mattia Eleuteri ([@mattia-eleuteri](https://github.com/mattia-eleuteri)) as Maintainer**: CSI, Storage, Networking & Security ([**@tym83**](https://github.com/tym83) in #2345).

* **Add Matthieu Robin ([@matthieu-robin](https://github.com/matthieu-robin)) as Maintainer**: Managed applications, platform quality, and benchmarking ([**@tym83**](https://github.com/tym83) in #2346).

## Contributors

We'd like to thank all contributors who made this release possible:

* [**@androndo**](https://github.com/androndo)
* [**@BROngineer**](https://github.com/BROngineer)
* [**@IvanHunters**](https://github.com/IvanHunters)
* [**@kitsunoff**](https://github.com/kitsunoff)
* [**@kvaps**](https://github.com/kvaps)
* [**@lexfrei**](https://github.com/lexfrei)
* [**@lllamnyp**](https://github.com/lllamnyp)
* [**@mattia-eleuteri**](https://github.com/mattia-eleuteri)
* [**@myasnikovdaniil**](https://github.com/myasnikovdaniil)
* [**@sircthulhu**](https://github.com/sircthulhu)
* [**@tym83**](https://github.com/tym83)

---

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v1.2.0...v1.3.0-rc.1
