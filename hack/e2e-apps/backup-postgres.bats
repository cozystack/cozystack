#!/usr/bin/env bats

# End-to-end backup + restore (in-place + to-copy) for the CNPG strategy.
# Drives the manifests from examples/backups/postgres/ which are numbered so
# `kubectl apply -f <dir>/` order matches the dependency graph.
#
# Prereqs in the cluster:
#   - cozystack apps: Postgres + Bucket
#   - postgres-operator (CloudNativePG) reachable from tenant-test
#   - backup-controller and backupstrategy-controller running with the CNPG
#     dispatch case wired (see internal/backupcontroller/cnpgstrategy_controller.go)

NAMESPACE='tenant-test'
SRC='pg-src'
TGT='pg-target'
BUCKET='pg-backups'
# Bucket user name from examples/backups/postgres/00-bucket.yaml. The COSI
# bucket chart materialises a per-user BucketAccess + credentials Secret
# named "bucket-<bucket>-<user>".
BUCKET_USER='backup'
BUCKET_ACCESS="bucket-${BUCKET}-${BUCKET_USER}"
EX_DIR='examples/backups/postgres'

# Step labels go to plain stdout. Real bats opens FD3 for runner output, but
# hack/cozytest.sh (the actual e2e runner here) does not, so writing to >&3
# would fail with "Bad file descriptor" before the first kubectl call. None
# of the sibling tests in hack/e2e-apps/ use >&3 either.
print_log() {
  echo "===== $1 ====="
}

apply_in_ns() {
  # Cluster-scoped manifests (CNPG strategy, BackupClass) are tolerated by
  # the namespace flag, so a single helper covers both scopes.
  kubectl apply -n "${NAMESPACE}" -f "$1"
}

primary_pod() {
  kubectl -n "${NAMESPACE}" get pods \
    -l "cnpg.io/cluster=postgres-$1,cnpg.io/instanceRole=primary" \
    -o jsonpath='{.items[0].metadata.name}'
}

