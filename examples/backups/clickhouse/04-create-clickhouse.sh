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

# backup.endpointCA is only meaningful when step 03 copied a CA (self-signed S3
# endpoint); on a publicly-trusted endpoint CH_BACKUP_CA_SECRET is empty and the
# block is omitted entirely so the sidecar stays on the system trust store.
ENDPOINT_CA_BLOCK=""
if [[ -n "${CH_BACKUP_CA_SECRET:-}" ]]; then
    ENDPOINT_CA_BLOCK=$(printf '\n    endpointCA:\n      name: "%s"\n      key: ca.crt' "$CH_BACKUP_CA_SECRET")
fi

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
    s3SecretKey: "${CH_BACKUP_SECRET_KEY}"${ENDPOINT_CA_BLOCK}
  clickhouseKeeper:
    enabled: true
    replicas: 1
    size: 1Gi
    resourcesPreset: small
EOF

log_substep "Waiting for ClickHouse HelmRelease..."
wait_hr_ready "clickhouse-${CLICKHOUSE_NAME}" 300

log_substep "Waiting for first ClickHouse pod..."
wait_sts_ready "chi-clickhouse-${CLICKHOUSE_NAME}-clickhouse-0-0" 300

log_substep "Writing sentinel data..."
clickhouse_query "$CLICKHOUSE_NAME" "CREATE TABLE IF NOT EXISTS default.sentinel (id UInt32, name String) ENGINE = MergeTree ORDER BY id"
clickhouse_query "$CLICKHOUSE_NAME" "INSERT INTO default.sentinel VALUES (1, 'before-backup')"

count=$(clickhouse_query "$CLICKHOUSE_NAME" "SELECT count() FROM default.sentinel" | tr -d '[:space:]')
log_success "Sentinel rows: ${count}"

echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-create-backupjob.sh"
