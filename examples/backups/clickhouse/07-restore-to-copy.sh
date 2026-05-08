#!/bin/bash
# Step 07: To-copy restore. Provision a second ClickHouse application and
# restore the same backup into it via RestoreJob.spec.targetApplicationRef.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"
# Same Bucket coordinates that were minted by step 03 are reused for the
# restore target so its sidecar talks to the same S3.
[[ -f "$SCRIPT_DIR/.bucket-info.env" ]] || { log_error "missing $SCRIPT_DIR/.bucket-info.env; run 03-create-bucket.sh first"; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.bucket-info.env"
# Defence in depth: 03-create-bucket.sh validates the BucketInfo fields, but
# the cache file may have been hand-edited or corrupted between the two
# runs. Refuse to apply the restore target with empty creds rather than
# letting the chart re-emit a half-empty backup-s3 Secret.
for v in CH_BACKUP_BUCKET CH_BACKUP_REGION CH_BACKUP_ENDPOINT CH_BACKUP_ACCESS_KEY CH_BACKUP_SECRET_KEY; do
    [[ -n "${!v:-}" ]] || { log_error "required variable is missing or empty: ${v}"; exit 1; }
done

print_header "Step 07: To-copy restore into '${CLICKHOUSE_RESTORE_NAME}'"

# backup.enabled=true so the chart materialises the same sidecar pattern in
# the target Pod; the strategy Pod's HTTP call hits the target's sidecar and
# pulls from the same S3 backup.
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: ${CLICKHOUSE_RESTORE_NAME}
  namespace: ${NAMESPACE}
spec:
  size: 2Gi
  logStorageSize: 512Mi
  shards: 1
  replicas: 1
  resources: {}
  resourcesPreset: small
  backup:
    enabled: true
    s3Bucket: "${CH_BACKUP_BUCKET}"
    s3Region: "${CH_BACKUP_REGION}"
    endpoint: "${CH_BACKUP_ENDPOINT}"
    s3AccessKey: "${CH_BACKUP_ACCESS_KEY}"
    s3SecretKey: "${CH_BACKUP_SECRET_KEY}"
    # Sidecars scope backups under the release name by default. To-copy
    # restore needs the destination's sidecar to read the *source*
    # release's prefix, so we pin s3PathOverride to the source release
    # name. The chart's clickhouse-rd prefixes the user-facing release
    # name with "clickhouse-" (see clickhouse-rd/cozyrds/clickhouse.yaml),
    # so the actual Helm release name (which the sidecar uses for
    # S3_PATH) is "clickhouse-${CLICKHOUSE_NAME}".
    s3PathOverride: "clickhouse-${CLICKHOUSE_NAME}"
  clickhouseKeeper:
    enabled: true
    replicas: 1
    size: 512Mi
    resourcesPreset: small
EOF

log_substep "Waiting for restore-target ClickHouse HelmRelease..."
kubectl -n "$NAMESPACE" wait hr "clickhouse-${CLICKHOUSE_RESTORE_NAME}" --for=condition=ready --timeout=300s
kubectl -n "$NAMESPACE" wait statefulset.apps/"chi-clickhouse-${CLICKHOUSE_RESTORE_NAME}-clickhouse-0-0" \
    --for=jsonpath='{.status.readyReplicas}'=1 --timeout=300s

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_TOCOPY_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${BACKUPJOB_NAME}
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: ClickHouse
    name: ${CLICKHOUSE_RESTORE_NAME}
EOF

log_substep "Waiting for to-copy RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

log_substep "Verifying sentinel data exists on the copy..."
count=$(clickhouse_query "$CLICKHOUSE_RESTORE_NAME" "SELECT count() FROM default.sentinel" | tr -d '[:space:]')
# Same numeric guard as 06-restore-in-place.sh: a failed SELECT returns an
# error string; bash arithmetic compares it as 0 and would let a misleading
# success/failure message escape.
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric sentinel count on copy: '${count}'"; exit 1; }
if (( count < 1 )); then
    log_error "Sentinel row count on copy is ${count}; expected >= 1"
    exit 1
fi
log_success "To-copy restore verified: ${count} sentinel row(s) on '${CLICKHOUSE_RESTORE_NAME}'."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./cleanup.sh"
