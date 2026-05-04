# cozy-lib

Shared Helm library chart for Cozystack platform charts.

Provides reusable template helpers — TLS certificate management (cert-manager integration)
and RBAC/quota utilities. Helpers are documented inline in their respective `_*.tpl` files.

This chart has no configurable `values.yaml` parameters; all configuration is passed
via template arguments at call sites.
