# E2E Testing Conventions

Guidance for writing, changing, and reviewing Cozystack's end-to-end (E2E) tests and the CI that runs them. Read this **before** touching anything under `hack/e2e-apps/`, `hack/e2e-*.bats`, `hack/*.sh` test helpers, the E2E CI workflows (`.github/workflows/pull-requests.yaml`, `.github/workflows/release-e2e.yaml`), or `packages/core/testing/`.

## The core principle

**Retries do not recover flakes — they hide deterministic bugs and triple diagnostic wall-time.** An audit of 30 PR runs found that across sampled failures, **25/25 retry attempts failed**: the retry loop never once turned a red run green, it only delayed surfacing real bugs (Helm namespace conflicts, seaweedfs/harbor races, a vminstance disk race) that *looked* like flakes because the retry sometimes coincided with transient state clearing.

Every convention below follows from that finding: **fail fast, fail loud, make the failure legible.** A test that flakes is a test (or a product) with a real race to fix, not a test to wrap in a retry.

## Conventions

### 1. No retries on deterministic steps; retry only pure infrastructure

- `Run E2E tests` and `Install Cozystack` run **once**. On failure they print `❌ ... (no retry — see diagnostics below)` and dump state. Do not reintroduce a retry loop around them.
- `Prepare environment` keeps its 3× retry — and *only* it — because it is pure infrastructure (Talos image download, sandbox VM boot, network) where a transient runner hiccup is a genuine flake with no application logic involved.
- Rule of thumb: retry is justified only for a step that contains **no product or test logic**.

### 2. Replace fixed `sleep`/`timeout` with event-driven backstops

The canonical wait pattern — an **existence backstop** before every `kubectl wait`:

```bash
timeout 60 sh -ec "until kubectl -n tenant-test get hr postgres-$name >/dev/null 2>&1; do sleep 2; done"
kubectl -n tenant-test wait hr postgres-$name --timeout=5m --for=condition=ready
```

- `kubectl wait` against a not-yet-created resource sits **silent for the full timeout**. The `until kubectl get` backstop makes "resource never appeared" fail in seconds with a clear message.
- A bare `sleep N` is allowed **only** when no Kubernetes API condition models the thing being awaited (external LB HTTP path, MetalLB advertisement, the "all platform HRs emitted" heuristic). When you must use one, annotate it with a `TODO(e2e-replace-fixed-timeouts):` comment explaining why no condition exists. See `hack/e2e-install-cozystack.bats` and `hack/e2e-apps/run-kubernetes.sh` for the sanctioned exceptions.

### 3. No `EXIT`/`RETURN` traps for cleanup

Trap-based cleanup was the single biggest source of false failures: an `EXIT` trap ran cleanup in a context where shell variables were unset, `set -u` killed it, and a **successful** test was marked failed — then retried twice into real failures.

- Do **pre-cleanup at test start** (delete stale resources, blocking) plus **inline cleanup at test end**.
- Do not manage port-forwards or temp files via `EXIT`/`RETURN` traps.

### 4. Do not mask cleanup or teardown failures

- Drop `|| true` from teardown deletes — a stuck finalizer, RBAC error, or wait timeout must fail the test, not leave stale state (HelmRelease, Secret, PVC) for the next test to trip over. `--ignore-not-found` still covers the legitimate clean-run case.
- For nested tenants, tear down **child → parent with a hard `kubectl wait ... --for=delete` between** each. Deleting the parent while a child is still uninstalling wedges the parent's cleanup Job on the child namespace, and both stuck uninstalls occupy helm-controller workers past the end of the test file — starving whichever app test runs next. See `hack/e2e-apps/gateway.bats`.

### 5. The install gate must have teeth

The install test fails if **any** HelmRelease is not Ready. A toothless gate (backgrounded `kubectl wait` discarding child exit codes + a bare `echo` instead of `exit 1`) once shipped a permanently-failing platform HR through green CI for weeks.

- Use a single `kubectl wait hr --all -A`, then an outcome-based re-list (covers HRs created after the snapshot), and `exit 1` on any non-Ready HR.
- Dump the full Ready-condition message per non-Ready HR so the real error is in the test output. See `hack/e2e-install-cozystack.bats`.

### 6. Assert the parent HelmRelease did not remediate — via `status.history`

A parent HelmRelease that hit its wait timeout, uninstalled, and reinstalled is a silent race we want to catch. **Do not** check `.status.installFailures` / `.status.upgradeFailures`: Flux's `ClearFailures()` zeroes those on every successful reconcile, so checking them after the HR is Ready is **vacuous** and passes against a reverted fix.

- Inspect `.status.history` instead — a `failed` or `uninstalled` Snapshot survives a later successful reconcile. Use the shared helper in `hack/e2e-apps/remediation-guard.sh` (`helmrelease_has_remediation_cycle`).

### 7. Test-Impact Analysis (TIA), default-on

`hack/select-e2e.sh` walks the `packages/core/platform/sources/*.yaml` dependency graph and runs only the bats files affected by a diff.

- Conservative escalation: edits to `packages/library/`, `packages/core/`, `api/`, `cmd/`, `internal/`, shared `hack/*.sh|*.bats` helpers, the `Makefile`, or the E2E workflows escalate to the **full suite**. A per-app bats edit selects **only** that app.
- The `full-e2e` PR label forces the whole suite.
- Companion: `.github/workflows/release-e2e.yaml` runs the **full** suite on every release tag, closing the coverage gap TIA opens on PRs.

