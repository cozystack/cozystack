#!/bin/bash
# Shared helpers for the Kafka topic-data backup/restore demo.
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

# Default settings (override via environment).
export NAMESPACE="${NAMESPACE:-tenant-test}"
export KAFKA_NAME="${KAFKA_NAME:-kafka-test}"
export KAFKA_RESTORE_NAME="${KAFKA_RESTORE_NAME:-kafka-restore}"
export TOPIC="${TOPIC:-orders}"
export PARTITIONS="${PARTITIONS:-3}"
export MESSAGE_COUNT="${MESSAGE_COUNT:-30}"
export BUCKET_NAME="${BUCKET_NAME:-kafka-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-kafka-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-kafka-job}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-kafka-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-kafka-restore-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-kafka-restore-to-copy}"
# The Strimzi Kafka image carries the full kafka-*.sh CLI plus bash, curl and
# tar - everything the generic Job strategy and these host-side helpers need,
# with no purpose-built backup image. Override KAFKA_IMAGE to match your
# operator's Kafka image if it differs:
#   kubectl -n cozy-kafka-operator get deploy strimzi-cluster-operator \
#     -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' | grep KAFKA_IMAGES
export KAFKA_IMAGE="${KAFKA_IMAGE:-quay.io/strimzi/kafka:0.45.1-rc1-kafka-3.8.0}"
export KAFKA_BIN="${KAFKA_BIN:-/opt/kafka/bin}"

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}> $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }
log_command() { echo -e "${WHITE}  $ $*${NC}" >&2; }

separator() {
    echo -e "\n${CYAN}------------------------------------------------------------${NC}\n" >&2
}

print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}== $title ==${NC}\n" >&2
}

