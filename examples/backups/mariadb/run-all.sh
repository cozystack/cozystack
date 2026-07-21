#!/bin/bash
# Convenience runner + e2e harness for the MariaDB backup/restore demo.
#
# It applies the SAME numbered manifests a human reads (00-bucket.yaml ..
# 40-restorejob-to-copy.yaml), filling the REPLACE_WITH_* placeholders from the
# provisioned Bucket and deriving the two Secrets the manifests reference
# (<app>-mariadb-backup-creds, <app>-mariadb-backup-ca) so the documented flow
# and the automated test can never drift. Stops on the first failure.
#
# Flow: Bucket -> source MariaDB (+ a sentinel row) -> MariaDB strategy +
# BackupClass -> ad-hoc BackupJob (wait Succeeded) -> empty target MariaDB +
# to-copy RestoreJob (wait Succeeded) -> assert the sentinel round-tripped
# through S3 into the restored copy while the source stays untouched.
#
# Why to-copy and not in-place: in-place replays the dump straight into the
# live source MariaDB (dropping and re-creating the restored databases there),
# so it cannot witness the restore on a separate instance. To-copy leaves the
# source running and lands the marker row on a distinct target, a stronger
# restore proof; the in-place dispatch is covered at the unit level by the
# controller tests.
#
# Override NAMESPACE / endpoint / CA via the environment; see 00-helpers.sh.
# hack/e2e-chainsaw/mariadb/ drives this file as mariadb-2-backup-roundtrip.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

# Substitute the manifest placeholders. $BUCKET / $S3_HOST are resolved from the
# Bucket below; $MARIADB_PASSWORD is the app user's password.
subst() {
    sed \
        -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${BUCKET}|g" \
        -e "s|REPLACE_WITH_S3_ENDPOINT|${S3_HOST}|g" \
        -e "s|REPLACE_WITH_PASSWORD|${MARIADB_PASSWORD}|g" \
        "$SCRIPT_DIR/$1"
}

# The mariadb-operator provisions the demo database + app user + grant from the
# chart as k8s.mariadb.com CRs named after the release (mariadb-<app>). The
# grant is the last of the three to reconcile, so its readiness is the signal
# that the app user can actually write into demo. Both "demo" and "app" are
# already DNS-safe, so the grant CR is <cr>-demo-app.
wait_app_grant_ready() {
    local cr="$1" timeout="${2:-180}"
    wait_for_field grants.k8s.mariadb.com "${cr}-demo-app" \
        '{.status.conditions[?(@.type=="Ready")].status}' True "$NAMESPACE" "$timeout"
}

print_header "Step 00: Provision Bucket '${BUCKET_NAME}' in ${NAMESPACE}"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/00-bucket.yaml"
wait_hr_ready "bucket-${BUCKET_NAME}" 300
wait_for_field bucketclaims.objectstorage.k8s.io "bucket-${BUCKET_NAME}" \
    '{.status.bucketReady}' true "$NAMESPACE" 300
wait_for_field bucketaccesses.objectstorage.k8s.io "bucket-${BUCKET_NAME}-${BUCKET_USER}" \
    '{.status.accessGranted}' true "$NAMESPACE" 300

