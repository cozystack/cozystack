# E2E Testing Conventions

Guidance for writing, changing, and reviewing Cozystack's end-to-end (E2E) tests and the CI that runs them. Read this **before** touching anything under `hack/e2e-chainsaw/`, the bootstrap/OpenAPI BATS (`hack/e2e-*.bats`), `hack/*.sh` test helpers, the E2E CI workflow (`.github/workflows/pull-requests.yaml`), or `packages/core/testing/`.

The app suite is **Kyverno Chainsaw** (`hack/e2e-chainsaw/`, one directory per app, each with a `chainsaw-test.yaml`). Cluster bootstrap (`hack/e2e-install-cozystack.bats`, `hack/e2e-prepare-cluster.bats`) and the OpenAPI checks (`hack/e2e-test-openapi.bats`) remain BATS.

## The core principle

**Retries do not recover flakes — they hide deterministic bugs and triple diagnostic wall-time.** An audit of 30 PR runs found that across sampled failures, **25/25 retry attempts failed**: the retry loop never once turned a red run green, it only delayed surfacing real bugs (Helm namespace conflicts, seaweedfs/harbor races, a vminstance disk race) that *looked* like flakes because the retry sometimes coincided with transient state clearing.

Every convention below follows from that finding: **fail fast, fail loud, make the failure legible.** A test that flakes is a test (or a product) with a real race to fix, not a test to wrap in a retry. Chainsaw's per-assertion polling is the structural expression of this: each `assert` waits on its own condition to its own timeout, and a failure is reported as a structured diff plus auto-captured events/describe/logs — not a generic non-zero exit.

## Conventions

### 1. No retries on deterministic steps; retry only pure infrastructure

- `Run E2E tests` and `Install Cozystack` run **once**. On failure they print `❌ ... (no retry — see diagnostics below)` and dump state. Do not reintroduce a retry loop around them. The E2E step is a single `chainsaw test` invocation — there is no per-app retry loop to bring back, because Chainsaw already polls each assertion to its own timeout.
- `Prepare environment` keeps its 3× retry — and *only* it — because it is pure infrastructure (Talos image download, sandbox VM boot, network) where a transient runner hiccup is a genuine flake with no application logic involved.
- Rule of thumb: retry is justified only for a step that contains **no product or test logic**.

### 2. Prefer declarative asserts over imperative waits

A Chainsaw `assert` polls until the resource matches its expected shape or the per-operation timeout fires, so it subsumes both "the resource exists" and "the field/condition reached this value" in one operation — the old `until kubectl get …; do sleep 2; done` existence backstop followed by `kubectl wait --for=…` is no longer needed.

```yaml
- assert:
    timeout: 6m
    resource:
      apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      metadata:
        name: postgres-test
      status:
        (conditions[?type == 'Ready']):
        - status: "True"
```

- Condition checks use the **filter-as-list** form `(conditions[?type == 'Ready'])`. Chainsaw v0.2.15 throws "field not found" on the indexed `(...)[0]` form.
- Imperative checks that no Kubernetes API condition models — S3 reachability through a port-forward, an external LB HTTP path, MetalLB advertisement — stay in a `script` step. A bare `sleep N` inside such a script is allowed **only** when nothing better models the wait; annotate it with a `TODO(e2e-replace-fixed-timeouts):` comment. The bootstrap BATS (`hack/e2e-install-cozystack.bats`, `hack/e2e-chainsaw/_lib/run-kubernetes.sh`) carries the sanctioned `until kubectl get` exceptions.

### 3. Let Chainsaw own cleanup; no test-level EXIT/RETURN traps

Trap-based cleanup was the single biggest source of false failures in the BATS suite: an `EXIT` trap ran cleanup in a context where shell variables were unset, `set -u` killed it, and a **successful** test was marked failed — then retried twice into real failures.

- Chainsaw deletes the resources it `apply`-ed during its cleanup phase (bounded by the `delete`/`cleanup` timeouts in `hack/e2e-chainsaw/.chainsaw.yaml`). Do not hand-roll teardown for resources Chainsaw created.
- A self-contained `trap '… ' EXIT` **inside a single `script` step** — to kill a port-forward or remove a temp dir — is fine, because it runs in a contained subprocess with its variables in scope. See `hack/e2e-chainsaw/bucket/chainsaw-test.yaml`. What is banned is test-level trap-based cleanup of the BATS kind.

