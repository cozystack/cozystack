#!/usr/bin/env bats

# E2E for the Etcd backup-strategy driver. Three tests prove the
# happy path end-to-end against a real cluster:
#
#   1. BackupJob captures a snapshot to S3 and surfaces a Backup
#      artefact with the destination coordinates in driverMetadata.
#   2. In-place RestoreJob suspends the HelmRelease, recreates the
#      EtcdCluster with bootstrap.restore, and the sentinel written
#      pre-backup is restored after a post-backup mutation.
#   3. A RestoreJob with a non-empty TargetApplicationRef is rejected
#      with phase=Failed (the chart pins release.name=etcd so to-copy
#      in the same namespace is unsupported).
#
# Edge cases (operator restart mid-restore, malformed snapshot, missing
# S3 creds Secret) belong in the Go unit tests at
# internal/backupcontroller/etcdstrategy_controller_test.go.

# Variables shared across tests.
TEST_NAMESPACE='tenant-test'
TEST_ETCD_NAME='etcd'
TEST_BUCKET_NAME='etcd-backup-bucket'
TEST_BACKUPCLASS_NAME='etcd-backup-default'
TEST_STRATEGY_NAME='etcd-strategy-default'
TEST_BACKUPJOB_NAME='etcd-backup-job'
TEST_RESTOREJOB_INPLACE='etcd-restore-inplace'
TEST_RESTOREJOB_TOCOPY='etcd-restore-to-copy'
TEST_SENTINEL_KEY='__cozystack_e2e_sentinel'
TEST_SENTINEL_VAL='pristine-value'

# setup_file runs once before all tests. We delete prior state so the
# tests are independent of each other and of any partial demo leftover.
setup_file() {
  kubectl -n "$TEST_NAMESPACE" delete restorejob.backups.cozystack.io --all --ignore-not-found --timeout=60s
  kubectl -n "$TEST_NAMESPACE" delete backupjob.backups.cozystack.io --all --ignore-not-found --timeout=60s
  kubectl -n "$TEST_NAMESPACE" delete backup.backups.cozystack.io --all --ignore-not-found --timeout=60s
  kubectl -n "$TEST_NAMESPACE" delete etcd.apps.cozystack.io --all --ignore-not-found --timeout=2m
  kubectl -n "$TEST_NAMESPACE" wait hr/${TEST_ETCD_NAME} --for=delete --timeout=2m --ignore-not-found
  kubectl -n "$TEST_NAMESPACE" delete bucket.apps.cozystack.io --all --ignore-not-found --timeout=60s
  kubectl delete backupclass.backups.cozystack.io "$TEST_BACKUPCLASS_NAME" --ignore-not-found
  kubectl delete etcd.strategy.backups.cozystack.io "$TEST_STRATEGY_NAME" --ignore-not-found
}

teardown_file() {
  setup_file
}

print_log() {
  echo "# $1" >&3
}

dump_diagnostics() {
  echo "# --- diagnostics ---" >&3
  kubectl -n "$TEST_NAMESPACE" get etcdcluster,etcdbackup,pvc,backupjobs,restorejobs,backups -o wide >&3 2>&1 || true
  kubectl -n "$TEST_NAMESPACE" describe backupjob "$TEST_BACKUPJOB_NAME" >&3 2>&1 || true
  kubectl -n "$TEST_NAMESPACE" describe restorejob "$TEST_RESTOREJOB_INPLACE" >&3 2>&1 || true
  kubectl -n cozy-backupstrategy-controller logs -l app.kubernetes.io/name=backupstrategy-controller --tail=200 >&3 2>&1 || true
}

# Shared helper to run etcdctl inside an etcd member pod. The chart
# mounts the client TLS material under /etc/etcd/pki/client so etcdctl
# can authenticate against the cluster's auto-signed CA.
etcdctl_exec() {
  kubectl -n "$TEST_NAMESPACE" exec etcd-0 -- env \
    ETCDCTL_API=3 \
    ETCDCTL_CACERT=/etc/etcd/pki/client/ca.crt \
    ETCDCTL_CERT=/etc/etcd/pki/client/tls.crt \
    ETCDCTL_KEY=/etc/etcd/pki/client/tls.key \
    ETCDCTL_ENDPOINTS=https://127.0.0.1:2379 \
    etcdctl "$@"
}