log_substep "Reading bucket coordinates from BucketInfo Secret..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
kubectl -n "$NAMESPACE" get secret "bucket-${BUCKET_NAME}-${BUCKET_USER}" \
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
# The strategy manifest takes a path-style endpoint WITHOUT scheme, so strip it.
S3_HOST=${S3_ENDPOINT#https://}; S3_HOST=${S3_HOST#http://}
log_success "Bucket '${BUCKET}' at endpoint '${S3_ENDPOINT}'."

print_header "Step 05a: Materialise the Secrets the MariaDB strategy references"
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
# The strategy (10) renders <app>-mariadb-backup-creds / <app>-mariadb-backup-ca
# against whichever application it drives. .Application is the cozystack
# apps.cozystack.io/MariaDB object, so .Application.metadata.name is the app
# name (mariadb-src), NOT the prefixed operator-side CR (mariadb-mariadb-src).
# mariadb-operator reads AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from the
# creds Secret and trusts ca.crt from the CA Secret when talking to a
# self-signed S3 endpoint, so a restore TARGET needs its own pair too
# (materialised again in step 30). kubectl apply (not create) so a stale pair
# from an earlier run is corrected.
materialise_backup_secrets() {
    local app="$1"
    kubectl -n "$NAMESPACE" create secret generic "${app}-mariadb-backup-creds" \
        --from-literal="AWS_ACCESS_KEY_ID=${S3_ACCESS_KEY}" \
        --from-literal="AWS_SECRET_ACCESS_KEY=${S3_SECRET_KEY}" \
        --dry-run=client -o yaml | kubectl apply -f -

    if [[ -n "$S3_CA_SECRET" ]]; then
        log_substep "Copying S3 CA ${S3_CA_NAMESPACE}/${S3_CA_SECRET}[${S3_CA_KEY}] -> ${app}-mariadb-backup-ca..."
        local ca_pem
        ca_pem=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" \
            -o jsonpath="{.data.${S3_CA_KEY//./\\.}}" | base64 -d)
        [[ -n "$ca_pem" ]] || { log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} has no ${S3_CA_KEY}"; exit 1; }
        kubectl -n "$NAMESPACE" create secret generic "${app}-mariadb-backup-ca" \
            --from-literal="ca.crt=${ca_pem}" \
            --dry-run=client -o yaml | kubectl apply -f -
    else
        log_warning "S3_CA_SECRET empty: the strategy still references ${app}-mariadb-backup-ca."
        log_warning "Remove the tls block from 10-mariadb-strategy.yaml by hand when the S3 endpoint uses a public CA."
    fi
}
materialise_backup_secrets "$MARIADB_SRC_NAME"

print_header "Step 05b: Deploy source MariaDB '${MARIADB_SRC_NAME}' and wait for it to be healthy"
subst 05-mariadb-src.yaml | kubectl -n "$NAMESPACE" apply -f -
wait_hr_ready "mariadb-${MARIADB_SRC_NAME}" 300
wait_for_field mariadbs.k8s.mariadb.com "$MARIADB_SRC_CR" \
    '{.status.conditions[?(@.type=="Ready")].status}' True "$NAMESPACE" 600
wait_app_grant_ready "$MARIADB_SRC_CR"

print_header "Step 05c: Write a sentinel row so the backup has something to prove"
SENTINEL_TOKEN="e2e-$(date +%s)-$$"
mysql_exec "$MARIADB_SRC_CR" "
    CREATE TABLE IF NOT EXISTS demo.e2e_sentinel (id INT PRIMARY KEY, token VARCHAR(64));
    REPLACE INTO demo.e2e_sentinel (id, token) VALUES (1, '${SENTINEL_TOKEN}');"
log_success "Sentinel token: ${SENTINEL_TOKEN}"

print_header "Step 10/15: Create the MariaDB strategy + BackupClass"
subst 10-mariadb-strategy.yaml | kubectl apply -f -
kubectl apply -f "$SCRIPT_DIR/15-backupclass.yaml"

print_header "Step 25: Submit ad-hoc BackupJob '${BACKUPJOB_NAME}' and wait for Succeeded"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/25-backupjob-adhoc.yaml"
wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed
BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
log_success "Backup artefact: ${BACKUP_NAME}"

if [[ "${SKIP_RESTORE:-0}" == "1" ]]; then
    log_warning "SKIP_RESTORE=1: stopping after a successful backup."
    exit 0
fi

print_header "Step 30/40: Restore to a copy '${MARIADB_TARGET_NAME}' and wait for Succeeded"
# The strategy renders <app>-mariadb-backup-creds / -ca against the TARGET
# during a restore, so the target needs its own Secret pair (same S3 coords).
materialise_backup_secrets "$MARIADB_TARGET_NAME"
subst 30-mariadb-target.yaml | kubectl -n "$NAMESPACE" apply -f -
wait_hr_ready "mariadb-${MARIADB_TARGET_NAME}" 300
wait_for_field mariadbs.k8s.mariadb.com "$MARIADB_TARGET_CR" \
    '{.status.conditions[?(@.type=="Ready")].status}' True "$NAMESPACE" 600
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/40-restorejob-to-copy.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
# The restore replays the logical dump (schema + rows), but the app user is NOT
# in the dump — it comes from the target chart. Wait for its grant before the
# read below so the SELECT authenticates.
wait_app_grant_ready "$MARIADB_TARGET_CR"

print_header "Step 40 verify: the sentinel round-tripped through S3 into the copy"
GOT=$(mysql_exec "$MARIADB_TARGET_CR" "SELECT token FROM demo.e2e_sentinel WHERE id = 1;" \
    | awk 'NR==2{print}' | tr -d '[:space:]')
if [[ "$GOT" != "$SENTINEL_TOKEN" ]]; then
    log_error "sentinel mismatch: target has '${GOT}', expected '${SENTINEL_TOKEN}'"
    exit 1
fi
log_success "Round-trip verified: '${MARIADB_TARGET_NAME}' restored sentinel '${GOT}' from S3."

# To-copy must not mutate the source. Regressing into a source-touching restore
# would corrupt the running instance, so assert the source still reads back.
SRC_GOT=$(mysql_exec "$MARIADB_SRC_CR" "SELECT token FROM demo.e2e_sentinel WHERE id = 1;" \
    | awk 'NR==2{print}' | tr -d '[:space:]')
if [[ "$SRC_GOT" != "$SENTINEL_TOKEN" ]]; then
    log_error "source sentinel changed after to-copy restore: source has '${SRC_GOT}', expected '${SENTINEL_TOKEN}'"
    exit 1
fi
log_success "Source '${MARIADB_SRC_NAME}' left untouched by the to-copy restore."
