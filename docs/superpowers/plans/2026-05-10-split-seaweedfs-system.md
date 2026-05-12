# Split `seaweedfs-system` into `seaweedfs-db` + `seaweedfs-system` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the `seaweedfs-system` HelmRelease so the CNPG `Cluster/seaweedfs-db` lives in its own HR (`seaweedfs-db`) gated by `HelmRelease.spec.healthCheckExprs` on `Cluster.status.conditions[type=Ready]`, and `seaweedfs-system` `dependsOn` it. Eliminates the EPERM crashloop where seaweedfs-filer schedules before postgres is Ready.

**Architecture:** New leaf package `packages/system/seaweedfs-db/` containing only the Cluster CR. New wrapper template `packages/extra/seaweedfs/templates/seaweedfs-db.yaml` emits the HR with `waitStrategy.name: poller` + `healthCheckExprs` so the helm-controller's wait phase blocks until CNPG flips `Ready=True`. Existing wrapper template `seaweedfs.yaml` adds `dependsOn` on the new HR, lowers `interval` to 30s for fast dependency rechecks, and stops passing `.Values.db.*` since those values now flow only to `seaweedfs-db`. A migration script re-annotates existing `Cluster/seaweedfs-db` resources so the new release adopts them on upgrade.

**Tech Stack:** Helm v3 charts, FluxCD HelmRelease v2 (helm-controller), CloudNativePG `Cluster` CRD, Cozystack `cozyvalues-gen` schema generator, POSIX shell migration.

**Spec:** [`docs/superpowers/specs/2026-05-10-split-seaweedfs-system-design.md`](../specs/2026-05-10-split-seaweedfs-system-design.md)

---

## File Inventory

**Create:**
- `packages/system/seaweedfs-db/Chart.yaml`
- `packages/system/seaweedfs-db/values.yaml`
- `packages/system/seaweedfs-db/values.schema.json` (generated)
- `packages/system/seaweedfs-db/README.md` (generated)
- `packages/system/seaweedfs-db/Makefile`
- `packages/system/seaweedfs-db/templates/database.yaml` (moved from system/seaweedfs)
- `packages/extra/seaweedfs/templates/seaweedfs-db.yaml`
- `packages/core/platform/images/migrations/migrations/39`

**Modify:**
- `packages/system/seaweedfs/values.yaml` (drop `db:` block + its annotations)
- `packages/system/seaweedfs/values.schema.json` (regenerated)
- `packages/system/seaweedfs/README.md` (regenerated)
- `packages/extra/seaweedfs/templates/seaweedfs.yaml` (add `dependsOn`, lower `interval`, drop db pass-through)
- `packages/core/platform/sources/seaweedfs-application.yaml` (register `seaweedfs-db` component)

**Delete:**
- `packages/system/seaweedfs/templates/database.yaml` (moved)

---

## Task 1: Create `packages/system/seaweedfs-db/` chart skeleton

