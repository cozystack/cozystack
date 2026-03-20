#!/usr/bin/env bats

@test "Create Qdrant" {
  name='test'
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
  sleep 5
  kubectl -n tenant-test wait hr qdrant-$name --timeout=180s --for=condition=ready || {
    echo "=== HelmRelease status ===" >&2
    kubectl -n tenant-test get hr qdrant-$name -o yaml 2>&1 || true
    echo "=== Pods ===" >&2
    kubectl -n tenant-test get pods 2>&1 || true
    echo "=== Events ===" >&2
    kubectl -n tenant-test get events --sort-by='.lastTimestamp' 2>&1 | tail -30 || true
    false
  }
  kubectl -n tenant-test wait hr qdrant-$name-system --timeout=120s --for=condition=ready
  kubectl -n tenant-test wait sts qdrant-$name --timeout=90s --for=jsonpath='{.status.readyReplicas}'=1
  kubectl -n tenant-test wait pvc qdrant-storage-qdrant-$name-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test delete qdrant.apps.cozystack.io $name
}
