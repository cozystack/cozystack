#!/usr/bin/env bats

@test "Create DB MySQL" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    resources=$(cat <<EOF
  resources:
    resources:
      cpu: 3000m
      memory: 3Gi
EOF
  )
  else
    resources='  resources: {}'
  fi
  kubectl apply -f- <<EOF
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
  kubectl -n tenant-test wait --timeout=30s hr mysql-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s mysqls.apps.cozystack.io $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=110s sts mysql-$name --for=jsonpath='{.status.replicas}'=2
  sleep 60
  kubectl -n tenant-test wait --timeout=60s deploy mysql-$name-metrics --for=jsonpath='{.status.replicas}'=1
  kubectl -n tenant-test wait --timeout=100s svc mysql-$name --for=jsonpath='{.spec.ports[0].port}'=3306
  kubectl -n tenant-test delete mysqls.apps.cozystack.io $name
}
