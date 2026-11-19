
<!--
https://github.com/cozystack/cozystack/releases/tag/v0.38.1
-->

## Improvements

* **[seaweedfs] Extended CA certificate duration to reduce disruptive CA rotations**: Extended CA certificate duration to reduce disruptive CA rotations. ([**@IvanHunters**](https://github.com/IvanHunters) in #1657, #1666).
* **[dashboard] Add config hash annotations to restart pods on config changes**: Added config hash annotations to restart pods when configuration changes, ensuring pods are automatically restarted when their configuration is updated ([**@kvaps**](https://github.com/kvaps) in #1662, #1665).

## Fixes

* **[tenant][kubernetes] Introduce better cleanup logic**: Improved cleanup logic for tenant Kubernetes resources, ensuring proper resource cleanup when tenants are deleted or updated ([**@kvaps**](https://github.com/kvaps) in #1661).
* **[dashboard] Fix loading arrays in forms when editing existing objects**: Fixed issue where arrays in forms were not loading correctly when editing existing objects in the dashboard ([**@kvaps**](https://github.com/kvaps)).

---

**Full Changelog**: [v0.38.0...v0.38.1](https://github.com/cozystack/cozystack/compare/v0.38.0...v0.38.1)

