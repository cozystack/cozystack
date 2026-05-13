#!/bin/bash
# Step 06: To-copy restore. Provision a second FoundationDB cluster and
# restore the source backup into it via RestoreJob.spec.targetApplicationRef.
# The driver creates a FoundationDBRestore CR in the destination namespace
# with destinationClusterName=<target>. This is a strictly stronger restore
# proof than the in-place variant: the assertion at the end witnesses the
# sentinel row landing on a different cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 06: To-copy restore into '${FDB_RESTORE_NAME}'"

log_substep "Provisioning target FoundationDB '${FDB_RESTORE_NAME}'..."
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: FoundationDB
metadata:
  name: ${FDB_RESTORE_NAME}
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

timeout 120 sh -ec "until kubectl -n ${NAMESPACE} get hr foundationdb-${FDB_RESTORE_NAME} >/dev/null 2>&1; do sleep 2; done"
kubectl -n "$NAMESPACE" wait hr "foundationdb-${FDB_RESTORE_NAME}" --for=condition=ready --timeout=300s
timeout 300 sh -ec "until kubectl -n ${NAMESPACE} get foundationdbclusters.apps.foundationdb.org ${FDB_DST_CLUSTER_NAME} >/dev/null 2>&1; do sleep 5; done"
timeout 600 sh -ec "until kubectl -n ${NAMESPACE} get foundationdbclusters.apps.foundationdb.org ${FDB_DST_CLUSTER_NAME} -o jsonpath='{.status.health.available}' | grep -q true; do sleep 10; done"

log_substep "Submitting to-copy RestoreJob..."
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
    kind: FoundationDB
    name: ${FDB_RESTORE_NAME}
EOF

log_substep "Waiting for to-copy RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 1800

log_substep "Verifying sentinel data lands on '${FDB_RESTORE_NAME}'..."
result=$(fdbcli_exec "$FDB_RESTORE_NAME" "get /backup-demo/sentinel" || true)
if echo "$result" | grep -q "is not found"; then
    log_error "Sentinel key missing on to-copy target after restore"
    echo "$result"
    exit 1
fi
log_success "To-copy restore verified on '${FDB_RESTORE_NAME}': ${result}"

echo -e "\n${GREEN}${BOLD}Done.${NC} Run ./cleanup.sh to tear everything down."