# Wait until a JSONPath value on a resource matches the desired string.
# Optional 7th arg is a TERMINAL failure value: once the field reaches it the
# wait returns 1 immediately instead of polling to the timeout. BackupJob and
# RestoreJob settle on a terminal phase=Failed that never becomes Succeeded (the
# strategy Job exhausts its backoffLimit), so failing fast on it keeps
# wall-clock - and the strategy Pod's log - in reach instead of burning the full
# timeout and then mislabelling a deterministic failure as "Timeout".
wait_for_field() {
    local resource_type="$1"
    local resource_name="$2"
    local jsonpath="$3"
    local desired="$4"
    local namespace="${5:-}"
    local timeout="${6:-300}"
    local fail_value="${7:-}"

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

# Wait for a HelmRelease to become Ready, with an existence backstop (the apps
# controller creates the HR asynchronously, so a bare `kubectl wait` right after
# `kubectl apply` races it) and a fail-fast on Stalled=True - a stalled HR has
# exhausted its remediation retries and will never turn Ready, so polling to the
# timeout only hides the real error.
wait_hr_ready() {
    local name="$1"
    local timeout="${2:-300}"
    local elapsed=0

    log_substep "Waiting for HelmRelease/$name to become Ready..."
    while true; do
        if kubectl -n "$NAMESPACE" get hr "$name" >/dev/null 2>&1; then
            local ready stalled
            ready=$(kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
            if [[ "$ready" == "True" ]]; then
                log_success "HelmRelease/$name is Ready"
                return 0
            fi
            stalled=$(kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Stalled")].status}' 2>/dev/null || true)
            if [[ "$stalled" == "True" ]]; then
                log_error "HelmRelease/$name is Stalled (terminal): $(kubectl -n "$NAMESPACE" get hr "$name" \
                    -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null)"
                return 1
            fi
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for HelmRelease/$name to become Ready:"
            kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' >&2 2>/dev/null || true
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# In-cluster bootstrap address for a Kafka application instance. The Cozystack
# chart names the Strimzi Kafka cluster "kafka-<app>", and Strimzi names the
# plaintext bootstrap Service "<cluster>-kafka-bootstrap" - so for an app named
# <app> the Service is "kafka-<app>-kafka-bootstrap". The internal plain
# listener on 9092 has no TLS or auth, so a bare host:port is all the CLI needs.
kafka_bootstrap() {
    local app="$1"
    echo "kafka-${app}-kafka-bootstrap.${NAMESPACE}.svc:9092"
}

# Run a bash snippet in a throwaway Strimzi Kafka Pod. The snippet runs with
# these variables pre-set, so it needs no nested shell quoting of its own:
#   $BOOT  - the target app's plaintext bootstrap (host:port)
#   $BIN   - the kafka CLI directory
#   $TOPIC, $PARTITIONS, $MESSAGE_COUNT - the demo knobs
# This is the host-side analogue of the strategy Pod: same stock image, same
# tools, no purpose-built backup container. Pass the snippet single-quoted so
# its own $VAR references reach the Pod's bash unexpanded.
kafka_run() {
    local app="$1"; shift
    local snippet="$1"
    local boot
    boot="$(kafka_bootstrap "$app")"
    kubectl -n "$NAMESPACE" run "kafka-cli-$RANDOM" \
        --image="$KAFKA_IMAGE" --restart=Never --rm -i --quiet \
        --pod-running-timeout=5m \
        --command -- bash -c "set -eu
BOOT=$(printf %q "$boot")
BIN=$(printf %q "$KAFKA_BIN")
TOPIC=$(printf %q "$TOPIC")
PARTITIONS=$(printf %q "$PARTITIONS")
MESSAGE_COUNT=$(printf %q "$MESSAGE_COUNT")
$snippet"
}

# Create the demo topic (idempotent) and publish MESSAGE_COUNT keyed sentinel
# messages spread across PARTITIONS. Keys ("k-<n>") make partition placement
# deterministic on restore: re-producing the same key into a topic with the
# same partition count lands it in the same partition via the default
# (murmur2) partitioner - no per-partition produce needed for keyed records.
seed_topic() {
    local app="$1"
    kafka_run "$app" '
        "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --create --if-not-exists \
            --topic "$TOPIC" --partitions "$PARTITIONS" --replication-factor 1
        i=1
        while [ "$i" -le "$MESSAGE_COUNT" ]; do
            printf "k-%s\torder-%s\n" "$i" "$i"
            i=$((i + 1))
        done | "$BIN"/kafka-console-producer.sh --bootstrap-server "$BOOT" \
            --topic "$TOPIC" --property parse.key=true
    '
}

# Total number of records currently stored in a topic, summed over partitions
# as (end offset - begin offset). Prints a bare integer, or "" if the topic
# does not exist / cannot be reached. Offsets, not a consumer, so it is exact
# and cheap even for a compacted or truncated topic. kafka-get-offsets emits
# "topic:partition:offset" lines; pure-shell parsing avoids awk quoting.
topic_message_count() {
    local app="$1"
    kafka_run "$app" '
        ends=$("$BIN"/kafka-get-offsets.sh --bootstrap-server "$BOOT" --topic "$TOPIC" --time -1 2>/dev/null) || exit 0
        begins=$("$BIN"/kafka-get-offsets.sh --bootstrap-server "$BOOT" --topic "$TOPIC" --time -2 2>/dev/null) || exit 0
        [ -n "$ends" ] || exit 0
        total=0
        for e in $ends; do
            ep=${e%:*}; ep=${ep##*:}; eo=${e##*:}
            for b in $begins; do
                bp=${b%:*}; bp=${bp##*:}
                if [ "$bp" = "$ep" ]; then total=$((total + eo - ${b##*:})); break; fi
            done
        done
        echo "$total"
    ' 2>/dev/null | tr -d '[:space:]'
}

# Dump every record of the demo topic as "partition<TAB>key<TAB>value" lines,
# partitions in ascending order and records in offset order within each
# partition. Deterministic, so the same topic dumps byte-identically twice -
# a source dump captured before backup and the restored dump can be diffed to
# prove keys, values, partition placement and per-partition ordering survived
# the round-trip, which a bare offset count cannot. Assumes begin offset 0
# (every topic in this demo is freshly created); GNU sed renders "\t" as a tab.
topic_dump() {
    local app="$1"
    kafka_run "$app" '
        ends=$("$BIN"/kafka-get-offsets.sh --bootstrap-server "$BOOT" --topic "$TOPIC" --time -1 2>/dev/null) || exit 0
        [ -n "$ends" ] || exit 0
        printf "%s\n" $ends | sort -t: -k2 -n | while IFS=: read -r t p end; do
            [ "${end:-0}" -gt 0 ] || continue
            "$BIN"/kafka-console-consumer.sh --bootstrap-server "$BOOT" --topic "$TOPIC" \
                --partition "$p" --offset 0 --max-messages "$end" --timeout-ms 60000 \
                --property print.key=true --property print.timestamp=false \
                | sed "s/^/$p\t/"
        done
    ' 2>/dev/null
}

# Create the "<app>-backup-s3" Secret the Job strategy Pod consumes, from the
# bucket coordinates cached by 03-create-bucket.sh. The generic Job strategy -
# unlike the app-specific drivers - has no chart support to emit this Secret,
# so the tenant provides it. Called for the source app (step 04) and the
# restore target (step 07).
create_s3_secret() {
    local app="$1"
    local SCRIPT_DIR
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    [[ -f "$SCRIPT_DIR/.bucket-info.env" ]] || { log_error "missing $SCRIPT_DIR/.bucket-info.env; run 03-create-bucket.sh first"; return 1; }
    # shellcheck disable=SC1091
    source "$SCRIPT_DIR/.bucket-info.env"
    for v in S3_ACCESS_KEY S3_SECRET_KEY S3_ENDPOINT S3_REGION S3_BUCKET; do
        [[ -n "${!v:-}" ]] || { log_error "required variable is missing or empty: ${v}"; return 1; }
    done
    kubectl -n "$NAMESPACE" create secret generic "${app}-backup-s3" \
        --from-literal=accessKey="$S3_ACCESS_KEY" \
        --from-literal=secretKey="$S3_SECRET_KEY" \
        --from-literal=endpoint="$S3_ENDPOINT" \
        --from-literal=region="$S3_REGION" \
        --from-literal=bucket="$S3_BUCKET" \
        --dry-run=client -o yaml | kubectl apply -f -
}
