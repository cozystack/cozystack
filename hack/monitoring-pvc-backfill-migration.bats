#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for packages/core/platform/images/migrations/migrations/51
#
# Migration 51 backfills apps.cozystack.io/application.name onto the pre-existing
# VictoriaMetrics/VictoriaLogs storage PVCs of the monitoring module so the
# post-delete cleanup hook (PR #3094) can reclaim storage on already-deployed
# clusters. Its correctness property is that it must relabel EXACTLY the
# monitoring cluster's vmstorage/vmselect/vlstorage PVCs — never a user's own
# VMCluster, never the CNPG db PVCs — and stamp the version on a clean run.
# helm-unittest cannot check this (it inspects rendered manifests, not the
# operator's runtime PVCs); a mis-scoped or no-op selector is exactly the class
# of bug this PR iterated on, so these tests pin the runtime behaviour.
#
# We put a fake `kubectl` on PATH that logs every call and simulates one tenant
# namespace holding: a monitoring VMCluster (shortterm) + VLCluster (generic)
# whose spec.managedMetadata carries application.name=monitoring-system; a user's
# own VMCluster (custom) with no such label; plus the operator storage PVCs and
# the CNPG db PVCs. We assert the label log:
#   - relabels the shortterm vmstorage/vmselect and generic vlstorage PVCs;
#   - never touches the CNPG db PVCs (grafana-db/alerta-db);
#   - never touches the user's `custom` VMCluster PVCs (no managedMetadata label);
#   - stamps the version (kubectl apply) on a clean run;
#   - is a safe no-op that still stamps when the CRDs are absent.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line (column 0); assertions are direct shell tests, and the fake-kubectl
# heredoc keeps every `}` off column 0. Each @test cleans up inline (no
# EXIT/RETURN trap — see docs/agents/e2e-testing.md §3).
#
# Run with: hack/cozytest.sh hack/monitoring-pvc-backfill-migration.bats
#           (or `bats hack/monitoring-pvc-backfill-migration.bats`)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
REPO_ROOT="$(cd "$HACK_DIR/.." && pwd)"
MIGRATION="$REPO_ROOT/packages/core/platform/images/migrations/migrations/51"

# write_fake_kubectl <dir>
# Emits a fake `kubectl` that logs every call to $KLOG and, when $MOCK_ABSENT=1,
# reports the VM/VL CRDs as absent. Ordering matters: the CR appname read is
# matched by *managedMetadata* before the generic *vmclusters*/*vlclusters* list.
# No line below is a bare column-0 `}`, so cozytest.sh's parser leaves it intact.
write_fake_kubectl() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
case "$*" in
  *"get crd"*)
    [ "${MOCK_ABSENT:-}" = 1 ] && exit 1
    exit 0
    ;;
  *label*pvc*)
    exit 0
    ;;
  *apply*)
    cat >/dev/null
    exit 0
    ;;
  *managedMetadata*)
    case "$*" in
      *shortterm*|*generic*) printf 'monitoring-system' ;;
      *) : ;;
    esac
    exit 0
    ;;
  *vmclusters*)
    printf 'tenant-test shortterm\ntenant-test custom\n'
    exit 0
    ;;
  *vlclusters*)
    printf 'tenant-test generic\n'
    exit 0
    ;;
  *"get pvc"*"items[*]"*)
    printf 'vmstorage-db-vmstorage-shortterm-0\n'
    printf 'vmselect-cachedir-vmselect-shortterm-0\n'
    printf 'vmstorage-db-vmstorage-custom-0\n'
    printf 'vlstorage-db-vlstorage-generic-0\n'
    printf 'grafana-db-1\n'
    printf 'alerta-db-1\n'
    exit 0
    ;;
  *"get pvc"*)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
KEOF
  chmod +x "$1/kubectl"
}

@test "migration 51 relabels only the monitoring VM/VL storage PVCs and stamps the version" {
  tmp=$(mktemp -d)
  export KLOG="$tmp/kubectl.log"
  : > "$KLOG"
  write_fake_kubectl "$tmp"

  if ! KLOG="$KLOG" PATH="$tmp:$PATH" "$MIGRATION" >"$tmp/out" 2>&1; then
    echo "expected a clean migration run to exit zero; output:" >&2
    cat "$tmp/out" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # The monitoring cluster's storage PVCs must be relabeled with the exact value.
  for pvc in vmstorage-db-vmstorage-shortterm-0 vmselect-cachedir-vmselect-shortterm-0 vlstorage-db-vlstorage-generic-0; do
    if ! grep -Eq "label pvc -n tenant-test $pvc apps.cozystack.io/application.name=monitoring-system --overwrite" "$KLOG"; then
      echo "expected $pvc to be relabeled with application.name=monitoring-system" >&2
      cat "$KLOG" >&2
      rm -rf "$tmp"
      exit 1
    fi
  done

  # The CNPG db PVCs must never be touched (they are not monitoring storage).
  if grep -Eq "label pvc.*(grafana-db|alerta-db)" "$KLOG"; then
    echo "regression: cleanup relabeled a CNPG db PVC" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # A user's own VMCluster (no managedMetadata label) must be skipped entirely.
  if grep -Eq "label pvc.*custom" "$KLOG"; then
    echo "regression: relabeled a non-cozystack VMCluster's PVC" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # Positive control: the version stamp (kubectl apply) ran on a clean pass.
  if ! grep -q apply "$KLOG"; then
    echo "expected the version stamp (kubectl apply) to run on a clean migration" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi
  rm -rf "$tmp"
}

@test "migration 51 is a safe no-op that still stamps when the VM/VL CRDs are absent" {
  tmp=$(mktemp -d)
  export KLOG="$tmp/kubectl.log"
  : > "$KLOG"
  write_fake_kubectl "$tmp"

  if ! KLOG="$KLOG" MOCK_ABSENT=1 PATH="$tmp:$PATH" "$MIGRATION" >"$tmp/out" 2>&1; then
    echo "expected the CRD-absent path to exit zero; output:" >&2
    cat "$tmp/out" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # No CRD -> no PVC is relabeled ...
  if grep -q "label pvc" "$KLOG"; then
    echo "expected no relabel when the VM/VL CRDs are absent" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi

  # ... but the version stamp must still advance so the upgrade completes.
  if ! grep -q apply "$KLOG"; then
    echo "expected the version stamp to run even when the CRDs are absent" >&2
    cat "$KLOG" >&2
    rm -rf "$tmp"
    exit 1
  fi
  rm -rf "$tmp"
}
