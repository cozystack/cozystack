#!/usr/bin/env bats

# End-to-end backup + to-copy restore for the CNPG strategy.
# Drives the manifests from examples/backups/postgres/ which are numbered so
# `kubectl apply -f <dir>/` order matches the dependency graph.
#
# Why to-copy and not in-place: in-place restore is the more dangerous
# operation - it deletes the source Cluster + PVCs and races
# helm-controller's reconcile loop on bootstrap immutability. To-copy
# leaves the source running, archive_command keeps shipping WALs to
# object storage, and the WAL-archive gate in the controller clears
# deterministically. The in-place code path is still shipped and is
# covered at the unit level by TestClusterHasRecoveryBootstrap_-
# TerminatingCluster, TestCNPGBackupWALArchived, and TestCNPGPurgeNeeded.
#
# Scope: this e2e runs entirely inside `tenant-root` so the postgres pods can
# reach `seaweedfs-s3` in the same namespace via the permissive
# `allow-internal-communication` CiliumNetworkPolicy. The cross-tenant
# variant (target Postgres in `tenant-test` → seaweedfs in `tenant-root`)
# is blocked by the `${tenant}-egress` CiliumClusterwideNetworkPolicy and
# stays a manual / dev-cluster flow.
#
# Prereqs in the cluster:
#   - cozystack apps: Postgres + Bucket
#   - postgres-operator (CloudNativePG) reachable from tenant-root
#   - seaweedfs deployed in tenant-root (cozystack default)
#   - backup-controller and backupstrategy-controller running with the CNPG
#     dispatch case wired (see internal/backupcontroller/cnpgstrategy_controller.go)

NAMESPACE='tenant-root'
SRC='pg-src'
DST='pg-target'
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