@test "BackupJob captures a snapshot of the source Etcd and creates a Ready Backup" {
  # --- strategy + bucket + backup-class ---
  print_log "Apply Etcd strategy"
  kubectl apply -f - <<EOF
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: ${TEST_STRATEGY_NAME}
spec:
  template:
    destination:
      s3:
        bucket: "{{ .Parameters.bucket }}"
        endpoint: "{{ .Parameters.endpoint }}"
        key: "{{ .Application.metadata.name }}/"
        region: "{{ .Parameters.region }}"
        forcePathStyle: true
        credentialsSecretRef:
          name: "{{ .Application.metadata.name }}-etcd-backup-creds"
EOF

  print_log "Provision bucket and wait for BucketAccess"
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${TEST_BUCKET_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  users:
    backup:
      readonly: false
EOF
  kubectl -n "$TEST_NAMESPACE" wait hr "bucket-${TEST_BUCKET_NAME}" --for=condition=ready --timeout=300s
  kubectl -n "$TEST_NAMESPACE" wait bucketclaims.objectstorage.k8s.io "bucket-${TEST_BUCKET_NAME}" \
    --for=jsonpath='{.status.bucketReady}'=true --timeout=300s
  kubectl -n "$TEST_NAMESPACE" wait bucketaccesses.objectstorage.k8s.io "bucket-${TEST_BUCKET_NAME}-backup" \
    --for=jsonpath='{.status.accessGranted}'=true --timeout=300s

  print_log "Extract bucket coordinates from BucketInfo"
  BI=$(kubectl -n "$TEST_NAMESPACE" get secret "bucket-${TEST_BUCKET_NAME}-backup" -o jsonpath='{.data.BucketInfo}' | base64 -d)
  ETCD_ACCESS_KEY=$(echo "$BI" | jq -r '.spec.secretS3.accessKeyID')
  ETCD_SECRET_KEY=$(echo "$BI" | jq -r '.spec.secretS3.accessSecretKey')
  ETCD_ENDPOINT=$(echo "$BI" | jq -r '.spec.secretS3.endpoint')
  ETCD_BUCKET=$(echo "$BI" | jq -r '.spec.bucketName')
  ETCD_REGION=$(echo "$BI" | jq -r 'if (.spec.secretS3.region // "") == "" then "us-east-1" else .spec.secretS3.region end')

  print_log "Materialise per-app backup credentials Secret"
  kubectl -n "$TEST_NAMESPACE" create secret generic "${TEST_ETCD_NAME}-etcd-backup-creds" \
    --from-literal="AWS_ACCESS_KEY_ID=${ETCD_ACCESS_KEY}" \
    --from-literal="AWS_SECRET_ACCESS_KEY=${ETCD_SECRET_KEY}" \
    --dry-run=client -o yaml | kubectl apply -f -

  print_log "Bind BackupClass to the Etcd strategy"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: ${TEST_BACKUPCLASS_NAME}
spec:
  strategies:
    - application:
        apiGroup: apps.cozystack.io
        kind: Etcd
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Etcd
        name: ${TEST_STRATEGY_NAME}
      parameters:
        bucket: "${ETCD_BUCKET}"
        endpoint: "${ETCD_ENDPOINT}"
        region: "${ETCD_REGION}"
EOF

  print_log "Apply source Etcd and wait for cluster Ready"
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: ${TEST_ETCD_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  size: 1Gi
  replicas: 1
  resources:
    cpu: 100m
    memory: 128Mi
EOF
  kubectl -n "$TEST_NAMESPACE" wait hr "${TEST_ETCD_NAME}" --for=condition=ready --timeout=300s
  kubectl -n "$TEST_NAMESPACE" wait etcdcluster.etcd.aenix.io etcd \
    --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True --timeout=300s
  kubectl -n "$TEST_NAMESPACE" wait pod etcd-0 --for=condition=ready --timeout=300s

  print_log "Write sentinel key"
  etcdctl_exec put "$TEST_SENTINEL_KEY" "$TEST_SENTINEL_VAL"
  [ "$(etcdctl_exec get "$TEST_SENTINEL_KEY" --print-value-only)" = "$TEST_SENTINEL_VAL" ]

  print_log "Submit BackupJob"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${TEST_BACKUPJOB_NAME}
  namespace: ${TEST_NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Etcd
    name: ${TEST_ETCD_NAME}
  backupClassName: ${TEST_BACKUPCLASS_NAME}
EOF

  print_log "Wait for BackupJob phase=Succeeded"
  kubectl -n "$TEST_NAMESPACE" wait backupjob.backups.cozystack.io "$TEST_BACKUPJOB_NAME" \
    --for=jsonpath='{.status.phase}'=Succeeded --timeout=1200s || { dump_diagnostics; false; }

  print_log "Inspect Backup driverMetadata"
  BACKUP_NAME=$(kubectl -n "$TEST_NAMESPACE" get backupjob.backups.cozystack.io "$TEST_BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
  [ -n "$BACKUP_NAME" ]
  PHASE=$(kubectl -n "$TEST_NAMESPACE" get backups.backups.cozystack.io "$BACKUP_NAME" -o jsonpath='{.status.phase}')
  [ "$PHASE" = "Ready" ]
  MD_BUCKET=$(kubectl -n "$TEST_NAMESPACE" get backups.backups.cozystack.io "$BACKUP_NAME" \
    -o jsonpath='{.spec.driverMetadata.etcd\.aenix\.io/bucket}')
  [ "$MD_BUCKET" = "$ETCD_BUCKET" ]
}

@test "RestoreJob with TargetApplicationRef is rejected as to-copy" {
  # Reuses the source Etcd + Backup landed by the previous test.
  BACKUP_NAME=$(kubectl -n "$TEST_NAMESPACE" get backupjob.backups.cozystack.io "$TEST_BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
  [ -n "$BACKUP_NAME" ]

  print_log "Submit RestoreJob with TargetApplicationRef.name != source"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${TEST_RESTOREJOB_TOCOPY}
  namespace: ${TEST_NAMESPACE}
spec:
  backupRef:
    name: ${BACKUP_NAME}
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: Etcd
    name: ${TEST_ETCD_NAME}-copy
EOF

  print_log "Wait for RestoreJob phase=Failed"
  kubectl -n "$TEST_NAMESPACE" wait restorejob.backups.cozystack.io "$TEST_RESTOREJOB_TOCOPY" \
    --for=jsonpath='{.status.phase}'=Failed --timeout=120s || { dump_diagnostics; false; }
  MSG=$(kubectl -n "$TEST_NAMESPACE" get restorejob.backups.cozystack.io "$TEST_RESTOREJOB_TOCOPY" \
    -o jsonpath='{.status.message}')
  echo "$MSG" | grep -q 'to-copy'
}

@test "In-place RestoreJob round-trips the sentinel through bootstrap.restore" {
  BACKUP_NAME=$(kubectl -n "$TEST_NAMESPACE" get backupjob.backups.cozystack.io "$TEST_BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
  [ -n "$BACKUP_NAME" ]

  print_log "Mutate sentinel before restore"
  etcdctl_exec put "$TEST_SENTINEL_KEY" 'mutated-value'
  [ "$(etcdctl_exec get "$TEST_SENTINEL_KEY" --print-value-only)" = 'mutated-value' ]

  print_log "Submit in-place RestoreJob (no targetApplicationRef)"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${TEST_RESTOREJOB_INPLACE}
  namespace: ${TEST_NAMESPACE}
spec:
  backupRef:
    name: ${BACKUP_NAME}
EOF

  print_log "Wait for RestoreJob phase=Succeeded"
  kubectl -n "$TEST_NAMESPACE" wait restorejob.backups.cozystack.io "$TEST_RESTOREJOB_INPLACE" \
    --for=jsonpath='{.status.phase}'=Succeeded --timeout=1800s || { dump_diagnostics; false; }

  print_log "Wait for etcd-0 Ready again after restore"
  kubectl -n "$TEST_NAMESPACE" wait pod etcd-0 --for=condition=ready --timeout=300s

  print_log "Read back sentinel — must match pristine value"
  POST=$(etcdctl_exec get "$TEST_SENTINEL_KEY" --print-value-only)
  [ "$POST" = "$TEST_SENTINEL_VAL" ]
}
