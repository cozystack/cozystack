#!/bin/bash
# Shared helpers for the Kafka topic-metadata backup/restore demo.
# Source this file in other scripts: source "$(dirname "$0")/00-helpers.sh"

export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export BLUE='\033[0;34m'
export MAGENTA='\033[0;35m'
export CYAN='\033[0;36m'
export WHITE='\033[1;37m'
export NC='\033[0m'
export BOLD='\033[1m'

# Default settings (override via environment). This demo uses the platform
# cozy-default flow: no per-demo Bucket is created — the cozy-default-kafka
# strategy carries the system-bucket coordinates and the controller projects
# cozy-backups-creds into the namespace before each run.
export NAMESPACE="${NAMESPACE:-tenant-root}"
export KAFKA_SRC_NAME="${KAFKA_SRC_NAME:-kafka-meta-src}"
export KAFKA_TARGET_NAME="${KAFKA_TARGET_NAME:-kafka-meta-target}"
export TOPIC="${TOPIC:-orders}"
export PARTITIONS="${PARTITIONS:-3}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-cozy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-kafka-meta-src-adhoc}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-kafka-meta-src-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-kafka-meta-src-to-target}"
export PLAN_NAME="${PLAN_NAME:-kafka-meta-src-daily}"
# The Cozystack chart names the Strimzi cluster kafka-<app>; its plaintext
# bootstrap Service is kafka-<app>-kafka-bootstrap:9092.
# KAFKA_IMAGE only drives the host-side throwaway CLI pods here (seed/verify);
# the backup/restore Jobs use the image pinned in the strategy. Override to
# match your operator's Kafka image if it differs.
export KAFKA_IMAGE="${KAFKA_IMAGE:-quay.io/strimzi/kafka:0.45.1-rc1-kafka-3.9.1@sha256:ba52ed046b1dccdbd96f4e68057ce014d862a7c9c1fc670760c023b9aa09f23f}"
export KAFKA_BIN="${KAFKA_BIN:-/opt/kafka/bin}"

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}> $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }

print_header() {
    echo -e "\n${MAGENTA}${BOLD}== $1 ==${NC}\n" >&2
}

# Wait until a JSONPath value on a resource matches the desired string. Optional
# 7th arg is a TERMINAL failure value: once the field reaches it the wait returns
# 1 immediately instead of polling to the timeout.
wait_for_field() {
    local resource_type="$1" resource_name="$2" jsonpath="$3" desired="$4"
    local namespace="${5:-}" timeout="${6:-300}" fail_value="${7:-}"

    log_substep "Waiting for $resource_type/$resource_name $jsonpath to become '$desired'..."
    local elapsed=0
    local ns_flag=()
    [[ -n "$namespace" ]] && ns_flag=(-n "$namespace")

    while true; do
        local current
        current=$(kubectl get "$resource_type" "$resource_name" "${ns_flag[@]}" -o jsonpath="$jsonpath" 2>/dev/null || true)
        if [[ "$current" == "$desired" ]]; then
            log_success "$resource_type/$resource_name reached '$desired'"
            return 0
        fi
        if [[ -n "$fail_value" && "$current" == "$fail_value" ]]; then
            log_error "$resource_type/$resource_name reached terminal '$current' (expected '$desired')"
            return 1
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait for a HelmRelease to become Ready, with an existence backstop and a
# fail-fast on Stalled=True.
wait_hr_ready() {
    local name="$1" timeout="${2:-300}" elapsed=0
    log_substep "Waiting for HelmRelease/$name to become Ready..."
    while true; do
        if kubectl -n "$NAMESPACE" get hr "$name" >/dev/null 2>&1; then
            local ready stalled
            ready=$(kubectl -n "$NAMESPACE" get hr "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
            if [[ "$ready" == "True" ]]; then
                log_success "HelmRelease/$name is Ready"
                return 0
            fi
            stalled=$(kubectl -n "$NAMESPACE" get hr "$name" -o jsonpath='{.status.conditions[?(@.type=="Stalled")].status}' 2>/dev/null || true)
            if [[ "$stalled" == "True" ]]; then
                log_error "HelmRelease/$name is Stalled: $(kubectl -n "$NAMESPACE" get hr "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null)"
                return 1
            fi
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for HelmRelease/$name to become Ready"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait until the Strimzi Kafka cluster kafka-<app> reports Ready=True — the same
# precondition the driver gates on.
kafka_wait_ready() {
    local app="$1" timeout="${2:-600}"
    wait_for_field kafka.kafka.strimzi.io "kafka-${app}" \
        '{.status.conditions[?(@.type=="Ready")].status}' True "$NAMESPACE" "$timeout"
}

# Run a bash snippet in a throwaway Strimzi Kafka Pod, with $BOOT / $BIN / $TOPIC
# pre-set (values injected via printf %q so the snippet needs no nested quoting).
# Host-side analogue used only to seed and verify — the backup/restore Jobs are
# created by the controller from the strategy.
kafka_run() {
    local app="$1"; shift
    local snippet="$1"
    local boot="kafka-${app}-kafka-bootstrap.${NAMESPACE}.svc:9092"
    kubectl -n "$NAMESPACE" run "kafka-cli-$RANDOM" \
        --image="$KAFKA_IMAGE" --restart=Never --rm -i --quiet \
        --pod-running-timeout=5m \
        --command -- bash -c "set -eu
BOOT=$(printf %q "$boot")
BIN=$(printf %q "$KAFKA_BIN")
TOPIC=$(printf %q "$TOPIC")
PARTITIONS=$(printf %q "$PARTITIONS")
$snippet"
}

# Create the demo topic with a distinctive retention.ms sentinel so the restore
# can prove the topic config — not just the name — round-tripped.
seed_topic() {
    local app="$1" retention="$2"
    kafka_run "$app" '
        "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --create --if-not-exists \
            --topic "$TOPIC" --partitions "$PARTITIONS" --replication-factor 1 \
            --config retention.ms='"$retention"'
    '
}

# Print "<partitions> <retention.ms>" for the demo topic, or "" if absent.
topic_meta() {
    local app="$1"
    kafka_run "$app" '
        line=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --describe --topic "$TOPIC" 2>/dev/null | head -1) || exit 0
        [ -n "$line" ] || exit 0
        parts=$(printf "%s" "$line" | grep -oE "PartitionCount: [0-9]+" | awk "{print \$2}")
        ret=$(printf "%s" "$line" | grep -oE "retention.ms=[0-9]+" | head -1 | cut -d= -f2)
        printf "%s %s\n" "$parts" "$ret"
    ' 2>/dev/null | tr -d '\r'
}
