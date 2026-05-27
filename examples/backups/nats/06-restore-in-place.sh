#!/bin/bash
# Step 06: In-place restore. Delete the stream on the source instance to
# simulate data loss, then ask the Job driver to restore it from S3 back into
# the same NATS application. `nats stream restore` recreates the stream, so it
# must be absent first - mirroring the ClickHouse demo dropping its table.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 06: In-place restore from '${BACKUPJOB_NAME}'"

log_substep "Deleting stream '${STREAM_NAME}' to simulate data loss..."
nats_cli "$NATS_NAME" stream rm "$STREAM_NAME" -f

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
wait_for_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

log_substep "Verifying stream messages are restored..."
count=$(stream_message_count "$NATS_NAME" "$STREAM_NAME")
# Guard the comparison: a failed `stream info` returns an empty string, which
# must not be mistaken for a successful restore of zero messages.
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric message count: '${count}' (stream missing?)"; exit 1; }
if (( count < MESSAGE_COUNT )); then
    log_error "Message count after in-place restore is ${count}; expected ${MESSAGE_COUNT}"
    exit 1
fi
log_success "In-place restore verified: ${count} message(s) in '${STREAM_NAME}'."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./07-restore-to-copy.sh"
