#!/usr/bin/env bats

# End-to-end backup + in-place restore for the CNPG strategy.
# Drives the manifests from examples/backups/postgres/ which are numbered so
# `kubectl apply -f <dir>/` order matches the dependency graph.
#
# Scope: this e2e runs entirely inside `tenant-root` so the postgres pods can
# reach `seaweedfs-s3` in the same namespace via the permissive
# `allow-internal-communication` CiliumNetworkPolicy. The cross-tenant case
# (postgres in tenant-test → seaweedfs in tenant-root) is blocked by the
# `${tenant}-egress` CiliumClusterwideNetworkPolicy, so the to-copy restore
# scenario stays in examples/ as a manual flow only and is not exercised here.
# That code path is covered by unit tests in
# internal/backupcontroller/cnpgstrategy_controller_test.go
# (TestBuildPostgresAppRestorePatch_*, TestMarshalUnmarshalCNPGBackupSnapshot,
# TestResolveCNPGRestoreTarget).
#
# Prereqs in the cluster:
#   - cozystack apps: Postgres + Bucket
#   - postgres-operator (CloudNativePG) reachable from tenant-root
#   - seaweedfs deployed in tenant-root (cozystack default)
#   - backup-controller and backupstrategy-controller running with the CNPG
#     dispatch case wired (see internal/backupcontroller/cnpgstrategy_controller.go)

NAMESPACE='tenant-root'
SRC='pg-src'
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

@test "CNPG Postgres backup + in-place restore" {
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
  # BucketInfo's .spec.secretS3.endpoint is the *external* ingress URL.
  # Inside the cluster we hit seaweedfs-s3 directly. The Service is TLS-only
  # (cozystack overrides readiness/liveness probes to scheme=HTTPS, see
  # packages/system/seaweedfs/values.yaml), so plain http://seaweedfs-s3 just
  # gets the connection closed by the server. The seaweedfs server cert is
  # signed by an internal CA whose SAN list contains '*.tenant-root', not
  # bare 'seaweedfs-s3', so we have to address the Service with its
  # namespace suffix or TLS hostname validation fails.
  S3_ENDPOINT="https://seaweedfs-s3.tenant-root:8333"
  kubectl -n "${NAMESPACE}" create secret generic "${SRC}-cnpg-backup-creds" \
    --from-literal=AWS_ACCESS_KEY_ID="${ACCESS}" \
    --from-literal=AWS_SECRET_ACCESS_KEY="${SECRETKEY}" \
    --dry-run=client -o yaml | kubectl apply -f -

  # Copy seaweedfs's internal CA into a per-app Secret so the strategy
  # template's endpointCA reference resolves. The original lives in
  # tenant-root; even when this test runs in tenant-root we still mirror
  # it under a stable name so the strategy YAML doesn't have to know the
  # platform Secret name.
  CA_BUNDLE=$(kubectl -n tenant-root get secret seaweedfs-ca-cert -o jsonpath='{.data.ca\.crt}' | base64 -d)
  kubectl -n "${NAMESPACE}" create secret generic "${SRC}-cnpg-backup-ca" \
    --from-literal=ca.crt="${CA_BUNDLE}" \
    --dry-run=client -o yaml | kubectl apply -f -

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
}
