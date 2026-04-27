#!/usr/bin/env bats

@test "Create DB MongoDB" {
  name='test'
  kubectl -n tenant-test delete mongodbs.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: MongoDB
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 10Gi
  replicas: 1
  storageClass: ""
  resourcesPreset: "small"
  users:
    testuser:
      password: xai7Wepo
  databases:
    testdb:
      roles:
        admin:
        - testuser
  backup:
    enabled: false
EOF
  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr mongodb-$name >/dev/null 2>&1; do sleep 2; done"
  # Wait for HelmRelease
  kubectl -n tenant-test wait hr mongodb-$name --timeout=60s --for=condition=ready
  # Wait for MongoDB service (port 27017)
  timeout 120 sh -ec "until kubectl -n tenant-test get svc mongodb-$name-rs0 -o jsonpath='{.spec.ports[0].port}' | grep -q '27017'; do sleep 10; done"
  # Wait for endpoints
  timeout 180 sh -ec "until kubectl -n tenant-test get endpoints mongodb-$name-rs0 -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  # Wait for StatefulSet replicas
  kubectl -n tenant-test wait statefulset.apps/mongodb-$name-rs0 --timeout=300s --for=jsonpath='{.status.replicas}'=1
  # Cleanup
  kubectl -n tenant-test delete mongodbs.apps.cozystack.io $name
}
