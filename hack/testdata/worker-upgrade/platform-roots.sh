#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
work=$(mktemp -d) mode=${1:-ready}
mkdir -p "$work/_out/cozyreport/snapshots/worker-upgrade"
export MODE=$mode
kubectl() {
  case " $* " in *' --context synthetic get deployments,statefulsets,daemonsets --all-namespaces '*) ;; *) return 1 ;; esac
  local items='{"kind":"Deployment","metadata":{"name":"cozystack-api","namespace":"cozy-system","uid":"old-root"}}'
  items+=',{"kind":"Deployment","metadata":{"name":"tenant","namespace":"tenant-test","uid":"tenant-root"}}'
  if [ "$MODE" = already-installed ] || [ "$(cat _out/phase)" = after ]; then
    items+=',{"kind":"Deployment","metadata":{"name":"kubernetes-worker-classes","namespace":"cozy-system","uid":"worker-root"}}'
    [ "$MODE" = missing-provider ] || items+=',{"kind":"Deployment","metadata":{"name":"capi-kairos-bootstrap-controller-manager","namespace":"cozy-cluster-api","uid":"provider-root"}}'
  fi
  if [ "$(cat _out/phase)" = after ]; then
    items+=',{"kind":"StatefulSet","metadata":{"name":"unrelated","namespace":"cozy-other","uid":"new-root"}}'
  fi
  printf '{"items":[%s]}\n' "$items"
}
cd "$work"
echo before > _out/phase
cozy_worker_upgrade_platform_before synthetic
echo after > _out/phase
rc=0
cozy_worker_upgrade_platform_roots synthetic || rc=$?
[ "$rc" -ne 0 ] || jq --raw-output 'map(.uid) | sort | join(" ")' _out/cozyreport/snapshots/worker-upgrade/platform-roots.json
cd /
rm -rf "$work"
exit "$rc"
