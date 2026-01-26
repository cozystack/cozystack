<!--
https://github.com/cozystack/cozystack/releases/tag/v0.38.3
-->

## Improvements

* **[core:installer] Address buildx warnings for installer image builds**: Aligns Dockerfile syntax casing to remove buildx warnings, keeping installer builds clean ([**@nbykov0**](https://github.com/nbykov0) in #1682).
* **[system:coredns] Align CoreDNS app labels with Talos defaults**: Matches CoreDNS labels to Talos conventions so services select pods consistently across platform and tenant clusters ([**@nbykov0**](https://github.com/nbykov0) in #1675).
* **[system:monitoring-agents] Rename CoreDNS metrics service to avoid conflicts**: Renames the metrics service so it no longer clashes with the CoreDNS service used for name resolution in tenant clusters ([**@nbykov0**](https://github.com/nbykov0) in #1676).

---

**Full Changelog**: [v0.38.2...v0.38.3](https://github.com/cozystack/cozystack/compare/v0.38.2...v0.38.3)

