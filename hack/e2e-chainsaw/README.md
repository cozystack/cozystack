# Chainsaw E2E tests

The Cozystack E2E app suite, written for [Kyverno Chainsaw](https://github.com/kyverno/chainsaw) — a declarative, Kubernetes-native E2E test framework (CNCF, part of the Kyverno project; successor to KUTTL).

This directory replaces the per-app BATS suite that used to live in `hack/e2e-apps/*.bats`. The CI `e2e` job runs every suite under `hack/e2e-chainsaw/` through `hack/e2e-chainsaw-run.sh` after `make install-cozystack`; cluster bootstrap (`hack/e2e-prepare-cluster-container.bats`, `hack/e2e-install-cozystack.bats`) and the OpenAPI checks (`hack/e2e-test-openapi.bats`) remain BATS.

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

A suite is a directory directly under `hack/e2e-chainsaw/` in or below which Chainsaw finds a Test. Three layouts do that, and the selector (`hack/select-e2e.sh`) and the leak-guard allowlist both recognise the same three:

- `chainsaw-test.yaml`, the usual one, or `chainsaw-test.yml` when the `.yaml` is absent;
- numbered step files such as `01-install.yaml`, which Chainsaw turns into a Test on its own when neither of the above exists;
- either of those in a subdirectory of the suite, since Chainsaw walks every subdirectory of the directory it is handed.

A parked `chainsaw-test.yaml.disabled` is none of these: the directory is then not a suite, is selected by nothing and cannot be allowlisted.

Suites: `postgres`, `bucket`, `mariadb`, `mongodb`, `redis`, `valkey`, `qdrant`, `clickhouse`, `kafka`, `kafka-metadata`, `rabbitmq`, `etcd`, `openbao`, `harbor`, `foundationdb`, `external-dns`, `kuberture`, `vminstance`, `gateway`, `opensearch`, `monitoring`, `kubernetes-latest`, `kubernetes-previous`, `securitygroup`, `seaweedfs`, `computeplane`, `observability-proxy`. `kubernetes-proxmox` is parked as `chainsaw-test.yaml.disabled` and runs only in `.github/workflows/e2e-proxmox.yaml`, which needs a Proxmox hypervisor.

## What Chainsaw buys over the BATS suite

- **Readable tests** — a list of resources and expected states, not shell control flow. The `timeout N sh -ec "until kubectl get ..."` + `kubectl wait` pair that appeared ~100 times collapses into a single `assert` that polls existence and state together.
- **Distinguishable failure modes** — "object never created", "created but never ready", and "wrong status" all surface as a structured diff instead of an opaque shell timeout.
- **Automatic diagnostics** — `catch` blocks dump events, `describe`, and pod logs on any failure (the BATS suite only did this in `harbor.bats`, by hand).
- **Automatic cleanup** — resources Chainsaw applied are deleted (and waited on) in reverse order; `finally` covers operator-created leftovers.
- **CI-ready output** — a JUnit `chainsaw-report.xml` is emitted for the test summary.

## Test anatomy

Each suite is a directory with one or more `Test` docs (in one of the layouts above, usually a `chainsaw-test.yaml`) plus the manifests it applies. A step has `try` (operations: `apply`, `assert`, `error`, `script`, `delete`, …), optional `catch` (diagnostics on failure), and optional `finally` (teardown regardless of outcome).

Declarative suites assert on `status.conditions` and concrete fields. Inherently imperative suites keep their logic in `script` steps:

- **openbao** — `bao operator init`/`unseal` via `kubectl exec`.
- **kuberture** — `jq` on Service annotations + external-dns split-horizon log assertions.
- **vminstance** — a server-side dry-run eviction of the VM's launcher pod, checked against the VMI's `LiveMigratable` condition.
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

## Initial WorkerClass upgrade scenario

For worker-class changes, and for any change to the parent `kubernetes` application, which owns the worker join path, the same-repository PR lane selects `COZY_WORKER_UPGRADE_MODE=required`; the fork lane (`e2e-fork.yaml`) does not compute the mode, so a PR from a fork never runs the upgrade scenario. `hack/worker-upgrade-inputs.sh` validates the immutable source/operator/packages bundle in `hack/testdata/worker-upgrade-baseline.json` and prepares the baseline installer from that exact Git object. Missing or floating inputs fail rather than fall back to a fresh install. The regular installer step installs this baseline; the runner moves the existing `kubernetes-latest` suite first, then runs candidate OpenAPI checks and each remaining selected suite once.

The suite creates one default and one custom classless Talos pool before upgrading, with one real worker and a scheduled workload per pool. The cluster runs the newest Kubernetes minor that the baseline and the candidate share; when the candidate offers a newer minor, `kubernetes-previous` runs that one instead of the second newest, so a change that adds a minor still brings it up. In this mode the default pool `md0` runs one worker instead of the usual two and does not scale beyond it, so the two tenant nodes the suite waits for come from the two pools. It captures application/release/worker identities and hashes of consumed templates, upgrades the same installer while preserving user values, waits for the candidate source digest and worker backend, and compares identities, readiness and workloads. A new default-class Talos worker must then join, report the provider/tenant identity and run a scheduler-placed pod. Only this new pool is scaled to zero, and its Machine, VM, VMI, launcher and captured PVCs must drain before Kairos uses the same slot. In required mode the Kairos pool test runs only for a Kairos change, and only while the job can still fit it, the teardown and the other Tests of the `kubernetes-latest` suite file; its outcome is recorded in `snapshots/worker-upgrade/kairos-operation.txt`. Kairos changes gate on the Kairos operation: when `hack/worker-upgrade-needed.sh --kairos` selects the change (the Kairos provider files, image and patches, and the templates and chart files of `capi-providers-bootstrap` it shares with the Talos provider, the pool chart outside its tests, since every template of it is evaluated for a Kairos pool, the worker admission and class code every Kairos pool passes through, the Kairos e2e library, the upgrade library `_lib/run-kubernetes-upgrade.sh` that bounds the operation, or `kubernetes-latest/chainsaw-test.yaml`, which runs it), missing headroom or a failure fails the lane. Every change to the pool chart, Talos-only changes included, is therefore gated on the Kairos operation and its step timeout. For any other change the required upgrade run does not start the Kairos pool test: it lives inside `kubernetes-latest`, whose teardown removes the cluster it joins, and running it there would spend the budget the candidate checks and the remaining suites need. The two original workers remain untouched until final cleanup; peak guest count stays at three. Worker specs and opaque application/release values are hashed rather than exported. Schema-injected Application GET defaults are retained as separate wire observations, not confused with changed stored Helm values. The app ArtifactGenerators must have consumed the candidate package source, with their inventories matching the generated chart artifacts. The parent and both pool releases must reconcile their current chart artifacts and observed generations without reverting below the baseline Helm revision. An unchanged chart can remain at its deployed revision; stale Ready conditions are insufficient. Evidence is retained under `snapshots/worker-upgrade` in the normal report archive. Cleanup runs once after the scenario; the creator and environment preparation are not repeated across the measurement boundary. Every Deployment, StatefulSet and DaemonSet the upgrade creates in a `cozy-*` namespace is recorded by its actual UID as an installation ancestor for the leak guard, since the pinned baseline lacks whatever main has added since; the worker controllers the baseline did not already run must be among them. This exempts no namespace as such, no labels and no tenant workload: a workload a suite creates later, or in a tenant namespace, is still checked.

The job cap is 255 minutes. Required mode derives its deadline from the actual Actions job start, reserving 35 minutes for reporting and teardown, starts a suite only while time is left before that deadline, stops a suite still running when it arrives, and reports budget exhaustion as a failure, including unrun suites in the merged report. Its operation ceilings are not a claim that every full-suite failure path fits; a path that does not fit ends as a budget failure instead of overrunning the reporting reserve. Ordinary jobs without required mode keep their fresh-install path and do not establish upgrade coverage. A change to the lane's own inputs (the installer chart or install step, image-cache inputs, Kubernetes suite definitions and budget configuration, the runner, the workflow) selects required mode, so that run installs the pinned baseline and upgrades it to the candidate; a fresh install of the candidate installer is not exercised in such a run. Changes limited to the shared leak/remediation guard, leak allowlist, generic shell test runner or OpenAPI checks do not select this worker-specific scenario. Source/unit results are not evidence that the real upgrade has executed successfully. The upgrade helper uses GNU coreutils; on macOS its unit fixtures require the installed GNU `gnubin` directory ahead of the system tools.

## Running locally

Requires a cluster with Cozystack installed and a `tenant-test` namespace (the environment `hack/e2e-install-cozystack.bats` produces). `bucket` and `seaweedfs` additionally need `mc`, `nc`, and `jq` on the host; `kuberture`/`openbao`/`foundationdb`/`securitygroup`/`computeplane`/`kubernetes-latest`/`kubernetes-previous`/`vminstance` need `jq`; `observability-proxy` needs `curl` and `nc`.

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

The `e2e` job in `.github/workflows/pull-requests.yaml` runs `make -C packages/core/testing test-chainsaw` (which execs `hack/e2e-chainsaw-run.sh` inside the e2e sandbox) and uploads `chainsaw-report.xml`. The runner calls `chainsaw test` once per suite directory, fails a suite whose pods outlive its cleanup by more than the default `timeouts.cleanup` (`LEAKED-PODS suite=<dir>` in the log; see `docs/agents/e2e-testing.md` §8 and `_lib/leak-allowlist.txt` for known, filed leaks), and merges the per-suite reports into that one file. A local `chainsaw test` as shown above runs the same tests without the leak check. The sandbox image (`packages/core/testing/images/e2e-sandbox/Dockerfile`) ships the `chainsaw` binary alongside `kubectl`/`helm`/`mc`/`jq`/`nc`, built from source with a patch that keeps an assert polling through transient API errors such as an etcd leader change; a released `chainsaw` run locally fails the assert on the first such error instead.