Boilerplate: `Chart.yaml` and `Makefile`. The `values.yaml`, generated files, and templates come in subsequent tasks. Done as a separate task so the chart directory is in place before adding content. (`.helmignore` is omitted — the sibling `packages/system/seaweedfs-rd/` has none, so it's not required by the project layout.)

**Files:**
- Create: `packages/system/seaweedfs-db/Chart.yaml`
- Create: `packages/system/seaweedfs-db/Makefile`

- [ ] **Step 1: Create `Chart.yaml`**

```yaml
apiVersion: v2
name: cozy-seaweedfs-db
version: 0.0.0 # Placeholder, the actual version will be automatically set during the build process
```

- [ ] **Step 2: Create `Makefile`** (mirrors `packages/system/seaweedfs/Makefile` but without the `update:` target — this chart has no upstream to fetch)

```make
export NAME=seaweedfs-db

include ../../../hack/common-envs.mk
include ../../../hack/package.mk
```

- [ ] **Step 3: Verify directory structure**

Run: `ls /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs-db/`
Expected: `Chart.yaml  Makefile`

- [ ] **Step 4: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/system/seaweedfs-db/Chart.yaml packages/system/seaweedfs-db/Makefile
git commit -s -m "feat(seaweedfs-db): add chart skeleton"
```

---

## Task 2: Add `values.yaml` to `seaweedfs-db`

Lift only the `db:` block from `packages/system/seaweedfs/values.yaml` (currently lines 191-194). Use the same `@param`/`@typedef` annotations the project uses for `cozyvalues-gen` schema generation.

**Files:**
- Create: `packages/system/seaweedfs-db/values.yaml`

- [ ] **Step 1: Write `values.yaml`**

```yaml
##
## @section Database parameters
##

## @typedef {struct} Resources - Resource configuration.
## @field {quantity} [cpu] - Number of CPU cores allocated.
## @field {quantity} [memory] - Amount of memory allocated.

## @typedef {struct} DB - Database configuration.
## @field {int} [replicas] - Number of database replicas.
## @field {quantity} [size] - Persistent Volume size.
## @field {string} [storageClass] - StorageClass used to store the data.

## @param {DB} db - Database configuration.
db:
  replicas: 2
  size: 10Gi
  storageClass: ""
```

- [ ] **Step 2: Generate schema and README**

Run from the package directory:

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs-db
make generate
```

Expected: `make generate` produces `values.schema.json` and `README.md`. No errors. (If the project's `make generate` for system packages doesn't run `cozyvalues-gen`, copy the equivalent invocation from `packages/extra/seaweedfs/Makefile`'s `generate` target.)

- [ ] **Step 3: Verify generated files exist**

Run:

```bash
ls /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs-db/
```

Expected output includes: `values.schema.json` and `README.md`.

- [ ] **Step 4: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/system/seaweedfs-db/values.yaml \
        packages/system/seaweedfs-db/values.schema.json \
        packages/system/seaweedfs-db/README.md
git commit -s -m "feat(seaweedfs-db): add values schema for db config"
```

---

## Task 3: Move `templates/database.yaml` to `seaweedfs-db`

Move the existing CNPG Cluster template verbatim. No content changes — same `metadata.name: seaweedfs-db`, same values references (`.Values.db.replicas`, `.Values.db.size`, `.Values.db.storageClass`).

**Files:**
- Create: `packages/system/seaweedfs-db/templates/database.yaml`
- Delete: `packages/system/seaweedfs/templates/database.yaml`

- [ ] **Step 1: Move the file with `git mv`**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
mkdir -p packages/system/seaweedfs-db/templates
git mv packages/system/seaweedfs/templates/database.yaml \
       packages/system/seaweedfs-db/templates/database.yaml
```

- [ ] **Step 2: Verify content unchanged**

Run:

```bash
head -5 /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs-db/templates/database.yaml
```

Expected:

```
---
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: seaweedfs-db
```

- [ ] **Step 3: Render the chart to confirm template is valid**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs-db
helm template . --namespace tenant-root --set db.replicas=2 --set db.size=10Gi
```

Expected: One `Cluster/seaweedfs-db` manifest emitted. No template errors. The Cluster spec should reference `instances: 2` and `storage.size: 10Gi`.

- [ ] **Step 4: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/system/seaweedfs-db/templates/database.yaml \
        packages/system/seaweedfs/templates/database.yaml
git commit -s -m "refactor(seaweedfs): move CNPG Cluster manifest to seaweedfs-db chart"
```

---

## Task 4: Strip `db:` from `packages/system/seaweedfs/values.yaml`

The `db:` block (and its `@param`/`@typedef DB` annotations) are no longer consumed by the `system/seaweedfs` chart — its only consumer was `templates/database.yaml`, which we just moved. Remove the block, regenerate the schema, verify nothing else in the chart references `.Values.db`.

**Files:**
- Modify: `packages/system/seaweedfs/values.yaml`

- [ ] **Step 1: Identify the exact block to remove**

```bash
grep -n "^db:\|^## .*DB\b\|@field.*storageClass" /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs/values.yaml | head -10
```

Expected: a `db:` line at ~191 with surrounding `@typedef DB` / `@field` annotations a few lines above.

- [ ] **Step 2: Remove the `db:` block + annotations**

Open `/home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs/values.yaml`. Delete:
- the `## @typedef {struct} DB` block (its `@field` lines)
- the `## @param {DB} db ...` line
- the `db:` mapping (replicas/size/storageClass) — lines 191-194 in the current file

Leave the `## @section`-style headers if they still describe other content; remove only those that exclusively introduce the `db` block.

- [ ] **Step 3: Confirm no remaining references in chart templates**

```bash
grep -rn "\.Values\.db" /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs/templates/ 2>&1
```

Expected: empty output (no template references `.Values.db.*` after Task 3 moved `database.yaml`).

- [ ] **Step 4: Regenerate schema and README**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs
make generate
```

Expected: `values.schema.json` and `README.md` regenerated, no `db` properties present.

- [ ] **Step 5: Render the chart to confirm it still templates cleanly**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs
make show 2>&1 | head -50
```

Expected: chart renders. No errors about missing `.Values.db` properties.

- [ ] **Step 6: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/system/seaweedfs/values.yaml \
        packages/system/seaweedfs/values.schema.json \
        packages/system/seaweedfs/README.md
git commit -s -m "refactor(seaweedfs): drop db.* values now owned by seaweedfs-db chart"
```

---

## Task 5: Register `seaweedfs-db` in `seaweedfs-application` PackageSource

The PackageSource at `packages/core/platform/sources/seaweedfs-application.yaml` enumerates the components of the `seaweedfs-application` source. Add a new entry for `seaweedfs-db` pointing at the new chart path.

**Files:**
- Modify: `packages/core/platform/sources/seaweedfs-application.yaml`

- [ ] **Step 1: Read the current file**

```bash
cat /home/daniil/aenix/cozystack-split-seaweedfs/packages/core/platform/sources/seaweedfs-application.yaml
```

Look at the `components:` list. The existing entries follow this shape:

```yaml
- name: seaweedfs-system
  path: system/seaweedfs
- name: seaweedfs
  path: extra/seaweedfs
  libraries: ["cozy-lib"]
- name: seaweedfs-rd
  path: system/seaweedfs-rd
  install:
    namespace: cozy-system
    releaseName: seaweedfs-rd
```

- [ ] **Step 2: Add `seaweedfs-db` component entry**

Insert above `seaweedfs-system` (build order doesn't actually matter — components are independent — but keeping db before system reads naturally):

```yaml
        - name: seaweedfs-db
          path: system/seaweedfs-db
        - name: seaweedfs-system
          path: system/seaweedfs
```

(Preserve the existing indentation — the file uses 8-space indent for component list entries based on its `variants:` nesting.)

- [ ] **Step 3: Verify YAML still parses**

```bash
yq . /home/daniil/aenix/cozystack-split-seaweedfs/packages/core/platform/sources/seaweedfs-application.yaml > /dev/null
echo "exit=$?"
```

Expected: `exit=0`.

- [ ] **Step 4: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/core/platform/sources/seaweedfs-application.yaml
git commit -s -m "feat(platform): register seaweedfs-db component in seaweedfs-application source"
```

---

## Task 6: Create wrapper template `extra/seaweedfs/templates/seaweedfs-db.yaml`

This is the load-bearing piece. The new HR carries `waitStrategy.name: poller` and `healthCheckExprs` for the CNPG Cluster. It receives only `.Values.db.*` and is wrapped in the same `if not Client topology` guard the existing `seaweedfs.yaml` uses (Client topology has no local DB).

**Files:**
- Create: `packages/extra/seaweedfs/templates/seaweedfs-db.yaml`

- [ ] **Step 1: Write the template**

```yaml
{{- if not (eq .Values.topology "Client") }}
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: {{ .Release.Name }}-db
  labels:
    sharding.fluxcd.io/key: tenants
spec:
  chartRef:
    kind: ExternalArtifact
    name: cozystack-seaweedfs-application-default-seaweedfs-db
    namespace: cozy-system
  interval: 5m
  timeout: 10m
  install:
    remediation:
      retries: -1
  upgrade:
    force: true
    remediation:
      retries: -1
  # `poller` waitStrategy is required for healthCheckExprs to be evaluated.
  # Without it, the HR flips Ready as soon as helm install applies the Cluster CR,
  # before CNPG has bootstrapped postgres. See spec for details.
  waitStrategy:
    name: poller
  healthCheckExprs:
    - apiVersion: postgresql.cnpg.io/v1
      kind: Cluster
      current: has(status.conditions) && status.conditions.exists(e, e.type == 'Ready' && e.status == 'True')
      failed:  has(status.conditions) && status.conditions.exists(e, e.type == 'Ready' && e.status == 'False')
  valuesFrom:
  - kind: Secret
    name: cozystack-values
  values:
    db:
      replicas: {{ .Values.db.replicas }}
      resources: {{- include "cozy-lib.resources.defaultingSanitize" (list .Values.db.resourcesPreset .Values.db.resources $) | nindent 8 }}
      size: {{ .Values.db.size }}
      {{- with .Values.db.storageClass }}
      storageClass: {{ . }}
      {{- end }}
{{- end }}
```

- [ ] **Step 2: Render the chart to verify the new HR is emitted correctly**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/extra/seaweedfs
helm template . --name-template seaweedfs --namespace tenant-root \
  --set _namespace.host=example.org \
  --set _namespace.ingress=tenant-root \
  --set _cluster.solver=http01 \
  2>&1 | yq 'select(.kind == "HelmRelease" and .metadata.name == "seaweedfs-db")' -
```

Expected: a single `HelmRelease/seaweedfs-db` with `spec.waitStrategy.name: poller` and `spec.healthCheckExprs[0].current` containing `e.type == 'Ready'`. If the helm template command fails because of unrelated required values, add the minimum `--set` flags needed (the existing `seaweedfs.yaml` template has preflight checks listed in its first 30 lines).

- [ ] **Step 3: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/extra/seaweedfs/templates/seaweedfs-db.yaml
git commit -s -m "feat(seaweedfs): emit seaweedfs-db HR with healthCheckExprs gating Cluster Ready"
```

---

## Task 7: Modify `extra/seaweedfs/templates/seaweedfs.yaml` — add `dependsOn`, lower `interval`, drop `db` pass-through

Three edits to the existing file:

1. Add `dependsOn: [{ name: "{{ .Release.Name }}-db" }]` to `spec`.
2. Lower `spec.interval` from `5m` to `30s` so the dependent HR re-checks the dependency promptly after `seaweedfs-db` flips Ready.
3. Remove the `db:` block from the `values:` map (it's no longer consumed by `system/seaweedfs` after Task 4).

**Files:**
- Modify: `packages/extra/seaweedfs/templates/seaweedfs.yaml`

- [ ] **Step 1: Add `dependsOn` immediately after the `spec:` line**

Locate (in the file at `packages/extra/seaweedfs/templates/seaweedfs.yaml`):

```yaml
spec:
  chartRef:
    kind: ExternalArtifact
    name: cozystack-seaweedfs-application-default-seaweedfs-system
    namespace: cozy-system
  interval: 5m
```

Replace the `interval: 5m` line and insert `dependsOn`:

```yaml
spec:
  chartRef:
    kind: ExternalArtifact
    name: cozystack-seaweedfs-application-default-seaweedfs-system
    namespace: cozy-system
  dependsOn:
    - name: {{ .Release.Name }}-db
  interval: 30s
```

- [ ] **Step 2: Remove the `db:` block from the rendered `values:`**

Locate (around lines 124-132 in the original):

```yaml
  values:
    global:
      serviceAccountName: "{{ .Release.Namespace }}-seaweedfs"
    db:
      replicas: {{ .Values.db.replicas }}
      resources: {{- include "cozy-lib.resources.defaultingSanitize" (list .Values.db.resourcesPreset .Values.db.resources $) | nindent 8 }}
      size: {{ .Values.db.size }}
      {{- with .Values.db.storageClass }}
      storageClass: {{ . }}
      {{- end }}
    seaweedfs:
```

Delete the `db:` mapping (everything from `db:` through the closing `{{- end }}` of the `with .Values.db.storageClass` block, inclusive). The result must be:

```yaml
  values:
    global:
      serviceAccountName: "{{ .Release.Namespace }}-seaweedfs"
    seaweedfs:
```

- [ ] **Step 3: Render the chart and inspect the seaweedfs-system HR**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/extra/seaweedfs
helm template . --name-template seaweedfs --namespace tenant-root \
  --set _namespace.host=example.org \
  --set _namespace.ingress=tenant-root \
  --set _cluster.solver=http01 \
  2>&1 | yq 'select(.kind == "HelmRelease" and .metadata.name == "seaweedfs-system")' -
```

Expected:
- `spec.dependsOn` contains `{name: seaweedfs-db}`
- `spec.interval` is `30s`
- `spec.values` does NOT contain a `db:` key

- [ ] **Step 4: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/extra/seaweedfs/templates/seaweedfs.yaml
git commit -s -m "feat(seaweedfs): seaweedfs-system dependsOn seaweedfs-db; drop db pass-through"
```

---

## Task 8: Migration script `migrations/39` — re-annotate existing Cluster ownership

For tenants upgraded from any pre-split version, the `Cluster/seaweedfs-db` resource currently has `meta.helm.sh/release-name=seaweedfs-system`. The new `seaweedfs-db` HR will refuse to manage it (Helm errors with "resource already exists, not owned by this release") unless we rewrite the annotation. Also stamp `helm.sh/resource-policy: keep` so the upgrade of the slimmed `seaweedfs-system` chart cannot delete the Cluster between migration and reconcile.

**Files:**
- Create: `packages/core/platform/images/migrations/migrations/39`

- [ ] **Step 1: Write the migration script**

```sh
#!/bin/sh
# Migration 38 --> 39
# Adopt existing Cluster/seaweedfs-db resources into the new seaweedfs-db
# HelmRelease introduced in this release.
#
# Pre-split, the CNPG Cluster lived inside the seaweedfs-system Helm release.
# Splitting moves it to a new release named seaweedfs-db. We rewrite the
# helm ownership annotations so Helm adopts the existing Cluster instead of
# erroring on the next reconcile, and stamp helm.sh/resource-policy: keep
# so the seaweedfs-system upgrade (which no longer renders the Cluster) does
# not delete it during the transition.

set -euo pipefail

# Iterate every namespace that has a Cluster named "seaweedfs-db".
for ns in $(kubectl get cluster.postgresql.cnpg.io -A \
              -o jsonpath='{range .items[?(@.metadata.name=="seaweedfs-db")]}{.metadata.namespace}{"\n"}{end}'); do
  current=$(kubectl get cluster.postgresql.cnpg.io seaweedfs-db -n "$ns" \
    -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}' 2>/dev/null || true)
  if [ "$current" = "seaweedfs-system" ]; then
    echo "Re-annotating Cluster/seaweedfs-db in $ns: seaweedfs-system -> seaweedfs-db"
    kubectl annotate cluster.postgresql.cnpg.io seaweedfs-db -n "$ns" \
      meta.helm.sh/release-name=seaweedfs-db \
      helm.sh/resource-policy=keep \
      --overwrite
    # release-namespace stays the same (tenant namespace, e.g. tenant-root)
  fi
done

# Stamp version
kubectl create configmap -n cozy-system cozystack-version \
  --from-literal=version=39 --dry-run=client -o yaml | kubectl apply -f-
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x /home/daniil/aenix/cozystack-split-seaweedfs/packages/core/platform/images/migrations/migrations/39
```

- [ ] **Step 3: Lint as POSIX shell**

```bash
sh -n /home/daniil/aenix/cozystack-split-seaweedfs/packages/core/platform/images/migrations/migrations/39
echo "exit=$?"
```

Expected: `exit=0` (syntactically valid).

- [ ] **Step 4: Verify it matches the project's existing migration pattern**

```bash
diff <(head -1 /home/daniil/aenix/cozystack-split-seaweedfs/packages/core/platform/images/migrations/migrations/38) \
     <(head -1 /home/daniil/aenix/cozystack-split-seaweedfs/packages/core/platform/images/migrations/migrations/39)
```

Expected: identical first line (`#!/bin/sh`), exit 0.

- [ ] **Step 5: Commit**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git add packages/core/platform/images/migrations/migrations/39
git commit -s -m "feat(migrations): adopt Cluster/seaweedfs-db into seaweedfs-db release (migration 39)"
```

---

## Task 9: End-to-end render check

Render the entire `extra/seaweedfs` chart for a representative tenant and confirm the topology of HRs and dependencies is what we designed.

- [ ] **Step 1: Render and grep both HRs**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/extra/seaweedfs
helm template . --name-template seaweedfs --namespace tenant-root \
  --set _namespace.host=example.org \
  --set _namespace.ingress=tenant-root \
  --set _cluster.solver=http01 \
  2>&1 | yq 'select(.kind == "HelmRelease") | {name: .metadata.name, dependsOn: .spec.dependsOn, interval: .spec.interval, waitStrategy: .spec.waitStrategy, healthCheckExprs: .spec.healthCheckExprs}' -
```

Expected output (order may vary):

```yaml
name: seaweedfs-db
dependsOn: null
interval: 5m
waitStrategy:
  name: poller
healthCheckExprs:
  - apiVersion: postgresql.cnpg.io/v1
    kind: Cluster
    current: has(status.conditions) && status.conditions.exists(e, e.type == 'Ready' && e.status == 'True')
    failed: has(status.conditions) && status.conditions.exists(e, e.type == 'Ready' && e.status == 'False')
---
name: seaweedfs-system
dependsOn:
  - name: seaweedfs-db
interval: 30s
waitStrategy: null
healthCheckExprs: null
```

- [ ] **Step 2: Confirm Cluster CR is no longer in the system/seaweedfs render**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs
make show 2>&1 | grep -c "kind: Cluster"
```

Expected: `0`.

- [ ] **Step 3: Confirm Cluster CR IS in the new seaweedfs-db render**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs/packages/system/seaweedfs-db
helm template . --namespace tenant-root --set db.replicas=2 --set db.size=10Gi 2>&1 | grep -c "kind: Cluster"
```

Expected: `1`.

- [ ] **Step 4: Run repo-level helm-unit-tests if any exist for these packages**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
make unit-tests 2>&1 | tail -30
```

Expected: existing tests pass; if any fail and the failure is unrelated to this change, note it but don't fix here.

- [ ] **Step 5: No commit needed** — this is verification only.

---

## Task 10: Final repo-level pre-commit and lint

Run `pre-commit` to catch any auto-format adjustments the project enforces (per `CLAUDE.md`, a pre-commit hook runs `make generate` in modified packages).

- [ ] **Step 1: Run pre-commit on the full diff**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
pre-commit run --all-files 2>&1 | tail -40
```

Expected: all hooks pass, or the hook auto-fixes whitespace/generated-file drift. If a hook auto-fixes files, run it again to confirm idempotence:

```bash
pre-commit run --all-files
```

- [ ] **Step 2: If files were modified by hooks, commit them**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git status
# If non-empty:
git add -A
git commit -s -m "chore: pre-commit auto-fixes after seaweedfs split"
```

(If clean, skip the commit.)

- [ ] **Step 3: Show the final diff stat**

```bash
cd /home/daniil/aenix/cozystack-split-seaweedfs
git log --stat main..HEAD
```

Expected: commits from Tasks 1–9 (and possibly 10) showing the new files under `packages/system/seaweedfs-db/`, the deleted `templates/database.yaml`, the new wrapper template, the modified existing wrapper, the source registration, and the migration script.

---

## Verification (manual / outside this plan)

After the plan is fully executed and pushed, the actual proof points are:

1. **CI: `Configure Tenant and wait for applications`** in `hack/e2e-install-cozystack.bats` should pass at the existing `kubectl wait hr/seaweedfs-system -n tenant-root --timeout=2m --for=condition=ready` line. The seaweedfs-filer pods should not enter CrashLoopBackOff during the install.
2. **HR topology on a fresh install**: `kubectl get hr -n tenant-root` shows both `seaweedfs-db` and `seaweedfs-system`, db Ready first, then system Ready within ~30 s.
3. **Upgrade smoke** (manual, on a dev cluster): apply this branch on top of v1.3.x → migration 39 runs → existing Cluster gets re-annotated → seaweedfs-db HR adopts it (no `InstallFailed`) → seaweedfs-system upgrade does not delete the Cluster (resource-policy: keep) → filer pods stay running throughout.

---

## Out of scope for this plan

- Same split for `monitoring-system` — separate spec + plan.
- Same split for harbor, openbao, etc. — file follow-up issues per spec.
- Helm-unittest tests for seaweedfs / seaweedfs-db — `packages/system/seaweedfs/` does not currently have a `tests/` directory; adding the testing infrastructure is out of scope here. The `make show` and end-to-end render checks in Tasks 3, 4, 6, 7, 9 cover regression testing.
