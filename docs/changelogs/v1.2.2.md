<!--
https://github.com/cozystack/cozystack/releases/tag/v1.2.2
-->

## Features and Improvements

* **[linstor] Update piraeus-server to v1.33.2 with selected backports**: Bumps LINSTOR server from v1.33.1 to v1.33.2 and adds backported patches for improved storage reliability: a stale bitmap adjust retry mechanism for automatic recovery after bitmap attach errors, LUKS2 header sizing and optimal I/O size detection improvements for more reliable disk formatting, and the maintainer implementation backport. All patches verified against upstream v1.33.2 with `git apply --check` and `gradlew compileJava` ([**@kvaps**](https://github.com/kvaps) in #2331, #2377).

## Fixes

* **[postgres] Fix system PostgreSQL images to 17.7-standard-trixie**: Hardcodes PostgreSQL 17.7-standard-trixie images for system PostgreSQL instances. This ensures system databases use the correct image variant consistent with the monitoring stack requirements introduced in v1.2.1 ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #2364, #2369).

* **[cilium] Opt-out of cri-containerd.apparmor.d for nsenter init containers**: On Ubuntu 22.04+, Debian, and other distributions that load the `cri-containerd.apparmor.d` AppArmor profile by default for containerd workloads, the kernel denied `nsenter` namespace entry in cilium-agent init containers (`mount-cgroup`, `apply-sysctl-overwrites`, `clean-cilium-state`), causing the agent to land in `Init:CrashLoopBackOff` and cascading platform failures. Per-container `container.apparmor.security.beta.kubernetes.io` annotations now opt the affected containers out of this profile, applied only on non-Talos cilium variants (`cilium-generic`, `kubeovn-cilium-generic`). The vendored daemonset template is also patched to strip the upstream `semverCompare "<1.30.0"` AppArmor block, preventing duplicate annotation keys. Talos variants are untouched as Talos does not load the AppArmor LSM ([**@lexfrei**](https://github.com/lexfrei) in #2370, #2378).

* **[virtual-machine] Exclude external VM services from Cilium BPF LB**: Adds the `service.kubernetes.io/service-proxy-name: "cozy-proxy"` label to VM LoadBalancer services when `external: true`, telling Cilium to skip BPF processing entirely for these services. This fixes two issues: inter-tenant connectivity via public LB IPs (Cilium's DNAT caused cross-tenant pod-to-pod flow classification, triggering CiliumClusterwideNetworkPolicy blocks) and WholeIP broken on Cilium 1.19+ (wildcard service drop entries blocked traffic to LB IPs on undeclared ports before it reached netfilter/cozy-proxy). MetalLB L2 advertisement and kube-ovn routing remain unaffected ([**@mattia-eleuteri**](https://github.com/mattia-eleuteri) in #2357, #2361).

* **[monitoring] Fix infra dashboards missing in default variant**: The default platform variant deploys the monitoring chart to the `cozy-monitoring` namespace, but the dashboard rendering condition introduced in #2197 only checked for `tenant-root`. Infrastructure dashboards were not rendered in the default variant. The `cozy-monitoring` namespace is now included in the rendering condition, consistent with the existing pattern in `vmagent.yaml` ([**@mattia-eleuteri**](https://github.com/mattia-eleuteri) in #2365, #2367).

* **[build] Filter git describe to match only v* tags**: Adds `--match 'v*'` to all `git describe` calls in `hack/common-envs.mk`. The `api/apps/v1alpha1/*` subtags share the same commit as release tags, causing `git describe --exact-match` to pick `api/apps/v1alpha1/vX.Y.Z` instead of `vX.Y.Z`, producing invalid Docker image tags ([**@kvaps**](https://github.com/kvaps) in #2386, #2389).

## Development, Testing, and CI/CD

* **[ci] Replace cozystack-bot PAT with cozystack-ci GitHub App**: Replaces the long-lived `cozystack-bot` personal access token with short-lived, scoped tokens from the `cozystack-ci` GitHub App across all release workflows (`tags.yaml`, `auto-release.yaml`, `pull-requests-release.yaml`). Improves security and auditability of CI operations ([**@tym83**](https://github.com/tym83) in #2351).

* **[ci] Use cozystack org noreply email for bot commits**: Updates CI workflows to use the cozystack organization noreply email for bot commits ([**@kvaps**](https://github.com/kvaps) in #2392, #2393).

* **[ci] Replace GH_PAT with cozystack-ci GitHub App token in pull-requests workflow**: Switches the pull-requests release workflow to use the cozystack-ci GitHub App token instead of the personal access token ([**@kvaps**](https://github.com/kvaps) in #2383, #2384).

## Documentation

* **[website] Add ApplicationDefinition naming convention reference**: Added reference documentation on ApplicationDefinition naming conventions and how `cozystack-api` resolves kinds to their backing definitions ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#478).

* **[website] Document Talos / talosctl / Cozystack version pairing**: Added documentation covering Talos, talosctl, and Cozystack version compatibility matrix for installation ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#484).

* **[website] Fix KubeOVN MASTER_NODES example path and key in troubleshooting**: Corrected the MASTER_NODES example path and key in the KubeOVN troubleshooting guide ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#483).

* **[website] Prefix bundle package names with cozystack. in v1 examples**: Updated documentation examples to use the correct `cozystack.` prefix for bundle package names in enabled/disabledPackages ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#482).

* **[website] Finish isolated-field removal and document opt-in policy labels**: Removed the obsolete `isolated` field from tenant documentation and documented the new opt-in policy labels approach ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#481).

* **[website] Add --take-ownership flag and describe networking.* fields**: Added documentation for the `--take-ownership` flag and described the `networking.*` fields in the installation guide ([**@lexfrei**](https://github.com/lexfrei) in cozystack/website#480).

* **[website] Add bonding (LACP) configuration how-to guide**: Added a guide for configuring network bonding with LACP on Cozystack installations ([**@sircthulhu**](https://github.com/sircthulhu) in cozystack/website#459).

* **[website] Improve registry mirrors for tenant Kubernetes in air-gapped guide**: Improved documentation for configuring registry mirrors in tenant Kubernetes clusters for air-gapped environments ([**@sircthulhu**](https://github.com/sircthulhu) in cozystack/website#461).

* **[website] Update backup/restore documentation for VMI/VMDisk**: Updated backup documentation with information related to VM instance and VM disk restore improvements ([**@androndo**](https://github.com/androndo) in cozystack/website#466).

* **[website] Add updated OpenAPI spec**: Updated the OpenAPI specification for managed applications reference ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in cozystack/website#469).

* **[website] Add OSS Health pages and OpenSSF badge**: Added OSS Health section with OpenSSF Scorecard and Best Practices badge to the website footer ([**@tym83**](https://github.com/tym83) in cozystack/website#470).

* **[website] Add CozySummit Virtual 2026 program announcement**: Published the CozySummit Virtual 2026 program announcement blog post ([**@tym83**](https://github.com/tym83) in cozystack/website#472).

* **[website] Add missing release announcements for v0.1–v0.41**: Backfilled missing release announcement blog posts for Cozystack versions v0.1 through v0.41 ([**@tym83**](https://github.com/tym83) in cozystack/website#468).

* **[talm] Render templates online in apply to resolve lookups**: Fixed talm `apply` command to render templates online, resolving template lookup failures when using modeline templates ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in cozystack/talm#119).

* **[talm] Update default Talos image to v1.12.6**: Updated the default Talos image version to v1.12.6 in talm ([**@kvaps**](https://github.com/kvaps) in cozystack/talm@03e9b6e).

---

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v1.2.1...v1.2.2
