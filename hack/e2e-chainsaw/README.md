# Chainsaw E2E tests

The Cozystack E2E app suite, written for [Kyverno Chainsaw](https://github.com/kyverno/chainsaw) — a declarative, Kubernetes-native E2E test framework (CNCF, part of the Kyverno project; successor to KUTTL).

This directory replaces the per-app BATS suite that used to live in `hack/e2e-apps/*.bats`. The CI `e2e` job runs `chainsaw test hack/e2e-chainsaw/` after `make install-cozystack`; cluster bootstrap (`hack/e2e-install-cozystack.bats`) and the OpenAPI checks (`hack/e2e-test-openapi.bats`) remain BATS.

## Layout

```
hack/e2e-chainsaw/
├── .chainsaw.yaml          # global Configuration (namespace, timeouts, JUnit report)
├── _lib/                   # shell reused by the script-heavy suites
│   ├── run-kubernetes.sh   #   tenant Kubernetes bringup + LB/NFS/ouroboros checks
│   └── remediation-guard.sh
├── <app>/
│   ├── chainsaw-test.yaml  # one or more Test definitions
│   └── <app>.yaml          # the manifests the test applies
└── ...
```

Suites: `postgres`, `bucket`, `mariadb`, `mongodb`, `redis`, `qdrant`, `clickhouse`, `kafka`, `etcd`, `openbao`, `harbor`, `foundationdb`, `external-dns`, `kuberture`, `vminstance`, `gateway`, `kubernetes-latest`, `kubernetes-previous`.

## What Chainsaw buys over the BATS suite

- **Readable tests** — a list of resources and expected states, not shell control flow. The `timeout N sh -ec "until kubectl get ..."` + `kubectl wait` pair that appeared ~100 times collapses into a single `assert` that polls existence and state together.
- **Distinguishable failure modes** — "object never created", "created but never ready", and "wrong status" all surface as a structured diff instead of an opaque shell timeout.
- **Automatic diagnostics** — `catch` blocks dump events, `describe`, and pod logs on any failure (the BATS suite only did this in `harbor.bats`, by hand).
- **Automatic cleanup** — resources Chainsaw applied are deleted (and waited on) in reverse order; `finally` covers operator-created leftovers.
- **CI-ready output** — a JUnit `chainsaw-report.xml` is emitted for the test summary.

## Test anatomy

Each suite is a directory with a `chainsaw-test.yaml` (one or more `Test` docs) plus the manifests it applies. A step has `try` (operations: `apply`, `assert`, `error`, `script`, `delete`, …), optional `catch` (diagnostics on failure), and optional `finally` (teardown regardless of outcome).

Declarative suites assert on `status.conditions` and concrete fields. Inherently imperative suites keep their logic in `script` steps:

- **openbao** — `bao operator init`/`unseal` via `kubectl exec`.
- **kuberture** — `jq` on Service annotations + external-dns split-horizon log assertions.
- **gateway** — admission-rejection cases that need `kubectl --as` impersonation or error-message greps; the tenant apex is read from the namespace `namespace.cozystack.io/host` label at runtime so they pass on any host.
- **kubernetes-latest/previous** — wrap `_lib/run-kubernetes.sh` (Kamaji bringup, port-forward, LB/NFS/ouroboros). The script `cd`s to the repo root (the test dir is `hack/e2e-chainsaw/<suite>`, so repo root is `../../..`).

## Conventions / gotchas (Chainsaw v0.2.15)

- Condition asserts use the **filter-as-list** form, not the documented `[0]` index:
  ```yaml
  status:
    (conditions[?type == 'Ready']):
    - status: "True"
  ```
  The `(conditions[?type == 'Ready'])[0]: {status: "True"}` idiom fails with "field not found in the input object".
- A reading of a failure diff: the **"actual" side is a projection onto the expected fields** — `status: {}` means the JMESPath expression failed, not that status is empty. Read the error line above the diff first.
- `error` with a bare resource ref (name only) asserts the resource **does not exist** (used for "PVC reclaimed", "no backup schedule", "child has no Gateway HR").
- `script` runs in the **test directory** with `$NAMESPACE` set to the config namespace (`tenant-test`).

## Running locally

Requires a cluster with Cozystack installed and a `tenant-test` namespace (the environment `hack/e2e-install-cozystack.bats` produces). `bucket` additionally needs `mc` and `nc` on the host; `kuberture`/`openbao`/`harbor` need `jq`.

```bash
# install chainsaw
go install github.com/kyverno/chainsaw@latest   # or: brew install kyverno/chainsaw/chainsaw

# run all suites (config picked up from .chainsaw.yaml)
cd hack/e2e-chainsaw
chainsaw test

# run a single suite
chainsaw test postgres/
```

`kubernetes-latest`/`kubernetes-previous` each bring up a full tenant Kubernetes cluster (~25 min, real worker nodes) and `vminstance` boots a nested VM — expect a long run for the full set.

## CI integration

The `e2e` job in `.github/workflows/pull-requests.yaml` runs `make -C packages/core/testing test-chainsaw` (which execs `chainsaw test hack/e2e-chainsaw/` inside the e2e sandbox) and uploads `chainsaw-report.xml`. The sandbox image (`packages/core/testing/images/e2e-sandbox/Dockerfile`) ships the `chainsaw` binary alongside `kubectl`/`helm`/`mc`/`jq`/`nc`.
