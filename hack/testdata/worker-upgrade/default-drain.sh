#!/usr/bin/env bash
set -euo pipefail
root=$PWD
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
work=$(mktemp -d)
mkdir -p "$work/_out/cozyreport/snapshots/worker-upgrade"
export COZY_UPGRADE_CONTEXT=synthetic COZY_UPGRADE_TENANT_CONTEXT=synthetic KAIROS_KC=synthetic
export SCALED="$work/scaled" DATE_MARKER="$work/clock" BUSY="${1:-}"
date() {
  if [ "$BUSY" = late ]; then
    now=$(cat "$DATE_MARKER" 2>/dev/null || echo 0)
    echo $((now + 5)) > "$DATE_MARKER"
    echo "$now"
  elif [ ! -f "$DATE_MARKER" ]; then
    : > "$DATE_MARKER"
    echo 0
  else
    echo 1000
  fi
}
cozy_wait_helmrelease_upgrade() {
  if [ "$BUSY" = late ]; then echo 290 > "$DATE_MARKER"; fi
}
sleep() { :; }
kubectl() {
  case " $* " in
    *' get helmrelease '*) echo 1 ;;
    *' delete pod default-class-probe '*) : ;;
    *' patch kubernetesnodeses.apps.cozystack.io example-default-create '*) : ;;
    *' scale machinedeployments.v1beta1.cluster.x-k8s.io kubernetes-example-default-create --replicas=0 '*)
      echo scale >> "${GUARD_LOG:-/dev/null}"
      : > "$SCALED" ;;
    *' get virtualmachineinstances '*)
      if [ -f "$SCALED" ]; then
        echo '{"items":[]}'
      else
        echo '{"items":[{"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"new-disk"}}]}}]}'
      fi ;;
    *' get pods '*)
      polls=$(( $(cat "$work/polls" 2>/dev/null || echo 0) + 1 )); echo "$polls" > "$work/polls"
      if [ "$BUSY" = busy ] || { [ "$BUSY" = late ] && [ "$polls" -lt 4 ]; }; then
        echo '{"items":[{"metadata":{"name":"launcher"},"status":{"phase":"Running"}}]}'
      else
        echo '{"items":[]}'
      fi ;;
    *' get machines.v1beta1.cluster.x-k8s.io '*|*' get virtualmachines '*|*' get persistentvolumeclaims '*) echo '{"items":[]}' ;;
    *) echo "unexpected drain request: $*" >&2; return 1 ;;
  esac
}
rc=0
cd "$work"
cozy_worker_upgrade_default_drain example || rc=$?
if [ "$rc" -eq 0 ]; then
  [ -f _out/cozyreport/snapshots/worker-upgrade/default-drained ] || exit 2
fi
cd "$root"
exit "$rc"