@test "CNPG Postgres backup + to-copy restore" {
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
  # Materialise per-app S3 credentials and CA Secrets for both pg-src and
  # pg-target. The CNPG strategy template renders secret names off
  # {{ .Application.metadata.name }}, so the controller looks for
  # `${app}-cnpg-backup-creds` / `${app}-cnpg-backup-ca` in the target's
  # namespace at restore time. Same S3 bucket / CA for both apps - this
  # is just naming the controller can resolve.
  CA_BUNDLE=$(kubectl -n tenant-root get secret seaweedfs-ca-cert -o jsonpath='{.data.ca\.crt}' | base64 -d)
  for app in "${SRC}" "${DST}"; do
    kubectl -n "${NAMESPACE}" create secret generic "${app}-cnpg-backup-creds" \
      --from-literal=AWS_ACCESS_KEY_ID="${ACCESS}" \
      --from-literal=AWS_SECRET_ACCESS_KEY="${SECRETKEY}" \
      --dry-run=client -o yaml | kubectl apply -f -
    kubectl -n "${NAMESPACE}" create secret generic "${app}-cnpg-backup-ca" \
      --from-literal=ca.crt="${CA_BUNDLE}" \
      --dry-run=client -o yaml | kubectl apply -f -
  done

  print_log "Step 1: source Postgres"
  # 05-postgres-src.yaml carries the same REPLACE_WITH_* placeholders as
  # 10-cnpg-strategy.yaml so a human reader sees a self-explanatory file;
  # substitute the e2e bucket / endpoint here so the chart-rendered
  # spec.backup.barmanObjectStore points at the real seaweedfs Service.
  # Without the substitution archive_command would still be /bin/true at
  # backup time and the recovery flow would fail with "WAL not found".
  sed -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${COSI_BUCKET}|g" \
      -e "s|https://REPLACE_WITH_S3_ENDPOINT|${S3_ENDPOINT}|g" \
      "${EX_DIR}/05-postgres-src.yaml" | kubectl apply -n "${NAMESPACE}" -f -
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

  print_log "Step 5: empty target Postgres app for to-copy restore"
  # The driver patches bootstrap.recovery onto the target app at restore
  # time. Target starts empty (no marker, no data) so the assertion at
  # the end is a real restore proof, not a noop.
  apply_in_ns "${EX_DIR}/30-postgres-target.yaml"
  kubectl -n "${NAMESPACE}" wait "hr/postgres-${DST}" --for=condition=ready --timeout=300s
  timeout 600 sh -ec "until kubectl -n ${NAMESPACE} get clusters.postgresql.cnpg.io postgres-${DST} -o jsonpath='{.status.phase}' | grep -q 'Cluster in healthy state'; do sleep 5; done"

  print_log "Step 6: to-copy RestoreJob (pg-src -> pg-target)"
  apply_in_ns "${EX_DIR}/40-restorejob-to-copy.yaml"
  if ! kubectl -n "${NAMESPACE}" wait restorejob.backups.cozystack.io/pg-src-to-pg-target \
       --for=jsonpath='{.status.phase}'=Succeeded --timeout=900s; then
    echo "----- RestoreJob status after timeout -----"
    kubectl -n "${NAMESPACE}" get restorejob.backups.cozystack.io/pg-src-to-pg-target -o yaml || true
    echo "----- target Postgres app spec (verify our restore patch landed) -----"
    kubectl -n "${NAMESPACE}" get postgres.apps.cozystack.io "${DST}" -o yaml | sed -n '/^spec:/,/^status:/p' || true
    echo "----- target HelmRelease status (look for UpgradeFailed / RollbackSucceeded) -----"
    kubectl -n "${NAMESPACE}" get hr "postgres-${DST}" -o yaml | sed -n '/^status:/,$p' | head -40 || true
    echo "----- target cnpg.io/Cluster spec (bootstrap, externalClusters, backup) -----"
    kubectl -n "${NAMESPACE}" get clusters.postgresql.cnpg.io "postgres-${DST}" -o jsonpath='{"bootstrap: "}{.spec.bootstrap}{"\nexternalClusters: "}{.spec.externalClusters}{"\nbackup: "}{.spec.backup}{"\nphase: "}{.status.phase}{"\nlastArchivedWAL: "}{.status.lastArchivedWAL}{"\n"}' || true
    echo "----- source cnpg.io/Cluster archive state -----"
    kubectl -n "${NAMESPACE}" get clusters.postgresql.cnpg.io "postgres-${SRC}" -o jsonpath='{"phase: "}{.status.phase}{"\nlastArchivedWAL: "}{.status.lastArchivedWAL}{"\nfirstRecoverabilityPoint: "}{.status.firstRecoverabilityPoint}{"\nlastFailedWAL: "}{.status.lastFailedWAL}{"\n"}' || true
    echo "----- recovery pods (target) -----"
    kubectl -n "${NAMESPACE}" get pods -l "cnpg.io/cluster=postgres-${DST}" || true
    echo "----- most recent recovery pod log -----"
    LAST=$(kubectl -n "${NAMESPACE}" get pods -l "cnpg.io/cluster=postgres-${DST},job-name" --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null)
    [ -n "$LAST" ] && kubectl -n "${NAMESPACE}" logs "$LAST" --all-containers --tail=80 || true
    echo "----- backup-controller log -----"
    kubectl -n cozy-backup-controller logs -l app.kubernetes.io/name=backup-controller --tail=80 || true
    echo "----- backupstrategy-controller log -----"
    kubectl -n cozy-backup-controller logs -l app.kubernetes.io/name=backupstrategy-controller --tail=80 || true
    return 1
  fi

  print_log "Step 7: verify marker is on the target, source still works"
  TARGET_PRIMARY=$(primary_pod "${DST}")
  ROW=$(kubectl -n "${NAMESPACE}" exec "${TARGET_PRIMARY}" -c postgres -- \
    psql -d demo -tAc "SELECT v FROM marker;")
  [ "$ROW" = "round-trip" ]

  # Belt-and-braces: source is undisturbed by a to-copy restore. If we ever
  # accidentally regress and start touching the source's Cluster lifecycle
  # for to-copy, this catches it before users do.
  SRC_PRIMARY=$(primary_pod "${SRC}")
  SRC_ROW=$(kubectl -n "${NAMESPACE}" exec "${SRC_PRIMARY}" -c postgres -- \
    psql -d demo -tAc "SELECT v FROM marker;")
  [ "$SRC_ROW" = "round-trip" ]
}
