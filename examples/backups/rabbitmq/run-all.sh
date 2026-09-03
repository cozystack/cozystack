#!/bin/bash
# End-to-end RabbitMQ definitions backup/restore demo. It proves DATA
# INTEGRITY, not liveness: a sentinel vhost (a broker definition) is seeded,
# backed up, dropped, and must reappear after a restore — first in place, then
# into a separate copy.
#
# The demo provisions its OWN Bucket + Rabbitmq strategy + BackupClass, filling
# the REPLACE_WITH_* markers in 03-rabbitmq-strategy.yaml from the Bucket, so the
# round-trip is self-contained and its S3 objects tear down with the demo. The
# platform-shipped cozy-default-rabbitmq strategy (which writes to the shared
# cozy-backups system bucket) is left untouched; this demo only reads the client
# image off it.
#
# Env knobs:
#   NAMESPACE     (default tenant-root)
#   SKIP_RESTORE=1  stop after a successful backup (backup-only smoke)
#   S3_ENDPOINT   override the S3 endpoint the backup Pod uses. BucketInfo
#                 advertises the EXTERNAL ingress endpoint, which in-cluster
#                 Pods cannot always reach or TLS-validate; the in-cluster
#                 alternative is https://seaweedfs-s3.<ns>.svc:8333 — the .svc
#                 FQDN is the name the seaweedfs serving cert's SAN covers, so
#                 curl verifies TLS against the copied CA below (the 2-label
#                 seaweedfs-s3.<ns> is not in the SAN). CI sets this; a real
#                 cluster can leave it unset to use the advertised endpoint.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

# Fill the strategy manifest's placeholders from the provisioned Bucket and the
# platform client image. $BUCKET / $S3_ENDPOINT / $CLIENT_IMAGE are resolved
# below; using '|' as the sed delimiter keeps the image ref's slashes intact.
subst() {
    sed \
        -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${BUCKET}|g" \
        -e "s|REPLACE_WITH_S3_ENDPOINT|${S3_ENDPOINT}|g" \
        -e "s|REPLACE_WITH_BACKUP_CLIENT_IMAGE|${CLIENT_IMAGE}|g" \
        "$SCRIPT_DIR/$1"
}

print_header "RabbitMQ definitions backup/restore demo (namespace: $NAMESPACE)"

# --- Bucket ------------------------------------------------------------------
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
S3_ENDPOINT="${S3_ENDPOINT:-$(jq -r '.spec.secretS3.endpoint' "$TMP")}"
BUCKET=$(jq -r '.spec.bucketName' "$TMP")
for v in S3_ACCESS_KEY S3_SECRET_KEY S3_ENDPOINT BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done
# The strategy's curl accepts a full URL (http:// or https://), so keep the
# scheme rather than stripping it as the mariadb-operator path does.
case "$S3_ENDPOINT" in http://*|https://*) : ;; *) S3_ENDPOINT="https://${S3_ENDPOINT}" ;; esac
log_success "Bucket '${BUCKET}' at endpoint '${S3_ENDPOINT}'."

# --- Secrets the strategy references -----------------------------------------
print_header "Step 01: Materialise the Secrets the Rabbitmq strategy references"
# Resolve the S3 endpoint CA secret. The default name tracks the seaweedfs
# chart's fullnameOverride (seaweedfs -> seaweedfs-ca-cert), but a downstream
# fullname change would rename it, so fall back to discovering the cert-manager
# CA Certificate (the seaweedfs-labelled one with spec.isCA=true) and read its
# secretName. Leave S3_CA_SECRET empty to skip the copy on a public-CA endpoint.
if [[ -n "$S3_CA_SECRET" ]] \
    && ! kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" >/dev/null 2>&1; then
    log_warning "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} not found; discovering the seaweedfs CA Certificate..."
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

log_substep "Projecting bucket access keys into ${CREDS_SECRET}..."
kubectl -n "$NAMESPACE" create secret generic "$CREDS_SECRET" \
    --from-literal="AWS_ACCESS_KEY_ID=${S3_ACCESS_KEY}" \
    --from-literal="AWS_SECRET_ACCESS_KEY=${S3_SECRET_KEY}" \
    --dry-run=client -o yaml | kubectl -n "$NAMESPACE" apply -f -

