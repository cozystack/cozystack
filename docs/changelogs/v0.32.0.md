Cozystack v0.32.0 is a significant release that brings new features, key fixes, and updates to underlying components.

## Major Features and Improvements

* [platform] Use `cozypkg` instead of Helm (@kvaps in https://github.com/cozystack/cozystack/pull/1057)
* [platform] Introduce the HelmRelease reconciler for system components. (@kvaps in https://github.com/cozystack/cozystack/pull/1033)
* [kubernetes] Enable using container registry mirrors by tenant Kubernetes clusters. Configure containerd for tenant Kubernetes clusters. (@klinch0 in https://github.com/cozystack/cozystack/pull/979, patched by @lllamnyp in https://github.com/cozystack/cozystack/pull/1032)
* [platform] Allow users to specify CPU requests in VCPUs. Use a library chart for resource management. (@lllamnyp in https://github.com/cozystack/cozystack/pull/972 and https://github.com/cozystack/cozystack/pull/1025)
* [platform] Annotate all child objects of apps with uniform labels for tracking by WorkloadMonitors. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1018 and https://github.com/cozystack/cozystack/pull/1024)
* [platform] Introduce `cluster-domain` option and un-hardcode `cozy.local`. (@kvaps in https://github.com/cozystack/cozystack/pull/1039)
* [platform] Get instance type when reconciling WorkloadMonitor (https://github.com/cozystack/cozystack/pull/1030)
* [virtual-machine] Add RBAC rules to allow port forwarding in KubeVirt for SSH via `virtctl`. (@mattia-eleuteri in https://github.com/cozystack/cozystack/pull/1027, patched by @klinch0 in https://github.com/cozystack/cozystack/pull/1028)
* [monitoring] Add events and audit inputs (@kevin880202 in https://github.com/cozystack/cozystack/pull/948)

## Security

* Resolve a security problem that allowed tenant administrator to gain enhanced privileges outside the tenant. (@kvaps in https://github.com/cozystack/cozystack/pull/1062)

## Fixes

* [dashboard] Fix a number of issues in the Cozystack Dashboard (@kvaps in https://github.com/cozystack/cozystack/pull/1042)
* [kafka] Specify minimal working resource presets. (@kvaps in https://github.com/cozystack/cozystack/pull/1040)
* [cilium] Fixed Gateway API manifest. (@zdenekjanda in https://github.com/cozystack/cozystack/pull/1016)
* [platform] Fix RBAC for annotating namespaces. (@kvaps in https://github.com/cozystack/cozystack/pull/1031)
* [platform] Fix dependencies for paas-hosted bundle. (@kvaps in https://github.com/cozystack/cozystack/pull/1034)
* [platform] Reduce system resource consumption by using lesser resource presets for VerticalPodAutoscaler, SeaweedFS, and KubeOVN. (@klinch0 in https://github.com/cozystack/cozystack/pull/1054)
* [virtual-machine] Fix handling of cloudinit and ssh-key input for `virtual-machine` and `vm-instance` applications. (@gwynbleidd2106 in https://github.com/cozystack/cozystack/pull/1019 and https://github.com/cozystack/cozystack/pull/1020)
* [apps] Fix Clickhouse version parsing. (@kvaps in https://github.com/cozystack/cozystack/commit/28302e776e9d2bb8f424cf467619fa61d71ac49a)
* [apps] Add resource quotas for PostgreSQL jobs and fix application readme generation check in CI. (@klinch0 in https://github.com/cozystack/cozystack/pull/1051)
* [kube-ovn] Enable database health check. (@kvaps in https://github.com/cozystack/cozystack/pull/1047)
* [kubernetes] Fix upstream issue by updating Kubevirt-CCM. (@kvaps in https://github.com/cozystack/cozystack/pull/1052)
* [kubernetes] Fix resources and introduce a migration when upgrading tenant Kubernetes to v0.32.4. (@kvaps in https://github.com/cozystack/cozystack/pull/1073)
* [cluster-api] Add a missing migration for `capi-providers`. (@kvaps in https://github.com/cozystack/cozystack/pull/1072)

## Dependencies

* Introduce cozykpg, update to v1.1.0. (@kvaps in https://github.com/cozystack/cozystack/pull/1057 and https://github.com/cozystack/cozystack/pull/1063)
* Update flux-operator to 0.22.0, Flux to 2.6.x. (@kingdonb in https://github.com/cozystack/cozystack/pull/1035)
* Update Talos Linux to v1.10.3. (@kvaps in https://github.com/cozystack/cozystack/pull/1006)
* Update Cilium to v1.17.4. (@kvaps in https://github.com/cozystack/cozystack/pull/1046)
* Update MetalLB to v0.15.2. (@kvaps in https://github.com/cozystack/cozystack/pull/1045)
* Update Kube-OVN to v1.13.13. (@kvaps in https://github.com/cozystack/cozystack/pull/1047)

## Documentation

* [Oracle Cloud Infrastructure installation guide](https://cozystack.io/docs/operations/talos/installation/oracle-cloud/). (@kvaps, @lllamnyp, and @NickVolynkin in https://github.com/cozystack/website/pull/168)
* [Cluster configuration with `talosctl`](https://cozystack.io/docs/operations/talos/configuration/talosctl/). (@NickVolynkin in https://github.com/cozystack/website/pull/211)
* [Configuring container registry mirrors for tenant Kubernetes clusters](https://cozystack.io/docs/operations/talos/configuration/air-gapped/#5-configure-container-registry-mirrors-for-tenant-kubernetes). (@klinch0 in https://github.com/cozystack/website/pull/210)
* [Explain application management strategies and available versions for managed applications.](https://cozystack.io/docs/guides/applications/). (@NickVolynkin in https://github.com/cozystack/website/pull/219)
* [How to clean up etcd state](https://cozystack.io/docs/operations/faq/#how-to-clean-up-etcd-state). (@gwynbleidd2106 in https://github.com/cozystack/website/pull/214)
* [State that Cozystack is a CNCF Sandbox project](https://github.com/cozystack/cozystack?tab=readme-ov-file#cozystack). (@NickVolynkin in https://github.com/cozystack/cozystack/pull/1055)

## Development, Testing, and CI/CD

* [tests] Add tests for applications `virtual-machine`, `vm-disk`, `vm-instance`, `postgresql`, `mysql`, and `clickhouse`. (@gwynbleidd2106 in https://github.com/cozystack/cozystack/pull/1048, patched by @kvaps in https://github.com/cozystack/cozystack/pull/1074)
* [tests] Fix concurrency for the `docker login` action. (@kvaps in https://github.com/cozystack/cozystack/pull/1014)
* [tests] Increase QEMU system disk size in tests. (@kvaps in https://github.com/cozystack/cozystack/pull/1011)
* [tests] Increase the waiting timeout for VMs in tests. (@kvaps in https://github.com/cozystack/cozystack/pull/1038)
* [ci] Separate build and testing jobs in CI. (@kvaps in https://github.com/cozystack/cozystack/pull/1005 and https://github.com/cozystack/cozystack/pull/1010)
* [ci] Fix the release assets. (@kvaps in https://github.com/cozystack/cozystack/pull/1006 and https://github.com/cozystack/cozystack/pull/1009)

## New Contributors

* @kevin880202 made their first contribution in https://github.com/cozystack/cozystack/pull/948
* @mattia-eleuteri made their first contribution in https://github.com/cozystack/cozystack/pull/1027

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v0.31.0...v0.32.0

<!-- 
HEAD https://github.com/cozystack/cozystack/commit/3ce6dbe8
-->
