# Render fixtures

`hack/check-render-matrix.sh` renders every app chart with its defaults plus these injected values. helm-unittest renders the templates selected by each suite, so a chart with passing unit tests can still have an untested template that fails to render. This sweep checks that Helm accepts the rendered YAML; it does not validate Kubernetes schemas or runtime behavior.

The source of `_cluster` is `packages/core/platform/templates/apps.yaml`; `_namespace` comes from `packages/apps/tenant/templates/namespace.yaml`. These fixtures model selected install states, not a complete copy of every platform setting. Top-level scalar values in these maps are strings, including booleans. Charts such as tenant compare `oidc-enabled` with `"true"`, so a YAML boolean has the wrong type.

`scheduling` and `branding` are maps copied with `toYaml`, which preserves their nested types. The scheduling keys read by app charts have a more specific contract: `globalAppTopologySpreadConstraints` is a string containing YAML (or an empty string), and `dedicatedNodesForWindowsVMs` is compared with the string `"true"`. The configured and wildcard fixtures supply a topology constraint; fresh leaves it empty. The embedded YAML retains numeric fields such as `maxSkew`.

`_namespace.<service>` contains the namespace providing the shared service, or an empty string when unavailable. A non-empty value enables the service-dependent templates in the Kubernetes chart. These states use ingress rather than Gateway API, so `gateway` stays empty: a non-empty gateway would disable Harbor Ingress and leave the certificate modes untested.

| File | State |
|---|---|
| `fresh.yaml` | OIDC off, no certificates or shared services, no topology constraint |
| `configured.yaml` | OIDC on, per-host ACME via http01, shared services and topology constraint |
| `wildcard.yaml` | OIDC on, dns01 with a platform-issued wildcard certificate, shared services and topology constraint |

Each chart is attempted under every fixture. `vm-instance` and `kubernetes-nodes` require a `VirtualMachineClusterInstancetype` lookup on their defaults. The sweep accepts their skip only while every fixture fails with the recorded lookup diagnostic; a successful render or a different error fails the sweep. For kubernetes-nodes it supplies a parent cluster and matching release name first. For tenant it declares the Keycloak API with `--api-versions v1.edp.epam.com/v1`, allowing OIDC-enabled fixtures to render the realm groups.

`lookup` returns an empty result during this check. Tests for existing objects belong in chart-specific helm-unittest suites using `kubernetesProvider`; `packages/apps/clickhouse/tests/backup_api_password_persistence_test.yaml` covers both password generation and preservation that way. Presets, replica counts and other app feature toggles are outside this sweep.

The BATS suite checks fixture types with the small Helm chart in `validator/`, using `--set-file` so explicit nulls are not removed by Helm's values merge. Separate assertions check PostgreSQL topology constraints, tenant OIDC groups, the Kubernetes metrics-server HelmRelease and Harbor per-host ACME versus wildcard TLS. Pairwise comparison of complete tenant renders detects duplicate fixture states, and a second render of each state checks that randomness cannot satisfy that comparison. The branch assertions are needed because tenant also copies injected values into a Secret, so different output alone does not prove a conditional resource was rendered.

When adding an injected value read by an app, update the relevant states and add an assertion for the branch it exercises. Review all read forms, including `index`, `get` and `dig`, before deciding a value is unused:

```sh
grep -rn -e '_cluster' -e '_namespace' packages/apps/*/templates/
```