### 4. Do not mask cleanup or teardown failures

- Resources a test creates that Chainsaw cannot reclaim — because a controller, not the test, owns them (e.g. the Velero `Backup`, `BackupStorageLocation`, and credentials secret in `cozy-velero` in the disabled `hack/e2e-chainsaw/backup/` suite) — must be pruned explicitly. Do not `|| true` over a stuck delete and leave stale state for the next run.
- For nested tenants, tear down **child → parent with a hard wait for deletion between** each. Deleting the parent while a child is still uninstalling wedges the parent's cleanup Job on the child namespace, and both stuck uninstalls occupy helm-controller workers past the end of the test — starving whichever suite runs next. The kubernetes suites encode this ordering in `hack/e2e-chainsaw/_lib/run-kubernetes.sh`.

### 5. The install gate must have teeth

The install test fails if **any** HelmRelease is not Ready. A toothless gate (backgrounded `kubectl wait` discarding child exit codes + a bare `echo` instead of `exit 1`) once shipped a permanently-failing platform HR through green CI for weeks.

- Use a single `kubectl wait hr --all -A`, then an outcome-based re-list (covers HRs created after the snapshot), and `exit 1` on any non-Ready HR.
- Dump the full Ready-condition message per non-Ready HR so the real error is in the test output. See `hack/e2e-install-cozystack.bats`.

### 6. Assert the parent HelmRelease did not remediate — via `status.history`

A parent HelmRelease that hit its wait timeout, uninstalled, and reinstalled is a silent race we want to catch. **Do not** check `.status.installFailures` / `.status.upgradeFailures`: Flux's `ClearFailures()` zeroes those on every successful reconcile, so checking them after the HR is Ready is **vacuous** and passes against a reverted fix.

- Inspect `.status.history` instead — a `failed` or `uninstalled` Snapshot survives a later successful reconcile. Use the shared helper in `hack/e2e-chainsaw/_lib/remediation-guard.sh` (`helmrelease_has_remediation_cycle`) from a `script` step.

### 7. Test-Impact Analysis (TIA), default-on

`hack/select-e2e.sh` walks the `packages/core/platform/sources/*.yaml` dependency graph and runs only the Chainsaw suites affected by a diff. A suite is a directory under `hack/e2e-chainsaw/` containing a `chainsaw-test.yaml`.

- Conservative escalation: edits to `packages/library/`, `packages/core/`, `api/`, `cmd/`, `internal/`, top-level `hack/*.sh|*.bats` helpers, the shared `hack/e2e-chainsaw/_lib/`, the chainsaw config `hack/e2e-chainsaw/.chainsaw.yaml`, the `Makefile`, or the E2E workflows escalate to the **full suite**. A per-suite edit (any file under `hack/e2e-chainsaw/<app>/`) selects **only** that suite.
- The `full-e2e` PR label forces the whole suite.
- The selection is passed to `make test-chainsaw CHAINSAW_SUITES="<names>"`, which runs `chainsaw test <names>` from `hack/e2e-chainsaw/` (empty = the whole suite).
- The coverage gap TIA opens on PRs is closed at release-cut time: the "Prepare release" commit bakes image digests into `packages/core/` (and other packages), which TIA escalates to the **full** suite — so the release PR exercises the whole suite against the commit the tag will point at.

When adding a new app package, confirm `select-e2e.sh` maps it correctly (it has a unit test, `hack/select-e2e_test.bats`). A new app whose suite directory does not yet exist will not be selected — add `hack/e2e-chainsaw/<app>/chainsaw-test.yaml` first.

#### What TIA does and does not do

Be precise about TIA's scope — it is narrower than "skip E2E for unrelated PRs," and the wiring has consequences worth knowing before you rely on it or change it.

