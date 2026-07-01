#!/bin/bash
# Step 05: In-place restore. Clear the sentinel key on the source instance
# (to prove the restore actually replayed something), then ask the driver
# to restore the most recent Backup back into the same FoundationDB
# application. The driver creates a FoundationDBRestore CR with
# destinationClusterName=<source>.
#
# Caveat: fdbrestore requires the destination cluster to be empty (or in
# the operator's "open for restore" state). The FDB operator handles
# pre-restore preparation - it pauses the cluster and clears the keyspace
# before invoking fdbrestore. If you ran step 05 on a populated cluster,
# expect the destination to be wiped and replaced with the backed-up
# keyspace. This script demonstrates that exact behaviour.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 05: In-place restore from '${BACKUPJOB_NAME}'"

log_substep "Clearing sentinel key to simulate data loss..."
fdbcli_exec "$FDB_NAME" "writemode on; clear /backup-demo/sentinel"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_INPLACE_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${BACKUPJOB_NAME}
EOF

log_substep "Waiting for in-place RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 1800

log_substep "Verifying sentinel data is restored..."
result=$(fdbcli_exec "$FDB_NAME" "get /backup-demo/sentinel" || true)
# fdbcli "get" prints lines like:
#   /backup-demo/sentinel is `hello from <date>'
# An empty key prints:
#   /backup-demo/sentinel is not found
if echo "$result" | grep -q "is not found"; then
    log_error "Sentinel key still missing after in-place restore"
    echo "$result"
    exit 1
fi
log_success "In-place restore verified: ${result}"

echo -e "\n${GREEN}${BOLD}Next:${NC} ./06-restore-to-copy.sh"
