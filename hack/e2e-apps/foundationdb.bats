#!/usr/bin/env bats

@test "Create DB FoundationDB" {
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: FoundationDB
metadata:
  name: $name
  namespace: tenant-test
spec:
  replicas: 3
  cluster:
    version: "7.4.1"
    processCounts:
      storage: 3
      stateless: -1
      cluster_controller: 1
    faultDomain:
      key: "foundationdb.org/none"
      valueFrom: "\$FDB_ZONE_ID"
  storage:
    size: "8Gi"
    storageClass: ""
  resources:
    preset: "nano"
  backup:
    enabled: false
    s3:
      bucket: "s3.example.org/fdb-backups"
      endpoint: ""
      region: "us-east-1"
      credentials:
        accessKeyId: "oobaiRus9pah8PhohL1ThaeTa4UVa7gu"
        secretAccessKey: "ju3eum4dekeich9ahM1te8waeGai0oog"
    retentionPolicy: "7d"
  monitoring:
    enabled: true
  advanced:
    customParameters:
      - "knob_disable_posix_kernel_aio=1"
    imageType: "split"
    automaticReplacements: true
EOF
  sleep 10
  
  # Wait for HelmRelease to be ready
  kubectl -n tenant-test wait hr foundationdb-\$name --timeout=180s --for=condition=ready
  
  # Wait for FoundationDBCluster to be created
  timeout 120 sh -ec "until kubectl -n tenant-test get foundationdbclusters.apps.foundationdb.org \$name; do sleep 10; done"
  
  # Wait for cluster to become available (this may take some time)
  timeout 300 sh -ec "until kubectl -n tenant-test get foundationdbclusters.apps.foundationdb.org \$name -o jsonpath='{.status.databaseConfiguration.usable_regions}' | grep -q '1'; do sleep 15; done"
  
  # Check that storage processes are running
  timeout 180 sh -ec "until [ \$(kubectl -n tenant-test get pods -l app=\$name,foundationdb.org/fdb-process-class=storage --field-selector=status.phase=Running --no-headers | wc -l) -eq 3 ]; do sleep 10; done"
  
  # Check that stateless processes are running
  timeout 180 sh -ec "until [ \$(kubectl -n tenant-test get pods -l app=\$name,foundationdb.org/fdb-process-class=stateless --field-selector=status.phase=Running --no-headers | wc -l) -ge 1 ]; do sleep 10; done"
  
  # Check that cluster controller is running
  timeout 180 sh -ec "until [ \$(kubectl -n tenant-test get pods -l app=\$name,foundationdb.org/fdb-process-class=cluster_controller --field-selector=status.phase=Running --no-headers | wc -l) -eq 1 ]; do sleep 10; done"
  
  # Check WorkloadMonitor is created and configured
  kubectl -n tenant-test get workloadmonitor \$name
  timeout 60 sh -ec "until kubectl -n tenant-test get workloadmonitor \$name -o jsonpath='{.spec.replicas}' | grep -q '3'; do sleep 5; done"
  
  # Check dashboard resource map is created
  kubectl -n tenant-test get configmap \$name-resourcemap
  
  # Verify cluster is healthy (check cluster status)
  timeout 120 sh -ec "until kubectl -n tenant-test get foundationdbclusters.apps.foundationdb.org \$name -o jsonpath='{.status.health.available}' | grep -q 'true'; do sleep 10; done"
  
  # Clean up
  kubectl -n tenant-test delete foundationdb \$name
  
  # Wait for cleanup to complete
  timeout 60 sh -ec "while kubectl -n tenant-test get foundationdbclusters.apps.foundationdb.org \$name 2>/dev/null; do sleep 5; done"
}