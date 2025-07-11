#!/usr/bin/env bats

@test "Create DB ClickHouse" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    resources=$(cat <<EOF
  resources:
    resources:
      cpu: 500m
      memory: 768Mi
EOF
  )
  else
    resources='  resources: {}'
  fi
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
  $resources
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait --timeout=40s hr clickhouse-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s clickhouses $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=120s sts chi-clickhouse-$name-clickhouse-0-0 --for=jsonpath='{.status.replicas}'=1
  timeout 210 sh -ec "until kubectl -n tenant-test wait svc chendpoint-clickhouse-$name --for=jsonpath='{.spec.ports[0].port}'=8123; do sleep 10; done"
  kubectl -n tenant-test delete clickhouse.apps.cozystack.io $name
}
