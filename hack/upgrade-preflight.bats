#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the cozystack upgrade preflight health gate:
#   packages/core/platform/images/migrations/preflight/run-preflight.sh
#   packages/core/platform/images/migrations/preflight/checks/*
#
# The preflight runs as a pre-upgrade Helm hook (weight 0, before the migration
# hook) and refuses to let an upgrade proceed on an unhealthy cluster, UNLESS an
# operator explicitly overrides it. These tests drive the real runner and real
# checks end-to-end against a fake kubectl (hack/testdata/upgrade-preflight/),
# mocking only the cluster boundary — the same approach migration-50 uses.
#
# The behaviours pinned here are the review blockers:
#   1. a healthy cluster passes (exit 0);
#   2. any failing check blocks the upgrade (exit 1) with a non-zero runner exit;
#   3. preflight.override=true downgrades failures to warnings and proceeds;
#   4. preflight.skipChecks skips a named check;
#   5. LINSTOR absent is a graceful N/A, not a failure;
#   6. preflight.enabled=false is a full bypass.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`/`setup`. Assertions are direct
# shell tests that exit non-zero on failure. Each test runs in its own subshell,
# so exported FAKE_*/PREFLIGHT_* knobs do not leak between tests.
#
# Run with: hack/cozytest.sh hack/upgrade-preflight.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/upgrade-preflight"
PF="$PWD/packages/core/platform/images/migrations/preflight/run-preflight.sh"
CHECKS="$PWD/packages/core/platform/images/migrations/preflight/checks"
LIB="$PWD/packages/core/platform/images/migrations/preflight/lib"

# prep resets PATH/env to a fully healthy scenario. Tests override individual
# FAKE_* knobs afterwards to model a specific fault.
prep() {
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export PATH="$FAKEBIN:$PATH"
  export PREFLIGHT_CHECKS_DIR="$CHECKS"
  export PREFLIGHT_ENABLED=true
  export PREFLIGHT_OVERRIDE=false
  export PREFLIGHT_SKIP=""
  # Baseline enforces every check (advisory empty), so the blocking-path tests
  # below stay meaningful. Advisory behaviour is exercised explicitly.
  export PREFLIGHT_ADVISORY=""
  export FAKE_LINSTOR_PRESENT=1
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n1"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
  export FAKE_LINSTOR_NODES_JSON='[[{"name":"n1","connection_status":"ONLINE"}]]'
  export FAKE_LINSTOR_VOL_JSON='[[{"name":"pvc-a","volumes":[{"state":{"disk_state":"UpToDate"}}]}]]'
  # Publishing is on by default so every test below exercises the real path;
  # the args of each publish land in $WORK/published for assertions.
  export NAMESPACE=cozy-system
  export PREFLIGHT_STATUS_CONFIGMAP=cozystack-preflight-status
  export FAKE_STATUS_ARGS_OUT="$WORK/published"
  export FAKE_STATUS_FAIL=0
}

# run_pf executes the runner, captures combined output to $WORK/out and the exit
# code to $RC (never aborts the test itself).
run_pf() {
  RC=0
  sh "$PF" >"$WORK/out" 2>&1 || RC=$?
  cat "$WORK/out"
}

@test "healthy cluster passes preflight" {
  prep
  run_pf
  [ "$RC" -eq 0 ]
}

@test "a NotReady node blocks the upgrade" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n1"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "n2" "$WORK/out"
  grep -q "nodes-ready" "$WORK/out"
}

@test "a faulty DRBD volume state blocks the upgrade" {
  prep
  export FAKE_LINSTOR_VOL_JSON='[[{"name":"pvc-a","volumes":[{"state":{"disk_state":"Inconsistent"}}]}]]'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "linstor" "$WORK/out"
}

@test "an offline LINSTOR satellite blocks the upgrade" {
  prep
  export FAKE_LINSTOR_NODES_JSON='[[{"name":"n1","connection_status":"ONLINE"},{"name":"n2","connection_status":"OFFLINE"}]]'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "n2" "$WORK/out"
  grep -q "linstor" "$WORK/out"
}

