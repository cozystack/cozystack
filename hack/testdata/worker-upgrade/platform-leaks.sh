#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/leak-guard.sh
LG_BASELINE_ROOT_UIDS='["platform-uid"]'
lg_pods() {
  printf '%s\n' '{"items":[{"kind":"Pod","metadata":{"uid":"platform-pod","name":"platform","namespace":"cozy-system","ownerReferences":[{"apiVersion":"apps/v1","kind":"Deployment","name":"platform"}]},"status":{"phase":"Running"}},{"kind":"Pod","metadata":{"uid":"tenant-pod","name":"tenant","namespace":"tenant-test","ownerReferences":[{"apiVersion":"apps/v1","kind":"Deployment","name":"tenant"}]},"status":{"phase":"Running"}}]}'
}
kubectl() {
  case " $* " in
    *' cozy-system platform '*) printf '%s\n' '{"kind":"Deployment","metadata":{"uid":"platform-uid","name":"platform","namespace":"cozy-system","creationTimestamp":"2026-01-01T00:00:01Z"}}' ;;
    *' tenant-test tenant '*) printf '%s\n' '{"kind":"Deployment","metadata":{"uid":"tenant-uid","name":"tenant","namespace":"tenant-test","creationTimestamp":"2026-01-01T00:00:01Z"}}' ;;
    *) return 1 ;;
  esac
}
lg_leaks '2026-01-01T00:00:00Z' '[]' | awk '{print $1}'
