# Implementation Plan v3 — Lightweight container-cluster e2e lane (cozystack#3238)

> **Framework decision: chainsaw-first.** This lane is built as a follow-up **on top of
> PR #2826** (`feat/e2e-chainsaw-example`, OPEN, mergeable), which migrates the whole app
> suite BATS→Chainsaw. Revised after plannotator feedback (6 notes).
>
> Foundation (from the k8s-distribution doc, feedback #1): platform **`default` variant +
> `cozypkg add`** composition — not "enable the paas bundle". Verified: `templates/sources.yaml`
> registers **every** PackageSource unconditionally, so `cozypkg add cozystack.<anything>`
> (incl. `bucket-application`) works standalone with no bundle enabled.

---

## 0. Where chainsaw-first lands the work (feedback #6)

#2826 is a **full** migration, not a pilot. Confirmed structure on the branch:

- App tests: `hack/e2e-chainsaw/<suite>/chainsaw-test.yaml` (+ manifests) for **all** 22 apps.
  Shared shell in `hack/e2e-chainsaw/_lib/`. Global `hack/e2e-chainsaw/.chainsaw.yaml`
  (namespace `tenant-test`, `parallel: 1`, crust-gather `catch` on failure, JUnit report).
- **Cluster bootstrap + install stay BATS** even post-migration:
  `hack/e2e-prepare-cluster.bats` (3 QEMU Talos VMs), `hack/e2e-install-cozystack.bats`
  (helm install + platform Package + `e2e-post-install-prep.sh`), `hack/e2e-test-openapi.bats`.
  **Only the app suite is chainsaw.**
- Run target: `make -C packages/core/testing test-chainsaw CHAINSAW_SUITES="…"` →
  `cd hack/e2e-chainsaw && chainsaw test ${CHAINSAW_SUITES}`.
- **Selection already chainsaw-aware:** `hack/select-e2e.sh` was rewritten to map changed files →
  suite dir names under `hack/e2e-chainsaw/`; the CI "Select E2E tests" step feeds them into
  `CHAINSAW_SUITES` (empty ⇒ whole suite).

**Implication for #3238:** the lane's *differentiators* (container cluster, lite package set,
local-path SC, cilium-only CNI) all live in the **provisioning + install phase, which is still
BATS**. The app-test phase reuses #2826's chainsaw suites **unchanged**, scoped to the 17
survivors via `CHAINSAW_SUITES`. So chainsaw-first does **not** change §1/§b/§c below; it changes
only §d (run/selection) and the sequencing (§e) — and it makes feedback #5 free.

---

## 1. Package composition — `default` variant + a lite preset (feedback #1, #2)

The k8s-distribution doc's path: operator `variant=generic`; platform **`default`** variant
(*"registers PackageSources for all packages, installs nothing"* — verified); then `cozypkg add`
per package. `cozypkg add` is a **different mechanism** from `bundles.enabledPackages` (which only
toggles `.optional` packages): it creates a standalone `Package` CR against any registered
PackageSource, so it reaches postgres-application, kafka-application, **and bucket-application**
with no paas/iaas bundle. `dependsOn` is an ordering gate, not an auto-installer → the lane must
add the **full closure**.

Per feedback #2, codify the closure as a **lite preset** rather than dozens of ad-hoc `cozypkg add`:
- **A1 (prototype):** a checked-in list (e.g. `hack/e2e-lite-packages.txt`) + an install-script
  loop of `cozypkg add`. No Go/bundle change.
