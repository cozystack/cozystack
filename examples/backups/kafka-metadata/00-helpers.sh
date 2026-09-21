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

# Default settings (override via environment). The demo derives its OWN Kafka
# strategy + BackupClass from the platform-shipped cozy-default-kafka strategy
# (run-all.sh patches its S3_ENDPOINT and mounts the S3 CA), so the round-trip
# can target an in-cluster, TLS-verifiable S3 endpoint the shipped strategy's
# advertised external ingress is not in CI. The shipped cozy-default-kafka is
# left untouched.
export NAMESPACE="${NAMESPACE:-tenant-root}"
export KAFKA_SRC_NAME="${KAFKA_SRC_NAME:-kafka-meta-src}"
export KAFKA_TARGET_NAME="${KAFKA_TARGET_NAME:-kafka-meta-target}"
export TOPIC="${TOPIC:-orders}"
export PARTITIONS="${PARTITIONS:-3}"
# Colliding topic pair that exercises the --topic regex-vs-literal fix in the
# strategy script: "audit.events" as a Java regex ALSO matches "audit-events"
# (. is any char), so a describe that does not wrap the name in \Q...\E records
# one topic's partition count against the other. Distinct counts make that
# cross-match fail the round-trip verify.
export COLLIDE_DOT="${COLLIDE_DOT:-audit.events}"
export COLLIDE_DOT_PARTS="${COLLIDE_DOT_PARTS:-2}"
export COLLIDE_DASH="${COLLIDE_DASH:-audit-events}"
export COLLIDE_DASH_PARTS="${COLLIDE_DASH_PARTS:-5}"
# The demo's own strategy + BackupClass, derived at run time from the shipped
# cozy-default-kafka so the driver script stays a single source of truth. Named
# distinctly so they never collide with the platform cozy-default class.
export STRATEGY_NAME="${STRATEGY_NAME:-kafka-strategy-default}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-kafka-metadata}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-kafka-meta-src-adhoc}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-kafka-meta-src-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-kafka-meta-src-to-target}"
export PLAN_NAME="${PLAN_NAME:-kafka-meta-src-daily}"
# S3_ENDPOINT overrides the endpoint the backup Pod uses. The shipped strategy
# advertises the EXTERNAL S3 ingress, which in-cluster Pods cannot always
# resolve or TLS-validate (in CI it is an unroutable placeholder). The
# in-cluster alternative is https://seaweedfs-s3.<ns>.svc:8333 — the .svc FQDN
# is what the seaweedfs serving cert's SAN covers, so curl verifies TLS against
# the copied CA below. CI sets this; a real cluster can leave it unset.
export S3_ENDPOINT="${S3_ENDPOINT:-}"
# Self-signed seaweedfs CA: run-all.sh discovers it and copies ca.crt into
# CA_SECRET, which the demo strategy Pod mounts and points CURL_CA_BUNDLE at.
# Set S3_CA_SECRET="" to skip on a publicly-trusted endpoint.
export CA_SECRET="${CA_SECRET:-kafka-backup-ca}"
export S3_CA_SECRET="${S3_CA_SECRET:-seaweedfs-ca-cert}"
export S3_CA_NAMESPACE="${S3_CA_NAMESPACE:-tenant-root}"
export S3_CA_KEY="${S3_CA_KEY:-ca.crt}"
export CA_MOUNT_DIR="${CA_MOUNT_DIR:-/etc/ssl/kafka-backup-ca}"
# The Cozystack chart names the Strimzi cluster kafka-<app>; its plaintext
# bootstrap Service is kafka-<app>-kafka-bootstrap:9092.
# KAFKA_IMAGE only drives the host-side throwaway CLI pods here (seed/verify).
# The backup/restore Jobs no longer use it: the controller resolves the target
# broker's own image at reconcile time and renders it as the strategy's
# .ClientImage. Override this only to match your operator's image for the
# seed/verify pods if it differs.
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

# copy_s3_ca copies the self-signed seaweedfs CA (ca.crt) into CA_SECRET in
# NAMESPACE so the demo strategy Pod can verify TLS against the in-cluster S3
# endpoint. Echoes "1" when a CA was copied, "0" when skipped (S3_CA_SECRET
# empty → a publicly-trusted endpoint). Mirrors examples/backups/rabbitmq.
copy_s3_ca() {
    if [[ -z "$S3_CA_SECRET" ]]; then echo 0; return 0; fi
    if ! kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" >/dev/null 2>&1; then
        log_warning "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} not found; discovering the seaweedfs CA Certificate..."
        local discovered
        discovered=$(kubectl -n "$S3_CA_NAMESPACE" get certificates.cert-manager.io \
            -l app.kubernetes.io/name=seaweedfs \
            -o jsonpath='{range .items[*]}{.spec.isCA}{" "}{.spec.secretName}{"\n"}{end}' 2>/dev/null \
            | awk '$1=="true"{print $2; exit}' || true)
        [[ -n "$discovered" ]] || { log_error "No seaweedfs CA Certificate found in ${S3_CA_NAMESPACE}; set S3_CA_SECRET explicitly (or empty for a public-CA endpoint)."; return 1; }
        log_success "Discovered seaweedfs CA secret ${S3_CA_NAMESPACE}/${discovered}"
        S3_CA_SECRET="$discovered"
    fi
    local ca_pem
    ca_pem=$(kubectl -n "$S3_CA_NAMESPACE" get secret "$S3_CA_SECRET" \
        -o jsonpath="{.data.${S3_CA_KEY//./\\.}}" | base64 -d)
    [[ -n "$ca_pem" ]] || { log_error "S3 CA secret ${S3_CA_NAMESPACE}/${S3_CA_SECRET} has no ${S3_CA_KEY}"; return 1; }
    kubectl -n "$NAMESPACE" create secret generic "$CA_SECRET" \
        --from-literal="ca.crt=${ca_pem}" \
        --dry-run=client -o yaml | kubectl -n "$NAMESPACE" apply -f - >&2
    echo 1
}

