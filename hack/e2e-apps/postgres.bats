#!/usr/bin/env bats

@test "Create DB PostgreSQL" {
  name='test'
  kubectl -n tenant-test delete postgreses.apps.cozystack.io $name --ignore-not-found
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 10Gi
  replicas: 2
  storageClass: ""
  postgresql:
    parameters:
      max_connections: 100
  quorum:
    minSyncReplicas: 0
    maxSyncReplicas: 0
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
    s3Region: us-east-1
    s3Bucket: s3.example.org/postgres-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3 --keep-daily=3 --keep-within-weekly=1m"
    s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
    s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
    resticPassword: ChaXoveekoh6eigh4siesheeda2quai0
  resources: {}
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait hr postgres-$name --timeout=100s --for=condition=ready
  kubectl -n tenant-test wait job.batch postgres-$name-init-job --timeout=50s --for=condition=Complete
  timeout 40 sh -ec "until kubectl -n tenant-test get svc postgres-$name-r -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc postgres-$name-ro -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc postgres-$name-rw -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-$name-r -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  # for some reason it takes longer for the read-only endpoint to be ready
  #timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-$name-ro -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-$name-rw -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  kubectl -n tenant-test delete postgreses.apps.cozystack.io $name
  kubectl -n tenant-test delete job.batch/postgres-$name-init-job
}
