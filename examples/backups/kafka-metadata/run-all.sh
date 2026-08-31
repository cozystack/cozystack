#!/bin/bash
# Convenience runner + e2e harness for the Kafka topic-metadata backup/restore
# demo. It applies the same numbered manifests a human reads, so the documented
# flow and the automated test cannot drift. Stops on the first failure.
#
# Flow (platform cozy-default, no per-demo Bucket):
#   source Kafka -> seed topic `orders` with a distinctive retention.ms sentinel
#   -> ad-hoc BackupJob against cozy-default (wait Succeeded)
#   -> in-place: drop the topic, restore, assert it came back with the sentinel
#   -> to-copy: bootstrap an empty target Kafka, restore the metadata onto it,
#      assert the topic + sentinel landed there while the source stays intact.
#
# Data integrity is proven at the METADATA layer: the retention.ms sentinel is a
# per-run value, so a restore that recreated a bare `orders` (wrong partitions or
# default config) fails the assertion. Message payloads are out of scope.
#
# Override NAMESPACE via the environment; see 00-helpers.sh.
# hack/e2e-chainsaw/kafka-metadata/ drives this file as kafka-3-metadata-roundtrip.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

# Per-run sentinel: a unique retention.ms (ms since epoch) so a passing verify
# proves this run's config round-tripped, not a leftover topic.
RETENTION="$(date +%s)000"

print_header "Step 05: Deploy source Kafka '${KAFKA_SRC_NAME}'"
kubectl apply -f "$SCRIPT_DIR/05-kafka-src.yaml"
wait_hr_ready "kafka-${KAFKA_SRC_NAME}" 300
kafka_wait_ready "$KAFKA_SRC_NAME" 600

print_header "Step 05b: Seed topic '${TOPIC}' (${PARTITIONS} partitions, retention.ms=${RETENTION})"
seed_topic "$KAFKA_SRC_NAME" "$RETENTION"
got=$(topic_meta "$KAFKA_SRC_NAME")
[[ "$got" == "${PARTITIONS} ${RETENTION}" ]] || { log_error "seed verify failed: got '${got}', want '${PARTITIONS} ${RETENTION}'"; exit 1; }
log_success "Seeded '${TOPIC}': ${got}"

print_header "Step 10: Submit ad-hoc BackupJob '${BACKUPJOB_NAME}' and wait for Succeeded"
kubectl apply -f "$SCRIPT_DIR/10-backupjob-adhoc.yaml"
wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed
BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
log_success "Backup artefact: ${BACKUP_NAME}"

if [[ "${SKIP_RESTORE:-0}" == "1" ]]; then
    log_warning "SKIP_RESTORE=1: stopping after a successful backup."
    exit 0
fi

print_header "Step 25: In-place restore — drop the topic, then restore it"
kafka_run "$KAFKA_SRC_NAME" '
    "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --delete --topic "$TOPIC" || true
    for _ in $(seq 1 60); do
        list=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --list) || { sleep 2; continue; }
        if ! printf "%s\n" "$list" | grep -qx "$TOPIC"; then echo "topic $TOPIC deleted"; exit 0; fi
        sleep 2
    done
    echo "topic still present after wait" >&2; exit 1
'
kubectl apply -f "$SCRIPT_DIR/25-restorejob-in-place.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed
got=$(topic_meta "$KAFKA_SRC_NAME")
[[ "$got" == "${PARTITIONS} ${RETENTION}" ]] || { log_error "in-place restore verify failed: got '${got}', want '${PARTITIONS} ${RETENTION}'"; exit 1; }
log_success "In-place restore verified: '${TOPIC}' back with ${got}"

print_header "Step 20/30: To-copy restore into a fresh '${KAFKA_TARGET_NAME}'"
kubectl apply -f "$SCRIPT_DIR/20-kafka-target.yaml"
wait_hr_ready "kafka-${KAFKA_TARGET_NAME}" 300
kafka_wait_ready "$KAFKA_TARGET_NAME" 600
kubectl apply -f "$SCRIPT_DIR/30-restorejob-to-copy.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed
got=$(topic_meta "$KAFKA_TARGET_NAME")
[[ "$got" == "${PARTITIONS} ${RETENTION}" ]] || { log_error "to-copy restore verify failed on target: got '${got}', want '${PARTITIONS} ${RETENTION}'"; exit 1; }
log_success "To-copy restore verified: '${KAFKA_TARGET_NAME}' has '${TOPIC}' with ${got}"

# To-copy must not mutate the source.
src=$(topic_meta "$KAFKA_SRC_NAME")
[[ "$src" == "${PARTITIONS} ${RETENTION}" ]] || { log_error "source changed after to-copy restore: got '${src}'"; exit 1; }
log_success "Source '${KAFKA_SRC_NAME}' left intact by the to-copy restore."