@test "an advisory linstor failure warns but does not block the upgrade" {
  prep
  export PREFLIGHT_ADVISORY="linstor"
  export FAKE_LINSTOR_VOL_JSON='[[{"name":"pvc-a","volumes":[{"state":{"disk_state":"Inconsistent"}}]}]]'
  run_pf
  [ "$RC" -eq 0 ]
  grep -qi "advisory" "$WORK/out"
}

@test "an advisory check does not mask a blocking check from another check" {
  prep
  export PREFLIGHT_ADVISORY="linstor"
  # linstor is advisory, but a NotReady node (nodes-ready, enforcing) still blocks.
  export FAKE_LINSTOR_VOL_JSON='[[{"name":"pvc-a","volumes":[{"state":{"disk_state":"Inconsistent"}}]}]]'
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "nodes-ready" "$WORK/out"
}

@test "a node reporting no Ready condition at all blocks the upgrade" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n1"},"spec":{},"status":{"conditions":[]}}]}'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "n1" "$WORK/out"
  grep -q "nodes-ready" "$WORK/out"
}

@test "a failure to list nodes fails closed, not open" {
  prep
  export FAKE_NODES_FAIL=1
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "nodes-ready" "$WORK/out"
}

@test "malformed 'kubectl get nodes' JSON fails closed, not open" {
  prep
  # A successful kubectl call returning unparseable output must NOT collapse into
  # an empty "all Ready" result and permit the upgrade.
  export FAKE_NODES_JSON='this is not json'
  run_pf
  [ "$RC" -eq 1 ]
  grep -qi "cannot parse" "$WORK/out"
}

@test "malformed LINSTOR node JSON fails closed (enforcing)" {
  prep
  export FAKE_LINSTOR_NODES_JSON='not json'
  run_pf
  [ "$RC" -eq 1 ]
  grep -qi "cannot parse LINSTOR node" "$WORK/out"
}

@test "malformed LINSTOR volume JSON fails closed (enforcing)" {
  prep
  export FAKE_LINSTOR_VOL_JSON='not json'
  run_pf
  [ "$RC" -eq 1 ]
  grep -qi "cannot parse LINSTOR volume" "$WORK/out"
}

@test "LINSTOR not installed is a graceful N/A, not a failure" {
  prep
  export FAKE_LINSTOR_PRESENT=0
  run_pf
  [ "$RC" -eq 0 ]
}

@test "a LINSTOR query failure is a FAIL, not a false 'not installed' skip" {
  prep
  # deploy probe fails with a non-NotFound error (RBAC/timeout): the check must
  # NOT swallow it as "not installed"; it must fail closed so a real inability
  # to determine health blocks (enforcing baseline) instead of passing.
  export FAKE_LINSTOR_DEPLOY_ERR=1
  run_pf
  [ "$RC" -eq 1 ]
  grep -qi "cannot determine LINSTOR state" "$WORK/out"
}

@test "no kubectl call uses --request-timeout (it breaks in-cluster config in the image)" {
  # Regression guard: --request-timeout in the pinned kubectl falls back to
  # localhost:8080 instead of the in-cluster ServiceAccount config, which fails
  # every call on a healthy cluster. Bounding is done with a timeout wrapper.
  # Match the invocation form "kubectl --request-timeout" so the lib comment
  # that names the flag as forbidden does not trip the guard. The runner is
  # covered too: it is as free to grow a kubectl call as the checks are. An explicit
  # return-on-match is used because a bare "! grep" would not fail the test
  # under set -e.
  if grep -rn 'kubectl --request-timeout' "$PF" "$CHECKS" "$LIB"; then
    echo "found a kubectl --request-timeout invocation; use the kubectl_t timeout wrapper instead" >&2
    return 1
  fi
}

@test "override proceeds past a failing check" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  export PREFLIGHT_OVERRIDE=true
  run_pf
  [ "$RC" -eq 0 ]
  grep -qi "override" "$WORK/out"
}

@test "skipChecks skips the named check" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  export PREFLIGHT_SKIP="nodes-ready"
  run_pf
  [ "$RC" -eq 0 ]
}

