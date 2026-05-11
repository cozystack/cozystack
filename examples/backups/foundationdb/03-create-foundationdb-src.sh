#!/bin/bash
# Step 03: Provision the source FoundationDB cluster, wait for it to
# become available, and write a sentinel key/value so we can prove the
# restore lands the same data on the destination cluster in step 07.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 03: Provision source FoundationDB '${FDB_NAME}'"

# Backup is disabled on the chart side: the Cozystack backups framework
# (BackupClass + FoundationDB strategy from steps 01..03) drives backups
# out-of-chart, and the in-chart backup.* values are DEPRECATED.
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: FoundationDB
metadata:
  name: ${FDB_NAME}
  namespace: ${NAMESPACE}
spec:
  cluster:
    version: "7.3.63"
    processCounts:
      storage: 1
      stateless: -1
      cluster_controller: 1
    redundancyMode: "single"
    storageEngine: "ssd-2"
    faultDomain:
      key: "foundationdb.org/none"
      valueFrom: "\$FDB_ZONE_ID"
  storage:
    size: "1Gi"
    storageClass: ""
  resourcesPreset: "small"
  backup:
    enabled: false
  monitoring:
    enabled: true
  imageType: "unified"
  automaticReplacements: true
EOF

log_substep "Waiting for HelmRelease and FoundationDBCluster..."
timeout 120 sh -ec "until kubectl -n ${NAMESPACE} get hr foundationdb-${FDB_NAME} >/dev/null 2>&1; do sleep 2; done"
kubectl -n "$NAMESPACE" wait hr "foundationdb-${FDB_NAME}" --for=condition=ready --timeout=300s
timeout 300 sh -ec "until kubectl -n ${NAMESPACE} get foundationdbclusters.apps.foundationdb.org ${FDB_CLUSTER_NAME} >/dev/null 2>&1; do sleep 5; done"

log_substep "Waiting for the cluster to report health.available=true..."
timeout 600 sh -ec "until kubectl -n ${NAMESPACE} get foundationdbclusters.apps.foundationdb.org ${FDB_CLUSTER_NAME} -o jsonpath='{.status.health.available}' | grep -q true; do sleep 10; done"

log_substep "Writing sentinel key to ${FDB_NAME}..."
fdbcli_exec "$FDB_NAME" "writemode on; set /backup-demo/sentinel 'hello from $(date -u +%Y%m%dT%H%M%SZ)'"
fdbcli_exec "$FDB_NAME" "get /backup-demo/sentinel"

log_success "Source FoundationDB '${FDB_NAME}' is up and sentinel is set."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-create-backupjob.sh"
