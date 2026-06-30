#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the fail-closed contract of
# packages/core/platform/images/migrations/migrations/47
#
# Migration 47 pins the implicit default `md0` node group on existing tenant
# Kubernetes clusters before the kubernetes chart stops shipping a baked-in
# md0. Its safety property is the ENTIRE reason it exists: if any per-object
# read or patch fails, it must abort (exit 1) BEFORE stamping the
# cozystack-version ConfigMap, so the pre-upgrade hook fails and Helm never
# rolls the new chart against an un-pinned cluster (whose live md0
# MachineDeployment would otherwise be pruned → CAPI Machine/node deletion).
#
# These tests pin that branch so it cannot silently regress to fail-open. We
# put a fake `kubectl` on PATH and assert that:
#   - a non-NotFound read failure aborts non-zero and never reaches the stamp;
#   - a patch failure aborts non-zero and never reaches the stamp;
#   - a clean run DOES reach the stamp (positive control, so "no stamp" above
#     is a real signal and not a script that simply never called kubectl).
# "The stamp ran" is detected by the version stamp's `kubectl apply` appearing
# in the fake's call log — equivalently, CURRENT_VERSION was/was not advanced.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line (column 0); there is no bats `run`/`$status`/`setup`/`teardown`.
# Assertions are direct shell tests that exit non-zero on failure, and the
# fake-kubectl heredocs deliberately keep every `}` off column 0 so the parser
# does not mistake one for the end of a test. Each @test runs in its own
# subshell, and cleanup is inline (no EXIT/RETURN trap — see
# docs/agents/e2e-testing.md §3).
#
# Run with: hack/cozytest.sh hack/kubernetes-md0-migration.bats
#           (or `bats hack/kubernetes-md0-migration.bats`)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
REPO_ROOT="$(cd "$HACK_DIR/.." && pwd)"
MIGRATION="$REPO_ROOT/packages/core/platform/images/migrations/migrations/47"

# write_fake_kubectl <dir> <logfile>
# Emits a fake `kubectl` that logs every call and behaves per $MOCK_FAIL:
#   - the --all-namespaces list always returns one object (tenant-test/demo);
#   - the per-object `get ... --output json` fails (MOCK_FAIL=read) or returns
#     an object whose nodeGroups lacks md0 (so the migration tries to patch);
#   - `patch` fails when MOCK_FAIL=patch, else succeeds;
#   - `apply` (the version stamp) is a no-op that drains stdin.
# No line below is a bare column-0 `}`, so cozytest.sh's parser leaves it intact.
write_fake_kubectl() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
case "$*" in
  *--all-namespaces*)
    printf 'tenant-test\tdemo\n'
    exit 0
    ;;
  *"--output json"*)
    if [ "${MOCK_FAIL:-}" = read ]; then
      echo 'error: an error on the server ("timeout") has prevented the request from succeeding' >&2
      exit 1
    fi
    printf '%s' '{"apiVersion":"apps.cozystack.io/v1alpha1","kind":"Kubernetes","metadata":{"namespace":"tenant-test","name":"demo"},"spec":{"nodeGroups":{"worker0":{"minReplicas":1,"maxReplicas":3}}}}'
    exit 0
    ;;
  *patch*)
    if [ "${MOCK_FAIL:-}" = patch ]; then
      echo 'error: kubernetes.apps.cozystack.io "demo" could not be patched (simulated)' >&2
      exit 1
    fi
    cat >/dev/null
    echo 'kubernetes.apps.cozystack.io/demo patched'
    exit 0
    ;;
  *apply*)
    cat >/dev/null
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
KEOF
  chmod +x "$1/kubectl"
}

@test "migration 47 aborts non-zero and skips the version stamp when a read fails" {
  tmp=$(mktemp -d)
  export KLOG="$tmp/kubectl.log"
  : > "$KLOG"
  write_fake_kubectl "$tmp" "$KLOG"

  if KLOG="$KLOG" MOCK_FAIL=read PATH="$tmp:$PATH" "$MIGRATION" >"$tmp/out" 2>&1; then
    echo "expected migration to exit non-zero on a non-NotFound read failure; output:" >&2
    cat "$tmp/out" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # The version stamp is `kubectl apply` of the cozystack-version ConfigMap.
  # If it ran, the migration advanced CURRENT_VERSION despite a failure.
  if grep -q apply "$KLOG"; then
    echo "fail-open regression: version stamp (kubectl apply) ran despite a read failure" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi
  rm -rf "$tmp"
}

@test "migration 47 aborts non-zero and skips the version stamp when a patch fails" {
  tmp=$(mktemp -d)
  export KLOG="$tmp/kubectl.log"
  : > "$KLOG"
  write_fake_kubectl "$tmp" "$KLOG"

  if KLOG="$KLOG" MOCK_FAIL=patch PATH="$tmp:$PATH" "$MIGRATION" >"$tmp/out" 2>&1; then
    echo "expected migration to exit non-zero on a patch failure; output:" >&2
    cat "$tmp/out" >&2
    rm -rf "$tmp"
    exit 1
  fi

  if grep -q apply "$KLOG"; then
    echo "fail-open regression: version stamp (kubectl apply) ran despite a patch failure" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi
  rm -rf "$tmp"
}

@test "migration 47 stamps the version on a clean run (positive control)" {
  tmp=$(mktemp -d)
  export KLOG="$tmp/kubectl.log"
  : > "$KLOG"
  write_fake_kubectl "$tmp" "$KLOG"

  # No MOCK_FAIL: read returns an md0-less object, patch succeeds, stamp runs.
  if ! KLOG="$KLOG" MOCK_FAIL=none PATH="$tmp:$PATH" "$MIGRATION" >"$tmp/out" 2>&1; then
    echo "expected a clean migration run to exit zero; output:" >&2
    cat "$tmp/out" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # Proves the stamp IS reachable here, so the two "no apply" assertions above
  # are meaningful rather than vacuously true.
  if ! grep -q apply "$KLOG"; then
    echo "expected the version stamp (kubectl apply) to run on a clean migration" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi
  rm -rf "$tmp"
}
