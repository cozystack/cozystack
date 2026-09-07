#!/bin/bash
# Convenience runner + e2e harness for the Kafka topic-metadata backup/restore
# demo. It applies the same numbered manifests a human reads, so the documented
# flow and the automated test cannot drift. Stops on the first failure.
#
# Flow (platform cozy-default, no per-demo Bucket):
#   source Kafka -> seed topic `orders` with a distinctive retention.ms sentinel,
#   plus a colliding pair (`audit.events` / `audit-events`) with distinct
#   partition counts -> ad-hoc BackupJob against cozy-default (wait Succeeded)
#   -> in-place: drop the topics, restore, assert `orders` came back with the
#      sentinel and each colliding topic with its own partition count
#   -> to-copy: bootstrap an empty target Kafka, restore the metadata onto it,
#      assert the topic + sentinel landed there while the source stays intact.
#
# Data integrity is proven at the METADATA layer: the retention.ms sentinel is a
# per-run value, so a restore that recreated a bare `orders` (wrong partitions or
# default config) fails the assertion; the colliding pair proves the strategy
# treats --topic as a literal, not a regex. Message payloads are out of scope.
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

print_header "Step 05a: Derive the demo Kafka strategy '${STRATEGY_NAME}' + BackupClass '${BACKUPCLASS_NAME}'"
# The shipped cozy-default-kafka advertises the platform's EXTERNAL S3 ingress,
# which in-cluster Pods cannot resolve or TLS-validate in CI. Derive a demo
# strategy from it (so the driver script stays a single source of truth) pointed
# at an in-cluster, verifiable endpoint. With an S3_ENDPOINT override, copy the
# self-signed seaweedfs CA and mount it (CURL_CA_BUNDLE); with none, reuse the
# shipped endpoint and skip the CA mount (its cert is publicly trusted, and
# CURL_CA_BUNDLE would REPLACE the system bundle rather than extend it).
if [[ -n "$S3_ENDPOINT" ]]; then
    CA_PRESENT=$(copy_s3_ca)
else
    S3_ENDPOINT=$(kubectl get kafka.strategy.backups.cozystack.io cozy-default-kafka -o json \
        | jq -r '.spec.template.spec.containers[0].env[] | select(.name=="S3_ENDPOINT") | .value')
    [[ -n "$S3_ENDPOINT" ]] || { log_error "shipped cozy-default-kafka has no S3_ENDPOINT; is the platform backup stack installed?"; exit 1; }
    CA_PRESENT=0
fi
provision_demo_strategy "$S3_ENDPOINT" "$CA_PRESENT"
log_success "Demo strategy at endpoint '${S3_ENDPOINT}' (CA mounted: ${CA_PRESENT})"

print_header "Step 05b: Seed topic '${TOPIC}' (${PARTITIONS} partitions, retention.ms=${RETENTION})"
seed_topic "$KAFKA_SRC_NAME" "$RETENTION"
got=$(topic_meta "$KAFKA_SRC_NAME")
[[ "$got" == "${PARTITIONS} ${RETENTION}" ]] || { log_error "seed verify failed: got '${got}', want '${PARTITIONS} ${RETENTION}'"; exit 1; }
log_success "Seeded '${TOPIC}': ${got}"

print_header "Step 05c: Seed colliding pair '${COLLIDE_DOT}' (${COLLIDE_DOT_PARTS}p) + '${COLLIDE_DASH}' (${COLLIDE_DASH_PARTS}p)"
# These names collide under Kafka's --topic regex ("audit.events" matches
# "audit-events"). Distinct partition counts make a backup that does not treat
# the name literally record the wrong shape, so the restore verify below fails.
seed_topic_partitions "$KAFKA_SRC_NAME" "$COLLIDE_DOT" "$COLLIDE_DOT_PARTS"
seed_topic_partitions "$KAFKA_SRC_NAME" "$COLLIDE_DASH" "$COLLIDE_DASH_PARTS"
gotdot=$(topic_partitions "$KAFKA_SRC_NAME" "$COLLIDE_DOT")
gotdash=$(topic_partitions "$KAFKA_SRC_NAME" "$COLLIDE_DASH")
[[ "$gotdot" == "$COLLIDE_DOT_PARTS" && "$gotdash" == "$COLLIDE_DASH_PARTS" ]] \
    || { log_error "collision seed verify failed: ${COLLIDE_DOT}=${gotdot} (want ${COLLIDE_DOT_PARTS}), ${COLLIDE_DASH}=${gotdash} (want ${COLLIDE_DASH_PARTS})"; exit 1; }
log_success "Seeded colliding pair: ${COLLIDE_DOT}=${gotdot} ${COLLIDE_DASH}=${gotdash}"

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

print_header "Step 25: In-place restore — drop the topics, then restore them"
delete_topic "$KAFKA_SRC_NAME" "$TOPIC"
delete_topic "$KAFKA_SRC_NAME" "$COLLIDE_DOT"
delete_topic "$KAFKA_SRC_NAME" "$COLLIDE_DASH"
kubectl apply -f "$SCRIPT_DIR/25-restorejob-in-place.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 600 Failed
got=$(topic_meta "$KAFKA_SRC_NAME")
[[ "$got" == "${PARTITIONS} ${RETENTION}" ]] || { log_error "in-place restore verify failed: got '${got}', want '${PARTITIONS} ${RETENTION}'"; exit 1; }
# The colliding pair must each come back with their OWN partition count. If the
# backup treated --topic as a regex, one recorded the other's shape and this
# fails.
gotdot=$(topic_partitions "$KAFKA_SRC_NAME" "$COLLIDE_DOT")
gotdash=$(topic_partitions "$KAFKA_SRC_NAME" "$COLLIDE_DASH")
[[ "$gotdot" == "$COLLIDE_DOT_PARTS" && "$gotdash" == "$COLLIDE_DASH_PARTS" ]] \
    || { log_error "collision restore verify failed: ${COLLIDE_DOT}=${gotdot} (want ${COLLIDE_DOT_PARTS}), ${COLLIDE_DASH}=${gotdash} (want ${COLLIDE_DASH_PARTS})"; exit 1; }
log_success "In-place restore verified: '${TOPIC}' back with ${got}; pair ${COLLIDE_DOT}=${gotdot} ${COLLIDE_DASH}=${gotdash}"

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
