#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 56 (adopt pre-split worker pools into
# per-pool kubernetes-nodes HelmReleases).
#
# These pin the value-mapping contract that guarantees "byte-identical child
# re-render, zero worker-VM churn". The migration builds the child HelmRelease's
# spec.values from the parent kubernetes HR values with a jq helper:
#
#     def pick($o; ks): reduce ks[] as $k ({}; if ($o | has($k)) ...);
#     ... + pick(.; ["version","talos"])   # images is narrowed to {kubectl} separately
#
# A subtle regression is to write `def pick(o; ks)` with a filter-parameter:
# inside `reduce ks[] as $k ({}; ...)` the `.` context is the accumulator, so
# `o | has($k)` tests the accumulator (not the source object) and pick(.; ...)
# returns {} for EVERY input. The child HR is then created WITHOUT the tenant's
# talos/version, Helm renders the chart defaults, talos.version changes, the
# content-hashed KubevirtMachineTemplate is renamed, and the MachineDeployment
# rolls every live worker VM on upgrade — the exact churn this migration exists
# to prevent. Static review missed it; only e2e on a real cluster caught it.
#
# The @test below drives the REAL migration script against a fake kubectl and
# asserts the captured child HelmRelease carries the tenant's non-default
# talos/version/images (plus the group fields and storageClass fallback). It
# fails against the buggy `pick(o; ...)` and passes against `pick($o; ...)`.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`/`setup`/`teardown`. Assertions are
# direct shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/migration-56-adopt-values.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-56"
MIG="$PWD/packages/core/platform/images/migrations/migrations/56"

# prep resets PATH/env to a clean scenario: one tenant Kubernetes HR (test3)
# with a single pool md0 and NON-default talos/version/images so the assertions
# distinguish "copied the tenant value" from "fell back to the chart default".
prep() {
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_HR_LIST="$WORK/hrlist.json"
  export FAKE_CHILD_HR="$WORK/child-hr.json"
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export PATH="$FAKEBIN:$PATH"
  export NAMESPACE=cozy-system
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[
  {"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},
   "spec":{"values":{
     "nodeGroups":{"md0":{"minReplicas":1,"maxReplicas":3,"roles":["ingress-nginx"],"logSerialConsole":true}},
     "talos":{"version":"v1.13.0","schematicID":"deadbeef","imageFactoryURL":"https://factory.talos.dev","installerRepository":"factory.talos.dev/installer"},
     "version":"v1.32",
     "storageClass":"replicated",
     "images":{"kubectl":"example.io/kubectl:v1.32","waitForKubeconfig":"drop-me","talosCsrSigner":"drop-me"}}}}
]}
JSON
}

@test "child HR carries the tenant's talos/version/images (not chart defaults) so the KMT hash is preserved" {
  prep
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]

  # The create path ran and captured a child HelmRelease.
  grep -qF -- "APPLY-HR" "$FAKE_CMDLOG"
  [ -s "$FAKE_CHILD_HR" ]

  # Shape sanity: it is the pool's child HR.
  [ "$(jq -r '.kind' "$FAKE_CHILD_HR")" = "HelmRelease" ]
  [ "$(jq -r '.metadata.name' "$FAKE_CHILD_HR")" = "kubernetes-nodes-test3-md0" ]
  [ "$(jq -r '.spec.values.cluster' "$FAKE_CHILD_HR")" = "test3" ]

  # THE REGRESSION: cluster-level inputs the worker templates consume must be
  # copied from the tenant, NOT dropped (which would let the chart default win).
  [ "$(jq -r '.spec.values.talos.version' "$FAKE_CHILD_HR")" = "v1.13.0" ]
  [ "$(jq -r '.spec.values.talos.schematicID' "$FAKE_CHILD_HR")" = "deadbeef" ]
  [ "$(jq -r '.spec.values.version' "$FAKE_CHILD_HR")" = "v1.32" ]
  [ "$(jq -r '.spec.values.images.kubectl' "$FAKE_CHILD_HR")" = "example.io/kubectl:v1.32" ]
  # images is narrowed to the declared kubectl key only — the parent's
  # waitForKubeconfig/talosCsrSigner (undeclared in the KubernetesNodes schema)
  # must NOT leak into the adopted pool values.
  [ "$(jq -r '.spec.values.images | keys | join(",")' "$FAKE_CHILD_HR")" = "kubectl" ]

  # Group fields and the storageClass cluster-level fallback carry through.
  [ "$(jq -r '.spec.values.minReplicas' "$FAKE_CHILD_HR")" = "1" ]
  [ "$(jq -r '.spec.values.roles[0]' "$FAKE_CHILD_HR")" = "ingress-nginx" ]
  # logSerialConsole feeds the content-hashed KMT (nodegroup.yaml define), so
  # dropping it on adoption would rename the KMT and roll every worker VM of the
  # pool (and silently disable the setting); it must carry through.
  [ "$(jq -r '.spec.values.logSerialConsole' "$FAKE_CHILD_HR")" = "true" ]
  [ "$(jq -r '.spec.values.storageClass' "$FAKE_CHILD_HR")" = "replicated" ]
  rm -rf "$WORK"
}

