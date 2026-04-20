#!/usr/bin/env bats

setup() {
  kubectl -n tenant-test delete etcd.apps.cozystack.io --all --ignore-not-found --timeout=2m || true
}

dump_diagnostics() {
  echo "# --- diagnostics ---" >&3
  kubectl -n tenant-test get etcdcluster,etcdbackupschedule,cronjob -o wide 2>&1 >&3 || true
  kubectl -n tenant-test describe etcdbackupschedule etcd 2>&1 >&3 || true
  kubectl -n cozy-etcd-operator logs -l app.kubernetes.io/name=etcd-operator --tail=100 2>&1 >&3 || true
}

@test "Create Etcd" {
  name='test'
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
EOF
  sleep 5
  kubectl -n tenant-test wait hr etcd-$name --timeout=60s --for=condition=ready || { dump_diagnostics; false; }
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=180s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True || { dump_diagnostics; false; }
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name
}

@test "Create Etcd with empty backup block (disabled by default)" {
  name='test-backup-default'
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
  backup: {}
EOF
  sleep 5
  kubectl -n tenant-test wait hr etcd-$name --timeout=60s --for=condition=ready || { dump_diagnostics; false; }
  # With backup disabled, neither the schedule nor the secret should be created.
  run kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd
  [ "$status" -ne 0 ]
  run kubectl -n tenant-test get secret etcd-s3-creds
  [ "$status" -ne 0 ]
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name
}

@test "Create Etcd with backup schedule" {
  name='test-backup'
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
  backup:
    enabled: true
    schedule: "*/1 * * * *"
    destinationPath: "s3://test-bucket/etcd-backups/"
    endpointURL: "http://minio-e2e.tenant-test.svc:9000"
    forcePathStyle: true
    s3AccessKey: "e2e-access-key"
    s3SecretKey: "e2e-secret-key"
    successfulJobsHistoryLimit: 1
    failedJobsHistoryLimit: 1
EOF
  sleep 5
  kubectl -n tenant-test wait hr etcd-$name --timeout=60s --for=condition=ready || { dump_diagnostics; false; }
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=180s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True || { dump_diagnostics; false; }
  kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd || { dump_diagnostics; false; }
  kubectl -n tenant-test get secret etcd-s3-creds -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d | grep -q '^e2e-access-key$'
  # The etcd-operator generates a CronJob from the EtcdBackupSchedule. Wait for it.
  timeout 120 sh -ec "until [ \"\$(kubectl -n tenant-test get cronjob -l etcd.aenix.io/etcdbackupschedule-name=etcd -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)\" != '' ]; do sleep 5; done" || { dump_diagnostics; false; }
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name
}
