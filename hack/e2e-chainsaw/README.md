# Chainsaw E2E tests (pilot)

This directory is a pilot for migrating the BATS E2E app tests (`hack/e2e-apps/*.bats`) to [Kyverno Chainsaw](https://github.com/kyverno/chainsaw) — a declarative, Kubernetes-native E2E test framework (CNCF, part of the Kyverno project; successor to KUTTL).

Two representative tests are ported as examples:

| Chainsaw test | BATS original |
|---|---|
| [`postgres/`](./postgres/) | `hack/e2e-apps/postgres.bats` |
| [`bucket/`](./bucket/) | `hack/e2e-apps/bucket.bats` |

## Why

The BATS suite carries ~100 copies of the same polling boilerplate:

```bash
timeout 60 sh -ec "until kubectl -n tenant-test get hr postgres-$name >/dev/null 2>&1; do sleep 2; done"
kubectl -n tenant-test wait hr postgres-$name --timeout=5m --for=condition=ready
```

The two-step dance exists because `kubectl wait` fails immediately when the object does not exist yet. In Chainsaw the same logic is one declarative assertion that polls existence and state together:

```yaml
- assert:
    timeout: 6m
    resource:
      apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      metadata:
        name: postgres-test
      status:
        (conditions[?type == 'Ready'])[0]:
          status: "True"
```

What this buys us:

- **Readable tests** — a test is a list of resources and expected states, not shell control flow. No `pipefail` traps, no `|| true`, no `grep` on jsonpath.
- **Distinguishable failure modes** — "object never created" and "created but never ready" both surface as an assertion timeout with a structured diff of expected vs. actual state; "wrong status" shows the exact mismatched field. Negative tests (a state that *must* be reached, e.g. a webhook rejection) use the `error` operation.
- **Configurable timeouts** at four levels: global config → test → step → operation.
- **Automatic diagnostics** — `catch` blocks dump events, `describe` output, and pod logs on any failure. Today only `harbor.bats` does this, by hand.
- **Automatic cleanup** — resources applied by Chainsaw are deleted (and waited on) in reverse order when the test finishes, pass or fail.
- **Custom checks** — `script` operations cover anything imperative (`psql` probe, `mc` S3 access checks), with JMESPath checks on stdout/stderr/exit code.
- **CI-ready output** — JUnit XML report (`chainsaw-report.xml`), built-in parallelism for when tests stop sharing fixed ports/names.

## Test anatomy

Each test is a directory with a `chainsaw-test.yaml` (the `Test` definition) plus the manifests it applies:

```
postgres/
├── chainsaw-test.yaml   # steps: apply → assert ready → probe; catch; finally
└── postgres.yaml        # the Postgres CR, namespace injected by chainsaw
```

A step has three blocks:

- `try` — the operations (`apply`, `assert`, `error`, `script`, `delete`, …)
- `catch` — diagnostics on failure (`events`, `describe`, `podLogs`, …); also definable test-wide in `spec.catch`
- `finally` — teardown that runs regardless of outcome

## Running locally

Requires a cluster with Cozystack installed and the `tenant-test` namespace (the same environment the BATS suite runs against, see `hack/e2e-install-cozystack.bats`). The bucket test additionally needs `mc`, `jq`, and `nc` on the host, same as its BATS original.

```bash
# install chainsaw
go install github.com/kyverno/chainsaw@latest
# or: brew install kyverno/chainsaw/chainsaw

# run all tests (config is picked up from .chainsaw.yaml)
cd hack/e2e-chainsaw
chainsaw test

# run a single test
chainsaw test postgres/
```

## CI integration (follow-up, not in this PR)

The suite slots into the existing sandbox flow next to `cozytest.sh`: run `chainsaw test hack/e2e-chainsaw/` after `make install-cozystack`, upload `chainsaw-report.xml` as an artifact. Once parity is proven, each migrated `.bats` file is deleted; the per-test 3-retry loop in `.github/workflows/pull-requests.yaml` can likely be reduced, since assertion polling removes the fixed-timeout flakiness most retries paper over.

## BATS → Chainsaw cheat sheet

| BATS construction | Chainsaw equivalent |
|---|---|
| `kubectl apply -f - <<EOF` heredoc | `apply: {file: app.yaml}` |
| `timeout N sh -ec "until kubectl get X; do sleep 2; done"` | implicit — `assert` polls until the object exists |
| `kubectl wait --for=condition=ready` | `assert` on `status.conditions` |
| `kubectl wait --for=jsonpath='{.status.x}'` | `assert` on the status field |
| `kubectl get svc -o jsonpath \| grep -q 5432` | `assert` with `(ports[0].port): 5432` |
| `! some-command` (must fail) | `script` with exit-code check, or `error` op for resource state |
| manual `kubectl describe` / `logs` on failure | `catch: [events, describe, podLogs]` |
| trailing `kubectl delete` cleanup | automatic cleanup + `finally` for operator-created leftovers |
| `--timeout=5m` scattered per command | timeout cascade: config → test → step → operation |
