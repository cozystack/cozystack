# E2E Testing Conventions

Guidance for writing, changing, and reviewing Cozystack's end-to-end (E2E) tests and the CI that runs them. Read this **before** touching anything under `hack/e2e-chainsaw/`, the bootstrap/OpenAPI BATS (`hack/e2e-*.bats`), `hack/*.sh` test helpers, the E2E CI workflow (`.github/workflows/pull-requests.yaml`), or `packages/core/testing/`.

The app suite is **Kyverno Chainsaw** (`hack/e2e-chainsaw/`, one directory per app, each with a `chainsaw-test.yaml`). Cluster bootstrap (`hack/e2e-install-cozystack.bats`, `hack/e2e-prepare-cluster.bats`) and the OpenAPI checks (`hack/e2e-test-openapi.bats`) remain BATS.

## The core principle

**Retries do not recover flakes — they hide deterministic bugs and triple diagnostic wall-time.** An audit of 30 PR runs found that across sampled failures, **25/25 retry attempts failed**: the retry loop never once turned a red run green, it only delayed surfacing real bugs (Helm namespace conflicts, seaweedfs/harbor races, a vminstance disk race) that *looked* like flakes because the retry sometimes coincided with transient state clearing.

Every convention below follows from that finding: **fail fast, fail loud, make the failure legible.** A test that flakes is a test (or a product) with a real race to fix, not a test to wrap in a retry. Chainsaw's per-assertion polling is the structural expression of this: each `assert` waits on its own condition to its own timeout, and a failure is reported as a structured diff plus auto-captured events/describe/logs — not a generic non-zero exit.

## Conventions

### 1. No retries on deterministic steps; retry only pure infrastructure

- `Run E2E tests` and `Install Cozystack` run **once**. On failure they print `❌ ... (no retry — see diagnostics below)` and dump state. Do not reintroduce a retry loop around them. The E2E step is a single `chainsaw test` invocation — there is no per-app retry loop to bring back, because Chainsaw already polls each assertion to its own timeout.
- `Prepare environment` keeps its 3× retry — and *only* it — because it is pure infrastructure (image pull, node container start, network) where a transient runner hiccup is a genuine flake with no application logic involved. The retry tears the compose project down between attempts, because a half-started project the next attempt adopts is not a fresh try.
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
- **Capture the PREVIOUS container instance, not just the current one.** For a crash-looping pod `kubectl logs` (and the Chainsaw `podLogs` collector, which in v0.2.15 exposes only `container`/`tail`/`selector` and has no `previous` option) shows the Nth restart — a fresh replay of the same startup path — while the evidence that explains the failure sits in the immediately preceding instance, reachable only via `--previous` and lost for good once the kubelet garbage-collects that container. `hack/e2e-capture-previous-logs.sh` closes this once for every suite from the global `error.catch` (and from `hack/cozytest.sh` for the BATS tests) rather than per-suite: it discovers containers with `restartCount > 0` at runtime — init containers included, since an interrupted bootstrap lives there — orders the app suites' `tenant-test` namespace first (a best-effort ordering only — it never restricts the capture, and for a suite whose pods live in a child tenant namespace it simply degrades to input order), and dumps each to the job log and to `snapshots/<test>/previous-logs/`. Containers that never restarted are skipped, so a healthy run emits one line, not a page of "previous terminated container not found". Every way the capture can come up short is named rather than inferred: the `COZY_PREVLOG_MAX` overflow, a malformed cap, a pod list that never returned (which is NOT the same as "nothing restarted" and must not be reported as such), a read that timed out versus one kubectl itself refused (kubelet GC, RBAC, an unreachable node — different problems that a blanket "kubelet GC" line would hide), a previous instance that produced no output, and a capture the outer backstop cut short. A partial log is kept and marked truncated, never deleted — it is the only copy of something already gone from the cluster.
- **A catch script needs an explicit `timeout:` that exceeds the inner budgets it contains.** A Chainsaw `script` op is bounded by `timeouts.exec` (5m in `.chainsaw.yaml`) unless it overrides it, so a catch whose own `timeout -k …` wrappers sum past that is *structurally guaranteed* to be killed — it reports `SCRIPT | ERROR | signal: killed` and yields nothing. That kill is strictly worse than an inner `timeout` firing: SIGKILL takes out the process group, so the collector dies mid-write **and** every later step in the script is skipped, whereas an inner `timeout` exits 124, leaves a partial capture on disk, and lets the rest of the catch run. Size the op timeout above the sum of the inner budgets and let the inner ones be what fires.
- **Set `crust-gather --duration` explicitly.** It is crust-gather's own budget for the collection phase and it defaults to **60s**; on elapse it abandons the collection and skips its finish step, leaving a partial tree with nothing to say the rest is missing. The outer wall-clock must exceed it, because the API discovery crust-gather runs *first* is not covered by that budget at all — with an unhealthy aggregated APIService (the state a failed e2e is usually in) aggregated discovery times out and the fallback re-walks every group, which can consume the entire outer timeout before a single object is written. Do not diagnose a killed snapshot by bumping the outer number alone; the two budgets bound different phases. Send the collector's output to a log inside the snapshot rather than `/dev/null` so "complete or truncated?" is answerable from the artifact.
- **Per-failed-test crust-gather snapshots** are the richest diagnostic — a `crust-gather serve`-able archive of the cluster *at the moment of failure*, before cleanup. The global `error.catch` in `hack/e2e-chainsaw/.chainsaw.yaml` captures the **host** cluster into `_out/cozyreport/snapshots/<test>/host` on any failure (the Chainsaw analog of the `hack/cozytest.sh` EXIT-trap snapshot used by the BATS tests); the kubernetes suites additionally snapshot their **tenant** cluster from `hack/e2e-chainsaw/_lib/run-kubernetes.sh` while the tenant API LB is still routable. `hack/cozyreport.sh` folds `snapshots/` into the artifact. Do not remove the global catch — without it the Chainsaw suite uploads a report with no per-test cluster state.

