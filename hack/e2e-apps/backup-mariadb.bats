#!/usr/bin/env bats

# End-to-end backup + to-copy restore for the MariaDB strategy.
# Drives the manifests from examples/backups/mariadb/ which are numbered so
# `kubectl apply -f <dir>/` order matches the dependency graph.
#
# Why to-copy and not in-place: in-place restore replays the dump straight
# into the live source MariaDB - it's not destructive at the cluster level
# the way the CNPG in-place flow is, but it does drop and re-create the
# restored databases on the source. To-copy leaves the source untouched
# and lets the assertion at the end witness the marker row landing on a
# separate target instance, which is a stronger restore proof. The
# in-place dispatch is exercised at the unit level by the controller
# tests.
#
# Scope: this e2e runs entirely inside `tenant-root` so the MariaDB pods
# can reach `seaweedfs-s3` in the same namespace via the permissive
# `allow-internal-communication` CiliumNetworkPolicy. The cross-tenant
# variant (target MariaDB in `tenant-test`, seaweedfs in `tenant-root`)
# is blocked by the `${tenant}-egress` CiliumClusterwideNetworkPolicy and
# stays a manual / dev-cluster flow.
#
# Prereqs in the cluster:
#   - cozystack apps: MariaDB + Bucket
#   - mariadb-operator (k8s.mariadb.com) reachable from tenant-root
#   - seaweedfs deployed in tenant-root (cozystack default)
#   - backup-controller and backupstrategy-controller running with the
#     MariaDB dispatch case wired (see internal/backupcontroller/mariadbstrategy_controller.go)

NAMESPACE='tenant-root'
SRC='mariadb-src'
DST='mariadb-target'
APP_PASSWORD='bats-app-password'
BUCKET='mariadb-backups'
# Bucket user name from examples/backups/mariadb/00-bucket.yaml. The COSI
# bucket chart materialises a per-user BucketAccess + credentials Secret
# named "bucket-<bucket>-<user>".
BUCKET_USER='backup'
BUCKET_ACCESS="bucket-${BUCKET}-${BUCKET_USER}"
EX_DIR='examples/backups/mariadb'

print_log() {
  echo "===== $1 ====="
}

apply_in_ns() {
  kubectl apply -n "${NAMESPACE}" -f "$1"
}

# Bats invokes teardown() after every @test, including failed ones. Every
# deletion is best-effort: --ignore-not-found absorbs a never-applied
# step, and `|| true` keeps a transient apiserver hiccup from masking
# the real assertion failure in the test body.
teardown() {
  print_log "Teardown: cleaning up MariaDB backup test resources"

  # Drop the per-run Cozystack jobs and the resulting Backup artefact in
  # one pass. Two non-obvious reasons for going beyond just the jobs:
  #   - BackupJob/RestoreJob deletion does NOT cascade to the operator-side
  #     k8s.mariadb.com/Backup or /Restore CRs (the driver only stamps
  #     OwningJob labels for idempotent ensure-by-label semantics, not
  #     OwnerReferences); leftover stale Backup CRs would be reused via
  #     findMariaDBBackupForJob on the next run.
  #   - The Cozystack Backup artefact (backups.backups.cozystack.io)
  #     persists `driverMetadata.k8s.mariadb.com/backup-name` pointing at
  #     the operator-side Backup CR. createMariaDBBackupArtifact returns
  #     the existing artefact unchanged on AlreadyExists, so a re-run with
  #     the same BackupJob name would succeed pointing at a stale
  #     operator-side name and then a downstream RestoreJob would fail
  #     with "operator-side artifact has been reaped".
  kubectl -n "${NAMESPACE}" delete --ignore-not-found --wait=false \
    restorejob.backups.cozystack.io/mariadb-src-to-mariadb-target \
    backupjob.backups.cozystack.io/mariadb-src-adhoc \
    backup.backups.cozystack.io/mariadb-src-adhoc || true
  for owner in mariadb-src-adhoc mariadb-src-to-mariadb-target; do
    kubectl -n "${NAMESPACE}" delete --ignore-not-found --wait=false \
      backups.k8s.mariadb.com,restores.k8s.mariadb.com \
      -l "backups.cozystack.io/owned-by.BackupJobName=${owner}" || true
  done

  # MariaDB apps (HelmReleases). Flux uninstalls the chart, which drops
  # the MariaDB CR + PVCs.
  kubectl -n "${NAMESPACE}" delete --ignore-not-found --wait=false \
    "hr/mariadb-${SRC}" "hr/mariadb-${DST}" || true

  # Bucket app (HelmRelease) - removing it tears down the COSI
  # BucketClaim/BucketAccess and the per-user credentials Secret.
  kubectl -n "${NAMESPACE}" delete --ignore-not-found --wait=false \
    "hr/bucket-${BUCKET}" || true

  # Per-app CA bundles + S3 creds materialised by Step 0.
  kubectl -n "${NAMESPACE}" delete --ignore-not-found \
    "secret/${SRC}-mariadb-backup-ca" \
    "secret/${DST}-mariadb-backup-ca" \
    "secret/${SRC}-mariadb-backup-creds" \
    "secret/${DST}-mariadb-backup-creds" || true

  # Cluster-scoped: BackupClass + MariaDB strategy.
  kubectl delete --ignore-not-found \
    backupclass.backups.cozystack.io/mariadb-default \
    mariadbs.strategy.backups.cozystack.io/mariadb-strategy-default || true

  rm -f /tmp/mariadb-bucket-info.json
}

