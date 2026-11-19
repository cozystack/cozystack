#!/bin/bash
# Step 04: Provision a NATS instance with JetStream, create the S3 Secret the
# Job strategy consumes, then seed a stream with sentinel messages used to
# verify the backup/restore round-trip.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 04: Provision NATS '${NATS_NAME}'"

# A fixed password on the 'backup' user keeps the demo deterministic: the
# strategy Pod reads it from the chart-emitted <name>-credentials Secret, and
# the seed/verify helpers reuse the same value via nats_url().
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: NATS
metadata:
  name: ${NATS_NAME}
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

log_substep "Waiting for NATS HelmRelease..."
kubectl -n "$NAMESPACE" wait hr "${NATS_NAME}-system" --for=condition=ready --timeout=300s

log_substep "Waiting for NATS pod..."
kubectl -n "$NAMESPACE" wait statefulset.apps/"${NATS_NAME}" \
    --for=jsonpath='{.status.readyReplicas}'=1 --timeout=300s

log_substep "Creating S3 credentials Secret '${NATS_NAME}-backup-s3'..."
create_s3_secret "$NATS_NAME"

log_substep "Creating JetStream stream '${STREAM_NAME}' and publishing ${MESSAGE_COUNT} sentinel messages..."
nats_cli "$NATS_NAME" stream add "$STREAM_NAME" \
    --subjects "orders.>" --storage file --replicas 1 --defaults
nats_cli "$NATS_NAME" pub "orders.new" "order {{Count}}" --count "$MESSAGE_COUNT"

count=$(stream_message_count "$NATS_NAME" "$STREAM_NAME")
[[ "$count" == "$MESSAGE_COUNT" ]] || { log_error "expected ${MESSAGE_COUNT} messages in '${STREAM_NAME}', got '${count}'"; exit 1; }
log_success "Stream '${STREAM_NAME}' holds ${count} message(s)."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-create-backupjob.sh"