When adding a new app package, confirm `select-e2e.sh` maps it correctly (it has a unit test, `hack/select-e2e_test.bats`).

#### What TIA does and does not do

Be precise about TIA's scope — it is narrower than "skip E2E for unrelated PRs," and the wiring has consequences worth knowing before you rely on it or change it.

- **It trims only the per-app test loop, not the expensive stages.** The `build` job (image + Talos build, unit/Go tests), `Prepare environment`, and `Install Cozystack` all run regardless of the selection. TIA only decides which `make test-apps-<app>` targets run in the final step. A perfect narrow saves the matrix tail, not the bulk of wall-time.
- **`Select E2E tests` runs *after* `Install Cozystack` and has no `always()`.** GitHub implicitly ANDs a step's `if:` with `success()`, so if install fails the selector (and `Run E2E tests`) are skipped entirely. On a red install you will not see TIA narrow anything — that is the ordering, not a selector bug.
- **Most PRs legitimately escalate to the full suite.** Platform manifests live under `packages/core/`, Go under `api/`/`internal/`, helpers under `hack/`, plus the `Makefile` — touching any one triggers the full suite. So "all apps selected" is usually correct, not a failure to narrow.
- **Two skip layers, with a gap.** The coarse `detect-changes` job (`dorny/paths-filter`, filter `code: '!docs/**'`) skips the whole build+E2E pipeline only for `docs/**`-only PRs. TIA's finer skip (`select-e2e.sh` ignoring `docs/`, `dashboards/`, and `*.md`) only trims the app loop. A PR touching only `dashboards/*.json` therefore still runs the full build + install, then skips just the app tests — near-zero savings. If you need such PRs to skip the pipeline, widen the `detect-changes` filter, not `select-e2e.sh`.

### 8. Failure must be self-explanatory — diagnostics are first-class

- On install failure, the CI step dumps `kubectl get hr -A -o wide`, a `describe` of each non-Ready HR, and sorted events, all under collapsible `::group::` blocks.
- Per-app failures additionally dump COSI bucket/claim/access readiness columns (those CRDs ship no printer columns, so plain `get` shows only NAME/AGE).
- `make collect-report` → `hack/cozyreport.sh` uploads a full `cozyreport.tgz` (operator/Flux/cert-manager logs, LINSTOR, Kamaji, Talos dmesg, COSI YAML) on every run.
- Inside a test, dump **scoped** state on assertion failure with a `... || { echo ...; kubectl get/describe ...; false; }` block rather than letting a bare failure through. See `hack/e2e-apps/harbor.bats`.

### 9. Keep the test environment deterministic

- **Pre-pull** every kubeovn / linstor / cert-manager image onto all nodes before those HelmReleases install (`hack/e2e-prepull-images.sh`), so clustered workloads (OVN raft, LINSTOR, the cert-manager webhook) do not fail on per-node image-pull stagger.
- **Exclude loop devices** from host LVM scanning in the Talos machine config so the host does not activate volume groups inside loop-mounted e2e disk images.
- **Fail fast on node readiness** (≈5m, then bail) rather than marching into LB/NFS tests that will also fail — it saves several minutes per attempt and keeps the real failure at the top of the log.

## Reviewer checklist for a new or changed E2E test

1. No new retry loop unless the step is pure infra (image pull / VM boot / network).
2. Every `kubectl wait` is preceded by an `until kubectl get …; do sleep 2; done` existence backstop.
3. Any bare `sleep` carries a `TODO(e2e-replace-fixed-timeouts):` justification.
4. No `EXIT`/`RETURN` trap — pre-cleanup at start, inline cleanup at end.
5. Teardown deletes drop `|| true`, keep `--ignore-not-found`; nested tenants delete child → parent with `wait --for=delete` between.
6. Standard HR-Ready wait is **5m**; longer waits (harbor 10m, NFS 10m, platform-wide install 15m) are justified in-line.
7. Failure path dumps scoped diagnostics (`|| { …; false; }`), never a silent pass.
8. If it touches parent-HR behavior, add the `status.history` remediation guard.

## In-flight direction (not yet the merged standard)

These are being explored on branches and may become conventions; do not assume they are the current `main` behavior:

- **BATS → Kyverno Chainsaw migration** — declarative asserts replace the `until … wait` boilerplate, with automatic events/describe/podLogs capture. Imperative suites (openbao unseal, vminstance, gateway, the kubernetes cluster tests) stay as script steps. Gotcha: Chainsaw v0.2.15 needs condition assertions in **filter-as-list** form `(conditions[?type == 'Ready'])`; the `(...)[0]` indexed form throws "field not found".
- **Cilium orphaned-endpoint self-heal** — an interim CI watchdog that evicts a single confirmed-orphan Cilium endpoint ("IP already in use", cilium/cilium#38313). Explicitly a mitigation to remove once a fixed Cilium ships; it refuses to touch an endpoint backing a live pod so real duplicate-IP bugs stay visible.
- **Pin the e2e management cluster Kubernetes version** to dodge the kube-controller-manager ValidatingAdmissionPolicy type-checker panic on `additionalProperties: true` schemas (kubernetes/kubernetes#135155).
- **Cluster state snapshot/restore** between test groups instead of reinstalling.