### 9. Keep the test environment deterministic

- **Pre-pull** every kubeovn / linstor / cert-manager image onto all nodes before those HelmReleases install (`hack/e2e-prepull-images.sh`), so clustered workloads (OVN raft, LINSTOR, the cert-manager webhook) do not fail on per-node image-pull stagger.
- **Exclude loop devices** from host LVM scanning in the Talos machine config so the host does not activate volume groups inside loop-mounted e2e disk images.
- **Fail fast on node readiness** (≈5m, then bail) rather than marching into LB/NFS tests that will also fail — it saves several minutes per attempt and keeps the real failure at the top of the log.

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

## The container lane: what the srv nodes are, and what that costs

The `srv1`-`srv3` nodes of the PR lane run as **Talos containers**, not QEMU guests. The sandbox used to boot three guests with Cozystack running KubeVirt inside them, which put a tenant worker at L3 — Oracle host, runner VM, `srv` VM, worker. Running the `srv` nodes as containers deletes one of those levels and puts the worker at L2.

What that buys, measured in CI on commits where both substrates ran: the two tenant-Kubernetes suites take **466-809s** on containers and pass whether or not the runner exposes the host's `kvm_amd` `vgif` flag, against **1143-2040s** on QEMU where the runners without it missed the node-join deadline outright. Whole-job wall clock came down 24-49 minutes across three such runs, and essentially all of it is in Chainsaw rather than in bringing the stand up: install takes about half an hour on either substrate.

Three properties of container mode drive the whole design, and each one fails quietly rather than loudly if ignored. `machine.kernel.modules` is a **silent no-op** — `kernel_module_spec.go` returns early on `ModeContainer` with no error and no event — so the host must load `openvswitch` and `zfs` before the nodes start, and both the host script and the BATS file assert it rather than trusting it. Docker gives a privileged container its **own tmpfs `/dev`, seeded once at creation**, so `/dev/kvm` works but ZFS zvols created later never appear and linstor-csi fails `ControllerPublishVolume`; the compose file binds the host devtmpfs instead, which is also why `talosctl cluster create docker` can never carry LINSTOR. And container mode has **no ARP VIP**, so `192.168.123.11` stands in for the QEMU lane's `192.168.123.10` and `hack/e2e-platform-packages.sh` takes the endpoint from `COZY_APISERVER_ENDPOINT`, defaulting to the unchanged QEMU value.

In CI this is the `e2e` job ("E2E Tests") in `.github/workflows/pull-requests.yaml`, the lane that gates a merge. The QEMU substrate is not wired into it any more, and the PR path no longer builds or downloads a nocloud disk at all. `make -C packages/core/testing prepare-cluster` and `hack/e2e-prepare-cluster.bats` stay in the tree because `e2e-tag.yaml` still runs the suite on QEMU: the release-candidate run is where DRBD, the `replicated` StorageClass and the Cozystack Talos node image keep being exercised, since none of the three can run on containers.

Root entry points are `make prepare-env-container` (host-side compose plus the in-sandbox BATS file) and `make -C packages/core/testing delete-cluster-container`, which is also the retry cleanup: a half-started compose project the next attempt adopts is not a fresh try.

The nodes run as **siblings on the runner's docker daemon**, not nested inside the sandbox, because those host binds have to resolve against the real host devtmpfs. That is why provisioning is two steps: `hack/e2e-container-up.sh` does the host-side half and needs a docker socket, then the sandbox is attached to the compose network and `hack/e2e-prepare-cluster-container.bats` runs inside it exactly as the QEMU lane's file does.

