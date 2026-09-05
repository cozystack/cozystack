#!/bin/bash
# Step 03: In-place restore. Overwrite the marker on the source to simulate data
# drift, then restore the RDB snapshot back into the SAME Redis app. The strategy
# FLUSHALLs the master and replays the RDB, so the marker returns to its backed-up
# value.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 03: In-place restore from '${BACKUPJOB_NAME}'"

log_substep "Corrupting the marker to simulate data loss..."
redis_cmd "$REDIS_NAME" SET "$MARKER_KEY" "corrupted-do-not-keep" >/dev/null
# Read the corruption back and assert it: redis_cmd swallows stderr, so a write
# that silently failed (wrong password, sentinel not answering) would leave the
# marker at its backed-up value and the final check would pass without a restore
# having done anything.
corrupted=$(redis_cmd "$REDIS_NAME" GET "$MARKER_KEY")
[[ "$corrupted" == "corrupted-do-not-keep" ]] || { log_error "corruption step did not take (got '${corrupted}'); aborting so the restore assertion cannot false-pass"; exit 1; }

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

log_substep "Verifying the marker is restored..."
got=$(redis_cmd "$REDIS_NAME" GET "$MARKER_KEY")
[[ "$got" == "$MARKER_VALUE" ]] || { log_error "in-place restore mismatch: got '${got}', expected '${MARKER_VALUE}'"; exit 1; }
log_success "In-place restore verified: marker round-tripped through object storage."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-restore-to-copy.sh"
