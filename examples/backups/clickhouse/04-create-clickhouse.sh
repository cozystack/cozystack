#!/bin/bash
# Step 04: Provision a ClickHouse instance and write a sentinel row used to
# verify backup/restore round-trips.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"
# Bucket coordinates produced by step 03; sourcing here makes them available
# for the chart's `backup.*` values so the chart-emitted backup-s3 Secret and
# the clickhouse-backup sidecar both materialise with the right credentials.
[[ -f "$SCRIPT_DIR/.bucket-info.env" ]] || { log_error "missing $SCRIPT_DIR/.bucket-info.env; run 03-create-bucket.sh first"; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.bucket-info.env"

print_header "Step 04: Provision ClickHouse '${CLICKHOUSE_NAME}'"

# backup.enabled=true triggers the chart to emit:
#   - <release>-backup-s3 Secret (with bucket coordinates from values)
#   - clickhouse-backup sidecar in the chi-* Pod (HTTP API on :7171)
# Both are consumed by the cluster-scoped Altinity strategy.
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: ${CLICKHOUSE_NAME}
  namespace: ${NAMESPACE}
spec:
  size: 5Gi
  logStorageSize: 1Gi
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
  clickhouseKeeper:
    enabled: true
    replicas: 1
    size: 1Gi
    resourcesPreset: small
EOF

log_substep "Waiting for ClickHouse HelmRelease..."
kubectl -n "$NAMESPACE" wait hr "clickhouse-${CLICKHOUSE_NAME}" --for=condition=ready --timeout=300s

log_substep "Waiting for first ClickHouse pod..."
kubectl -n "$NAMESPACE" wait statefulset.apps/"chi-clickhouse-${CLICKHOUSE_NAME}-clickhouse-0-0" \
    --for=jsonpath='{.status.readyReplicas}'=1 --timeout=300s

log_substep "Writing sentinel data..."
clickhouse_query "$CLICKHOUSE_NAME" "CREATE TABLE IF NOT EXISTS default.sentinel (id UInt32, name String) ENGINE = MergeTree ORDER BY id"
clickhouse_query "$CLICKHOUSE_NAME" "INSERT INTO default.sentinel VALUES (1, 'before-backup')"

count=$(clickhouse_query "$CLICKHOUSE_NAME" "SELECT count() FROM default.sentinel" | tr -d '[:space:]')
log_success "Sentinel rows: ${count}"

echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-create-backupjob.sh"
