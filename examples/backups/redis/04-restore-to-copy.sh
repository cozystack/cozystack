#!/bin/bash
# Step 04: To-copy restore. Provision a second, differently-named Redis and
# restore the same backup into it via RestoreJob.spec.targetApplicationRef. The
# strategy connects to the TARGET's master but reads the S3 object keyed by the
# SOURCE app name (via .Backup.ApplicationRef.Name), so the copy lands the
# source's data while the source keeps running untouched.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 04: To-copy restore into '${REDIS_RESTORE_NAME}'"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: ${REDIS_RESTORE_NAME}
  namespace: ${NAMESPACE}
spec:
  replicas: 2
  resourcesPreset: nano
  size: 1Gi
  authEnabled: true
EOF

# OnDelete StatefulSet: see 01-create-redis.sh. Gate on master election instead.
wait_redis_master "$REDIS_RESTORE_NAME"

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
    kind: Redis
    name: ${REDIS_RESTORE_NAME}
EOF

log_substep "Waiting for to-copy RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

log_substep "Verifying the marker landed on the copy..."
got=$(redis_cmd "$REDIS_RESTORE_NAME" GET "$MARKER_KEY")
[[ "$got" == "$MARKER_VALUE" ]] || { log_error "to-copy restore mismatch: got '${got}', expected '${MARKER_VALUE}'"; exit 1; }
log_success "To-copy restore verified: marker present on '${REDIS_RESTORE_NAME}'."

echo -e "\n${GREEN}${BOLD}Done.${NC} Run ./cleanup.sh to remove the demo resources."
