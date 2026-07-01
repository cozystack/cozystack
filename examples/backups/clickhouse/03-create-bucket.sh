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
kubectl -n "$NAMESPACE" wait hr "bucket-${BUCKET_NAME}" --for=condition=ready --timeout=300s
kubectl -n "$NAMESPACE" wait bucketclaims.objectstorage.k8s.io "bucket-${BUCKET_NAME}" --for=jsonpath='{.status.bucketReady}'=true --timeout=300s
# Cozystack's bucket app provisions a BucketAccess named "<bucket-name>-backup"
# (the "-backup" suffix is the BucketAccessClass name); the BucketInfo Secret
# carries the same name.
kubectl -n "$NAMESPACE" wait bucketaccesses.objectstorage.k8s.io "bucket-${BUCKET_NAME}-backup" --for=jsonpath='{.status.accessGranted}'=true --timeout=300s

log_substep "Reading bucket coordinates from BucketInfo Secret..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
kubectl -n "$NAMESPACE" get secret "bucket-${BUCKET_NAME}-backup" -o jsonpath='{.data.BucketInfo}' | base64 -d > "$TMP"

CH_BACKUP_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' "$TMP")
CH_BACKUP_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' "$TMP")
CH_BACKUP_ENDPOINT=$(jq -r '.spec.secretS3.endpoint' "$TMP")
CH_BACKUP_REGION=$(jq -r 'if (.spec.secretS3.region // "") == "" then "us-east-1" else .spec.secretS3.region end' "$TMP")
CH_BACKUP_BUCKET=$(jq -r '.spec.bucketName' "$TMP")

# `jq -r` returns the literal string "null" for missing JSON paths; fail fast
# here so the missing field surfaces at extraction time instead of as a
# confusing helm/CHI error after the cache is sourced by 04 / 07.
for v in CH_BACKUP_ACCESS_KEY CH_BACKUP_SECRET_KEY CH_BACKUP_ENDPOINT CH_BACKUP_REGION CH_BACKUP_BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done

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
ENV
chmod 600 "$SCRIPT_DIR/.bucket-info.env"

log_success "Bucket '${BUCKET_NAME}' ready; coordinates cached in $(basename "$SCRIPT_DIR")/.bucket-info.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-create-clickhouse.sh"
