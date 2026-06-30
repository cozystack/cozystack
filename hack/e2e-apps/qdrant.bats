#!/usr/bin/env bats

@test "Create Qdrant" {
  name='test'
  kubectl -n tenant-test delete qdrant.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Qdrant
metadata:
  name: $name
  namespace: tenant-test
spec:
  replicas: 1
  size: 10Gi
  storageClass: ""
  resourcesPreset: "nano"
  resources: {}
  external: false
EOF
  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr qdrant-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr qdrant-$name --timeout=5m --for=condition=ready
  kubectl -n tenant-test wait hr qdrant-$name-system --timeout=5m --for=condition=ready
  timeout 60 sh -ec "until kubectl -n tenant-test get sts qdrant-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait sts qdrant-$name --timeout=90s --for=jsonpath='{.status.readyReplicas}'=1
  timeout 60 sh -ec "until kubectl -n tenant-test get pvc qdrant-storage-qdrant-$name-0 >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait pvc qdrant-storage-qdrant-$name-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test delete qdrant.apps.cozystack.io $name
  # Issue #3059: Kubernetes never garbage-collects StatefulSet-templated PVCs,
  # so the post-delete cleanup hook must reclaim the data PVC. The hook Job runs
  # asynchronously during helm uninstall — wait for the PVC to actually be gone.
  # Gate on a NotFound result specifically: a transient API/auth error returns
  # non-zero too, and must not be misread as "PVC reclaimed".
  pvc="qdrant-storage-qdrant-$name-0"
  deleted=false
  for _ in $(seq 1 60); do
    if err=$(kubectl -n tenant-test get pvc "$pvc" 2>&1 >/dev/null); then
      sleep 2; continue   # PVC still exists
    fi
    case "$err" in
      *NotFound*) deleted=true; break ;;
      *) echo "transient error querying $pvc, retrying: $err" >&2; sleep 2 ;;
    esac
  done
  [ "$deleted" = true ] \
    || { echo "❌ data PVC $pvc not reclaimed after Qdrant delete (issue #3059 regression)"; kubectl -n tenant-test get pvc -o wide; kubectl -n tenant-test get jobs,pods -l app.kubernetes.io/instance=qdrant-$name -o wide; false; }
}
