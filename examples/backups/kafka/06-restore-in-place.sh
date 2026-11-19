#!/bin/bash
# Step 06: In-place restore. Delete the topic on the source instance to
# simulate data loss, then ask the Job driver to restore it from S3 back into
# the same Kafka application. The driver recreates the topic (--if-not-exists)
# and replays every partition file, so the topic must be absent first -
# mirroring the ClickHouse demo dropping its table and the NATS demo removing
# its stream. A second RestoreJob against the now-populated topic then has to
# be refused without touching it.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 06: In-place restore from '${BACKUPJOB_NAME}'"

[[ -f "$SCRIPT_DIR/.backup-name.env" ]] || { log_error "missing $SCRIPT_DIR/.backup-name.env; run 05-create-backupjob.sh first"; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.backup-name.env"
[[ -f "$SCRIPT_DIR/.source-dump.txt" ]] || { log_error "missing $SCRIPT_DIR/.source-dump.txt; run 04-create-kafka.sh first"; exit 1; }

log_substep "Deleting topic '${TOPIC}' to simulate data loss..."
# Delete and wait for the topic to actually disappear in the same Pod: topic
# deletion is asynchronous, and the driver's --create would fail with "topic
# marked for deletion" if the restore raced ahead of the tombstone.
#
# kafka-topics treats --topic as a Java regex, so an unquoted name deletes
# every topic it happens to match ("audit.events" also matches "audit-events").
# \Q...\E quotes the name back to a literal - the same fix the kafka-metadata
# example carries. --create below takes a literal name and must NOT be quoted.
kafka_run "$KAFKA_NAME" '
    "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --delete --topic "\Q$TOPIC\E" || true
    for _ in $(seq 1 60); do
        # Capture --list on its own so a transient broker error does not read
        # as empty output and false-positive "deleted"; the pod snippet runs
        # under set -eu without pipefail, so the pipe would otherwise mask it.
        list=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --list) || { sleep 2; continue; }
        if ! printf "%s\n" "$list" | grep -qxF -- "$TOPIC"; then
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
    name: ${BACKUP_NAME}
EOF

log_substep "Waiting for in-place RestoreJob to Succeed..."
wait_for_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed

# Verifies count, content and shape of the restored topic against the source.
# Shared with the refusal check below, which must find all three unchanged.
verify_restored() {
    local when="$1"
    local count rf
    count=$(topic_message_count "$KAFKA_NAME")
    # Guard the comparison: a failed offsets lookup returns an empty string, which
    # must not be mistaken for a successful restore of zero records.
    [[ "$count" =~ ^[0-9]+$ ]] || { log_error "non-numeric record count ${when}: '${count}' (topic missing?)"; return 1; }
    # Exact count, not >=: restore replays unconditionally and appends into a
    # non-empty topic, so a double-replay (e.g. the strategy Job's backoffLimit
    # retry) lands 2x the records and must fail here, not pass.
    if (( count != MESSAGE_COUNT )); then
        log_error "Record count ${when} is ${count}; expected exactly ${MESSAGE_COUNT}"
        return 1
    fi
    # Content, not just count: diff the restored topic against the source snapshot
    # from step 04. Byte-equality of the per-partition ordered dumps proves keys,
    # values, partition placement and ordering survived - the fidelity claims the
    # README makes.
    if ! diff -u "$SCRIPT_DIR/.source-dump.txt" <(topic_dump "$KAFKA_NAME"); then
        log_error "Restored content ${when} does not match the source snapshot"
        return 1
    fi
    # The replication factor comes from the backup manifest, not from a
    # parameter: on this KAFKA_REPLICAS-broker cluster the chart's
    # min.insync.replicas rejects every write into a topic recreated with
    # fewer replicas, so a replay into an under-replicated topic would have
    # landed nothing and failed the count above - but pin the value itself too.
    rf=$(topic_replication_factor "$KAFKA_NAME")
    if [[ "$rf" != "$TOPIC_REPLICAS" ]]; then
        log_error "Replication factor ${when} is '${rf}'; expected ${TOPIC_REPLICAS} as captured from the source"
        return 1
    fi
    log_success "Verified ${when}: ${count} record(s) in '${TOPIC}' at replication factor ${rf}, content matches source."
}

log_substep "Verifying topic records are restored..."
verify_restored "after in-place restore"

# The delete above names ${TOPIC}, which as a Java regex would also match the
# decoy. Its survival is what proves the \Q...\E pin is in place; without it
# this reads back empty.
decoy=$(decoy_message_count "$KAFKA_NAME")
if [[ "$decoy" != "$DECOY_COUNT" ]]; then
    log_error "Decoy topic '${DECOY_TOPIC}' holds '${decoy}', expected ${DECOY_COUNT}: the topic-name match is behaving as a regex, not a literal"
    exit 1
fi
log_success "Decoy topic '${DECOY_TOPIC}' untouched: ${decoy} record(s)."

# Restore appends, so the strategy refuses a target that already holds
# records - before replaying, so a retried Pod cannot stack a second copy onto
# a live topic. Drive that path: a second RestoreJob against the topic just
# restored must settle Failed, name the reason in the strategy Pod's log, and
# leave the topic exactly as the verification above found it.
log_substep "Submitting RestoreJob '${RESTOREJOB_NONEMPTY_NAME}' against the populated topic; it must be refused..."
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_NONEMPTY_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${BACKUP_NAME}
EOF
wait_for_field restorejob "$RESTOREJOB_NONEMPTY_NAME" '{.status.phase}' Failed "$NAMESPACE" 600 Succeeded
# The driver names the batch Job "<restorejob>-restore"; batch/v1 labels its
# Pods job-name=<job>. At least one attempt must have logged the refusal;
# that nothing was written by any attempt is what verify_restored below
# establishes.
refusals=$(kubectl -n "$NAMESPACE" logs -l "job-name=${RESTOREJOB_NONEMPTY_NAME}-restore" --tail=-1 2>/dev/null \
    | grep -c "already holds" || true)
if [[ "$refusals" -lt 1 ]]; then
    log_error "RestoreJob '${RESTOREJOB_NONEMPTY_NAME}' failed, but no attempt logged the non-empty-target refusal"
    kubectl -n "$NAMESPACE" logs -l "job-name=${RESTOREJOB_NONEMPTY_NAME}-restore" --tail=50 >&2 || true
    exit 1
fi
verify_restored "after the refused restore"
log_success "Restore into the populated topic was refused (${refusals} attempt(s)) and left it untouched."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./07-restore-to-copy.sh"
