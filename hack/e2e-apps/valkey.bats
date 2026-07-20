#!/usr/bin/env bats

@test "Create Valkey" {
  name='test'
  kubectl -n tenant-test delete valkey.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Valkey
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
  timeout 60 sh -ec "until kubectl -n tenant-test get hr valkey-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr valkey-$name --timeout=5m --for=condition=ready
  timeout 60 sh -ec "until kubectl -n tenant-test get pvc redisfailover-persistent-data-rfr-valkey-$name-0 >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait pvc redisfailover-persistent-data-rfr-valkey-$name-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  timeout 60 sh -ec "until kubectl -n tenant-test get deploy rfs-valkey-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait deploy rfs-valkey-$name --timeout=90s --for=condition=available
  timeout 60 sh -ec "until kubectl -n tenant-test get sts rfr-valkey-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait sts rfr-valkey-$name --timeout=90s --for=jsonpath='{.status.replicas}'=2
  kubectl -n tenant-test delete valkey.apps.cozystack.io $name
}
