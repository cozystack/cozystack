#!/usr/bin/env bats

# The etcd chart pins the Helm release name to 'etcd' via
# packages/extra/etcd/templates/check-release-name.yaml, so every test
# must apply its Etcd with metadata.name: etcd. setup() clears any
# prior run so tests remain independent despite the singleton name.

setup() {
  kubectl -n tenant-test delete etcd.apps.cozystack.io --all --ignore-not-found --timeout=2m || true
  # HelmRelease teardown is async relative to the CR deletion above; wait for
  # downstream resources so the next test starts from a clean state.
  kubectl -n tenant-test wait hr/etcd --for=delete --timeout=2m --ignore-not-found || true
  kubectl -n tenant-test wait secret/etcd-s3-creds --for=delete --timeout=1m --ignore-not-found || true
  kubectl -n tenant-test wait etcdbackupschedule.etcd.aenix.io/etcd --for=delete --timeout=1m --ignore-not-found || true
}

dump_diagnostics() {
  echo "# --- diagnostics ---" >&3
  kubectl -n tenant-test get etcdcluster,etcdbackupschedule,cronjob -o wide 2>&1 >&3 || true
  kubectl -n tenant-test describe etcdbackupschedule etcd 2>&1 >&3 || true
  kubectl -n cozy-etcd-operator logs -l app.kubernetes.io/name=etcd-operator --tail=100 2>&1 >&3 || true
}

@test "Create Etcd" {
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: etcd
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
  kubectl -n tenant-test wait hr etcd --timeout=60s --for=condition=ready || { dump_diagnostics; false; }
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=180s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True || { dump_diagnostics; false; }
}

@test "Create Etcd with empty backup block (disabled by default)" {
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: etcd
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
  kubectl -n tenant-test wait hr etcd --timeout=60s --for=condition=ready || { dump_diagnostics; false; }
  # With backup disabled, neither the schedule nor the secret should be created.
  run kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd
  [ "$status" -ne 0 ]
  run kubectl -n tenant-test get secret etcd-s3-creds
  [ "$status" -ne 0 ]
}

@test "Create Etcd with backup schedule" {
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: etcd
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
    # This test verifies that the chart renders EtcdBackupSchedule/Secret and
    # that the etcd-operator materializes a CronJob from the schedule — it does
    # NOT verify that backups reach S3. The endpoint below intentionally
    # resolves nowhere to keep the test self-contained.
    destinationPath: "s3://test-bucket/etcd-backups/"
    endpointURL: "http://no-such-endpoint.invalid:9000"
    region: "us-east-1"
    forcePathStyle: true
    s3AccessKey: "e2e-access-key"
    s3SecretKey: "e2e-secret-key"
    successfulJobsHistoryLimit: 1
    failedJobsHistoryLimit: 1
EOF
  sleep 5
  kubectl -n tenant-test wait hr etcd --timeout=60s --for=condition=ready || { dump_diagnostics; false; }
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=180s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True || { dump_diagnostics; false; }
  kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd || { dump_diagnostics; false; }
  # Verify the region field propagated to the EtcdBackupSchedule.
  REGION=$(kubectl -n tenant-test get etcdbackupschedule.etcd.aenix.io etcd -o jsonpath='{.spec.destination.s3.region}')
  [ "$REGION" = "us-east-1" ]
  kubectl -n tenant-test get secret etcd-s3-creds -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d | grep -q '^e2e-access-key$'
  # The etcd-operator generates a CronJob from the EtcdBackupSchedule. Wait for it.
  timeout 120 sh -ec "until [ \"\$(kubectl -n tenant-test get cronjob -l etcd.aenix.io/etcdbackupschedule-name=etcd -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)\" != '' ]; do sleep 5; done" || { dump_diagnostics; false; }
}