if [[ -n "$S3_CA_SECRET" ]]; then
    log_substep "Copying S3 CA ${S3_CA_NAMESPACE}/${S3_CA_SECRET}[${S3_CA_KEY}] -> ${CA_SECRET}..."
    CA_PEM=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" \
        -o jsonpath="{.data.${S3_CA_KEY//./\\.}}" | base64 -d)
    [[ -n "$CA_PEM" ]] || { log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} has no ${S3_CA_KEY}"; exit 1; }
    kubectl -n "$NAMESPACE" create secret generic "$CA_SECRET" \
        --from-literal="ca.crt=${CA_PEM}" \
        --dry-run=client -o yaml | kubectl -n "$NAMESPACE" apply -f -
else
    # The strategy still mounts CA_SECRET; create an empty one so the Pod
    # schedules, and warn: a public-CA endpoint should have the CURL_CA_BUNDLE
    # env + CA volume dropped from 03-rabbitmq-strategy.yaml by hand instead.
    log_warning "S3_CA_SECRET empty: creating an empty ${CA_SECRET} so the Pod schedules."
    log_warning "Remove the CURL_CA_BUNDLE env + s3-ca volume from 03-rabbitmq-strategy.yaml when the S3 endpoint uses a public CA."
    kubectl -n "$NAMESPACE" create secret generic "$CA_SECRET" \
        --from-literal="ca.crt=" \
        --dry-run=client -o yaml | kubectl -n "$NAMESPACE" apply -f -
fi

# --- Strategy + BackupClass --------------------------------------------------
print_header "Step 02: Create the Rabbitmq strategy + BackupClass"
# Reuse the image the platform's cozy-default-rabbitmq strategy runs, so the demo
# Pod needs no package install at run time. Its presence also confirms the
# platform default backups stack (backupstrategy-controller + CRDs) is installed,
# without which the BackupJob below cannot reconcile at all.
CLIENT_IMAGE=$(kubectl get rabbitmqs.strategy.backups.cozystack.io cozy-default-rabbitmq \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="rabbitmq-backup")].image}' 2>/dev/null || true)
[[ -n "$CLIENT_IMAGE" ]] || { log_error "cozy-default-rabbitmq strategy not found: enable the platform default backups (backupstrategy-controller) before running this demo"; exit 1; }
log_substep "Reusing the platform strategy's client image: ${CLIENT_IMAGE}"
subst 03-rabbitmq-strategy.yaml | kubectl apply -f -
kubectl apply -f "$SCRIPT_DIR/04-backupclass.yaml"

# --- Source application + sentinel -------------------------------------------
log_step "Provisioning source RabbitMQ '$RABBITMQ_SRC_NAME'"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/05-rabbitmq-src.yaml"
wait_hr_ready "$RABBITMQ_SRC_CR" 300
wait_rabbitmq_ready "$RABBITMQ_SRC_CR" 600

SENTINEL="sentinel-$(date +%s)-$$"
log_step "Seeding sentinel definition (vhost + policy): $SENTINEL"
rabbitmq_seed_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL"
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    || { log_error "sentinel vhost did not seed on the source"; exit 1; }
log_success "sentinel present on the source"

# --- Backup ------------------------------------------------------------------
log_step "Creating ad-hoc BackupJob (backupClassName=$BACKUPCLASS_NAME)"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/10-backupjob-adhoc.yaml"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/15-plan.yaml"
wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed

BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
ARTIFACT_URI=$(kubectl -n "$NAMESPACE" get backups.backups.cozystack.io "$BACKUP_NAME" \
    -o jsonpath='{.status.artifact.uri}' 2>/dev/null || true)
log_success "backup complete: Backup/$BACKUP_NAME (artifact: ${ARTIFACT_URI:-<none>})"

if [[ "${SKIP_RESTORE:-0}" == "1" ]]; then
    log_warning "SKIP_RESTORE=1: stopping after a successful backup."
    exit 0
fi

# --- In-place restore --------------------------------------------------------
log_step "Dropping the sentinel, then restoring in place"
rabbitmqctl_exec "$RABBITMQ_SRC_CR" delete_vhost "$SENTINEL" >/dev/null
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    && { log_error "sentinel vhost still present after delete_vhost"; exit 1; }
log_substep "sentinel dropped from the source"

kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/25-restorejob-in-place.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    || { log_error "in-place restore did not bring back the sentinel vhost"; exit 1; }
log_success "in-place restore round-tripped the sentinel"

# --- Restore-to-copy ---------------------------------------------------------
log_step "Provisioning target RabbitMQ '$RABBITMQ_TARGET_NAME' and restoring the source's definitions into it"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/20-rabbitmq-target.yaml"
wait_hr_ready "$RABBITMQ_TARGET_CR" 300
wait_rabbitmq_ready "$RABBITMQ_TARGET_CR" 600
rabbitmq_has_sentinel "$RABBITMQ_TARGET_CR" "$SENTINEL" \
    && { log_error "target already has the sentinel before restore — cannot prove the copy"; exit 1; }

kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/30-restorejob-to-copy.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
rabbitmq_has_sentinel "$RABBITMQ_TARGET_CR" "$SENTINEL" \
    || { log_error "to-copy restore did not put the sentinel on the target"; exit 1; }
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    || { log_error "source lost the sentinel during to-copy restore (source must stay untouched)"; exit 1; }
log_success "to-copy restore placed the sentinel on the target, source untouched"

print_header "Demo complete — definitions round-tripped through object storage"
