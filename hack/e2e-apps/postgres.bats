#!/usr/bin/env bats

@test "Create DB PostgreSQL" {
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
  $resources
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait --timeout=200s hr postgres-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s postgreses $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=50s job.batch postgres-$name-init-job --for=condition=Complete
  kubectl -n tenant-test wait --timeout=40s svc postgres-$name-r --for=jsonpath='{.spec.ports[0].port}'=5432
  kubectl -n tenant-test delete postgreses.apps.cozystack.io $name
  kubectl -n tenant-test delete job.batch/postgres-$name-init-job
}
