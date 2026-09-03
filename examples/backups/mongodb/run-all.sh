#!/bin/bash
# Convenience runner + e2e harness for the MongoDB backup/restore demo.
#
# It applies the SAME numbered manifests a human reads (00-bucket.yaml ..
# 30-restorejob-to-copy.yaml), filling the REPLACE_WITH_* placeholders from the
# provisioned Bucket so the documented flow and the automated test can never
# drift. Stops on the first failure.
#
# Flow: Bucket -> source MongoDB with backup.enabled (+ a sentinel document) ->
# ad-hoc BackupJob against the cozy-default BackupClass (wait Succeeded) ->
# empty target MongoDB + to-copy RestoreJob (wait Succeeded) -> assert the
# sentinel round-tripped through S3 into the restored copy while the source
# stays untouched.
#
# Why to-copy and not in-place: in-place replays the dump straight into the live
# source cluster, so it cannot witness the restore on a separate instance.
# To-copy leaves the source running and lands the marker document on a distinct
# target, a stronger restore proof; the in-place dispatch is covered at the unit
# level by the controller tests.
#
# Override NAMESPACE / endpoint via the environment; see 00-helpers.sh.
# hack/e2e-chainsaw/mongodb/ drives this file as mongodb-2-backup-roundtrip.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

# App user password baked into the source spec; the demo writes the sentinel as
# the operator databaseAdmin (see mongosh_exec), so this is just to satisfy the
# chart's user declaration.
MONGODB_PASSWORD="${MONGODB_PASSWORD:-Xai7Wepo0aeThie8}"

# Substitute the manifest placeholders. $BUCKET / $S3_ENDPOINT / the access keys
# are resolved from the Bucket below.
subst() {
    sed \
        -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${BUCKET}|g" \
        -e "s|REPLACE_WITH_S3_ENDPOINT|${S3_ENDPOINT}|g" \
        -e "s|REPLACE_WITH_S3_ACCESS_KEY|${S3_ACCESS_KEY}|g" \
        -e "s|REPLACE_WITH_S3_SECRET_KEY|${S3_SECRET_KEY}|g" \
        -e "s|REPLACE_WITH_PASSWORD|${MONGODB_PASSWORD}|g" \
        "$SCRIPT_DIR/$1"
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
# EXTERNAL ingress endpoint, which in-cluster Pods (the pbm agent runs inside
# the mongod pod) cannot always reach or TLS-validate; the in-cluster
# alternative is https://seaweedfs-s3.<ns>:8333, reached with
# insecureSkipTLSVerify=true (psmdb s3 storage has no CA-bundle field).
S3_ENDPOINT="${S3_ENDPOINT:-$(jq -r '.spec.secretS3.endpoint' "$TMP")}"
BUCKET=$(jq -r '.spec.bucketName' "$TMP")
for v in S3_ACCESS_KEY S3_SECRET_KEY S3_ENDPOINT BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done
log_success "Bucket '${BUCKET}' at endpoint '${S3_ENDPOINT}'."

print_header "Step 05: Deploy source MongoDB '${MONGODB_SRC_NAME}' with backups enabled"
subst 05-mongodb-src.yaml | kubectl -n "$NAMESPACE" apply -f -
wait_hr_ready "$MONGODB_SRC_CR" 300
wait_psmdb_ready "$MONGODB_SRC_CR" 600

print_header "Step 05b: Write a sentinel document so the backup has something to prove"
SENTINEL_TOKEN="e2e-$(date +%s)-$$"
mongosh_exec "$MONGODB_SRC_CR" \
    "db.getSiblingDB('demo').e2e_sentinel.replaceOne({_id:1},{_id:1,token:'${SENTINEL_TOKEN}'},{upsert:true})"
log_success "Sentinel token: ${SENTINEL_TOKEN}"

print_header "Step 10: Submit ad-hoc BackupJob '${BACKUPJOB_NAME}' and wait for Succeeded"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/10-backupjob-adhoc.yaml"
wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
log_success "Backup artefact: ${BACKUP_NAME}"

if [[ "${SKIP_RESTORE:-0}" == "1" ]]; then
    # hack/e2e-chainsaw/mongodb/ asserts this line to prove the skip really
    # happened, so rewording it reds that suite with a bare match failure.
    log_warning "SKIP_RESTORE=1: stopping after a successful backup."
    exit 0
fi

print_header "Step 20/30: Restore to a copy '${MONGODB_TARGET_NAME}' and wait for Succeeded"
subst 20-mongodb-target.yaml | kubectl -n "$NAMESPACE" apply -f -
wait_hr_ready "$MONGODB_TARGET_CR" 300
wait_psmdb_ready "$MONGODB_TARGET_CR" 600
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/30-restorejob-to-copy.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed

print_header "Step 30 verify: the sentinel round-tripped through S3 into the copy"
GOT=$(mongosh_exec "$MONGODB_TARGET_CR" \
    "print(db.getSiblingDB('demo').e2e_sentinel.findOne({_id:1}).token)" | tr -d '[:space:]')
if [[ "$GOT" != "$SENTINEL_TOKEN" ]]; then
    log_error "sentinel mismatch: target has '${GOT}', expected '${SENTINEL_TOKEN}'"
    exit 1
fi
log_success "Round-trip verified: '${MONGODB_TARGET_NAME}' restored sentinel '${GOT}' from S3."

# To-copy must not mutate the source. Regressing into a source-touching restore
# would corrupt the running instance, so assert the source still reads back.
SRC_GOT=$(mongosh_exec "$MONGODB_SRC_CR" \
    "print(db.getSiblingDB('demo').e2e_sentinel.findOne({_id:1}).token)" | tr -d '[:space:]')
if [[ "$SRC_GOT" != "$SENTINEL_TOKEN" ]]; then
    log_error "source sentinel changed after to-copy restore: source has '${SRC_GOT}', expected '${SENTINEL_TOKEN}'"
    exit 1
fi
log_success "Source '${MONGODB_SRC_NAME}' left untouched by the to-copy restore."
