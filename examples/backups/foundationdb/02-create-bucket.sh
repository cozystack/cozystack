#!/bin/bash
# Step 02: Provision an in-cluster Bucket, mint the FoundationDB
# blob_credentials.json Secret in each app namespace (the source CR's
# namespace today; the to-copy target gets the same Secret name mirrored in
# its own namespace - here both are in NAMESPACE), and patch the
# BackupClass parameters to point at the Bucket's S3 endpoint.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 02: Provision Bucket '${BUCKET_NAME}', bind BackupClass to FoundationDB strategy, project credentials"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${BUCKET_NAME}
  namespace: ${NAMESPACE}
spec:
  users:
    backup:
      readonly: false
EOF

log_substep "Waiting for bucket HelmRelease to be Ready..."
kubectl -n "$NAMESPACE" wait hr "bucket-${BUCKET_NAME}" --for=condition=ready --timeout=300s
kubectl -n "$NAMESPACE" wait bucketclaims.objectstorage.k8s.io "bucket-${BUCKET_NAME}" --for=jsonpath='{.status.bucketReady}'=true --timeout=300s
kubectl -n "$NAMESPACE" wait bucketaccesses.objectstorage.k8s.io "bucket-${BUCKET_NAME}-backup" --for=jsonpath='{.status.accessGranted}'=true --timeout=300s

log_substep "Reading bucket coordinates from BucketInfo Secret..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
kubectl -n "$NAMESPACE" get secret "bucket-${BUCKET_NAME}-backup" -o jsonpath='{.data.BucketInfo}' | base64 -d > "$TMP"

FDB_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' "$TMP")
FDB_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' "$TMP")
FDB_ENDPOINT_FULL=$(jq -r '.spec.secretS3.endpoint' "$TMP")
FDB_REGION=$(jq -r 'if (.spec.secretS3.region // "") == "" then "us-east-1" else .spec.secretS3.region end' "$TMP")
FDB_BUCKET=$(jq -r '.spec.bucketName' "$TMP")

for v in FDB_ACCESS_KEY FDB_SECRET_KEY FDB_ENDPOINT_FULL FDB_REGION FDB_BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done

# FoundationDB's backup_agent treats accountName as "<api_key>@<host>". The
# host portion is the S3 endpoint host:port without scheme; strip
# http://|https:// off and we're done. seaweedfs-s3 advertises plaintext on
# port 8333 for in-cluster service traffic by default.
FDB_ENDPOINT_HOSTPORT=${FDB_ENDPOINT_FULL#http://}
FDB_ENDPOINT_HOSTPORT=${FDB_ENDPOINT_HOSTPORT#https://}
FDB_ACCOUNT_NAME="${FDB_ACCESS_KEY}@${FDB_ENDPOINT_HOSTPORT}"

# Determine whether the endpoint is TLS-terminated. When the BucketInfo
# endpoint URL starts with https:// we toggle the operator's
# secure_connection url parameter to 1. seaweedfs-s3 default in cozystack
# is plaintext on the in-tenant service.
SECURE_CONNECTION="0"
case "$FDB_ENDPOINT_FULL" in
    https://*) SECURE_CONNECTION="1" ;;
esac

log_substep "Materialising blob_credentials.json Secret for FoundationDB instances..."
# The strategy template references this Secret by name:
#   "{{ .Application.metadata.name }}-fdb-backup-creds"
# We create one Secret per app (source and to-copy target) so both
# FoundationDBBackup and FoundationDBRestore CRs can read the same
# blob-store credentials regardless of which app is the operand.
create_creds_secret() {
    local app_name="$1"
    local secret_name="${app_name}-fdb-backup-creds"
    local creds_json
    creds_json=$(jq -nc \
        --arg account "$FDB_ACCOUNT_NAME" \
        --arg key "$FDB_ACCESS_KEY" \
        --arg secret "$FDB_SECRET_KEY" \
        '{accounts: {($account): {api_key: $key, secret: $secret}}}')
    kubectl -n "$NAMESPACE" create secret generic "$secret_name" \
        --from-literal="blob_credentials.json=${creds_json}" \
        --dry-run=client -o yaml | kubectl apply -f -
}
create_creds_secret "$FDB_NAME"
create_creds_secret "$FDB_RESTORE_NAME"

log_substep "Creating BackupClass '${BACKUPCLASS_NAME}' bound to the FoundationDB strategy..."
# This is the single point at which the BackupClass is created. The
# earlier draft of this demo applied a placeholder version of the same
# resource in a separate "step 02" before the Bucket existed, then
# patched it here once the Bucket coordinates resolved. The placeholder
# wrote `accountName: "REPLACE_ME"` which passed the operator's
# MinLength=1 validation but failed at backup_agent runtime - a tenant
# who skipped this step burned 45 wall-clock minutes on the backup
# deadline before finding out. Folding the apply here means the
# BackupClass exists only with resolved coordinates, and there is no
# half-configured state that a tenant can stumble into.
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: ${BACKUPCLASS_NAME}
spec:
  strategies:
    - application:
        apiGroup: apps.cozystack.io
        kind: FoundationDB
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: FoundationDB
        name: ${STRATEGY_NAME}
      parameters:
        accountName: "${FDB_ACCOUNT_NAME}"
        bucket: "${FDB_BUCKET}"
        region: "${FDB_REGION}"
        secureConnection: "${SECURE_CONNECTION}"
EOF

# Persist for the next steps in a chmod-600 cache (raw access keys).
# Use printf %q so values containing spaces, quotes, or shell
# metacharacters round-trip through `source .bucket-info.env`. A bare
# here-doc would emit `export VAR=foo bar` for a multi-word value,
# which silently truncates VAR to "foo" and turns "bar" into a stray
# command. The COSI bucket coords are unlikely to contain spaces in
# practice, but the cache file is sourced by other scripts in this
# directory and the shell-safety guarantee is cheap.
umask 077
{
    printf 'export FDB_ACCESS_KEY=%q\n'        "$FDB_ACCESS_KEY"
    printf 'export FDB_SECRET_KEY=%q\n'        "$FDB_SECRET_KEY"
    printf 'export FDB_ENDPOINT_FULL=%q\n'     "$FDB_ENDPOINT_FULL"
    printf 'export FDB_ENDPOINT_HOSTPORT=%q\n' "$FDB_ENDPOINT_HOSTPORT"
    printf 'export FDB_REGION=%q\n'            "$FDB_REGION"
    printf 'export FDB_BUCKET=%q\n'            "$FDB_BUCKET"
    printf 'export FDB_ACCOUNT_NAME=%q\n'      "$FDB_ACCOUNT_NAME"
    printf 'export SECURE_CONNECTION=%q\n'     "$SECURE_CONNECTION"
} > "$SCRIPT_DIR/.bucket-info.env"
chmod 600 "$SCRIPT_DIR/.bucket-info.env"

log_success "Bucket '${BUCKET_NAME}' ready; coordinates cached in $(basename "$SCRIPT_DIR")/.bucket-info.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./03-create-foundationdb-src.sh"
