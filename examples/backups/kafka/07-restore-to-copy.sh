#!/bin/bash
# Step 07: To-copy restore. Provision a second Kafka application and restore the
# same backup into it via RestoreJob.spec.targetApplicationRef. The strategy
# connects to the TARGET app (its bootstrap, its <target>-backup-s3 Secret) but
# reads the S3 object keyed by the SOURCE app name, via .Backup.ApplicationRef.Name
# - so the copy lands the source's data.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 07: To-copy restore into '${KAFKA_RESTORE_NAME}'"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kafka
metadata:
  name: ${KAFKA_RESTORE_NAME}
  namespace: ${NAMESPACE}
spec:
  external: false
  kafka:
    replicas: 1
    size: 2Gi
    resourcesPreset: "small"
  zookeeper:
    replicas: 1
    size: 1Gi
    resourcesPreset: "small"
  topics: []
EOF

log_substep "Waiting for restore-target Kafka HelmRelease..."
kubectl -n "$NAMESPACE" wait hr "kafka-${KAFKA_RESTORE_NAME}" --for=condition=ready --timeout=300s
log_substep "Waiting for the restore-target Strimzi Kafka cluster to be Ready..."
kubectl -n "$NAMESPACE" wait kafka.kafka.strimzi.io "kafka-${KAFKA_RESTORE_NAME}" \
    --for=condition=Ready --timeout=600s

log_substep "Creating S3 credentials Secret '${KAFKA_RESTORE_NAME}-backup-s3'..."
# Same bucket as the source; the strategy reads the source-keyed object.
create_s3_secret "$KAFKA_RESTORE_NAME"

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
    kind: Kafka
    name: ${KAFKA_RESTORE_NAME}
EOF

log_substep "Waiting for to-copy RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

log_substep "Verifying topic records exist on the copy..."
count=$(topic_message_count "$KAFKA_RESTORE_NAME")
# Same numeric guard as step 06: an empty result means the topic is missing,
# not a successful restore of zero records.
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric record count on copy: '${count}' (topic missing?)"; exit 1; }
if (( count < MESSAGE_COUNT )); then
    log_error "Record count on copy is ${count}; expected ${MESSAGE_COUNT}"
    exit 1
fi
log_success "To-copy restore verified: ${count} record(s) in '${TOPIC}' on '${KAFKA_RESTORE_NAME}'."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./cleanup.sh"
