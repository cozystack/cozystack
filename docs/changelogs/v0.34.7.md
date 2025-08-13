<!--
https://github.com/cozystack/cozystack/releases/tag/v0.34.7
-->

## Fixes

* [seaweedfs] Disable proxy buffering and proxy request buffering for ingress. (@kvaps in https://github.com/cozystack/cozystack/pull/1330, backported in https://github.com/cozystack/cozystack/commit/96d462e911d4458704b596533d3f10e4b5e80862)
* [linstor] Update LINSTOR monitoring configuration to use label `controller_node` instead of `node`. (@kvaps in https://github.com/cozystack/cozystack/pull/1326, backported in https://github.com/cozystack/cozystack/pull/1327)
* [kubernetes] Disable VPA for VPA in tenant Kubernetes clusters, patched a fix from v0.34.5. (@lllamnyp in https://github.com/cozystack/cozystack/pull/1318, backported in https://github.com/cozystack/cozystack/pull/1319)

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v0.34.6...v0.34.7
