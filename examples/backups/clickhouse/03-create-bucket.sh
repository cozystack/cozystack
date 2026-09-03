#!/bin/bash
# Step 03: Provision an in-cluster Bucket and stash its S3 coordinates so step
# 04 can pass them to the ClickHouse application's `backup.*` values. The
# clickhouse chart materialises `<release>-backup-s3` and the
# clickhouse-backup sidecar from those values; the backup strategy then
# consumes the chart-emitted Secret without any tenant-side glue.
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

log_substep "Waiting for bucket HelmRelease to be Ready..."
wait_hr_ready "bucket-${BUCKET_NAME}" 300
kubectl -n "$NAMESPACE" wait bucketclaims.objectstorage.k8s.io "bucket-${BUCKET_NAME}" --for=jsonpath='{.status.bucketReady}'=true --timeout=180s
# Cozystack's bucket app provisions a BucketAccess named "<bucket-name>-backup"
# (the "-backup" suffix is the BucketAccessClass name); the BucketInfo Secret
# carries the same name.
kubectl -n "$NAMESPACE" wait bucketaccesses.objectstorage.k8s.io "bucket-${BUCKET_NAME}-backup" --for=jsonpath='{.status.accessGranted}'=true --timeout=180s

log_substep "Reading bucket coordinates from BucketInfo Secret..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
kubectl -n "$NAMESPACE" get secret "bucket-${BUCKET_NAME}-backup" -o jsonpath='{.data.BucketInfo}' | base64 -d > "$TMP"

CH_BACKUP_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' "$TMP")
CH_BACKUP_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' "$TMP")
# S3_ENDPOINT can be overridden via the environment: BucketInfo advertises the
# EXTERNAL ingress endpoint, which in-cluster Pods cannot always reach or
# TLS-validate (in CI it is the unroutable s3.example.org placeholder); the
# in-cluster alternative is https://seaweedfs-s3.<ns>:8333, trusted via the CA
# copied below.
CH_BACKUP_ENDPOINT="${S3_ENDPOINT:-$(jq -r '.spec.secretS3.endpoint' "$TMP")}"
CH_BACKUP_REGION=$(jq -r 'if (.spec.secretS3.region // "") == "" then "us-east-1" else .spec.secretS3.region end' "$TMP")
CH_BACKUP_BUCKET=$(jq -r '.spec.bucketName' "$TMP")

# `jq -r` returns the literal string "null" for missing JSON paths; fail fast
# here so the missing field surfaces at extraction time instead of as a
# confusing helm/CHI error after the cache is sourced by 04 / 07.
for v in CH_BACKUP_ACCESS_KEY CH_BACKUP_SECRET_KEY CH_BACKUP_ENDPOINT CH_BACKUP_REGION CH_BACKUP_BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done

# Resolve and copy the S3 endpoint CA. The default name tracks the seaweedfs
# chart's fullnameOverride (seaweedfs -> seaweedfs-ca-cert), but a downstream
# fullname change would rename it, so fall back to discovering the cert-manager
# CA Certificate (the seaweedfs-labelled one with spec.isCA=true) and read its
# secretName. Leave S3_CA_SECRET empty to skip the copy on a public-CA endpoint.
CH_BACKUP_CA_SECRET=""
if [[ -n "$S3_CA_SECRET" ]]; then
    # Reading the platform's CA lives in S3_CA_NAMESPACE (tenant-root by
    # default), which is an admin/platform capability, not a tenant one — see
    # the note on step 03 in README.md. So the three outcomes have to be kept
    # apart: present, absent, and not-allowed-to-look. Swallowing stderr here
    # would report an RBAC denial as "not found" and then advise setting
    # S3_CA_SECRET, which cannot fix a permissions problem.
    ca_probe_err=""
    if ! ca_probe_err=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" -o name 2>&1 >/dev/null); then
        if echo "$ca_probe_err" | grep -qiE 'forbidden|cannot (get|list)|Unauthorized'; then
            log_error "Cannot read ${S3_CA_NAMESPACE}/${S3_CA_SECRET}: ${ca_probe_err}"
            log_error "This is a permissions problem, not a missing Secret: copying the S3 endpoint CA needs read access to ${S3_CA_NAMESPACE}. Ask an admin to copy it into ${NAMESPACE}/${CH_CA_SECRET_NAME}, then re-run with S3_CA_SECRET=\"\" to skip the copy."
            exit 1
        fi
        log_warning "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} not found; discovering the seaweedfs CA Certificate..."
        # List every seaweedfs Certificate as "<isCA> <secretName>" and pick the
        # CA one in shell — avoids kubectl jsonpath's finicky boolean-literal
        # filter.
        DISCOVERED_CA=$(kubectl -n "$S3_CA_NAMESPACE" get certificates.cert-manager.io \
            -l app.kubernetes.io/name=seaweedfs \
            -o jsonpath='{range .items[*]}{.spec.isCA}{" "}{.spec.secretName}{"\n"}{end}' 2>/dev/null \
            | awk '$1=="true"{print $2; exit}' || true)
        if [[ -n "$DISCOVERED_CA" ]]; then
            log_success "Discovered seaweedfs CA secret ${S3_CA_NAMESPACE}/${DISCOVERED_CA}"
            S3_CA_SECRET="$DISCOVERED_CA"
        elif [[ "${S3_CA_SECRET_EXPLICIT}" == "1" ]]; then
            # The user named a specific Secret and it is not there: that is a
            # typo or a missing prerequisite, and guessing past it would hide it.
            log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} does not exist and no seaweedfs CA Certificate was found in ${S3_CA_NAMESPACE}. Fix the name, or set S3_CA_SECRET=\"\" for a publicly-trusted endpoint."
            exit 1
        else
            # Nothing was named and there is no seaweedfs here, so this is most
            # likely a cluster whose S3 endpoint is publicly trusted — a legal
            # input for this demo. Warn and continue with endpointCA unset
            # rather than failing on a default the user never chose.
            log_warning "No seaweedfs CA found in ${S3_CA_NAMESPACE} and S3_CA_SECRET was not set explicitly."
            log_warning "Continuing with backup.endpointCA unset — correct for a publicly-trusted S3 endpoint. If your endpoint uses a private CA, set S3_CA_SECRET to the Secret holding its bundle."
            S3_CA_SECRET=""
        fi
    fi
