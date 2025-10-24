#!/usr/bin/env bats

@test "Create DB FerretDB" {
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: FerretDB
metadata:
  name: $name
  namespace: tenant-test
spec:
  backup:
    destinationPath: "s3://bucket/path/to/folder/"
    enabled: false
    endpointURL: "http://minio-gateway-service:9000"
    retentionPolicy: "30d"
    s3AccessKey: "<your-access-key>"
    s3SecretKey: "<your-secret-key>"
    schedule: "0 2 * * * *"
  bootstrap:
    enabled: false
  external: false
  quorum:
    maxSyncReplicas: 0
    minSyncReplicas: 0
  replicas: 2
  resources: {}
  resourcesPreset: "micro"
  size: "10Gi"
  users:
    testuser:
      password: xai7Wepo
EOF
  sleep 5
  kubectl -n tenant-test wait hr ferretdb-$name --timeout=100s --for=condition=ready
  timeout 40 sh -ec "until kubectl -n tenant-test get svc ferretdb-$name-postgres-r -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc ferretdb-$name-postgres-ro -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc ferretdb-$name-postgres-rw -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints ferretdb-$name-postgres-r -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  # for some reason it takes longer for the read-only endpoint to be ready
  #timeout 120 sh -ec "until kubectl -n tenant-test get endpoints ferretdb-$name-postgres-ro -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints ferretdb-$name-postgres-rw -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  kubectl -n tenant-test delete ferretdb.apps.cozystack.io $name
}
