#!/bin/bash
# Convenience runner + e2e harness for the PostgreSQL barman-cloud backup demo.
#
# It applies the SAME numbered manifests a human reads (00-bucket.yaml ..
# 40-restorejob-to-copy.yaml), filling the REPLACE_WITH_* placeholders from the
# provisioned Bucket and deriving the two Secrets the manifests reference
# (<app>-cnpg-backup-creds, <app>-cnpg-backup-ca) so the documented flow and the
# automated test can never drift. Stops on the first failure.
#
# Flow: Bucket -> source Postgres (+ a sentinel row) -> CNPG strategy +
# BackupClass -> ad-hoc BackupJob (wait Succeeded) -> empty target Postgres +
# to-copy RestoreJob (wait Succeeded) -> assert the sentinel round-tripped
# through S3 into the restored copy.
#
# Override NAMESPACE / endpoint / CA via the environment; see 00-helpers.sh.
# hack/e2e-chainsaw/postgres/ drives this file as postgres-2-backup-roundtrip.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

# Substitute the manifest placeholders. $BUCKET / $S3_HOST are resolved from the
# Bucket below; $PG_PASSWORD is the app user's password.
subst() {
    sed \
        -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${BUCKET}|g" \
        -e "s|REPLACE_WITH_S3_ENDPOINT|${S3_HOST}|g" \
        -e "s|REPLACE_WITH_PASSWORD|${PG_PASSWORD}|g" \
        "$SCRIPT_DIR/$1"
}

print_header "Step 00: Provision Bucket '${BUCKET_NAME}' in ${NAMESPACE}"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/00-bucket.yaml"
wait_hr_ready "bucket-${BUCKET_NAME}" 300
wait_for_field bucketclaims.objectstorage.k8s.io "bucket-${BUCKET_NAME}" \
    '{.status.bucketReady}' true "$NAMESPACE" 300
wait_for_field bucketaccesses.objectstorage.k8s.io "bucket-${BUCKET_NAME}-backup" \
    '{.status.accessGranted}' true "$NAMESPACE" 300

log_substep "Reading bucket coordinates from BucketInfo Secret..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
kubectl -n "$NAMESPACE" get secret "bucket-${BUCKET_NAME}-backup" \
    -o jsonpath='{.data.BucketInfo}' | base64 -d > "$TMP"
