#!/bin/bash
# Step 02: Map the ClickHouse application kind to the Altinity strategy from
# step 01, parameterized with the name of the Secret that holds S3 credentials.
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
        kind: ClickHouse
      strategyRef:
        apiGroup: strategy.backups.cozystack.io
        kind: Altinity
        name: ${STRATEGY_NAME}
EOF

log_success "BackupClass '${BACKUPCLASS_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./03-create-bucket.sh"
