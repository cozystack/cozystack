## Security

* Resolve a security problem that allowed a tenant administrator to gain enhanced privileges outside the tenant. (@kvaps in https://github.com/cozystack/cozystack/pull/1062, backported in https://github.com/cozystack/cozystack/pull/1066)

## Fixes

* [platform] Fix dependencies in `distro-full` bundle. (@klinch0 in  https://github.com/cozystack/cozystack/pull/1056, backported in https://github.com/cozystack/cozystack/pull/1064)
* [platform] Fix RBAC for annotating namespaces. (@kvaps in https://github.com/cozystack/cozystack/pull/1031, backported in https://github.com/cozystack/cozystack/pull/1037)
* [platform] Reduce system resource consumption by using smaller resource presets for VerticalPodAutoscaler, SeaweedFS, and KubeOVN. (@klinch0 in https://github.com/cozystack/cozystack/pull/1054, backported in https://github.com/cozystack/cozystack/pull/1058)
* [dashboard] Fix a number of issues in the Cozystack Dashboard (@kvaps in https://github.com/cozystack/cozystack/pull/1042, backported in https://github.com/cozystack/cozystack/pull/1066)
* [apps] Specify minimal working resource presets. (@kvaps in https://github.com/cozystack/cozystack/pull/1040, backported in https://github.com/cozystack/cozystack/pull/1041)
* [apps] Update built-in documentation and configuration reference for managed Clickhouse application. (@NickVolynkin in https://github.com/cozystack/cozystack/pull/1059, backported in https://github.com/cozystack/cozystack/pull/1065)
