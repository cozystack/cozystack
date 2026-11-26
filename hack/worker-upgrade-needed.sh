#!/usr/bin/env bash
set -euo pipefail
# Usage: worker-upgrade-needed.sh [--kairos] <changed-path file>
# Without --kairos: "required" when the upgrade scenario must run. With it:
# "required" when the Kairos operation of that scenario gates the lane.
gate=upgrade
if [ "${1:-}" = --kairos ]; then
  gate=kairos
  shift
fi
[ "$#" -eq 1 ] && [ -f "$1" ] || { echo "worker upgrade selection requires a changed-path file" >&2; exit 1; }
# Every template of the pool chart is evaluated for a Kairos pool, and the
# Kairos helpers call shared ones, so the whole chart outside its tests counts;
# so does the admission and class code every Kairos pool passes through, and
# so do the parent chart that owns the join path, the ApplicationDefinitions
# that give both charts their schemas and defaults, the platform sources that
# install those charts, the Chainsaw step that runs the pool, the shared library
# whose teardown releases it, and the upgrade code that bounds it.
kairos='^(api/workers/|internal/kubernetesworker/|cmd/kubernetes-worker-controller/|packages/system/kubernetes-worker-classes|packages/system/capi-providers-bootstrap/(templates/|Makefile$|Chart\.yaml$|\.helmignore$|values\.yaml$|files/[^/]*kairos|images/cluster-api-provider-kairos|patches/kairos/)|packages/core/platform/sources/(capi-provider-bootstrap-|kubernetes-)|packages/apps/kubernetes(-nodes)?/|packages/system/kubernetes(-nodes)?-rd/|hack/e2e-chainsaw/_lib/run-kubernetes(-kairos|-upgrade)?\.sh|hack/e2e-chainsaw/kubernetes-latest/chainsaw-test\.yaml)'
pattern='^(api/workers/|internal/kubernetesworker/|cmd/(kubernetes-worker-controller|workerclass-restore)/|packages/apps/kubernetes/|packages/apps/kubernetes-nodes/|packages/system/kubernetes-worker-classes|packages/system/capi-providers-bootstrap/|packages/core/installer/|packages/core/testing/Makefile|packages/core/platform/sources/(kubernetes-|capi-provider-bootstrap-)|hack/(worker-upgrade|testdata/worker-upgrade|e2e-chainsaw-run\.sh|e2e-install-cozystack\.bats|e2e-talos-image-cache\.yaml|e2e-chainsaw/(kubernetes-(latest|previous)/|\.chainsaw\.yaml)|e2e-chainsaw/_lib/(run-kubernetes(-upgrade)?|talos-image-cache)\.sh)|\.github/workflows/pull-requests\.yaml)'
while IFS= read -r path || [ -n "$path" ]; do
  case "$path" in *.md|*.MD) continue ;; esac
  case "$path" in packages/apps/kubernetes/tests/*|packages/apps/kubernetes-nodes/tests/*|packages/core/platform/sources/kubernetes-worker-image.yaml) [ "$gate" = kairos ] && continue ;; esac
  # For the Kairos gate a path outside both lists is unclassified and counts as
  # Kairos-relevant: only the upgrade inputs named above narrow the pool out.
  if [[ "$path" =~ $kairos ]] || { [ "$gate" = upgrade ] && [[ "$path" =~ $pattern ]]; } || { [ "$gate" = kairos ] && ! [[ "$path" =~ $pattern ]]; }; then
    echo required
    exit 0
  fi
done < "$1"
echo off
