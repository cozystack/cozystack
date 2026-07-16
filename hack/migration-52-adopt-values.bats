#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 52 (adopt pre-split worker pools into
# per-pool kubernetes-nodes HelmReleases).
#
# These pin the value-mapping contract that guarantees "byte-identical child
# re-render, zero worker-VM churn". The migration builds the child HelmRelease's
# spec.values from the parent kubernetes HR values with a jq helper:
#
#     def pick($o; ks): reduce ks[] as $k ({}; if ($o | has($k)) ...);
#     ... + pick(.; ["version","talos","images"])
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
# Run with: hack/cozytest.sh hack/migration-52-adopt-values.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-52"
MIG="$PWD/packages/core/platform/images/migrations/migrations/52"

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
     "nodeGroups":{"md0":{"minReplicas":1,"maxReplicas":3,"roles":["ingress-nginx"]}},
     "talos":{"version":"v1.13.0","schematicID":"deadbeef","imageFactoryURL":"https://factory.talos.dev","installerRepository":"factory.talos.dev/installer"},
     "version":"v1.32",
     "storageClass":"replicated",
     "images":{"kubectl":"example.io/kubectl:v1.32"}}}}
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

  # Group fields and the storageClass cluster-level fallback carry through.
  [ "$(jq -r '.spec.values.minReplicas' "$FAKE_CHILD_HR")" = "1" ]
  [ "$(jq -r '.spec.values.roles[0]' "$FAKE_CHILD_HR")" = "ingress-nginx" ]
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
