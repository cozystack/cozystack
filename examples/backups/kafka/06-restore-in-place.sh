#!/bin/bash
# Step 06: In-place restore. Delete the topic on the source instance to
# simulate data loss, then ask the Job driver to restore it from S3 back into
# the same Kafka application. The driver recreates the topic (--if-not-exists)
# and replays every partition file, so the topic must be absent first -
# mirroring the ClickHouse demo dropping its table and the NATS demo removing
# its stream.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 06: In-place restore from '${BACKUPJOB_NAME}'"

log_substep "Deleting topic '${TOPIC}' to simulate data loss..."
# Delete and wait for the topic to actually disappear in the same Pod: topic
# deletion is asynchronous, and the driver's --create would fail with "topic
# marked for deletion" if the restore raced ahead of the tombstone.
kafka_run "$KAFKA_NAME" '
    "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --delete --topic "$TOPIC" || true
    for _ in $(seq 1 60); do
        # Capture --list on its own so a transient broker error does not read
        # as empty output and false-positive "deleted"; the pod snippet runs
        # under set -eu without pipefail, so the pipe would otherwise mask it.
        list=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --list) || { sleep 2; continue; }
        if ! printf "%s\n" "$list" | grep -qx "$TOPIC"; then
            echo "topic $TOPIC deleted"; exit 0
        fi
        sleep 2
    done
    echo "topic still present after wait" >&2; exit 1
'

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
wait_for_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed

log_substep "Verifying topic records are restored..."
[[ -f "$SCRIPT_DIR/.source-dump.txt" ]] || { log_error "missing $SCRIPT_DIR/.source-dump.txt; run 04-create-kafka.sh first"; exit 1; }
count=$(topic_message_count "$KAFKA_NAME")
# Guard the comparison: a failed offsets lookup returns an empty string, which
# must not be mistaken for a successful restore of zero records.
[[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric record count: '${count}' (topic missing?)"; exit 1; }
# Exact count, not >=: restore replays unconditionally and appends into a
# non-empty topic, so a double-replay (e.g. the strategy Job's backoffLimit
# retry) lands 2x the records and must fail here, not pass.
if (( count != MESSAGE_COUNT )); then
    log_error "Record count after in-place restore is ${count}; expected exactly ${MESSAGE_COUNT}"
    exit 1
fi
# Content, not just count: diff the restored topic against the source snapshot
# from step 04. Byte-equality of the per-partition ordered dumps proves keys,
# values, partition placement and ordering survived - the fidelity claims the
# README makes.
if ! diff -u "$SCRIPT_DIR/.source-dump.txt" <(topic_dump "$KAFKA_NAME"); then
    log_error "Restored content does not match the source snapshot"
    exit 1
fi
log_success "In-place restore verified: ${count} record(s) in '${TOPIC}', content matches source."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./07-restore-to-copy.sh"