# provision_demo_strategy derives the demo Kafka strategy from the shipped
# cozy-default-kafka (single source of truth for the driver script), overriding
# S3_ENDPOINT and, when $2==1, mounting CA_SECRET + pointing CURL_CA_BUNDLE at
# it. Then applies the demo BackupClass. Args: <endpoint> <ca_present 0|1>.
provision_demo_strategy() {
    local endpoint="$1" ca_present="$2"
    kubectl get kafka.strategy.backups.cozystack.io cozy-default-kafka -o json \
      | jq \
          --arg ep "$endpoint" --arg name "$STRATEGY_NAME" \
          --arg caDir "$CA_MOUNT_DIR" --arg caPath "${CA_MOUNT_DIR}/ca.crt" \
          --arg caSecret "$CA_SECRET" --argjson ca "$ca_present" '
        .metadata = {name: $name}
        | del(.status)
        | .spec.template.spec.containers[0].env = (
            ((.spec.template.spec.containers[0].env // [])
              | map(if .name == "S3_ENDPOINT" then {name: "S3_ENDPOINT", value: $ep} else . end))
            + (if $ca == 1 then [{name: "CURL_CA_BUNDLE", value: $caPath}] else [] end))
        | if $ca == 1 then
            .spec.template.spec.containers[0].volumeMounts =
              ((.spec.template.spec.containers[0].volumeMounts // [])
                + [{name: "s3-ca", mountPath: $caDir, readOnly: true}])
            | .spec.template.spec.volumes =
              ((.spec.template.spec.volumes // []) + [{name: "s3-ca", secret: {secretName: $caSecret}}])
          else . end
      ' | kubectl apply -f - >&2
    kubectl apply -f "$SCRIPT_DIR/03-backupclass.yaml" >&2
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

# Create a topic with an explicit partition count (no config sentinel). Used for
# the colliding pair, whose distinct partition counts are the round-trip proof.
seed_topic_partitions() {
    local app="$1" topic="$2" partitions="$3"
    TOPIC="$topic" PARTITIONS="$partitions" kafka_run "$app" '
        "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --create --if-not-exists \
            --topic "$TOPIC" --partitions "$PARTITIONS" --replication-factor 1
    '
}

# Delete a specific topic (literal name via \Q) and wait for it to disappear.
delete_topic() {
    local app="$1" topic="$2"
    TOPIC="$topic" kafka_run "$app" '
        "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --delete --topic "\Q$TOPIC\E" || true
        for _ in $(seq 1 60); do
            list=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --list) || { sleep 2; continue; }
            printf "%s\n" "$list" | grep -qxF -- "$TOPIC" || { echo "topic $TOPIC deleted"; exit 0; }
            sleep 2
        done
        echo "topic $TOPIC still present after wait" >&2; exit 1
    '
}

# Print the partition count for a specific topic (literal \Q match), or "" if
# absent. The literal match is what a restore of the colliding pair must honour.
topic_partitions() {
    local app="$1" topic="$2"
    TOPIC="$topic" kafka_run "$app" '
        line=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --describe --topic "\Q$TOPIC\E" 2>/dev/null | head -1) || exit 0
        [ -n "$line" ] || exit 0
        printf "%s" "$line" | grep -oE "PartitionCount: [0-9]+" | awk "{print \$2}"
    ' 2>/dev/null | tr -d '\r\n'
}

# Print "<partitions> <retention.ms>" for the demo topic, or "" if absent.
topic_meta() {
    local app="$1"
    kafka_run "$app" '
        line=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --describe --topic "\Q$TOPIC\E" 2>/dev/null | head -1) || exit 0
        [ -n "$line" ] || exit 0
        parts=$(printf "%s" "$line" | grep -oE "PartitionCount: [0-9]+" | awk "{print \$2}")
        ret=$(printf "%s" "$line" | grep -oE "retention.ms=[0-9]+" | head -1 | cut -d= -f2)
        printf "%s %s\n" "$parts" "$ret"
    ' 2>/dev/null | tr -d '\r'
}
