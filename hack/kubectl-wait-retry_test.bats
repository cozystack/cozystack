#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for kubectl_wait_retry in hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# The wrapper exists to retry `kubectl wait` against a curated allowlist of
# transient server errors (etcd leader flap et al.) while letting real failures
# (NotFound, --timeout expired) surface. The chainsaw scripts source the lib
# under `set -eu`, where a bare `_out=$(kubectl wait ...)` on a failing wait
# aborts the whole script before the retry loop runs -- silently, since stderr
# is captured into the substitution. These tests execute the wrapper in a
# `sh -eu` subshell, exactly as chainsaw runs it, against a fake kubectl.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`. Assertions are direct shell tests.
# Run with: hack/cozytest.sh hack/kubectl-wait-retry_test.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/kubectl-wait-retry"

prep() {
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
}

@test "a transient etcd error is retried under set -eu instead of silently killing the script" {
  prep
  export FAKE_MODE=transient-once
  rc=0
  out=$(PATH="$FAKEBIN:$PATH" sh -euc '
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl_wait_retry deploy x -n tenant-test --for=condition=available
    echo SCRIPT-REACHED-END
  ' 2>&1) || rc=$?
  echo "$out"
  # The wrapper retried past the transient error and the sourcing script lived.
  [ "$rc" -eq 0 ]
  echo "$out" | grep -q 'retrying'
  echo "$out" | grep -q 'SCRIPT-REACHED-END'
  [ "$(grep -c . "$FAKE_CMDLOG")" -eq 2 ]
  rm -rf "$WORK"
}

@test "NotFound is not retried, fails the script, and its message is printed (fail-loud preserved)" {
  prep
  export FAKE_MODE=notfound
  rc=0
  out=$(PATH="$FAKEBIN:$PATH" sh -euc '
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl_wait_retry deploy x -n tenant-test --for=condition=available
    echo SCRIPT-REACHED-END
  ' 2>&1) || rc=$?
  echo "$out"
  # A real failure still aborts, is not retried, and the error is visible.
  [ "$rc" -ne 0 ]
  echo "$out" | grep -q 'NotFound'
  ! echo "$out" | grep -q 'SCRIPT-REACHED-END'
  [ "$(grep -c . "$FAKE_CMDLOG")" -eq 1 ]
  rm -rf "$WORK"
}
