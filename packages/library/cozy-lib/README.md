# cozy-lib

Shared Helm library chart for Cozystack platform charts.

Provides reusable template helpers:

- **Input validation** (`_checkinput.tpl`) — input validation utilities
- **CozyStack config** (`_cozyconfig.tpl`) — platform configuration helpers
- **Network** (`_network.tpl`) — network configuration helpers
- **RBAC** (`_rbac.tpl`) — role-based access control helpers
- **Resource presets** (`_resourcepresets.tpl`) — resource preset management
- **Resources** (`_resources.tpl`) — Kubernetes resource helpers
- **Strings** (`_strings.tpl`) — string manipulation utilities
- **TLS** (`_tls.tpl`) — cert-manager Certificate CR generation and TLS secret name resolution

Helpers are documented inline in their respective `_*.tpl` files.

This chart has no configurable `values.yaml` parameters; all configuration is passed
via template arguments at call sites.