@test "CNPG Postgres backup + in-place restore + to-copy restore" {
  print_log "Step 0: Bucket + S3 credentials"
  apply_in_ns "${EX_DIR}/00-bucket.yaml"
  # The Bucket HR materialises the BucketClaim/BucketAccess asynchronously
  # via COSI. kubectl wait fails immediately with NotFound when the object
  # does not yet exist, so wait for the HR to settle and the BucketAccess
  # CR to appear before waiting on its accessGranted condition.
  kubectl -n "${NAMESPACE}" wait "hr/bucket-${BUCKET}" --for=condition=ready --timeout=300s
  timeout 300 sh -ec "until kubectl -n ${NAMESPACE} get bucketaccesses.objectstorage.k8s.io ${BUCKET_ACCESS} >/dev/null 2>&1; do sleep 2; done"
  kubectl -n "${NAMESPACE}" wait "bucketaccesses.objectstorage.k8s.io/${BUCKET_ACCESS}" \
    --for=jsonpath='{.status.accessGranted}'=true --timeout=300s

  kubectl -n "${NAMESPACE}" get secret "${BUCKET_ACCESS}" \
    -o jsonpath='{.data.BucketInfo}' | base64 -d > /tmp/cnpg-bucket-info.json
  ACCESS=$(jq -r '.spec.secretS3.accessKeyID' /tmp/cnpg-bucket-info.json)
  SECRETKEY=$(jq -r '.spec.secretS3.accessSecretKey' /tmp/cnpg-bucket-info.json)
  COSI_BUCKET=$(jq -r '.spec.bucketName' /tmp/cnpg-bucket-info.json)
  # BucketInfo's .spec.secretS3.endpoint is the *external* ingress URL
  # (e.g. https://s3.example.org). In a CI sandbox that DNS name does not
  # resolve from inside the cluster, so CNPG cannot reach it. Use the
  # in-cluster Service URL instead - same target sibling tests reach via
  # 'kubectl port-forward service/seaweedfs-s3 -n tenant-root 8333:8333'.
  S3_ENDPOINT="http://seaweedfs-s3.tenant-root:8333"
  for app in "${SRC}" "${TGT}"; do
    kubectl -n "${NAMESPACE}" create secret generic "${app}-cnpg-backup-creds" \
      --from-literal=AWS_ACCESS_KEY_ID="${ACCESS}" \
      --from-literal=AWS_SECRET_ACCESS_KEY="${SECRETKEY}" \
      --dry-run=client -o yaml | kubectl apply -f -
  done

  print_log "Step 1: source Postgres"
  apply_in_ns "${EX_DIR}/05-postgres-src.yaml"
  kubectl -n "${NAMESPACE}" wait "hr/postgres-${SRC}" --for=condition=ready --timeout=300s
  timeout 600 sh -ec "until kubectl -n ${NAMESPACE} get clusters.postgresql.cnpg.io postgres-${SRC} -o jsonpath='{.status.phase}' | grep -q 'Cluster in healthy state'; do sleep 5; done"

  print_log "Step 2: CNPG strategy + BackupClass"
  # The example strategy YAML carries REPLACE_WITH_* placeholders so a
  # human reader knows where the real bucket / endpoint go. The e2e harness
  # has both values from the BucketInfo Secret already; substitute before
  # apply so CNPG actually writes WAL to a real S3 endpoint instead of
  # https://REPLACE_WITH_S3_ENDPOINT.
  sed -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${COSI_BUCKET}|g" \
      -e "s|https://REPLACE_WITH_S3_ENDPOINT|${S3_ENDPOINT}|g" \
      "${EX_DIR}/10-cnpg-strategy.yaml" | kubectl apply -n "${NAMESPACE}" -f -
  apply_in_ns "${EX_DIR}/15-backupclass.yaml"

  print_log "Step 3: write a marker row before backup"
  PRIMARY=$(primary_pod "${SRC}")
  kubectl -n "${NAMESPACE}" exec "${PRIMARY}" -c postgres -- \
    psql -d demo -c "CREATE TABLE IF NOT EXISTS marker(v text); INSERT INTO marker VALUES ('round-trip');"

  print_log "Step 4: ad-hoc BackupJob"
  apply_in_ns "${EX_DIR}/25-backupjob-adhoc.yaml"
  if ! kubectl -n "${NAMESPACE}" wait backupjob.backups.cozystack.io/pg-src-adhoc \
       --for=jsonpath='{.status.phase}'=Succeeded --timeout=600s; then
    echo "----- BackupJob status after timeout -----"
    kubectl -n "${NAMESPACE}" get backupjob.backups.cozystack.io/pg-src-adhoc -o yaml || true
    echo "----- cnpg.io/Backup objects -----"
    kubectl -n "${NAMESPACE}" get backups.postgresql.cnpg.io -o wide || true
    echo "----- cnpg.io/Cluster spec.backup -----"
    kubectl -n "${NAMESPACE}" get clusters.postgresql.cnpg.io "postgres-${SRC}" -o jsonpath='{.spec.backup}' || true
    echo
    echo "----- Postgres primary pod recent log -----"
    kubectl -n "${NAMESPACE}" logs "$(primary_pod "${SRC}")" -c postgres --tail=80 || true
    echo "----- backup-controller log -----"
    kubectl -n cozy-backup-controller logs -l app.kubernetes.io/name=backup-controller --tail=80 || true
    echo "----- backupstrategy-controller log -----"
    kubectl -n cozy-backup-controller logs -l app.kubernetes.io/name=backupstrategy-controller --tail=80 || true
    return 1
  fi
  kubectl -n "${NAMESPACE}" get backup.backups.cozystack.io/pg-src-adhoc

  print_log "Step 5: in-place RestoreJob (destructive)"
  apply_in_ns "${EX_DIR}/35-restorejob-in-place.yaml"
  kubectl -n "${NAMESPACE}" wait restorejob.backups.cozystack.io/pg-src-in-place \
    --for=jsonpath='{.status.phase}'=Succeeded --timeout=900s
  PRIMARY=$(primary_pod "${SRC}")
  ROW=$(kubectl -n "${NAMESPACE}" exec "${PRIMARY}" -c postgres -- \
    psql -d demo -tAc "SELECT v FROM marker;")
  [ "$ROW" = "round-trip" ]

  print_log "Step 6: deploy empty target + to-copy RestoreJob"
  apply_in_ns "${EX_DIR}/30-postgres-target.yaml"
  kubectl -n "${NAMESPACE}" wait "hr/postgres-${TGT}" --for=condition=ready --timeout=300s
  apply_in_ns "${EX_DIR}/40-restorejob-to-copy.yaml"
  kubectl -n "${NAMESPACE}" wait restorejob.backups.cozystack.io/pg-src-to-pg-target \
    --for=jsonpath='{.status.phase}'=Succeeded --timeout=900s
  PRIMARY=$(primary_pod "${TGT}")
  ROW=$(kubectl -n "${NAMESPACE}" exec "${PRIMARY}" -c postgres -- \
    psql -d demo -tAc "SELECT v FROM marker;")
  [ "$ROW" = "round-trip" ]
}
