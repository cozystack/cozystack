#!/bin/bash
# Step 03: Provision an in-cluster Bucket and cache its S3 coordinates. The
# generic Job strategy has no chart support to emit a backup Secret, so the
# tenant materialises the "<app>-backup-s3" Secret itself. This step only
# caches the coordinates; create_s3_secret (00-helpers.sh) turns them into
# per-app Secrets in steps 04 and 07.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 03: Provision Bucket '${BUCKET_NAME}' in '${NAMESPACE}'"

log_command "kubectl apply -f - (Bucket: $BUCKET_NAME)"
# spec.users.backup is required for the Bucket app to provision a
# BucketAccess (and credentials Secret) the backup tooling can read.
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

wait_hr_ready "bucket-${BUCKET_NAME}"
kubectl -n "$NAMESPACE" wait bucketclaims.objectstorage.k8s.io "bucket-${BUCKET_NAME}" --for=jsonpath='{.status.bucketReady}'=true --timeout=300s
# Cozystack's bucket app provisions a BucketAccess named "<bucket-name>-backup"
# (the "-backup" suffix is the BucketAccessClass name); the BucketInfo Secret
# carries the same name.
kubectl -n "$NAMESPACE" wait bucketaccesses.objectstorage.k8s.io "bucket-${BUCKET_NAME}-backup" --for=jsonpath='{.status.accessGranted}'=true --timeout=300s

# E2E only: accessGranted describes the COSI object, not whether the S3 server
# has reloaded the new IAM identity, so a freshly granted key can still answer
# the strategy Pod's SigV4 PUT with 403 - which reds the run as a backup defect
# rather than the grant-propagation race it is. The shared preflight proves the
# key works before anything depends on it; the other backup round-trips gate it
# the same way.
if [[ "${COZY_E2E_BACKUP_PREFLIGHT:-0}" == "1" ]]; then
    # shellcheck source=/dev/null
    source "$SCRIPT_DIR/../../../hack/e2e-chainsaw/_lib/backup-access-preflight.sh"
    cozy_backup_access_preflight "$NAMESPACE" "bucket-${BUCKET_NAME}-backup" 90
fi

log_substep "Reading bucket coordinates from BucketInfo Secret..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
kubectl -n "$NAMESPACE" get secret "bucket-${BUCKET_NAME}-backup" -o jsonpath='{.data.BucketInfo}' | base64 -d > "$TMP"

S3_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' "$TMP")
S3_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' "$TMP")
# S3_ENDPOINT can be overridden via the environment: BucketInfo advertises the
# external ingress URL, which in CI is an unroutable placeholder; the e2e
# harness points this at the in-cluster seaweedfs Service instead.
S3_ENDPOINT="${S3_ENDPOINT:-$(jq -r '.spec.secretS3.endpoint' "$TMP")}"
S3_REGION=$(jq -r 'if (.spec.secretS3.region // "") == "" then "us-east-1" else .spec.secretS3.region end' "$TMP")
S3_BUCKET=$(jq -r '.spec.bucketName' "$TMP")

# `jq -r` returns the literal string "null" for missing JSON paths; fail fast
# here so a missing field surfaces at extraction time instead of as a confusing
# curl/SigV4 error inside the backup Pod.
for v in S3_ACCESS_KEY S3_SECRET_KEY S3_ENDPOINT S3_REGION S3_BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done

# The endpoint's CA, so the strategy Pod verifies TLS instead of skipping it.
# The default name tracks the seaweedfs chart's fullnameOverride (seaweedfs ->
# seaweedfs-ca-cert), but a downstream fullname change would rename it, so fall
# back to discovering the cert-manager CA Certificate (the seaweedfs-labelled
# one with spec.isCA=true) and read its secretName. S3_CA_SECRET="" skips the
# copy for a publicly-trusted endpoint (see 00-helpers.sh).
S3_CA_B64=""
if [[ -n "$S3_CA_SECRET" ]]; then
    # Only an absent Secret earns a discovery pass. The read crosses into
    # another namespace, which a tenant persona is not granted, and a denial
    # must say so rather than surface as "not found" and point at a name
    # that cannot help.
    if ! err=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" 2>&1 >/dev/null); then
        case "$err" in
            *NotFound*|*"not found"*) ;;
            *)
                log_error "Cannot read S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET}: ${err}"
                log_error "Run this step with a persona that can read Secrets in ${S3_CA_NAMESPACE}, or set S3_CA_SECRET=\"\" for a publicly-trusted endpoint."
                exit 1 ;;
        esac
        log_warning "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} not found; discovering the seaweedfs CA Certificate..."
        discovered=$(kubectl -n "$S3_CA_NAMESPACE" get certificates.cert-manager.io \
            -l app.kubernetes.io/name=seaweedfs \
            -o jsonpath='{range .items[*]}{.spec.isCA}{" "}{.spec.secretName}{"\n"}{end}' \
            | awk '$1=="true"{print $2; exit}' || true)
        [[ -n "$discovered" ]] || { log_error "No seaweedfs CA Certificate found in ${S3_CA_NAMESPACE} (or the read was denied, see above); set S3_CA_SECRET explicitly (or empty for a publicly-trusted endpoint)."; exit 1; }
        log_success "Discovered seaweedfs CA secret ${S3_CA_NAMESPACE}/${discovered}"
        S3_CA_SECRET="$discovered"
    fi
    log_substep "Caching S3 CA ${S3_CA_NAMESPACE}/${S3_CA_SECRET}[${S3_CA_KEY}]..."
    S3_CA_B64=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" -o jsonpath="{.data.${S3_CA_KEY//./\\.}}")
    [[ -n "$S3_CA_B64" ]] || { log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} has no ${S3_CA_KEY}"; exit 1; }
fi

# Persist for steps 04 / 07. The cache stores raw S3 credentials, so apply
# restrictive perms before writing the body - umask alone could leave the file
# group/world-readable. The CA stays base64 so the file remains one export per
# line.
umask 077
cat > "$SCRIPT_DIR/.bucket-info.env" <<ENV
export S3_ACCESS_KEY=${S3_ACCESS_KEY}
export S3_SECRET_KEY=${S3_SECRET_KEY}
export S3_ENDPOINT=${S3_ENDPOINT}
export S3_REGION=${S3_REGION}
export S3_BUCKET=${S3_BUCKET}
export S3_CA_B64=${S3_CA_B64}
ENV
chmod 600 "$SCRIPT_DIR/.bucket-info.env"

log_success "Bucket '${BUCKET_NAME}' ready; coordinates cached in $(basename "$SCRIPT_DIR")/.bucket-info.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-create-kafka.sh"