primary_pod() {
  # mariadb-operator 25.10.x exposes the primary identity on
  # MariaDB.status.currentPrimary instead of a pod label, so read it from
  # the CR. The cozystack mariadb ApplicationDefinition prefixes its
  # release name with "mariadb-" (packages/system/mariadb-rd/cozyrds/mariadb.yaml),
  # so the operator-side MariaDB CR is mariadb-<app>.
  kubectl -n "${NAMESPACE}" get mariadbs.k8s.mariadb.com "mariadb-$1" \
    -o jsonpath='{.status.currentPrimary}'
}

# mysql_exec runs a single SQL statement against the primary as 'app',
# routing through the operator-managed primary Service (which the chart
# renders as mariadb-<app>-primary - again the cozystack release prefix).
mysql_exec() {
  local app="$1" sql="$2"
  kubectl -n "${NAMESPACE}" exec "$(primary_pod "${app}")" -c mariadb -- \
    mariadb -uapp -p"${APP_PASSWORD}" -h "mariadb-${app}-primary" -e "${sql}"
}

@test "MariaDB backup + to-copy restore" {
  print_log "Step 0: Bucket + S3 credentials"
  apply_in_ns "${EX_DIR}/00-bucket.yaml"
  kubectl -n "${NAMESPACE}" wait "hr/bucket-${BUCKET}" --for=condition=ready --timeout=300s
  timeout 300 sh -ec "until kubectl -n ${NAMESPACE} get bucketaccesses.objectstorage.k8s.io ${BUCKET_ACCESS} >/dev/null 2>&1; do sleep 2; done"
  kubectl -n "${NAMESPACE}" wait "bucketaccesses.objectstorage.k8s.io/${BUCKET_ACCESS}" \
    --for=jsonpath='{.status.accessGranted}'=true --timeout=300s

  kubectl -n "${NAMESPACE}" get secret "${BUCKET_ACCESS}" \
    -o jsonpath='{.data.BucketInfo}' | base64 -d > /tmp/mariadb-bucket-info.json
  COSI_BUCKET=$(jq -r '.spec.bucketName' /tmp/mariadb-bucket-info.json)
  # COSI ships AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY nested inside the
  # BucketInfo JSON blob, but mariadb-operator expects them as separate
  # Secret data entries. Extract them once here and re-emit per-app
  # creds Secrets below (mirrors the postgres CNPG flow).
  ACCESS=$(jq -r '.spec.secretS3.accessKeyID' /tmp/mariadb-bucket-info.json)
  SECRETKEY=$(jq -r '.spec.secretS3.accessSecretKey' /tmp/mariadb-bucket-info.json)
  # In-cluster S3 endpoint (TLS, internal CA SAN includes *.tenant-root).
  # The mariadb-operator's Backup CR takes the endpoint without scheme.
  S3_ENDPOINT="seaweedfs-s3.tenant-root:8333"

  # Materialise per-app CA + creds Secrets for both source and target. The
  # strategy template renders both names off
  # `{{ .Application.metadata.name }}`, so the operator looks for
  # `${app}-mariadb-backup-ca` and `${app}-mariadb-backup-creds` in the
  # same namespace at backup time.
  CA_BUNDLE=$(kubectl -n tenant-root get secret seaweedfs-ca-cert -o jsonpath='{.data.ca\.crt}' | base64 -d)
  for app in "${SRC}" "${DST}"; do
    kubectl -n "${NAMESPACE}" create secret generic "${app}-mariadb-backup-ca" \
      --from-literal=ca.crt="${CA_BUNDLE}" \
      --dry-run=client -o yaml | kubectl apply -f -
    kubectl -n "${NAMESPACE}" create secret generic "${app}-mariadb-backup-creds" \
      --from-literal=AWS_ACCESS_KEY_ID="${ACCESS}" \
      --from-literal=AWS_SECRET_ACCESS_KEY="${SECRETKEY}" \
      --dry-run=client -o yaml | kubectl apply -f -
  done

  print_log "Step 1: source MariaDB"
  sed -e "s|REPLACE_WITH_PASSWORD|${APP_PASSWORD}|g" \
      "${EX_DIR}/05-mariadb-src.yaml" | kubectl apply -n "${NAMESPACE}" -f -
  kubectl -n "${NAMESPACE}" wait "hr/mariadb-${SRC}" --for=condition=ready --timeout=300s
  # The cozystack mariadb release prefix ("mariadb-") makes the operator-side
  # MariaDB CR mariadb-<app> rather than <app>. The strategy controller does
  # the same name mapping via mariadbNameForApp.
  timeout 600 sh -ec "until kubectl -n ${NAMESPACE} get mariadbs.k8s.mariadb.com mariadb-${SRC} -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}' | grep -q True; do sleep 5; done"

  print_log "Step 2: MariaDB strategy + BackupClass"
  sed -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${COSI_BUCKET}|g" \
      -e "s|REPLACE_WITH_S3_ENDPOINT|${S3_ENDPOINT}|g" \
      "${EX_DIR}/10-mariadb-strategy.yaml" | kubectl apply -f -
  apply_in_ns "${EX_DIR}/15-backupclass.yaml"

  print_log "Step 3: write a marker row before backup"
  mysql_exec "${SRC}" "CREATE TABLE IF NOT EXISTS demo.marker(v VARCHAR(64)); INSERT INTO demo.marker VALUES ('round-trip');"

  print_log "Step 4: ad-hoc BackupJob"
  apply_in_ns "${EX_DIR}/25-backupjob-adhoc.yaml"
  if ! kubectl -n "${NAMESPACE}" wait backupjob.backups.cozystack.io/mariadb-src-adhoc \
       --for=jsonpath='{.status.phase}'=Succeeded --timeout=600s; then
    echo "----- BackupJob status after timeout -----"
    kubectl -n "${NAMESPACE}" get backupjob.backups.cozystack.io/mariadb-src-adhoc -o yaml || true
    echo "----- k8s.mariadb.com/Backup objects -----"
    kubectl -n "${NAMESPACE}" get backups.k8s.mariadb.com -o wide || true
    echo "----- backupstrategy-controller log -----"
    kubectl -n cozy-backup-controller logs -l app.kubernetes.io/name=backupstrategy-controller --tail=80 || true
    return 1
  fi
  kubectl -n "${NAMESPACE}" get backup.backups.cozystack.io/mariadb-src-adhoc

  print_log "Step 5: empty target MariaDB for to-copy restore"
  sed -e "s|REPLACE_WITH_PASSWORD|${APP_PASSWORD}|g" \
      "${EX_DIR}/30-mariadb-target.yaml" | kubectl apply -n "${NAMESPACE}" -f -
  kubectl -n "${NAMESPACE}" wait "hr/mariadb-${DST}" --for=condition=ready --timeout=300s
  timeout 600 sh -ec "until kubectl -n ${NAMESPACE} get mariadbs.k8s.mariadb.com mariadb-${DST} -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}' | grep -q True; do sleep 5; done"

  print_log "Step 6: to-copy RestoreJob (mariadb-src -> mariadb-target)"
  apply_in_ns "${EX_DIR}/40-restorejob-to-copy.yaml"
  if ! kubectl -n "${NAMESPACE}" wait restorejob.backups.cozystack.io/mariadb-src-to-mariadb-target \
       --for=jsonpath='{.status.phase}'=Succeeded --timeout=900s; then
    echo "----- RestoreJob status after timeout -----"
    kubectl -n "${NAMESPACE}" get restorejob.backups.cozystack.io/mariadb-src-to-mariadb-target -o yaml || true
    echo "----- k8s.mariadb.com/Restore objects -----"
    kubectl -n "${NAMESPACE}" get restores.k8s.mariadb.com -o wide || true
    echo "----- backupstrategy-controller log -----"
    kubectl -n cozy-backup-controller logs -l app.kubernetes.io/name=backupstrategy-controller --tail=80 || true
    return 1
  fi

  print_log "Step 7: verify marker is on the target, source still works"
  ROW_DST=$(mysql_exec "${DST}" "SELECT v FROM demo.marker\G" | awk '/v: /{print $2}')
  [ "${ROW_DST}" = "round-trip" ]

  # To-copy must not touch the source. If we ever regress and start
  # mutating mariadb-src on a to-copy restore this catches it before
  # users do.
  ROW_SRC=$(mysql_exec "${SRC}" "SELECT v FROM demo.marker\G" | awk '/v: /{print $2}')
  [ "${ROW_SRC}" = "round-trip" ]
}
