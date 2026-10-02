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

[[ -f "$SCRIPT_DIR/.backup-name.env" ]] || { log_error "missing $SCRIPT_DIR/.backup-name.env; run 05-create-backupjob.sh first"; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.backup-name.env"

# Same broker count as the source: the restore recreates the topic with the
# replication factor the backup captured, which a smaller target could not
# host (see the replicationFactor override in 02-create-backupclass.sh).
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kafka
metadata:
  name: ${KAFKA_RESTORE_NAME}
  namespace: ${NAMESPACE}
spec:
  external: false
  kafka:
    replicas: ${KAFKA_REPLICAS}
    size: 2Gi
    resourcesPreset: "c1.small"
  topics: []
EOF

wait_hr_ready "kafka-${KAFKA_RESTORE_NAME}"
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
    name: ${BACKUP_NAME}
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: Kafka
    name: ${KAFKA_RESTORE_NAME}
EOF

log_substep "Waiting for to-copy RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed

log_substep "Verifying topic records exist on the copy..."
[[ -f "$SCRIPT_DIR/.source-dump.txt" ]] || { log_error "missing $SCRIPT_DIR/.source-dump.txt; run 04-create-kafka.sh first"; exit 1; }
count=$(topic_message_count "$KAFKA_RESTORE_NAME")
# Same numeric guard as step 06: an empty result means the topic is missing,
# not a successful restore of zero records.
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric record count on copy: '${count}' (topic missing?)"; exit 1; }
# Exact count, not >=: see step 06 - a double-replay would inflate this.
if (( count != MESSAGE_COUNT )); then
    log_error "Record count on copy is ${count}; expected exactly ${MESSAGE_COUNT}"
    exit 1
fi
# Content, not just count: the copy must match the source snapshot byte-for-byte.
if ! diff -u "$SCRIPT_DIR/.source-dump.txt" <(topic_dump "$KAFKA_RESTORE_NAME"); then
    log_error "Restored content on copy does not match the source snapshot"
    exit 1
fi
# Shape too: the copy's topic must carry the replication factor the backup
# captured from the source, not a default (see step 06).
rf=$(topic_replication_factor "$KAFKA_RESTORE_NAME")
if [[ "$rf" != "$TOPIC_REPLICAS" ]]; then
    log_error "Replication factor on copy is '${rf}'; expected ${TOPIC_REPLICAS} as captured from the source"
    exit 1
fi
log_success "To-copy restore verified: ${count} record(s) in '${TOPIC}' on '${KAFKA_RESTORE_NAME}' at replication factor ${rf}, content matches source."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./cleanup.sh"
