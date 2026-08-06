#!/bin/bash
# Step 04: Provision a single-broker Kafka instance, create the S3 Secret the
# Job strategy consumes, then seed a topic with sentinel messages used to
# verify the backup/restore round-trip. The topic is created via the CLI (not
# the chart's `topics:`) so there is no KafkaTopic CR - the in-place restore in
# step 06 can then delete and let the driver recreate the topic deterministically,
# with no Topic Operator reconciliation racing the restore.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 04: Provision Kafka '${KAFKA_NAME}'"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kafka
metadata:
  name: ${KAFKA_NAME}
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

log_substep "Waiting for Kafka HelmRelease..."
kubectl -n "$NAMESPACE" wait hr "kafka-${KAFKA_NAME}" --for=condition=ready --timeout=300s

log_substep "Waiting for the Strimzi Kafka cluster to be Ready (brokers + ZooKeeper up)..."
kubectl -n "$NAMESPACE" wait kafka.kafka.strimzi.io "kafka-${KAFKA_NAME}" \
    --for=condition=Ready --timeout=600s

log_substep "Creating S3 credentials Secret '${KAFKA_NAME}-backup-s3'..."
create_s3_secret "$KAFKA_NAME"

log_substep "Creating topic '${TOPIC}' (${PARTITIONS} partitions) and publishing ${MESSAGE_COUNT} sentinel messages..."
seed_topic "$KAFKA_NAME"

count=$(topic_message_count "$KAFKA_NAME")
[[ "$count" == "$MESSAGE_COUNT" ]] || { log_error "expected ${MESSAGE_COUNT} messages in '${TOPIC}', got '${count}'"; exit 1; }
log_success "Topic '${TOPIC}' holds ${count} record(s)."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-create-backupjob.sh"