@test "positive control: a clean run reaches the version stamp" {
  prep
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  # The stamp ran (so "values present" above is a real signal, not a no-op run).
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "implicit md0: an empty nodeGroups materialises the default ingress-nginx pool" {
  prep
  # A parent cluster whose spec.values carries no nodeGroups relied on the chart's
  # implicit md0 default. Migration 56 must materialise DEFAULT_MD0 and create the
  # child HR kubernetes-nodes-<cluster>-md0 with minReplicas 0 and the
  # ingress-nginx role, so a cluster that never set nodeGroups keeps its pool.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qF -- "materialising implicit md0 default" "$WORK/out"
  grep -qF -- "APPLY-HR" "$FAKE_CMDLOG"
  [ "$(jq -r '.metadata.name' "$FAKE_CHILD_HR")" = "kubernetes-nodes-test3-md0" ]
  [ "$(jq -r '.spec.values.minReplicas' "$FAKE_CHILD_HR")" = "0" ]
  [ "$(jq -r '.spec.values.roles[0]' "$FAKE_CHILD_HR")" = "ingress-nginx" ]
  # DEFAULT_MD0 is the sole source of the implicit pool's shape, and the fields
  # below feed the content-hashed KubevirtMachineTemplate (kubernetes-nodes
  # nodegroup.yaml). An edit to any of them re-hashes the KMT and silently rolls
  # every implicit-md0 pool in the fleet on the next upgrade; no other assertion
  # here catches it (name/minReplicas/role do not enter the hash). Pin the
  # pre-split md0 values so such an edit reds this case instead of shipping.
  [ "$(jq -r '.spec.values.instanceType' "$FAKE_CHILD_HR")" = "u1.medium" ]
  [ "$(jq -r '.spec.values.diskSize' "$FAKE_CHILD_HR")" = "20Gi" ]
  [ "$(jq -c '.spec.values.resources' "$FAKE_CHILD_HR")" = "{}" ]
  [ "$(jq -c '.spec.values.gpus' "$FAKE_CHILD_HR")" = "[]" ]
  [ "$(jq -c '.spec.values.kubelet' "$FAKE_CHILD_HR")" = "{}" ]
  # storageClass "" in DEFAULT_MD0 falls through to the (absent here) cluster-
  # level value, so the child carries no storageClass key — the pre-split md0
  # shape, where md0 rendered with the chart's empty-string default.
  [ "$(jq -r '.spec.values.storageClass // "ABSENT"' "$FAKE_CHILD_HR")" = "ABSENT" ]
  rm -rf "$WORK"
}

@test "adoption carries podCpuLimit/podCpuRequest and the cluster-wide MHC timeouts so a tuned pool is neither rolled nor reset" {
  prep
  # A pre-split parent that tuned worker sizing and MachineHealthCheck timeouts.
  # podCpuLimit/podCpuRequest feed the content-hashed KubevirtMachineTemplate, so
  # dropping them on adoption renames the KMT and rolls every worker VM of the
  # pool. readyUnknownTimeout / readyFalseTimeout / maxNodeProvisionTime feed the
  # pool's MachineHealthCheck and the autoscaler annotation, so dropping them
  # silently resets the tuning to the chart default. All must survive the
  # parent -> child value mapping (these fields arrived with the main merge that
  # made the pool chart configurable; the pick list has to keep pace with it).
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{
  "nodeGroups":{"md0":{"minReplicas":1,"maxReplicas":3,"resources":{"cpu":"4","memory":"8Gi"},"podCpuLimit":"3","podCpuRequest":"2"}},
  "nodeHealthCheck":{"maxUnhealthy":"30%","nodeStartupTimeout":"25m","readyUnknownTimeout":"90s","readyFalseTimeout":"7m"},
  "maxNodeProvisionTime":"45m",
  "talos":{"version":"v1.13.0","schematicID":"deadbeef","imageFactoryURL":"https://factory.talos.dev","installerRepository":"factory.talos.dev/installer"},
  "version":"v1.32"}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qF -- "APPLY-HR" "$FAKE_CMDLOG"
  [ "$(jq -r '.metadata.name' "$FAKE_CHILD_HR")" = "kubernetes-nodes-test3-md0" ]

  # KMT-hash inputs: carried verbatim from the group, or the pool rolls on adoption.
  [ "$(jq -r '.spec.values.podCpuLimit' "$FAKE_CHILD_HR")" = "3" ]
  [ "$(jq -r '.spec.values.podCpuRequest' "$FAKE_CHILD_HR")" = "2" ]

  # cluster-wide MHC / autoscaler tuning: flattened onto the pool, not reset.
  [ "$(jq -r '.spec.values.maxUnhealthy' "$FAKE_CHILD_HR")" = "30%" ]
  [ "$(jq -r '.spec.values.nodeStartupTimeout' "$FAKE_CHILD_HR")" = "25m" ]
  [ "$(jq -r '.spec.values.readyUnknownTimeout' "$FAKE_CHILD_HR")" = "90s" ]
  [ "$(jq -r '.spec.values.readyFalseTimeout' "$FAKE_CHILD_HR")" = "7m" ]
  [ "$(jq -r '.spec.values.maxNodeProvisionTime' "$FAKE_CHILD_HR")" = "45m" ]
  rm -rf "$WORK"
}

@test "an unlabelled parent HR (kubernetes chartRef, no application.kind label) is restamped before the sweep" {
  prep
  # A parent kubernetes HelmRelease predating the apps.cozystack.io/application.kind
  # label (fdca49838, ~v1.2), or one restored/created by hand, is identified by its
  # chartRef rather than the label. The migration must relabel it BEFORE the
  # label-selected sweep, otherwise the sweep skips it, stamps, and the parent's
  # control-plane-only re-render prunes its un-pinned workers. Fixture carries the
  # parent chartRef and NO application labels; assert all three labels are written.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-legacy","name":"kubernetes-old"},"spec":{"chartRef":{"name":"cozystack-kubernetes-application-kubevirt-kubernetes"},"values":{"nodeGroups":{"md0":{"minReplicas":0,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qF -- "restamping application labels on unlabelled parent tenant-legacy/kubernetes-old" "$WORK/out"
  grep -qE 'label helmrelease kubernetes-old .*apps.cozystack.io/application.kind=Kubernetes' "$FAKE_CMDLOG"
  grep -qE 'label helmrelease kubernetes-old .*apps.cozystack.io/application.group=apps.cozystack.io' "$FAKE_CMDLOG"
  grep -qE 'label helmrelease kubernetes-old .*apps.cozystack.io/application.name=old' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a labelled parent HR (chartRef + application.kind label) is NOT restamped (no redundant label write)" {
  prep
  # A parent that carries BOTH the kubernetes chartRef AND the application.kind
  # label is already correctly labelled: the restamp's label-absence guard
  # (select .kind != "Kubernetes") must exclude it so it is not re-labelled on
  # every upgrade. A fixture WITHOUT the chartRef would be dropped by the chartRef
  # filter regardless and leave this guard untested, so seed the chartRef too and
  # exercise exactly the label guard. Assert no `label helmrelease` is issued
  # (the sweep's own `label machinedeployment ... managed-by` does not match).
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3","labels":{"apps.cozystack.io/application.kind":"Kubernetes"}},"spec":{"chartRef":{"name":"cozystack-kubernetes-application-kubevirt-kubernetes"},"values":{"nodeGroups":{"md0":{"minReplicas":1,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  ! grep -qE 'label helmrelease' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "pinned-but-unadopted pools are published to the unadopted ConfigMap before the stamp" {
  prep
  # A pool the migration pins prune-proof but cannot adopt (here: child release
  # name over 53 chars) must leave a durable record — the Job-log warnings are
  # reaped by the next upgrade's before-hook-creation policy and migration 56 does
  # not re-run. Assert the unadopted ConfigMap is applied AND the run still stamps.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-verylongclustername-thirty"},"spec":{"values":{"nodeGroups":{"poolnamethatislong":{"minReplicas":0,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qF -- "pinned prune-proof but NOT adopted" "$WORK/out"
  grep -qF -- "APPLY-UNADOPTED-CM" "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a clean run writes NO unadopted ConfigMap (empty skip record)" {
  prep
  # The default fixture adopts cleanly (no skip branch), so no unadopted ConfigMap
  # must be applied — only the version STAMP.
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  ! grep -qF -- "APPLY-UNADOPTED-CM" "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failed unadopted-ConfigMap publish does NOT abort the migration (best-effort record)" {
  prep
  # The record is a convenience artifact, not a safety mechanism: every skipped
  # pool was already pinned prune-proof earlier in the sweep. A permanent publish
  # failure (e.g. exceeding etcd's object-size limit) must NOT fail the migration
  # closed and deadlock the fleet upgrade — it must warn and still stamp.
  export FAKE_UNADOPTED_CM_FAIL=1
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-verylongclustername-thirty"},"spec":{"values":{"nodeGroups":{"poolnamethatislong":{"minReplicas":0,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qF -- "could not publish the unadopted-pools record" "$WORK/out"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  unset FAKE_UNADOPTED_CM_FAIL
  rm -rf "$WORK"
}

@test "an unlabelled parent HR on the OLD inline spec.chart shape is restamped too" {
  prep
  # The application.kind label predates spec.chartRef, so the real unlabelled
  # population is the pre-chartRef inline shape: spec.chart.spec.chart="kubernetes"
  # from the cozystack-apps HelmRepository, no chartRef, no label. Matching only
  # chartRef would miss every one of these and let their workers be pruned. Assert
  # the restamp discovers the inline shape and writes the labels.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-legacy","name":"kubernetes-old"},"spec":{"chart":{"spec":{"chart":"kubernetes","sourceRef":{"kind":"HelmRepository","name":"cozystack-apps"}}},"values":{"nodeGroups":{"md0":{"minReplicas":0,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qF -- "restamping application labels on unlabelled parent tenant-legacy/kubernetes-old" "$WORK/out"
  grep -qE 'label helmrelease kubernetes-old .*apps.cozystack.io/application.kind=Kubernetes' "$FAKE_CMDLOG"
  grep -qE 'label helmrelease kubernetes-old .*apps.cozystack.io/application.name=old' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a non-kubernetes app on the inline shape is NOT restamped (chart-name exact match)" {
  prep
  # A different app on the old inline shape (chart != "kubernetes") must not be
  # mislabelled as a Kubernetes application by the restamp.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-legacy","name":"kubernetes-decoy"},"spec":{"chart":{"spec":{"chart":"postgres","sourceRef":{"kind":"HelmRepository","name":"cozystack-apps"}}},"values":{}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  ! grep -qE 'label helmrelease kubernetes-decoy' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
