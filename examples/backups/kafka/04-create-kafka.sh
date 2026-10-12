#!/bin/bash
# Step 04: Provision a Kafka instance with KAFKA_REPLICAS brokers, create the
# S3 Secret the Job strategy consumes, then seed a topic with sentinel messages
# used to verify the backup/restore round-trip. The topic is created via the
# CLI (not the chart's `topics:`) so there is no KafkaTopic CR - the in-place
# restore in step 06 can then delete and let the driver recreate the topic
# deterministically, with no Topic Operator reconciliation racing the restore.
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
    replicas: ${KAFKA_REPLICAS}
    size: 2Gi
    resourcesPreset: "c1.small"
  topics: []
EOF

wait_hr_ready "kafka-${KAFKA_NAME}"

log_substep "Waiting for the Strimzi Kafka cluster to be Ready (broker and controller pools up)..."
kubectl -n "$NAMESPACE" wait kafka.kafka.strimzi.io "kafka-${KAFKA_NAME}" \
    --for=condition=Ready --timeout=600s

log_substep "Creating S3 credentials Secret '${KAFKA_NAME}-backup-s3'..."
create_s3_secret "$KAFKA_NAME"

log_substep "Creating topic '${TOPIC}' (${PARTITIONS} partitions, ${TOPIC_REPLICAS} replicas) and publishing ${MESSAGE_COUNT} sentinel messages..."
seed_topic "$KAFKA_NAME"

count=$(topic_message_count "$KAFKA_NAME")
[[ "$count" == "$MESSAGE_COUNT" ]] || { log_error "expected ${MESSAGE_COUNT} messages in '${TOPIC}', got '${count}'"; exit 1; }
rf=$(topic_replication_factor "$KAFKA_NAME")
[[ "$rf" == "$TOPIC_REPLICAS" ]] || { log_error "expected '${TOPIC}' to have ${TOPIC_REPLICAS} replicas, got '${rf}'"; exit 1; }
log_success "Topic '${TOPIC}' holds ${count} record(s) at replication factor ${rf}."

# Seed the decoy. It is never named in the BackupClass, so a correct run must
# leave it untouched; a --topic call that matched it as a regex instead would
# either pull its partitions into the backup or delete it in step 06.
log_substep "Creating decoy topic '${DECOY_TOPIC}' (${DECOY_COUNT} records) to pin literal topic matching..."
seed_decoy_topic "$KAFKA_NAME"
decoy=$(decoy_message_count "$KAFKA_NAME")
[[ "$decoy" == "$DECOY_COUNT" ]] || { log_error "expected ${DECOY_COUNT} messages in '${DECOY_TOPIC}', got '${decoy}'"; exit 1; }
log_success "Decoy topic '${DECOY_TOPIC}' holds ${decoy} record(s)."

# Snapshot the source content so the restore steps can diff against it and
# prove the records - not just their count - round-tripped. cleanup.sh removes
# this file.
topic_dump "$KAFKA_NAME" > "$SCRIPT_DIR/.source-dump.txt"
dumped=$(wc -l < "$SCRIPT_DIR/.source-dump.txt" | tr -d '[:space:]')
[[ "$dumped" == "$MESSAGE_COUNT" ]] || { log_error "source dump has ${dumped} records, expected ${MESSAGE_COUNT}"; exit 1; }

echo -e "\n${GREEN}${BOLD}Next:${NC} ./05-create-backupjob.sh"
