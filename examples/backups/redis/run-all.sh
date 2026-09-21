#!/bin/bash
# End-to-end Redis backup/restore demo. It proves DATA INTEGRITY, not liveness:
# a sentinel key is seeded, backed up, and must reappear after a restore — first
# in place, then into a separate copy.
#
# The demo provisions its OWN Bucket + Redis strategy + BackupClass, filling the
# REPLACE_WITH_* markers in redis-strategy.yaml from the Bucket, so the
# round-trip is self-contained (its S3 objects tear down with the demo) and runs
# in CI, where the shared cozy-backups system bucket's advertised external
# endpoint is not routable. The platform-shipped cozy-default-redis strategy is
# left untouched; this demo only reads the client image off it.
#
# Env knobs:
#   NAMESPACE       (default tenant-root)
#   SKIP_RESTORE=1  stop after a successful backup (steps 01-02)
#   S3_ENDPOINT     override the endpoint the backup Pod uses. BucketInfo
#                   advertises the EXTERNAL ingress endpoint, which in-cluster
#                   Pods cannot always reach or TLS-validate; the in-cluster
#                   alternative is https://seaweedfs-s3.<ns>.svc:8333 — the .svc
#                   FQDN is the name the seaweedfs serving cert's SAN covers, so
#                   curl verifies TLS against the copied CA. CI sets this; a real
#                   cluster can leave it unset to use the advertised endpoint.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

# Fill the strategy manifest's placeholders from the provisioned Bucket and the
# platform client image. '|' as the sed delimiter keeps the image ref's slashes
# and the endpoint URL intact.
subst() {
    sed \
        -e "s|REPLACE_WITH_COSI_BUCKET_NAME|${BUCKET}|g" \
        -e "s|REPLACE_WITH_S3_ENDPOINT|${S3_ENDPOINT}|g" \
        -e "s|REPLACE_WITH_BACKUP_CLIENT_IMAGE|${CLIENT_IMAGE}|g" \
        "$SCRIPT_DIR/$1"
}

print_header "Redis backup/restore demo (namespace: $NAMESPACE)"

# --- Bucket ------------------------------------------------------------------
print_header "Step 00: Provision Bucket '${BUCKET_NAME}' in ${NAMESPACE}"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/bucket.yaml"
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
# The strategy script accepts a full URL (it prepends https:// only when the
# endpoint carries no scheme), so keep the scheme rather than stripping it.
case "$S3_ENDPOINT" in http://*|https://*) : ;; *) S3_ENDPOINT="https://${S3_ENDPOINT}" ;; esac
export S3_ENDPOINT
log_success "Bucket '${BUCKET}' at endpoint '${S3_ENDPOINT}'."

# --- Secrets the strategy references -----------------------------------------
print_header "Step 00b: Materialise the Secrets the Redis strategy references"
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
    # The strategy mounts CA_SECRET unconditionally; create an empty one so the
    # Pod schedules. On a public-CA endpoint the script falls through to the
    # system CA store (no /etc/s3-ca/ca.crt content to prefer).
    log_warning "S3_CA_SECRET empty: creating an empty ${CA_SECRET} so the Pod schedules (public-CA endpoint)."
    kubectl -n "$NAMESPACE" create secret generic "$CA_SECRET" \
        --from-literal="ca.crt=" \
        --dry-run=client -o yaml | kubectl -n "$NAMESPACE" apply -f -
fi

# --- Strategy + BackupClass --------------------------------------------------
print_header "Step 00c: Create the Redis strategy + BackupClass"
# Reuse the image the platform's cozy-default-redis strategy runs, so the demo
# Pod needs no package install at run time. Its presence also confirms the
# platform default backups stack (backupstrategy-controller + CRDs) is
# installed, without which the BackupJob below cannot reconcile at all.
CLIENT_IMAGE=$(kubectl get redis.strategy.backups.cozystack.io cozy-default-redis \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="redis-backup")].image}' 2>/dev/null || true)
[[ -n "$CLIENT_IMAGE" ]] || { log_error "cozy-default-redis strategy not found: enable the platform default backups (backupstrategy-controller) before running this demo"; exit 1; }
log_substep "Reusing the platform strategy's client image: ${CLIENT_IMAGE}"
subst redis-strategy.yaml | kubectl apply -f -
kubectl apply -f "$SCRIPT_DIR/backupclass.yaml"

# --- Round-trip (source + seed -> backup -> restore in place -> to a copy) ---
steps=(01 02 03 04)
[[ "${SKIP_RESTORE:-0}" == "1" ]] && steps=(01 02)

for step in "${steps[@]}"; do
    # find is glob-safe under set -euo pipefail and degrades to empty output
    # when no file matches, unlike `ls glob | head`.
    script="$(find "$SCRIPT_DIR" -maxdepth 1 -type f -name "${step}-*.sh" | sort | head -n1)"
    if [[ -z "$script" ]]; then
        echo "no script found for step $step" >&2
        exit 1
    fi
    echo "=== Running $(basename "$script") ==="
    bash "$script"
done

[[ "${SKIP_RESTORE:-0}" == "1" ]] && log_warning "SKIP_RESTORE=1: stopped after a successful backup."
print_header "Demo complete — data round-tripped through object storage"
