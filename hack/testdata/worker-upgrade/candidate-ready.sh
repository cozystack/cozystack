#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
work=$1 mode=${2:-ready}
SOURCE_REVISION=sha256:abc bash hack/testdata/worker-upgrade/app-artifacts.sh "${3:-current}" emit > "$work/artifacts.json"
mkdir "$work/_out"
mkdir "$work/_out/cozyreport"
mkdir "$work/_out/cozyreport/snapshots"
mkdir "$work/_out/cozyreport/snapshots/worker-upgrade"
printf '0\n' > "$work/clock"
export EVENTS="$work/events" CLOCK="$work/clock"
: > "$EVENTS"
date() {
  local tick
  tick=$(cat "$CLOCK")
  printf '%s\n' "$((tick + 1000))" > "$CLOCK"
  printf '%s\n' "$tick"
}
sleep() { :; }
kubectl() {
  local namespace='' arg
  local args=()
  while [ "$#" -gt 0 ]; do
    arg=$1; shift
    case "$arg" in
      --namespace) namespace=$1; shift ;;
      --context|--output) shift ;;
      --request-timeout=*|--timeout=*|--ignore-not-found) ;;
      *) args+=("$arg") ;;
    esac
  done
  case "${args[*]}" in
    'get ocirepository '*)
      printf '%s\n' '{"metadata":{"generation":1},"status":{"observedGeneration":1,"artifact":{"revision":"sha256:abc"},"conditions":[{"type":"Ready","status":"True"}]}}'
      ;;
    'get ocirepositories,artifactgenerators,externalartifacts --all-namespaces')
      echo app-artifacts >> "$EVENTS"
      cat "$work/artifacts.json"
      ;;
    'get deployments'*)
      local worker='{"metadata":{"name":"kubernetes-worker-classes","namespace":"cozy-system","uid":"worker-root"}}'
      local provider='{"metadata":{"name":"capi-kairos-bootstrap-controller-manager","namespace":"cozy-cluster-api","uid":"provider-root"}}'
      if [ "$mode" = wrong-namespace ]; then provider=${provider/cozy-cluster-api/cozy-system}; fi
      if [ "$namespace" = cozy-system ]; then
        if [ "${args[2]:-}" = kubernetes-worker-classes ]; then printf '%s\n' "$worker"
        elif [ "${args[2]:-}" = capi-kairos-bootstrap-controller-manager ]; then return 0
        else printf '{"items":[%s]}\n' "$worker"; fi
      elif [ "$namespace" = cozy-cluster-api ] && [ "$mode" != absent ] && [ "$mode" != wrong-namespace ]; then
        if [ "${args[2]:-}" = capi-kairos-bootstrap-controller-manager ]; then printf '%s\n' "$provider"
        else printf '{"items":[%s]}\n' "$provider"; fi
      fi
      ;;
    'rollout status deployment/'*)
      printf '%s %s\n' "$namespace" "${args[2]}" >> "$EVENTS"
      if [ "$mode" = not-ready ] && [ "$namespace" = cozy-cluster-api ]; then return 1; fi
      ;;
    *) echo "unexpected kubectl ${args[*]}" >&2; return 1 ;;
  esac
}
cd "$work"
if cozy_worker_upgrade_candidate_ready synthetic sha256:abc; then
  [ "$mode" = ready ] || { echo "accepted missing/not-ready provider" >&2; exit 1; }
  [ ! -e _out/cozyreport/snapshots/worker-upgrade/platform-roots.json ]
  grep --quiet '^cozy-cluster-api deployment/capi-kairos-bootstrap-controller-manager$' "$EVENTS"
else
  exit 1
fi
