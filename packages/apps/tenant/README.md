# Tenant

A tenant is the main unit of security on the platform. The closest analogy would be Linux kernel namespaces.

Tenants can be created recursively and are subject to the following rules:

### Tenant naming

Tenant names must be alphanumeric.
Using dashes (`-`) in tenant names is not allowed, unlike with other services.
This limitation exists to keep consistent naming in tenants, nested tenants, and services deployed in them.

For example:

-   The root tenant is named `root`, but internally it's referenced as `tenant-root`.
-   A nested tenant could be named `foo`, which would result in `tenant-foo` in service names and URLs.
-   However, a tenant can not be named `foo-bar`, because parsing names such as `tenant-foo-bar` would be ambiguous.

### Unique domains

Each tenant has its own domain.
By default, (unless otherwise specified), it inherits the domain of its parent with a prefix of its name.
For example, if the parent had the domain `example.org`, then `tenant-foo` would get the domain `foo.example.org` by default.

Kubernetes clusters created in this tenant namespace would get domains like: `kubernetes-cluster.foo.example.org`

Example:
```text       
tenant-root (example.org)
└── tenant-foo (foo.example.org)
    └── kubernetes-cluster1 (kubernetes-cluster1.foo.example.org)
```

### Nesting tenants and reusing parent services

Tenants can be nested.
A tenant administrator can create nested tenants using the "Tenant" application from the catalogue.

Higher-level tenants can view and manage the applications of all their children tenants.
If a tenant does not run their own cluster services, it can access ones of its parent.

For example, you create:
-   Tenant `tenant-u1` with a set of services like `etcd`, `ingress`, `monitoring`.
-   Tenant `tenant-u2` nested in `tenant-u1`.

Let's see what will happen when you run Kubernetes and Postgres under `tenant-u2` namespace.

Since `tenant-u2` does not have its own cluster services like `etcd`, `ingress`, and `monitoring`,
the applications running in `tenant-u2` will use the cluster services of the parent tenant.

This in turn means:

-    The Kubernetes cluster data will be stored in `etcd` for `tenant-u1`.
-    Access to the cluster will be through the common `ingress` of `tenant-u1`.
-    Essentially, all metrics will be collected in the `monitoring` from `tenant-u1`, and only that tenant will have access to them.

Example:
```
tenant-u1
├── etcd
├── ingress
├── monitoring
└── tenant-u2
    ├── kubernetes-cluster1
    └── postgres-db1
```

## Parameters

### Common parameters

| Name             | Description                                                                                                                | Type                  | Value   |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------- | --------------------- | ------- |
| `host`           | The hostname used to access tenant services (defaults to using the tenant name as a subdomain for its parent tenant host). | `string`              | `""`    |
| `etcd`           | Deploy own Etcd cluster.                                                                                                   | `bool`                | `false` |
| `monitoring`     | Deploy own Monitoring Stack.                                                                                               | `bool`                | `false` |
| `ingress`        | Deploy own Ingress Controller.                                                                                             | `bool`                | `false` |
| `seaweedfs`      | Deploy own SeaweedFS.                                                                                                      | `bool`                | `false` |
| `isolated`       | Enforce tenant namespace with network policies (default: true).                                                            | `bool`                | `true`  |
| `resourceQuotas` | Define resource quotas for the tenant.                                                                                     | `map[string]quantity` | `{}`    |

