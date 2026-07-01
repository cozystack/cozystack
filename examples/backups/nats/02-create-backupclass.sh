#!/bin/bash
# Step 02: Map the NATS application kind to the Job strategy from step 01. The
# strategy parameters (which stream to back up, which NATS user to connect as)
# travel through the BackupClass and are exposed to the strategy template as
# `.Parameters`. They are also snapshotted onto the resulting Backup's
# driverMetadata, so a later restore re-renders with the same values.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 02: Create BackupClass '${BACKUPCLASS_NAME}'"
log_command "kubectl apply -f - (BackupClass: $BACKUPCLASS_NAME -> $STRATEGY_NAME)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupClass
metadata:
  name: ${BACKUPCLASS_NAME}
spec:
  strategies:
    - application:
        apiGroup: apps.cozystack.io
        kind: NATS
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Job
        name: ${STRATEGY_NAME}
      parameters:
        stream: "${STREAM_NAME}"
        natsUser: "${NATS_USER}"
EOF

log_success "BackupClass '${BACKUPCLASS_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./03-create-bucket.sh"
