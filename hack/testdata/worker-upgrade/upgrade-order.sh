#!/usr/bin/env bash
set -euo pipefail
root=$PWD
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
work=$(mktemp -d)
mkdir -p "$work/packages/core/installer" "$work/_out/cozyreport/snapshots/worker-upgrade/before"
cat > "$work/packages/core/installer/values.yaml" <<'JSON'
{"cozystackOperator":{"image":"registry.example.com/operator@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","platformSourceUrl":"oci://registry.example.com/packages","platformSourceRef":"digest=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","helmReleaseInterval":"","variant":"talos"}}
JSON
printf '%s\n' '{"current-context":"synthetic"}' > "$work/kubeconfig"
printf '%s\n' identity > "$work/_out/cozyreport/snapshots/worker-upgrade/before/identity"
date +%s > "$work/_out/worker-upgrade-before-epoch"
export EVENTS="$work/events" KUBECONFIG="$work/kubeconfig" FAIL_CANDIDATE="${1:-}"
: > "$EVENTS"
helm() {
  case " $* " in
    *' --reset-then-reuse-values '*) ;;
    *) return 1 ;;
  esac
  jq --exit-status '.cozystackOperator | keys == ["image","platformSourceRef","platformSourceUrl"]' _out/worker-upgrade-candidate-values.json >/dev/null || return 1
  echo upgrade >> "$EVENTS"
}
kubectl() {
  case " $* " in *' --context synthetic '*) ;; *) return 1 ;; esac
  case " $* " in *' wait helmreleases '*) return 1 ;; esac
  echo readiness >> "$EVENTS"
}
cozy_worker_upgrade_platform_converged() { echo readiness >> "$EVENTS"; }
cozy_worker_upgrade_candidate_ready() {
  echo candidate-source >> "$EVENTS"
  [ "$FAIL_CANDIDATE" != fail ]
}
cozy_worker_upgrade_controllers_ready() { echo controllers >> "$EVENTS"; }
cozy_worker_upgrade_platform_roots() { echo platform-roots >> "$EVENTS"; }
cozy_worker_upgrade_releases_ready() { echo release-actions >> "$EVENTS"; }
cozy_worker_upgrade_tenant() { echo tenant >> "$EVENTS"; }
cozy_worker_upgrade_snapshot() {
  echo snapshot >> "$EVENTS"
  mkdir -p "$2"
  printf '%s\n' identity > "$2/identity"
}
sleep() { echo interval >> "$EVENTS"; }
cozy_worker_upgrade_default_create() { echo default-create >> "$EVENTS"; }
rc=0
cd "$work"
cozy_worker_upgrade_apply example > "$work/output" 2>&1 || rc=$?
if [ "$FAIL_CANDIDATE" = stages ]; then
  cat "$work/_out/cozyreport/snapshots/worker-upgrade/apply-stage"
else
  cat "$EVENTS"
fi
cd "$root"
exit "$rc"
