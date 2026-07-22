#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the ADOPTION path of platform migration 54.
#
# migration-54-adopt-values.bats pins the jq value-mapping; this file pins the
# adopt_one branch that mutates ownership of running worker objects (annotate
# helm.sh/resource-policy=keep + meta.helm.sh/release-name). The fake kubectl
# serves worker objects via FAKE_OBJS ("<res> <name> <release-name>" lines) and
# KubevirtMachineTemplate names via FAKE_KMT_NAMES, so the annotate, idempotency
# and refusal branches and the 6-hex KMT anchor are exercised directly rather
# than short-circuited on "absent".
#
# cozytest.sh awk parser: @test blocks only, a bare `}` at column 0 ends a test,
# no run/$status/setup/teardown. Assertions are direct shell tests.
# Run with: hack/cozytest.sh hack/migration-54-adopt-path.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-54"
MIG="$PWD/packages/core/platform/images/migrations/migrations/54"

prep() {
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_HR_LIST="$WORK/hrlist.json"
  export FAKE_CHILD_HR="$WORK/child-hr.json"
  export FAKE_CMDLOG="$WORK/cmdlog"
  export FAKE_OBJS="$WORK/objs"
  : > "$FAKE_CMDLOG"
  export PATH="$FAKEBIN:$PATH"
  export NAMESPACE=cozy-system
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"md0":{"minReplicas":1,"roles":["ingress-nginx"]}}}}}]}
JSON
}

@test "adopt_one pins keep + child release-name on parent-owned worker objects, anchoring the KMT match" {
  prep
  # Worker objects currently owned by the parent release, plus a sibling pool's
  # KMT (md0-large) the 6-hex anchor must NOT adopt while processing md0.
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md0-abc123 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md0-large-def456 kubernetes-test3
OBJS
  FAKE_KMT_NAMES=$(printf '%s\n%s' kubernetes-test3-md0-abc123 kubernetes-test3-md0-large-def456)
  export FAKE_KMT_NAMES
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  # MD/MHC/WM adopted onto the child release with keep.
  grep -qE 'annotate machinedeployment.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate machinedeployment.* meta.helm.sh/release-name=kubernetes-nodes-test3-md0' "$FAKE_CMDLOG"
  grep -qE 'annotate machinehealthcheck.* kubernetes-test3-md0 ' "$FAKE_CMDLOG"
  grep -qE 'annotate workloadmonitor.* kubernetes-test3-md0 ' "$FAKE_CMDLOG"
  # The pool's own KMT (6-hex suffix) is adopted...
  grep -qE 'annotate kubevirtmachinetemplate.* kubernetes-test3-md0-abc123 ' "$FAKE_CMDLOG"
  # ...but the sibling pool md0-large's KMT is NOT (anchor stops the mis-adopt).
  ! grep -qE 'kubernetes-test3-md0-large-def456' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "adopt_one is idempotent: objects already on the child release are skipped" {
  prep
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-nodes-test3-md0
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-nodes-test3-md0
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-nodes-test3-md0
OBJS
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  # Already adopted -> no annotate issued at all.
  ! grep -qE 'annotate ' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "adopt_one refuses an object owned by an unexpected release and fails the run closed" {
  prep
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 some-other-release
OBJS
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  # Foreign owner -> refuse -> non-zero exit (fail closed, retried next upgrade).
  [ "$rc" -ne 0 ]
  grep -qi 'refusing' "$WORK/out"
  rm -rf "$WORK"
}

@test "an RFC-1123-invalid nodeGroup key is pinned and skipped, not applied (no upgrade deadlock)" {
  prep
  # The old nodeGroups map key was never schema-constrained, so a parent HR can
  # carry a key like 'My_Pool' that yields the RFC-1123-invalid child HR name
  # kubernetes-nodes-test3-My_Pool. Without the guard the create path reaches
  # `kubectl apply`, the fake apiserver rejects the name, and the migration exits
  # 1 -- deadlocking every tenant's platform pre-upgrade hook. The guard must
  # warn, pin the (absent) pool objects as a no-op, skip adoption, and continue.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"My_Pool":{"minReplicas":1,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  # Guard caught it: the run succeeds instead of deadlocking.
  [ "$rc" -eq 0 ]
  grep -qiE 'not a valid RFC-1123 label' "$WORK/out"
  # The invalid child HelmRelease is never applied.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