- **A2 (durable, feedback #2's "bundle/preset for local tests"):** a named `lite`/`e2e` platform
  variant (`values-lite.yaml` + entry in `cmd/cozystack-operator/main.go:678`) or a dedicated
  bundle block. Reproducible for local dev; touches the Go binary + `helm-unittest`. Do after A1.

**Closure (17 survivors):**
- *Engine/control:* `cozystack-engine`, `cozystack-basics`, `tenant-application` (creates the
  `tenant-test` tenant the suites install into — matches `.chainsaw.yaml`'s namespace),
  `cert-manager`, `gateway-api-crds`, `reloader`, `snapshot-controller`,
  `cozystack-scheduler` (**variant `default`**, not `linstor` — §b), `securitygroup-controller`.
- *Networking:* `networking` **`cilium-generic`** (or `cilium` on Talos-in-Docker, or `noop` if the
  cluster ships Cilium already — §c).
- *Storage (new pkg):* the local-path/host-path package (§b) → default SC.
- *Object storage:* `seaweedfs-application`, `objectstorage-controller` (bucket/harbor COSI).
- *LB:* `metallb` or Cilium LB-IPAM (§c, R-C1).
- *Operators:* `etcd-operator`, `postgres-operator`, `mariadb-operator`, `kafka-operator`,
  `clickhouse-operator`, `foundationdb-operator`, `redis-operator`, `mongodb-operator`.
- *App defs:* `etcd-application`, `gateway-application`, `external-dns`(+`-application`),
  `kuberture`, `bucket-application`, `harbor-application`, `postgres-application`,
  `clickhouse-application`, `foundationdb-application`, `kafka-application`, `mariadb-application`,
  `mongodb-application`, `openbao-application`, `qdrant-application`, `redis-application`.
- `serviceexposure` needs nothing extra (controller ships in `cozystack-engine`).

(For `etcd.bats`/`etcd` chainsaw suite's `workloadmonitor`, add `victoria-metrics-operator` +
`monitoring-agents` — the operator+CRD, not the whole tenant monitoring stack; see R-D3.)

---

## (b) Default StorageClass swap — new package, non-Rancher (feedback #2, #3)

**Finding (verified):** the linstor chart renders **no `StorageClass`**. `local` (default) +
`replicated` are created imperatively by `hack/e2e-post-install-prep.sh:69-102` after ZFS pools.
The 15 SURVIVES suites use `storageClass: ""` → they need only **one default SC**, name irrelevant
(`local`/`replicated`-by-name are used only by the 5 NEEDS-REAL suites).

**Swap:** ship a small `packages/system/<local-storage>` package (declarative, Flux-managed —
not a `kubectl apply` in a script, per feedback #2) wired only into the lite preset; drop
`linstor`/`linstor-scheduler`/`linstor-gui`.

**Provisioner — DECIDED: OpenEBS LocalPV-Hostpath** (non-Rancher, feedback #3).

Implementation-time correction to the earlier ranking: `csi-driver-host-path` (the previous #1) ships no upstream Helm chart — only raw `deploy/kubernetes-*` manifests — so it does not fit cozystack's vendor-an-upstream-chart package pattern (`nfs-driver`'s Makefile does `helm pull csi-driver-nfs --untar`). Its only advantage over the alternatives, CSI snapshots, is not needed by the 17 survivors (they only need PVCs to bind; none snapshot in e2e). So it is the most work for no benefit here.

OpenEBS LocalPV-Hostpath is CNCF, non-Rancher, and publishes an official Helm chart (`localpv-provisioner` at `https://openebs.github.io/dynamic-localpv-provisioner`) → vendors cleanly via the `nfs-driver` pattern. Dynamic provisioning + `WaitForFirstConsumer`. The chart's own `hostpathClass.isDefaultClass: true` mints the default StorageClass (named `local` for parity with the LINSTOR default SC name), so no separate SC template is needed. Images stay on their upstream registry (like `nfs-driver`), so no image-mirroring step.

Rejected: `csi-driver-host-path` (no chart, snapshot advantage unused); `local-static-provisioner` (no dynamic provisioning); `nfs-driver`+NFS server (heavier); Rancher local-path (excluded by feedback #3).

**`cozystack-scheduler` coupling:** `system.yaml:28` uses variant `linstor`; the lite preset must
use **`default`** (as `isp-hosted` does, `system.yaml:33`). No linstor `VolumeSnapshotClass` will
exist — use the host-path CSI's snapshot class if a suite needs one.

---

## (c) Cilium-only networking — 👍 (feedback #4)

Unchanged and confirmed: cilium-only variants **already exist** in `sources/networking.yaml` —
`cilium` (Talos) and `cilium-generic`. Base `system/cilium/values.yaml` is a full primary CNI
(`ipam.mode: kubernetes`, `kubeProxyReplacement: true`, `gatewayAPI.enabled: true`); the kube-ovn
coupling lives only in the `values-kubeovn.yaml` overlay, omitted by these variants. **No new CNI
code** — pick `cilium` (Talos-in-Docker), `cilium-generic` (kind), or `noop` (only if a
pre-installed Cilium exists; kindnet won't satisfy `gateway`/`securitygroup`).

Lost with kube-ovn (none affect the 17): VPC subnets/overlays (iaas, off), `multus`, `cozy-proxy`.
**Tenant isolation survives** — `packages/apps/tenant/templates/networkpolicy.yaml` is 100%
`cilium.io/v2` CNP/CCNP, zero kube-ovn, so `tenant.spec.isolated: true` and `securitygroup` work.

**R-C1 — LB IP:** `gateway` needs a `LoadBalancer` VIP (→ `Programmed`). MetalLB L2 or Cilium
LB-IPAM pinned to a docker-bridge range. `serviceexposure` uses `externalIPs`, needs none.

---

## (d) Workflow / lane — chainsaw run + chainsaw-aware selection (feedback #5, #6)

Add a **parallel `e2e-container` job** to `pull-requests.yaml`, alongside the QEMU lane (which
keeps the 5 NEEDS-REAL suites). It reuses #2826's chainsaw machinery end to end:

1. **Provision (BATS, no nested virt).** New `hack/e2e-prepare-cluster-container.bats` (or script):
   **Talos-in-Docker** (`talosctl cluster create --provisioner docker --cni none`) preferred for
   Talos fidelity (KubePrism `localhost:7445` → `cilium` variant applies unchanged); **kind** as
   the lighter alt (→ `cilium-generic` + `k8sServiceHost`=kind API). No `_out/assets/nocloud-*`
   dependency → skips image-build gating.
2. **Install (BATS) via the lite preset (§1).** Fork `e2e-install-cozystack.bats`: operator
   `variant=generic`, platform **`default`** variant, `cozypkg add` the §1 closure (or apply the
   lite preset). Replace `e2e-post-install-prep.sh` with a container variant (install the storage
   package + LB pool; **no LINSTOR**). Keep the cilium-leak-healer step (CNI-level flake).
   Still creates `tenant-test` so `.chainsaw.yaml`'s namespace holds.
3. **Run the app suite (Chainsaw, unchanged).**
   `make -C packages/core/testing test-chainsaw CHAINSAW_SUITES="<selected 17-subset>"`. The
   chainsaw suites are reused **verbatim** from #2826 — no per-suite edits.
4. **Selection (feedback #5 — satisfied natively).** Reuse #2826's chainsaw-aware
   `hack/select-e2e.sh` (already maps diffs → suite dirs). Add a lane filter that **intersects**
   its output with the **17-survivor allowlist**, then sets `CHAINSAW_SUITES`. Diff-scoping is
   preserved; the 5 NEEDS-REAL suites (`vminstance`, `kubernetes-latest/previous`,
   `kubernetes-oidc-system/customconfig`) are never dispatched to this lane. Codify the allowlist
   next to the selector (or derive the excluded set from a `needs: [virt|drbd]` label on those
   suites).
5. **Report:** reuse the `chainsaw-report.xml` collect/upload steps.

**Conventions.** Chainsaw suites already comply with `docs/agents/e2e-testing.md` (declarative
asserts poll existence+state; `catch`/`finally` diagnostics; JUnit). The **new BATS provisioning/
install** scripts must still follow the repo rules (event-driven `until` before `kubectl wait`, no
`EXIT`/`RETURN` traps, fail-fast on HR readiness) and `hack/cozytest.sh` limits (only `@test`;
dash `/bin/sh`, no `pipefail`; `set -u`). Chainsaw v0.2.15 gotchas (filter-as-list conditions;
"actual" is a projection; bare-ref `error` = must-not-exist) are #2826's concern, inherited as-is.

---

## (e) Sequencing — build on #2826 (feedback #6)

- **Hard dependency: #2826 must merge (or this lane rebases onto it) first.** It is OPEN and
  mergeable on `main` as of 2026-07-09. Do not build a BATS app lane — it would be thrown away.
- **Framework-agnostic work can start now** and lands identically either way: §b storage package,
  §c CNI variant selection + LB wiring, §1 lite preset/closure. Prototype these against a
  `default`-variant install on a dev stand independent of #2826.
- **Framework-coupled work waits on #2826:** §d.3/§d.4 (the `test-chainsaw CHAINSAW_SUITES` run +
  the chainsaw-aware `select-e2e.sh` lane filter) must target #2826's files, so land after it.
- Watch for churn on #2826 (suite dir names, `select-e2e.sh` mapping, `.chainsaw.yaml`) — the
  lane's allowlist and selector filter key off those.

---

## Open risks
- **R-B1 (dissolved) — bucket.** Under `default`+`cozypkg`, `bucket-application`'s PackageSource is
  registered like any other → addable with **no iaas bundle**. Blocker only exists on the
  (rejected) "enable paas bundle" route.
- **R-C1 — LB IP on a container network** (§c). Range must be routable on the kind/docker bridge or
  `gateway` hangs.
- **R-C2 — preset drift.** Hand-maintained closure can rot. Derive it from the PackageSource
  `dependsOn` graph (same yq logic `select-e2e.sh` uses) or add a CI completeness check.
- **R-D1 — Talos-in-Docker vs kind fidelity.** `cilium` (Talos) values hard-code KubePrism
  `localhost:7445`; kind → `cilium-generic` + kind API endpoint. Prototype both.
- **R-D2 — seaweedfs/CNPG weight.** bucket/harbor need seaweedfs → CNPG `seaweedfs-db` + raft
  quorum (~5-6 min) on the default SC; validate host-path CSI satisfies its `WaitForFirstConsumer`
  PVCs before assuming bucket/harbor pass.
- **R-D3 — monitoring weight.** The lite closure can skip the full VictoriaMetrics/Grafana stack;
  only `etcd`'s `workloadmonitor` needs `victoria-metrics-operator` + `monitoring-agents`.
- **R-E1 — #2826 is not merged.** The whole chainsaw-first plan is gated on it (§e). If #2826
  stalls, the framework-agnostic work (§1/§b/§c) still has standalone value.
- **R-E2 — chainsaw `parallel: 1`.** The shared `.chainsaw.yaml` runs suites sequentially (bucket's
  fixed port-forward port blocks parallelism). The lightweight lane inherits this → its wall-clock
  win comes from a faster *cluster* (no QEMU/DRBD/kube-ovn), not from parallel suites, unless
  #2826 later makes ports dynamic.

---

## Suggested delivery order (chainsaw-first)
1. **Track/land #2826** — hard prerequisite for §d (feedback #6).
2. **Storage package** (§b): `csi-driver-host-path` + default SC. Standalone, unit-testable now.
3. **CNI + LB** (§c): select `cilium`/`cilium-generic`, wire MetalLB/Cilium-LBIPAM pool.
4. **Lite preset** (§1, A1 first; derive closure from the dependsOn graph per R-C2). Prove on a
   dev stand with the `default` platform variant + `cozypkg add`.
5. **Container provisioning + install** (§d.1/§d.2, BATS): Talos-in-Docker first.
6. **Wire onto #2826:** lane filter in `select-e2e.sh` (17-allowlist intersection) + the
   `e2e-container` job running `test-chainsaw CHAINSAW_SUITES` (§d.3/§d.4, feedback #5), parallel
   to the QEMU lane.
7. Optionally promote A1 → A2 (named `lite` variant/bundle) for local dev testing (feedback #2).
