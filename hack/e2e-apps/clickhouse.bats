#!/usr/bin/env bats

@test "Create DB ClickHouse" {
  name='test'
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 10Gi
  logStorageSize: 2Gi
  shards: 1
  replicas: 2
  storageClass: ""
  logTTL: 15
  users:
    testuser:
      password: xai7Wepo
  backup:
    enabled: false
    s3Region: us-east-1
    s3Bucket: s3.example.org/clickhouse-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3 --keep-daily=3 --keep-within-weekly=1m"
    s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
    s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
    resticPassword: ChaXoveekoh6eigh4siesheeda2quai0
  clickhouseKeeper:
    enabled: true
    resourcesPreset: "micro"
    size: "1Gi"
  resources: {}
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait hr clickhouse-$name --timeout=20s --for=condition=ready
  timeout 180 sh -ec "until kubectl -n tenant-test get svc chendpoint-clickhouse-$name -o jsonpath='{.spec.ports[*].port}' | grep -q '8123 9000'; do sleep 10; done"
  kubectl -n tenant-test wait statefulset.apps/chi-clickhouse-$name-clickhouse-0-0 --timeout=120s --for=jsonpath='{.status.replicas}'=1
  timeout 80 sh -ec "until kubectl -n tenant-test get endpoints chi-clickhouse-$name-clickhouse-0-0 -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  timeout 100 sh -ec "until kubectl -n tenant-test get svc chi-clickhouse-$name-clickhouse-0-0 -o jsonpath='{.spec.ports[*].port}' | grep -q '9000 8123 9009'; do sleep 10; done"
  timeout 80 sh -ec "until kubectl -n tenant-test get sts chi-clickhouse-$name-clickhouse-0-1 ; do sleep 10; done"
  kubectl -n tenant-test wait statefulset.apps/chi-clickhouse-$name-clickhouse-0-1 --timeout=140s --for=jsonpath='{.status.replicas}'=1
  kubectl -n tenant-test delete clickhouse $name
}
