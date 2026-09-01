# Render fixtures

Values the platform injects into every app chart at install time, reproduced
here so `hack/check-render-matrix.sh` can render a chart the way it is actually
rendered in a cluster.

The authority is `packages/core/platform/templates/apps.yaml`, which builds the
`_cluster` map into the `cozystack-values` Secret, plus
`templates/bundles/system.yaml` for `oidc-enabled`. **Every value there goes
through `| quote`, so every value here is a string** — including the booleans.
That is not cosmetic: charts compare them with `eq $oidcEnabled "true"`, and a
real bool makes helm fail with "incompatible types for comparison". A fixture
that guesses the type tests a shape the platform never produces.

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
grep -rhoE '_cluster "[a-z-]*"|_cluster\.[a-zA-Z-]*' packages/apps/*/templates/
```