fi
if [[ -n "$S3_CA_SECRET" ]]; then
    log_substep "Copying S3 CA ${S3_CA_NAMESPACE}/${S3_CA_SECRET}[${S3_CA_KEY}] -> ${CH_CA_SECRET_NAME}..."
    # The destination name is predictable, so don't clobber a Secret this demo
    # does not own. A get that succeeds returns the ownership label (empty if
    # absent); a get that fails means the Secret is gone, which is fine.
    if existing_owner=$(kubectl -n "$NAMESPACE" get secret "$CH_CA_SECRET_NAME" \
            -o jsonpath="{.metadata.labels.${CH_CA_SECRET_LABEL_KEY//./\\.}}" 2>/dev/null); then
        if [[ "$existing_owner" != "$CH_CA_SECRET_LABEL_VALUE" ]]; then
            log_error "Secret ${NAMESPACE}/${CH_CA_SECRET_NAME} already exists and is not owned by this demo (missing ${CH_CA_SECRET_LABEL_KEY}=${CH_CA_SECRET_LABEL_VALUE}). Refusing to overwrite it; set CH_CA_SECRET_NAME to a free name."
            exit 1
        fi
    fi
    # The dot in the default key has to be escaped for kubectl jsonpath.
    ca_pem=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" \
        -o jsonpath="{.data.${S3_CA_KEY//./\\.}}" | base64 -d)
    [[ -n "$ca_pem" ]] || { log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} has no ${S3_CA_KEY}"; exit 1; }
    # apply (not create) so a stale copy from an earlier run is corrected;
    # kubectl label --local stamps the ownership label into the manifest.
    kubectl -n "$NAMESPACE" create secret generic "$CH_CA_SECRET_NAME" \
        --from-literal="ca.crt=${ca_pem}" \
        --dry-run=client -o yaml | \
        kubectl label --local -f - "${CH_CA_SECRET_LABEL_KEY}=${CH_CA_SECRET_LABEL_VALUE}" \
            --dry-run=client -o yaml | \
        kubectl apply -f -
    CH_BACKUP_CA_SECRET="$CH_CA_SECRET_NAME"
else
    log_warning "S3_CA_SECRET empty: leaving backup.endpointCA unset (assumes a publicly-trusted S3 endpoint)."
fi

# Persist for the next step. 04-create-clickhouse.sh sources this file when it
# applies the ClickHouse spec so the chart can render <release>-backup-s3.
# The cache stores raw S3 credentials, so apply restrictive perms before
# writing the body - umask alone could leave the file group/world-readable.
umask 077
cat > "$SCRIPT_DIR/.bucket-info.env" <<ENV
export CH_BACKUP_ACCESS_KEY=${CH_BACKUP_ACCESS_KEY}
export CH_BACKUP_SECRET_KEY=${CH_BACKUP_SECRET_KEY}
export CH_BACKUP_ENDPOINT=${CH_BACKUP_ENDPOINT}
export CH_BACKUP_REGION=${CH_BACKUP_REGION}
export CH_BACKUP_BUCKET=${CH_BACKUP_BUCKET}
export CH_BACKUP_CA_SECRET=${CH_BACKUP_CA_SECRET}
ENV
chmod 600 "$SCRIPT_DIR/.bucket-info.env"

log_success "Bucket '${BUCKET_NAME}' ready; coordinates cached in $(basename "$SCRIPT_DIR")/.bucket-info.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-create-clickhouse.sh"