@test "disabled preflight is a full bypass" {
  prep
  export PREFLIGHT_ENABLED=false
  # Even with a broken cluster the runner must not fail when disabled.
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  run_pf
  [ "$RC" -eq 0 ]
}

@test "cordoned-but-Ready node is a warning, not a block" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n1"},"spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}'
  run_pf
  [ "$RC" -eq 0 ]
  grep -qi "cordoned" "$WORK/out"
}

@test "a checks directory with no checks fails closed, not open" {
  prep
  # A gate that ran nothing must not report the verdict it exists to withhold.
  # The image build (RUN chmod +x /preflight/checks/*) is the other guard on
  # this, but the runner must not depend on a neighbouring build step for its
  # fail-closed property.
  mkdir -p "$WORK/empty-checks"
  export PREFLIGHT_CHECKS_DIR="$WORK/empty-checks"
  run_pf
  [ "$RC" -ne 0 ]
  grep -qi "no preflight checks" "$WORK/out"
}

@test "a glob in skipChecks does not match a file in the working directory" {
  prep
  # $SKIP is deliberately word-split; pathname expansion is not wanted. Without
  # `set -f` a skipChecks entry of "*" expands against the runner's working
  # directory (/migrations in the image) and can silently disable a check whose
  # id happens to name a file there.
  mkdir -p "$WORK/cwd"
  : > "$WORK/cwd/nodes-ready"
  export PREFLIGHT_SKIP='*'
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  cd "$WORK/cwd"
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "nodes-ready" "$WORK/out"
}

@test "a passing run publishes its verdict to the status ConfigMap" {
  prep
  run_pf
  [ "$RC" -eq 0 ]
  # Two writes: one when the run starts (so a Job killed by
  # activeDeadlineSeconds still leaves a record), one with the outcome.
  grep -q "verdict=running" "$WORK/published"
  grep -q "verdict=passed" "$WORK/published"
  grep -q "namespace cozy-system" "$WORK/published"
}

@test "a blocked upgrade publishes the failing check ids, not just a bare failure" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "verdict=blocked" "$WORK/published"
  grep -q "failedChecks=nodes-ready" "$WORK/published"
}

@test "an advisory-only problem still publishes a passing verdict with the advisory id" {
  prep
  export PREFLIGHT_ADVISORY="linstor"
  export FAKE_LINSTOR_VOL_JSON='[[{"name":"pvc-a","volumes":[{"state":{"disk_state":"Inconsistent"}}]}]]'
  run_pf
  [ "$RC" -eq 0 ]
  grep -q "verdict=passed" "$WORK/published"
  grep -q "advisoryChecks=linstor" "$WORK/published"
}

@test "a failure to publish the status does not change the verdict" {
  prep
  # Missing RBAC must not turn a healthy cluster into a blocked upgrade, nor a
  # broken one into a permitted upgrade. Publishing is observability, never the
  # gate itself.
  export FAKE_STATUS_FAIL=1
  run_pf
  [ "$RC" -eq 0 ]
  grep -qi "could not publish" "$WORK/out"

  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  run_pf
  [ "$RC" -eq 1 ]
}

@test "publishing is skipped when no namespace is configured" {
  prep
  export NAMESPACE=""
  run_pf
  [ "$RC" -eq 0 ]
  [ ! -s "$WORK/published" ]
}

@test "a cordoned NotReady node is a warning, not a block" {
  prep
  # cordon, drain, power off is the normal maintenance sequence and leaves the
  # node at Ready=Unknown. The operator already signalled it is out of service.
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n1"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"n2"},"spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"Unknown"}]}}]}'
  run_pf
  [ "$RC" -eq 0 ]
  grep -q "n2 (Ready=Unknown)" "$WORK/out"
  grep -qi "cordoned" "$WORK/out"
}

@test "an uncordoned NotReady node still blocks when another node is cordoned" {
  prep
  export FAKE_NODES_JSON='{"items":[{"metadata":{"name":"n1"},"spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"Unknown"}]}},{"metadata":{"name":"n2"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}'
  run_pf
  [ "$RC" -eq 1 ]
  grep -q "failedChecks=nodes-ready" "$WORK/published"
}