- **It trims only which suites `chainsaw test` runs, not the expensive stages.** `Prepare environment` and `Install Cozystack` (the full platform install) run regardless of the selection. TIA only decides the `CHAINSAW_SUITES` passed to the final E2E step. A perfect narrow saves the suite tail, not the bulk of wall-time. (Package image builds are scoped separately — by `hack/build-matrix.sh`, on the PR diff — while the Talos and installer/finalize legs still run for non-docs PRs; that is a distinct mechanism from TIA's suite trimming.)
- **`Select E2E tests` runs *after* `Install Cozystack` and has no `always()`.** GitHub implicitly ANDs a step's `if:` with `success()`, so if install fails the selector (and `Run E2E tests`) are skipped entirely. On a red install you will not see TIA narrow anything — that is the ordering, not a selector bug.
- **Most PRs legitimately escalate to the full suite.** Platform manifests live under `packages/core/`, Go under `api/`/`internal/`, helpers under `hack/`, plus the `Makefile` — touching any one triggers the full suite. So "all suites selected" is usually correct, not a failure to narrow.
- **Two skip layers, with a gap.** The `plan` job computes a coarse docs-only gate (`code`, true for any change outside `docs/**`) that skips the whole build+E2E pipeline for docs-only PRs, plus a per-package build matrix (`hack/build-matrix.sh`) that scopes which images build. TIA's finer skip (`select-e2e.sh` ignoring `docs/`, `dashboards/`, and `*.md`) only trims the suite selection. A PR touching only `dashboards/*.json` therefore still runs install, then skips just the Chainsaw suites — near-zero savings. If you need such PRs to skip the pipeline, widen the `plan` docs-gate, not `select-e2e.sh`.

### 8. Failure must be self-explanatory — diagnostics are first-class

- On install failure, the CI step dumps `kubectl get hr -A -o wide`, a `describe` of each non-Ready HR, and sorted events, all under collapsible `::group::` blocks.
- On E2E failure, the CI step dumps `kubectl get hr -A`, sorted events, and COSI bucket/claim/access readiness columns (those CRDs ship no printer columns, so plain `get` shows only NAME/AGE). The `chainsaw test` run is one step, so this dump is at run level; Chainsaw's own per-`Test` `catch` blocks add scoped events/describe/podLogs for the failing suite.
- Inside a suite, attach scoped diagnostics with a top-level `catch:` block (`events`, `describe`, `podLogs`, `get`) rather than letting a bare assert failure through — see `hack/e2e-chainsaw/bucket/chainsaw-test.yaml` and `hack/e2e-chainsaw/harbor/chainsaw-test.yaml`. This replaces the BATS `... || { echo …; kubectl get/describe …; false; }` idiom.
- `make collect-report` → `hack/cozyreport.sh` uploads a full `cozyreport.tgz` (operator/Flux/cert-manager logs, LINSTOR, Kamaji, Talos dmesg, COSI YAML) on every run, and the Chainsaw JUnit report (`chainsaw-report.xml`) is uploaded as its own artifact.
- **Per-failed-test crust-gather snapshots** are the richest diagnostic — a `crust-gather serve`-able archive of the cluster *at the moment of failure*, before cleanup. The global `error.catch` in `hack/e2e-chainsaw/.chainsaw.yaml` captures the **host** cluster into `_out/cozyreport/snapshots/<test>/host` on any failure (the Chainsaw analog of the `hack/cozytest.sh` EXIT-trap snapshot used by the BATS tests); the kubernetes suites additionally snapshot their **tenant** cluster from `hack/e2e-chainsaw/_lib/run-kubernetes.sh` while the tenant API LB is still routable. `hack/cozyreport.sh` folds `snapshots/` into the artifact. Do not remove the global catch — without it the Chainsaw suite uploads a report with no per-test cluster state.

### 9. Keep the test environment deterministic

- **Pre-pull** every kubeovn / linstor / cert-manager image onto all nodes before those HelmReleases install (`hack/e2e-prepull-images.sh`), so clustered workloads (OVN raft, LINSTOR, the cert-manager webhook) do not fail on per-node image-pull stagger.
- **Exclude loop devices** from host LVM scanning in the Talos machine config so the host does not activate volume groups inside loop-mounted e2e disk images.
- **Fail fast on node readiness** (≈5m, then bail) rather than marching into LB/NFS tests that will also fail — it saves several minutes per attempt and keeps the real failure at the top of the log.

### 10. The E2E workflow split: same-repo vs fork, and the "E2E Tests" check-run

A fork's `pull_request` run carries no registry credentials, so it cannot push the images the e2e sandbox pulls. E2E therefore runs across **two** workflows:

- **`.github/workflows/pull-requests.yaml`** runs e2e in-tree for **same-repo** PRs (the `e2e` job, named `E2E (in-tree)`). Fork PRs skip this job.
- **`.github/workflows/e2e-fork.yaml`** runs e2e for **fork** PRs via a privileged `workflow_run` that fires after the fork's Pull Request run completes. It pushes the fork-built images to the registry and runs the suite — the same Chainsaw + TIA path as in-tree (§7) — in an ephemeral sandbox that itself has no push credentials.

Consequences to keep in mind when touching either workflow:

- **Fork builds export, they do not push (`OCI_EXPORT_DIR`).** The fork's unprivileged build sets `OCI_EXPORT_DIR` (see `hack/common-envs.mk`) so every image is written to a per-image OCI archive artifact instead of being pushed; the privileged `e2e-fork.yaml` later pushes those archives to the registry **by digest**. Any build recipe that shells out to push directly — e.g. a `skopeo copy … docker://…` that bypasses the `image-tags`/`oci-output` macros — must honour `OCI_EXPORT_DIR` too, or a fork build dies with an anonymous-push `denied` (#3257). This is covered by `hack/common-envs_test.bats`.
- **"E2E Tests" is a commit status — not a job, and not a check-run.** The required merge-gate context named `E2E Tests` is a GitHub **commit status**, posted by the `e2e-report` job for same-repo PRs and by `e2e-fork.yaml`'s `resolve`/`report` for forks — **not** the `e2e` job itself. Two reasons it is a status:
  - *Not a job:* a required *job* that is skipped (as the `e2e` job is for forks) counts as passed by branch protection — exactly how fork PRs used to merge with e2e never run (#3257). A status is `success` only when the report step posts it; a never-posted required status stays "Expected" and blocks the merge. If you rename the `e2e` job, do **not** name it `E2E Tests` and do not wire branch protection to a job name — the status must stay the gate.
  - *Not a check-run:* a check-run created with the default `GITHUB_TOKEN` floats into an arbitrary GitHub Actions check-suite (in practice the earliest-registered one, a tiny labeler — that is why `E2E Tests` once rendered under "PR size label"), and an app-token check-run would need the COZYSTACK_CI app granted `checks: write`. A commit status has neither problem: branch protection matches it by context name and it carries no check-suite grouping. It is safe to post with the default token because a fork's `pull_request` token is **read-only** and cannot post a status — so only same-repo runs (`e2e-report`, guarded `!fork`) and the privileged `workflow_run` in `e2e-fork.yaml` can set it. Each publishing job carries `permissions: statuses: write` and calls `repos.createCommitStatus`.
  **Consequence for branch protection:** require the `E2E Tests` **status context** (no GitHub App and no app-id pin needed).

- **Fork TIA is driven from trusted data, not a merge-ref diff.** `e2e-fork.yaml`'s `e2e` job runs the same `select-e2e.sh` TIA as in-tree (§7), but a `workflow_run` job cannot trust a `git diff` of the fork-controlled merge ref — a fork could shape that diff so TIA selects nothing and the suite is skipped. So the changed-file list and the `full-e2e` label both come from `resolve`'s base-repo API lookups (`pulls.listFiles` / the PR's labels), the same trusted source as the docs-only decision, passed to the `e2e` job as outputs (`changed_b64`, `full_e2e`). Keep it that way: never re-derive the fork selection from the checked-out tree.

**Security prerequisite (repo setting).** The split is only safe if fork runs require maintainer approval before they execute. The repository must keep **Settings → Actions → "Require approval for all outside collaborators"** enabled, so a fork's `pull_request` run — which produces the artifacts `e2e-fork.yaml` consumes — cannot run untrusted code (or feed the privileged run) without a maintainer's go-ahead.
## Chainsaw v0.2.15 gotchas

The suite is pinned to Chainsaw **v0.2.15** (the latest release as of May 2026); none of the traps below are fixed upstream yet, so the workarounds stay until a bump is possible. Each one has already cost a debugging session — this is the "no one gets it right the first time" list, worth a read before writing a new assert.

- **Condition asserts must use the filter-as-list form.** `(conditions[?type == 'Ready'])` evaluates to a *list*; assert against it with a list body (`- status: "True"`). The indexed `(conditions[?type == 'Ready'])[0]` form throws `field not found` (also enforced by convention 2).
- **No number literals / backticks in JMESPath.** `` ports[?port == `443`] `` mis-evaluates in v0.2.15 — the numeric comparison silently returns the wrong set. Assert ports (and any numeric field) with the exact-ordered projection form instead: `(ports[*].port): [80, 443]`. `sort()` and `contains(list, <number>)` are likewise broken with numeric arguments.
- **`error:` fails hard on an unknown GVK.** Unlike the BATS `! kubectl get <kind>` idiom (which passes when the type is absent), a Chainsaw `error:` op against a kind whose CRD is not installed *errors the step* rather than asserting absence. If a type may legitimately not exist (a version rename, an optional component), gate the check behind a positive precondition or do it in a `script` step — never a bare `error:`. This is what broke the etcd suite on the `v1alpha1` → `v1alpha2` rename.
- **No conditional Test-level `skip`.** There is no `skip: <expr>`; an env-var-gated test must `exit 0` early inside a `script` step (see the `ETCD_E2E_S3_ROUNDTRIP` gate). A Test with no matching gate always runs.
- **`finally` is step-level, not test-level.** To guarantee teardown of something Chainsaw did not create, put the work *and* its own cleanup in a single `script` step; there is no test-wide `finally` block to hang it on.
- **Cleanup is bounded and blocking — it surfaces latent teardown bugs.** Chainsaw waits for the resources it applied to actually delete (within the `delete`/`cleanup` timeouts in `hack/e2e-chainsaw/.chainsaw.yaml`) and *fails* the test if they don't. This is deliberate — a stuck uninstall starves the next suite — but it means a teardown path the BATS suite masked with `|| true` now surfaces as `context deadline exceeded` at cleanup. The failure is honest, not new; fix the teardown rather than widening the timeout (see cozystack/cozystack#3271, a pre-delete hook that hung on a nodeless tenant).
- **Reading a failure diff: the "actual" side is a projection.** Chainsaw projects the live object onto the expected fields, so `status: {}` in a diff means "the expression matched nothing / evaluated empty," not "status is literally empty." The real reason is the `error` line *above* the diff — read that first.
- **A `catch`/cleanup `script`'s cwd is the failing suite's directory** (`hack/e2e-chainsaw/<suite>/`), so `../../..` is the repo root. The suites `cd ../../..` and then source shared helpers by repo-root-relative path (`hack/e2e-chainsaw/_lib/…`) rather than juggling `../` hops.

## Reviewer checklist for a new or changed E2E test

1. No new retry loop unless the step is pure infra (image pull / VM boot / network).
2. Resource readiness uses a Chainsaw `assert` (not an imperative `until kubectl get …; kubectl wait`); condition checks use the filter-as-list form `(conditions[?type == 'Ready'])`.
3. Imperative-only waits live in a `script` step; any bare `sleep` carries a `TODO(e2e-replace-fixed-timeouts):` justification.
4. No test-level `EXIT`/`RETURN` trap — rely on Chainsaw cleanup; a self-contained trap is allowed only inside a single `script` step (port-forward / temp dir).
5. Controller-created artifacts the test cannot reclaim are pruned explicitly; nested tenants delete child → parent with a wait-for-deletion between.
6. Standard HR-Ready assert timeout is **5–6m**; longer waits (harbor 10m, NFS 10m, VM image pulls, platform-wide install 15m) are justified in-line.
7. Failure path attaches scoped diagnostics via a `catch:` block, never a silent pass.
8. If it touches parent-HR behavior, add the `status.history` remediation guard (`hack/e2e-chainsaw/_lib/remediation-guard.sh`).

## In-flight direction (not yet the merged standard)

These are being explored on branches and may become conventions; do not assume they are the current `main` behavior:

- **Cilium orphaned-endpoint self-heal** — an interim CI watchdog that evicts a single confirmed-orphan Cilium endpoint ("IP already in use", cilium/cilium#38313). Explicitly a mitigation to remove once a fixed Cilium ships; it refuses to touch an endpoint backing a live pod so real duplicate-IP bugs stay visible.
- **Cluster state snapshot/restore** between test groups instead of reinstalling.
