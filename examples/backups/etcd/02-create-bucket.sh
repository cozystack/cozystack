#!/bin/bash
# Step 02: Provision an in-cluster Bucket, mint the per-app credentials
# Secret in the source namespace, and create the BackupClass with the
# resolved S3 coordinates baked into Parameters. Folded into one step
# (vs an earlier admin/tenant split with a REPLACE_ME placeholder) so
# the BackupClass only ever exists with real coordinates - a tenant who
# applies it before the Bucket resolves cannot stumble into a
# half-configured class that passes validation but fails at backup time.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 02: Provision Bucket '${BUCKET_NAME}' and bind BackupClass to the Etcd strategy"

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

ETCD_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' "$TMP")
ETCD_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' "$TMP")
ETCD_ENDPOINT=$(jq -r '.spec.secretS3.endpoint' "$TMP")
ETCD_REGION=$(jq -r 'if (.spec.secretS3.region // "") == "" then "us-east-1" else .spec.secretS3.region end' "$TMP")
ETCD_BUCKET=$(jq -r '.spec.bucketName' "$TMP")

for v in ETCD_ACCESS_KEY ETCD_SECRET_KEY ETCD_ENDPOINT ETCD_REGION ETCD_BUCKET; do
    [[ -n "${!v}" && "${!v}" != "null" ]] || { log_error "BucketInfo missing required field: ${v}"; exit 1; }
done

log_substep "Materialising the per-app credentials Secret consumed by the rendered EtcdSnapshot..."
# The strategy template references this Secret by name:
#   "{{ .Application.metadata.name }}-etcd-backup-creds"
# The etcd-operator's snapshot Job mounts the Secret and exposes
# AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY to etcdctl's S3 client.
SECRET_NAME="${ETCD_NAME}-etcd-backup-creds"
kubectl -n "$NAMESPACE" create secret generic "$SECRET_NAME" \
    --from-literal="AWS_ACCESS_KEY_ID=${ETCD_ACCESS_KEY}" \
    --from-literal="AWS_SECRET_ACCESS_KEY=${ETCD_SECRET_KEY}" \
    --dry-run=client -o yaml | kubectl apply -f -

log_substep "Creating BackupClass '${BACKUPCLASS_NAME}' bound to the Etcd strategy..."
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: ${BACKUPCLASS_NAME}
spec:
  strategies:
    - application:
        apiGroup: apps.cozystack.io
        kind: Etcd
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Etcd
        name: ${STRATEGY_NAME}
      parameters:
        bucket: "${ETCD_BUCKET}"
        endpoint: "${ETCD_ENDPOINT}"
        region: "${ETCD_REGION}"
EOF

umask 077
{
    printf 'export ETCD_ACCESS_KEY=%q\n' "$ETCD_ACCESS_KEY"
    printf 'export ETCD_SECRET_KEY=%q\n' "$ETCD_SECRET_KEY"
    printf 'export ETCD_ENDPOINT=%q\n'   "$ETCD_ENDPOINT"
    printf 'export ETCD_REGION=%q\n'     "$ETCD_REGION"
    printf 'export ETCD_BUCKET=%q\n'     "$ETCD_BUCKET"
} > "$SCRIPT_DIR/.bucket-info.env"
chmod 600 "$SCRIPT_DIR/.bucket-info.env"

log_success "Bucket '${BUCKET_NAME}' ready; coordinates cached in $(basename "$SCRIPT_DIR")/.bucket-info.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./03-create-etcd-src.sh"
