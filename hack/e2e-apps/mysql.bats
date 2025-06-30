#!/usr/bin/env bats

@test "Create DB MySQL" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    resources=$(cat <<EOF
  resources:
    requests:
      cpu: 3000m
      memory: 3Gi
    limits:
      cpu: "4000m"
      memory: "4Gi"
EOF
  )
  else
    resources='  resources: {}'
  fi
  kubectl -n tenant-test get mysqls.apps.cozystack.io $name || 
  kubectl create -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: MySQL
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 10Gi
  replicas: 2
  storageClass: ""
  users:
    testuser:
      maxUserConnections: 1000
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
  $resources
  resourcesPreset: "nano"
EOF
  sleep 10
  kubectl -n tenant-test wait hr mysql-$name --timeout=30s --for=condition=ready
  timeout 80 sh -ec "until kubectl -n tenant-test get svc mysql-$name -o jsonpath='{.spec.ports[0].port}' | grep -q '3306'; do sleep 10; done"
  kubectl -n tenant-test wait statefulset.apps/mysql-$name --timeout=110s --for=jsonpath='{.status.replicas}'=2
  timeout 80 sh -ec "until kubectl -n tenant-test get svc mysql-$name -o jsonpath='{.spec.ports[0].port}' | grep -q '3306'; do sleep 10; done"
  kubectl -n tenant-test wait deployment.apps/mysql-$name-metrics --timeout=300s --for=jsonpath='{.status.replicas}'=1
  kubectl -n tenant-test delete mysqls.apps.cozystack.io $name
}