S3_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' "$TMP")
S3_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' "$TMP")
# S3_ENDPOINT can be overridden via the environment: BucketInfo advertises the
# EXTERNAL ingress endpoint, which in-cluster Pods cannot always reach or
# TLS-validate; the in-cluster alternative is https://seaweedfs-s3.<ns>:8333
# (trusted via the copied CA below).
S3_ENDPOINT="${S3_ENDPOINT:-$(jq -r '.spec.secretS3.endpoint' "$TMP")}"
BUCKET=$(jq -r '.spec.bucketName' "$TMP")
for v in S3_ACCESS_KEY S3_SECRET_KEY S3_ENDPOINT BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done
# The manifests template "https://REPLACE_WITH_S3_ENDPOINT", so strip the scheme.
S3_HOST=${S3_ENDPOINT#https://}; S3_HOST=${S3_HOST#http://}
log_success "Bucket '${BUCKET}' at endpoint '${S3_ENDPOINT}'."

print_header "Step 05a: Materialise the Secrets the barman-cloud ObjectStore references"
# Resolve the S3 endpoint CA secret. The default name tracks the seaweedfs
# chart's fullnameOverride (seaweedfs -> seaweedfs-ca-cert), but a downstream
# fullname change would rename it, so fall back to discovering the cert-manager
# CA Certificate (the seaweedfs-labelled one with spec.isCA=true) and read its
# secretName. Leave S3_CA_SECRET empty to skip the copy on a public-CA endpoint.
if [[ -n "$S3_CA_SECRET" ]] \
    && ! kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" >/dev/null 2>&1; then
    log_warning "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} not found; discovering the seaweedfs CA Certificate..."
    # List every seaweedfs Certificate as "<isCA> <secretName>" and pick the CA
    # one in shell — avoids kubectl jsonpath's finicky boolean-literal filter.
    DISCOVERED_CA=$(kubectl -n "$S3_CA_NAMESPACE" get certificates.cert-manager.io \
        -l app.kubernetes.io/name=seaweedfs \
        -o jsonpath='{range .items[*]}{.spec.isCA}{" "}{.spec.secretName}{"\n"}{end}' 2>/dev/null \
        | awk '$1=="true"{print $2; exit}' || true)
    if [[ -n "$DISCOVERED_CA" ]]; then
        log_success "Discovered seaweedfs CA secret ${S3_CA_NAMESPACE}/${DISCOVERED_CA}"
        S3_CA_SECRET="$DISCOVERED_CA"
    else
        log_error "No seaweedfs CA Certificate found in ${S3_CA_NAMESPACE}; set S3_CA_SECRET explicitly (or empty for a public-CA endpoint)."
        exit 1
    fi
fi
# The chart (05) and the strategy (10) both reference <app>-cnpg-backup-creds
# and <app>-cnpg-backup-ca. The barman-cloud sidecar reads AWS_ACCESS_KEY_ID /
# AWS_SECRET_ACCESS_KEY from the creds Secret and trusts ca.crt from the CA
# Secret when talking to a self-signed S3 endpoint. The strategy template
# renders the names against whichever application it drives, so a restore
# TARGET needs its own pair too (materialised again in step 30 below).
# kubectl apply (not create) so a stale pair from an earlier run is corrected.
materialise_backup_secrets() {
    local app="$1"
    kubectl -n "$NAMESPACE" create secret generic "${app}-cnpg-backup-creds" \
        --from-literal="AWS_ACCESS_KEY_ID=${S3_ACCESS_KEY}" \
        --from-literal="AWS_SECRET_ACCESS_KEY=${S3_SECRET_KEY}" \
        --dry-run=client -o yaml | kubectl apply -f -

    if [[ -n "$S3_CA_SECRET" ]]; then
        log_substep "Copying S3 CA ${S3_CA_NAMESPACE}/${S3_CA_SECRET}[${S3_CA_KEY}] -> ${app}-cnpg-backup-ca..."
        local ca_pem
        ca_pem=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" \
            -o jsonpath="{.data.${S3_CA_KEY//./\\.}}" | base64 -d)
        [[ -n "$ca_pem" ]] || { log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} has no ${S3_CA_KEY}"; exit 1; }
        kubectl -n "$NAMESPACE" create secret generic "${app}-cnpg-backup-ca" \
            --from-literal="ca.crt=${ca_pem}" \
            --dry-run=client -o yaml | kubectl apply -f -
    else
        log_warning "S3_CA_SECRET empty: the manifests still reference ${app}-cnpg-backup-ca."
        log_warning "Remove endpointCA from 05/10 by hand when the S3 endpoint uses a public CA."
    fi
}
materialise_backup_secrets "$PG_SRC_NAME"

print_header "Step 05b: Deploy source Postgres '${PG_SRC_NAME}' and wait for it to be healthy"
subst 05-postgres-src.yaml | kubectl -n "$NAMESPACE" apply -f -
wait_hr_ready "postgres-${PG_SRC_NAME}" 360
wait_for_field clusters.postgresql.cnpg.io "$PG_SRC_CLUSTER" \
    '{.status.phase}' 'Cluster in healthy state' "$NAMESPACE" 360
# The 'demo' database and 'app' user are created by the chart's init Job, not
# by cluster readiness — wait for it before writing into demo.
wait_for_field jobs.batch "postgres-${PG_SRC_NAME}-init-job" \
    '{.status.succeeded}' 1 "$NAMESPACE" 180

print_header "Step 05c: Write a sentinel row so the backup has something to prove"
SENTINEL_TOKEN="e2e-$(date +%s)-$$"
psql_exec "$PG_SRC_CLUSTER" demo "
    CREATE TABLE IF NOT EXISTS e2e_sentinel (id int PRIMARY KEY, token text);
    INSERT INTO e2e_sentinel (id, token) VALUES (1, '${SENTINEL_TOKEN}')
      ON CONFLICT (id) DO UPDATE SET token = EXCLUDED.token;
    CHECKPOINT;
    SELECT pg_switch_wal();"
log_success "Sentinel token: ${SENTINEL_TOKEN}"

print_header "Step 10/15: Create the CNPG strategy + BackupClass"
subst 10-cnpg-strategy.yaml | kubectl apply -f -
kubectl apply -f "$SCRIPT_DIR/15-backupclass.yaml"

print_header "Step 25: Submit ad-hoc BackupJob '${BACKUPJOB_NAME}' and wait for Succeeded"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/25-backupjob-adhoc.yaml"
wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 1200 Failed
BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
log_success "Backup artefact: ${BACKUP_NAME}"

if [[ "${SKIP_RESTORE:-0}" == "1" ]]; then
    log_warning "SKIP_RESTORE=1: stopping after a successful backup."
    exit 0
fi

print_header "Step 30/40: Restore to a copy '${PG_TARGET_NAME}' and wait for Succeeded"
# The strategy template renders <app>-cnpg-backup-creds / -ca against the
# TARGET application during a restore, so the target needs its own Secret pair
# (same S3 coordinates as the source's archive).
materialise_backup_secrets "$PG_TARGET_NAME"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/30-postgres-target.yaml"
# Let the target's first install settle before the RestoreJob driver suspends
# its HelmRelease — suspending an HR mid-install races helm-controller.
wait_hr_ready "postgres-${PG_TARGET_NAME}" 360
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/40-restorejob-to-copy.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 1200 Failed
# The RestoreJob succeeding means the driver finished re-bootstrapping the
# target from S3; wait for the copy to actually come up before reading it.
wait_for_field clusters.postgresql.cnpg.io "$PG_TARGET_CLUSTER" \
    '{.status.phase}' 'Cluster in healthy state' "$NAMESPACE" 600

print_header "Step 40 verify: the sentinel round-tripped through S3 into the copy"
GOT=$(psql_exec "$PG_TARGET_CLUSTER" demo "SELECT token FROM e2e_sentinel WHERE id = 1;" | tr -d '[:space:]')
if [[ "$GOT" != "$SENTINEL_TOKEN" ]]; then
    log_error "sentinel mismatch: target has '${GOT}', expected '${SENTINEL_TOKEN}'"
    exit 1
fi
log_success "Round-trip verified: '${PG_TARGET_NAME}' restored sentinel '${GOT}' from S3."
