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
# KAFKA_IMAGE runs the long-lived CLI Pod the seed/verify helpers exec into (see
# kafka_run). The backup/restore Jobs no longer use it: the controller resolves
# the target broker's own image at reconcile time and renders it as the
# strategy's .ClientImage. Override this only to match your operator's image if
# the CLI it carries differs.
export KAFKA_IMAGE="${KAFKA_IMAGE:-quay.io/strimzi/kafka:0.45.1-rc1-kafka-3.9.1@sha256:ba52ed046b1dccdbd96f4e68057ce014d862a7c9c1fc670760c023b9aa09f23f}"
export KAFKA_BIN="${KAFKA_BIN:-/opt/kafka/bin}"
# Name of that long-lived CLI Pod; cleanup.sh removes it.
export KAFKA_CLI_POD="${KAFKA_CLI_POD:-kafka-cli}"

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

# wait_for_field, wait_hr_ready and wait_deleted live in one file shared by
# every backup walkthrough, so a fix to one reaches all of them.
# shellcheck source-path=SCRIPTDIR source=../_lib/wait-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../_lib/wait-helpers.sh"

# Wait until the Strimzi Kafka cluster kafka-<app> reports Ready=True — the same
# precondition the driver gates on.
kafka_wait_ready() {
    local app="$1" timeout="${2:-600}"
    wait_for_field kafka.kafka.strimzi.io "kafka-${app}" \
        '{.status.conditions[?(@.type=="Ready")].status}' True "$NAMESPACE" "$timeout"
}

# Ensure the long-lived kafka-cli Pod exists and is Ready, so kafka_run can exec
# into it. Idempotent: a Ready Pod this demo owns is reused across calls and
# across the numbered demo scripts; a leftover in a terminal phase (Succeeded /
# Failed) is replaced rather than waited on; and a same-named Pod this demo does
# not own is refused rather than hijacked or deleted. cleanup.sh removes it.
# Every step returns on failure explicitly: this runs as the left side of
# `|| return 1`, and some callers also wrap it in $(...), so errexit never
# applies inside it and a bare failure would carry on to the next step.
kafka_cli_pod() {
    local phase owner
    phase=$(kubectl -n "$NAMESPACE" get pod "$KAFKA_CLI_POD" --ignore-not-found -o jsonpath='{.status.phase}') || return 1
    owner=$(kubectl -n "$NAMESPACE" get pod "$KAFKA_CLI_POD" --ignore-not-found -o jsonpath='{.metadata.labels.cozystack\.io/backup-demo}') || return 1
    if [ -n "$phase" ] && [ "$owner" != "kafka-metadata" ]; then
        log_error "Pod $NAMESPACE/$KAFKA_CLI_POD exists but this demo does not own it; refusing to use or delete it"
        return 1
    fi
    if [ "$phase" != "Running" ] && [ "$phase" != "Pending" ]; then
        kubectl -n "$NAMESPACE" delete pod "$KAFKA_CLI_POD" --grace-period=1 --ignore-not-found >/dev/null || return 1
        kubectl -n "$NAMESPACE" run "$KAFKA_CLI_POD" --image="$KAFKA_IMAGE" \
            --labels=cozystack.io/backup-demo=kafka-metadata \
            --restart=Never --command -- sleep infinity >/dev/null || return 1
    fi
    kubectl -n "$NAMESPACE" wait --for=condition=Ready "pod/$KAFKA_CLI_POD" \
        --timeout=5m >/dev/null
}

# Run a bash snippet against the source Kafka, with $BOOT / $BIN / $TOPIC /
# $PARTITIONS pre-set (values injected via printf %q so the snippet needs no
# nested quoting). Host-side analogue used only to seed and verify — the
# backup/restore Jobs are created by the controller from the strategy.
#
# The snippet runs by `kubectl exec` in the long-lived CLI Pod, not a throwaway
# `kubectl run -i` Pod per call. A throwaway Pod's stdout comes back over an
# attach that only carries what the container writes after the attach registers,
# so a CLI that finishes in between returns empty with exit 0. An exec'd process
# owns its pipes, so its output cannot be missed that way, and kubectl's and the
# CLI's stderr stay attached so a failed read says why instead of reading as ''.
kafka_run() {
    local app="$1"; shift
    local snippet="$1"
    local boot="kafka-${app}-kafka-bootstrap.${NAMESPACE}.svc:9092"
    kafka_cli_pod || return 1
    kubectl -n "$NAMESPACE" exec -i "$KAFKA_CLI_POD" -- bash -c "set -eu
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
    ' | tr -d '\r\n'
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
    ' | tr -d '\r'
}
