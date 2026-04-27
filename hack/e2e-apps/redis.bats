#!/usr/bin/env bats

@test "Create Redis" {
  name='test'
  kubectl -n tenant-test delete redis.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 1Gi
  replicas: 2
  storageClass: ""
  authEnabled: true
  resources: {}
  resourcesPreset: "nano"
EOF
  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr redis-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr redis-$name --timeout=20s --for=condition=ready
  kubectl -n tenant-test wait pvc redisfailover-persistent-data-rfr-redis-$name-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait deploy rfs-redis-$name --timeout=90s --for=condition=available
  kubectl -n tenant-test wait sts rfr-redis-$name --timeout=90s --for=jsonpath='{.status.replicas}'=2
  kubectl -n tenant-test delete redis.apps.cozystack.io $name
}
