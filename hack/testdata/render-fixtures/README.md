# Render fixtures

Values the platform injects into every app chart at install time, reproduced
here so `hack/check-render-matrix.sh` can render a chart the way it is actually
rendered in a cluster.

The authority is `packages/core/platform/templates/apps.yaml`, which builds the `_cluster` map
into the `cozystack-values` Secret, plus `templates/bundles/system.yaml` for
`oidc-enabled`. `_namespace` comes from
`packages/apps/tenant/templates/namespace.yaml`.

**Every scalar there goes through `| quote`, so every scalar here is a string** —
including the booleans. That is not cosmetic: `tenant` compares one with
`eq $oidcEnabled "true"`, and a real bool makes helm fail with "incompatible
types for comparison". `scheduling` and `branding` are the exceptions: the
platform emits those as maps.

`_namespace.<service>` is not a boolean at all, which is easy to get wrong: the
value is the NAMESPACE providing that service, or an empty string when the tenant
has none (`$etcd = $tenantName` in namespace.yaml). Charts test it with a bare
`{{- if .Values._namespace.etcd }}`, so `""` is the off side and any name is the
on side. Omitting the key entirely renders the off side, which is a state the
platform does produce — but then nothing renders the on side, and in
`packages/apps/kubernetes` that is 15 manifests.

Each file is one cluster state, and a chart is rendered under all of them. The
states exist because charts branch on these values, so a single fixture renders
one side of each branch and passes a chart whose other side is broken.

| file | state it represents |
|---|---|
| `fresh.yaml` | a new install: nothing configured, OIDC off, no certificates |
| `configured.yaml` | OIDC on, per-host ACME via http01, services exposed |
| `wildcard.yaml` | dns01 with a platform-issued wildcard certificate |

When a chart starts reading a `_cluster` key none of these set, add it to all
three. A key absent from every fixture is a branch nothing renders, and it fails
silently by passing. To list what the charts read:

```sh
grep -rn '_cluster' packages/apps/*/templates/
```

Grep for the bare word rather than a shape: the reads are written four different
ways (`.Values._cluster.foo`, `index .Values._cluster "foo"`, `dig "foo" ...`,
and `(.Values._cluster | default dict)`), so any pattern narrow enough to look
tidy misses some of them. A key absent from every fixture is a branch nothing
renders, and it fails silently by passing, so a false negative here is the
expensive kind.
