#!/bin/bash
# Step 07: To-copy restore. Provision a second NATS application and restore the
# same backup into it via RestoreJob.spec.targetApplicationRef. The strategy
# connects to the TARGET app (its credentials, its <target>-backup-s3 Secret)
# but reads the S3 object keyed by the SOURCE app name, via
# .Backup.ApplicationRef.Name - so the copy lands the source's data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 07: To-copy restore into '${NATS_RESTORE_NAME}'"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: NATS
metadata:
  name: ${NATS_RESTORE_NAME}
  namespace: ${NAMESPACE}
spec:
  replicas: 1
  resourcesPreset: nano
  jetstream:
    enabled: true
    size: 1Gi
  users:
    ${NATS_USER}:
      password: ${NATS_PASSWORD}
EOF

log_substep "Waiting for restore-target NATS HelmRelease..."
kubectl -n "$NAMESPACE" wait hr "${NATS_RESTORE_NAME}-system" --for=condition=ready --timeout=300s
kubectl -n "$NAMESPACE" wait statefulset.apps/"${NATS_RESTORE_NAME}" \
    --for=jsonpath='{.status.readyReplicas}'=1 --timeout=300s

log_substep "Creating S3 credentials Secret '${NATS_RESTORE_NAME}-backup-s3'..."
# Same bucket as the source; the strategy reads the source-keyed object.
create_s3_secret "$NATS_RESTORE_NAME"

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
    kind: NATS
    name: ${NATS_RESTORE_NAME}
EOF

log_substep "Waiting for to-copy RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

log_substep "Verifying stream messages exist on the copy..."
count=$(stream_message_count "$NATS_RESTORE_NAME" "$STREAM_NAME")
# Same numeric guard as step 06: an empty result means the stream is missing,
# not a successful restore of zero messages.
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric message count on copy: '${count}' (stream missing?)"; exit 1; }
if (( count < MESSAGE_COUNT )); then
    log_error "Message count on copy is ${count}; expected ${MESSAGE_COUNT}"
    exit 1
fi
log_success "To-copy restore verified: ${count} message(s) in '${STREAM_NAME}' on '${NATS_RESTORE_NAME}'."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./cleanup.sh"