What the lane gives up is **DRBD, and with it more than the `replicated` StorageClass**. DRBD's resource registry is global per kernel, so three containers sharing one kernel cannot represent three DRBD nodes — a second `new-resource` for the same name returns success while changing nothing, which is worse than an error because LINSTOR then records a replica it does not have. It applies to kind equally; it is a property of one kernel, not of Talos. The linstor chart always adds a `drbd-logger` sidecar to the satellites, which exits at once without DRBD and keeps every satellite unready, so no PVC binds at all. The chart has no value that drops it, so `hack/e2e-post-install-prep.sh` applies a `LinstorSatelliteConfiguration` of its own on this lane, `e2e-no-drbd`, that deletes the sidecar from the satellite DaemonSet, and reads the live DaemonSets back once the nodes are Online. The DRBD-only initContainers are already removed by the chart's Talos satellite configuration. Because linstor-csi couples RWX to DRBD, tenant worker disks become RWO and **live migration is unavailable on this lane**, which nothing in the tree tests today either way. Everything else survives: kube-ovn runs the real OVS dataplane, and LINSTOR keeps real ZFS pools and the production `local` StorageClass.

Two further things move off the per-PR path with the substrate, and both keep their release-candidate coverage rather than losing it. `replicated` is `volumeBindingMode: Immediate` while `local` is `WaitForFirstConsumer`, so the Immediate binding path is not exercised per PR; and tenant StorageClass propagation only applies to classes that are not `allowRemoteVolumeAccess: "false"`, which on this lane is none of them. The management nodes also boot upstream `ghcr.io/siderolabs/talos` rather than the Cozystack image, so the extensions that image carries — `drbd`, `zfs`, `openvswitch` — and the LVM filter are validated by the release-candidate run rather than on every pull request. Tenant worker nodes still import a Talos disk, so that half of the Talos path is unaffected.

The local-only mode has two mandatory install-time adaptations. CDI otherwise ranks `ReadWriteMany`/`Block` first for any LINSTOR StorageClass, so post-install prep waits for `StorageProfile/local` and pins it to `ReadWriteOnce`/`Block` before any tenant DataVolume is created; without that override linstor-csi rejects the first worker disk because RWX requires DRBD. Root VictoriaLogs is the only install workload that explicitly defaults to `replicated` instead of inheriting the default class, so the E2E installer adds its `logsStorages` override to `tenant-root/cozystack-values` before enabling monitoring; patching the generated monitoring HelmRelease afterward is unsafe because it can create an immutable replicated PVC first.

Three more settings the lane needs have no chart value on this branch, so the harness applies them to the live cluster, on this lane only. The last test of `hack/e2e-install-cozystack.bats` adds `--strict-topology` to the CSI provisioner through the `LinstorCluster`, and raises CDI's worker memory ceiling from 600M to 4Gi through the `CDI` CR — not `CDIConfig`, which the operator reconciles back from the CR. It is the last test because the earlier ones that change platform values upgrade every release, and an upgrade re-renders both objects without the settings; the tenant-Kubernetes and `vminstance` suites re-check the CSI flag before they import, so an override a later upgrade dropped fails there by name. The memory ceiling answers an import that OOMed near 100% on this lane and then cycled forever, and that completes at 600M on QEMU nodes, which is why it is a lane setting. And the `vminstance` suite requests immediate binding on each VMDisk's DataVolume and claim, because on a `WaitForFirstConsumer` class CDI populates no disk that nothing consumes yet.

Two capacity edges are handled explicitly. A container node reports the **host's CPU and memory** as capacity rather than its compose cgroup limits, so `hack/e2e-container-up.sh` computes kubelet `systemReserved` from the host totals and caps each node's scheduler-visible allocatable near 8 CPU / 24 GiB; it refuses a host without strict aggregate headroom, and the live preparation suite asserts every Node's resulting allocatable. That corrects the scheduler and not what a pod reads from `/proc`, so a workload that sizes itself from `/proc/cpuinfo` or `/proc/meminfo` is configured for the whole runner. Each ZFS pool uses a sparse 200 GB backing file, matching the QEMU lane's per-node data disk: root-tenant reservations can otherwise leave less than the ~21 GiB required by CDI scratch on the node where its 20 GiB target disk is already bound. The apparent 600 GB is not preallocated, but blocks written during the run consume the shared runner disk until teardown destroys the pools and backing files.

That second point used to be much worse than a sizing note. An importer pod mounts two node-pinned volumes — the disk, and a scratch volume CDI creates only *after* the pod is scheduled — so the scheduler sizes the node against the disk alone and can place two importers where both disks plus both scratch volumes do not fit. Without `--strict-topology` the scratch volume was then provisioned on a **different** node and the pod became unschedulable forever against `pv ... node affinity doesn't match node`, with no error surfaced anywhere and the DataVolume reporting `ImportScheduled` indefinitely. With the flag, provisioning is constrained to the node the scheduler actually chose, and the failure becomes a legible `1 node(s) did not have enough free storage` that resolves on its own once space frees up. Both the deadlock and the fix were reproduced by measurement on 2026-08-25.

## In-flight direction (not yet the merged standard)

These are being explored on branches and may become conventions; do not assume they are the current `main` behavior:

- **Cilium orphaned-endpoint self-heal** — an interim CI watchdog that evicts a single confirmed-orphan Cilium endpoint ("IP already in use", cilium/cilium#38313). Explicitly a mitigation to remove once a fixed Cilium ships; it refuses to touch an endpoint backing a live pod so real duplicate-IP bugs stay visible.
- **Cluster state snapshot/restore** between test groups instead of reinstalling.
