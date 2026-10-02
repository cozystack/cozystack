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
export NAMESPACE="${NAMESPACE:-tenant-root}"
export KAFKA_NAME="${KAFKA_NAME:-kafka-test}"
export KAFKA_RESTORE_NAME="${KAFKA_RESTORE_NAME:-kafka-restore}"
# Two brokers, not one: the Cozystack Kafka chart raises the broker-level
# min.insync.replicas to 2 as soon as kafka.replicas reaches 2, and every
# acks=all write into a topic with fewer replicas than that is rejected. A
# single-broker demo could never show that a restore recreates the topic with
# the replication factor the backup captured; the demo topics are seeded with
# TOPIC_REPLICAS replicas and the restore steps assert it survived the
# round-trip. Both Kafka instances use KAFKA_REPLICAS so the to-copy target can
# host the same replication factor.
export KAFKA_REPLICAS="${KAFKA_REPLICAS:-2}"
export TOPIC_REPLICAS="${TOPIC_REPLICAS:-2}"
# The demo topic carries a "." and a decoy topic differs from it only where
# that "." sits: as a Java regex "orders.v1" also matches "orders-v1", as a
# literal it does not. Every --topic call the CLI treats as a regex is pinned
# with \Q...\E, and this pair is what makes that pinning testable - drop a pin
# and the run fails on its own (the partition-set guard sees the decoy's
# partitions, or the in-place delete takes the decoy with it).
#
# What the decoy reliably proves: the offset listing (the partition-set guard
# counts its partitions), the in-place delete (it disappears), and the two
# record counts (they pick up its records). It does NOT prove the two
# PartitionCount reads: kafka-topics --describe yields topics in the admin
# client result-map order rather than sorted, so which one their head -1 sees
# when a pin is missing is not something to lean on.
export TOPIC="${TOPIC:-orders.v1}"
export DECOY_TOPIC="${DECOY_TOPIC:-orders-v1}"
export DECOY_COUNT="${DECOY_COUNT:-5}"
export PARTITIONS="${PARTITIONS:-3}"
export MESSAGE_COUNT="${MESSAGE_COUNT:-30}"
export BUCKET_NAME="${BUCKET_NAME:-kafka-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-kafka-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-kafka-job}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-kafka-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-kafka-restore-inplace}"
export RESTOREJOB_NONEMPTY_NAME="${RESTOREJOB_NONEMPTY_NAME:-kafka-restore-nonempty}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-kafka-restore-to-copy}"
# S3 endpoint CA. Cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; 03-create-bucket.sh
# caches its ca.crt and create_s3_secret projects it into each
# "<app>-backup-s3" Secret, which the strategy Pod mounts and points curl at.
# The name follows the seaweedfs chart's fullnameOverride ("seaweedfs" ->
# "seaweedfs-ca-cert"); 03-create-bucket.sh auto-discovers the CA Certificate's
# actual secret when this default is absent. On a cluster whose S3 endpoint is
# signed by a publicly-trusted CA, set S3_CA_SECRET="" to skip the copy: the
# Pod then verifies against the image's trust store.
export S3_CA_SECRET="${S3_CA_SECRET:-seaweedfs-ca-cert}"
export S3_CA_NAMESPACE="${S3_CA_NAMESPACE:-tenant-root}"
export S3_CA_KEY="${S3_CA_KEY:-ca.crt}"
# The Strimzi Kafka image carries the full kafka-*.sh CLI plus bash, curl and
# tar - everything the generic Job strategy and these host-side helpers need,
# with no purpose-built backup image.
#
# Default to the image the operator already caches on the nodes so the backup
# Pod and these CLI Pods reuse a warm image with no version skew. The operator's
# STRIMZI_KAFKA_IMAGES env is a newline-separated "version=image" map (one entry
# per supported version); split on whitespace, drop the "version=" prefix and
# take the newest (last) entry - the version the operator itself defaults to.
# The image reference is taken as-is: a digest-pinned operator emits
# "<version>=<repo>@sha256:..." with no ":<tag>", and that is exactly the
# air-gapped deployment the fallback below cannot serve.
# Fall back to a pinned literal only when the operator cannot be read (an
# air-gapped clone with no cluster), so the demo still has a runnable default.
# Setting KAFKA_IMAGE in the environment skips the lookup entirely.
resolve_kafka_image() {
    local resolved
    resolved=$(kubectl -n cozy-kafka-operator get deploy strimzi-cluster-operator \
        -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="STRIMZI_KAFKA_IMAGES")].value}' 2>/dev/null \
        | tr '[:space:]' '\n' | grep '=' | sed 's/^[^=]*=//' | tail -n1)
    if [ -n "$resolved" ]; then
        echo "$resolved"
        return
    fi
    # Do not fail open silently: warn on stderr (stdout is captured as the
    # image) that the operator could not be read and a public quay.io literal
    # is being assumed. On an air-gapped or operator-less cluster - the persona
    # the README's override note is for - that literal is unreachable, so the
    # run must set KAFKA_IMAGE to an image the nodes can actually pull.
    echo "! could not read STRIMZI_KAFKA_IMAGES from deploy/strimzi-cluster-operator in cozy-kafka-operator; assuming quay.io/strimzi/kafka:0.45.1-rc1-kafka-3.9.1. Set KAFKA_IMAGE explicitly if that image is wrong or unreachable (e.g. air-gapped)." >&2
    echo "quay.io/strimzi/kafka:0.45.1-rc1-kafka-3.9.1"
}
export KAFKA_IMAGE="${KAFKA_IMAGE:-$(resolve_kafka_image)}"
export KAFKA_BIN="${KAFKA_BIN:-/opt/kafka/bin}"
# Name of the long-lived CLI Pod kafka_run execs into; cleanup.sh removes it.
# A variable of its own, not the kafka-metadata demo's KAFKA_CLI_POD: the two
# demos may share a namespace, and one setting must not move the other's Pod.
export KAFKA_TOPIC_CLI_POD="${KAFKA_TOPIC_CLI_POD:-kafka-topic-cli}"

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

# wait_for_field, wait_hr_ready and wait_deleted live in one file shared by
# every backup walkthrough, so a fix to one reaches all of them.
# shellcheck source-path=SCRIPTDIR source=../_lib/wait-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../_lib/wait-helpers.sh"

# In-cluster bootstrap address for a Kafka application instance. The Cozystack
# chart names the Strimzi Kafka cluster "kafka-<app>", and Strimzi names the
# plaintext bootstrap Service "<cluster>-kafka-bootstrap" - so for an app named
# <app> the Service is "kafka-<app>-kafka-bootstrap". The internal plain
# listener on 9092 has no TLS or auth, so a bare host:port is all the CLI needs.
kafka_bootstrap() {
    local app="$1"
    echo "kafka-${app}-kafka-bootstrap.${NAMESPACE}.svc:9092"
}

# Ensure the long-lived CLI Pod exists and is Ready, so kafka_run can exec into
# it. A Ready Pod this demo owns is reused across calls and across the numbered
# scripts; a leftover in a terminal phase is replaced; a same-named Pod the demo
# does not own is refused rather than hijacked or deleted.
# Every step returns on failure explicitly: this runs as the left side of
# `|| return 1`, and some callers also wrap it in $(...), so errexit never
# applies inside it and a bare failure would carry on to the next step.
kafka_cli_pod() {
    local phase owner overrides
    phase=$(kubectl -n "$NAMESPACE" get pod "$KAFKA_TOPIC_CLI_POD" --ignore-not-found -o jsonpath='{.status.phase}') || return 1
    owner=$(kubectl -n "$NAMESPACE" get pod "$KAFKA_TOPIC_CLI_POD" --ignore-not-found -o jsonpath='{.metadata.labels.cozystack\.io/backup-demo}') || return 1
    if [ -n "$phase" ] && [ "$owner" != "kafka" ]; then
        log_error "Pod $NAMESPACE/$KAFKA_TOPIC_CLI_POD exists but this demo does not own it; refusing to use or delete it"
        return 1
    fi
    if [ "$phase" != "Running" ] && [ "$phase" != "Pending" ]; then
        # restricted-clean Pod. The Strimzi image runs as a non-root numeric
        # UID (1001), so runAsNonRoot/seccomp at the Pod level plus the
        # container's allowPrivilegeEscalation=false and drop-ALL satisfy PSA
        # "restricted" without pinning runAsUser to a value specific to this
        # image tag. An add-only JSON patch layers these onto the run-generated
        # Pod's sole container (index 0), leaving image, command and args
        # untouched.
        overrides='[{"op":"add","path":"/spec/securityContext","value":{"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}},{"op":"add","path":"/spec/containers/0/securityContext","value":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]'
        kubectl -n "$NAMESPACE" delete pod "$KAFKA_TOPIC_CLI_POD" --grace-period=1 --ignore-not-found >/dev/null || return 1
        kubectl -n "$NAMESPACE" run "$KAFKA_TOPIC_CLI_POD" --image="$KAFKA_IMAGE" \
            --labels=cozystack.io/backup-demo=kafka \
            --override-type=json --overrides="$overrides" \
            --restart=Never --command -- sleep infinity >/dev/null || return 1
    fi
    kubectl -n "$NAMESPACE" wait --for=condition=Ready "pod/$KAFKA_TOPIC_CLI_POD" \
        --timeout=5m >/dev/null
}

# Run a bash snippet in the demo's Strimzi Kafka CLI Pod. The snippet runs with
# these variables pre-set, so it needs no nested shell quoting of its own:
#   $BOOT  - the target app's plaintext bootstrap (host:port)
#   $BIN   - the kafka CLI directory
#   $TOPIC, $PARTITIONS, $TOPIC_REPLICAS, $MESSAGE_COUNT - the demo knobs
# This is the host-side analogue of the strategy Pod: same stock image, same
# tools, no purpose-built backup container. Pass the snippet single-quoted so
# its own $VAR references reach the Pod's bash unexpanded.
#
# The snippet runs by `kubectl exec`, not in a throwaway `kubectl run -i` Pod.
# That Pod's stdout comes back over an attach carrying only what the container
# writes after the attach registers, so a CLI that finishes first reads as an
# empty reply with exit 0. An exec'd process owns its pipes, so its output
# cannot be missed that way.
kafka_run() {
    local app="$1"; shift
    local snippet="$1"
    local boot
    boot="$(kafka_bootstrap "$app")"
    kafka_cli_pod || return 1
    kubectl -n "$NAMESPACE" exec -i "$KAFKA_TOPIC_CLI_POD" -- bash -c "set -eu
BOOT=$(printf %q "$boot")
BIN=$(printf %q "$KAFKA_BIN")
TOPIC=$(printf %q "$TOPIC")
PARTITIONS=$(printf %q "$PARTITIONS")
TOPIC_REPLICAS=$(printf %q "$TOPIC_REPLICAS")
MESSAGE_COUNT=$(printf %q "$MESSAGE_COUNT")
DECOY_TOPIC=$(printf %q "$DECOY_TOPIC")
DECOY_COUNT=$(printf %q "$DECOY_COUNT")
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
            --topic "$TOPIC" --partitions "$PARTITIONS" --replication-factor "$TOPIC_REPLICAS"
        i=1
        while [ "$i" -le "$MESSAGE_COUNT" ]; do
            printf "k-%s\torder-%s\n" "$i" "$i"
            i=$((i + 1))
        done | "$BIN"/kafka-console-producer.sh --bootstrap-server "$BOOT" \
            --topic "$TOPIC" --property parse.key=true
    '
}

# Seed the decoy topic: a single partition and a handful of records, enough to
# tell "still there, untouched" from "deleted" or "backed up by mistake".
seed_decoy_topic() {
    local app="$1"
    kafka_run "$app" '
        "$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --create --if-not-exists \
            --topic "$DECOY_TOPIC" --partitions 1 --replication-factor "$TOPIC_REPLICAS"
        i=1
        while [ "$i" -le "$DECOY_COUNT" ]; do
            printf "d-%s\tdecoy-%s\n" "$i" "$i"
            i=$((i + 1))
        done | "$BIN"/kafka-console-producer.sh --bootstrap-server "$BOOT" \
            --topic "$DECOY_TOPIC" --property parse.key=true
    '
}

# Record count of the decoy topic. Prints a bare integer, or "" when the topic
# is gone - which is itself the signal an unpinned --topic regex deleted it.
decoy_message_count() {
    topic_message_count "$1" "$DECOY_TOPIC"
}

# Total number of records currently stored in a topic (the demo topic unless a
# second argument names another), summed over partitions as (end offset -
# begin offset). Prints a bare integer, or "" if the topic does not exist /
# cannot be reached. Offsets, not a consumer, so it is exact and cheap even for
# a compacted or truncated topic. kafka-get-offsets emits
# "topic:partition:offset" lines; pure-shell parsing avoids awk quoting.
topic_message_count() {
    local app="$1"
    TOPIC="${2:-$TOPIC}" kafka_run "$app" '
        ends=$("$BIN"/kafka-get-offsets.sh --bootstrap-server "$BOOT" --topic "\Q$TOPIC\E" --time -1 2>/dev/null) || exit 0
        begins=$("$BIN"/kafka-get-offsets.sh --bootstrap-server "$BOOT" --topic "\Q$TOPIC\E" --time -2 2>/dev/null) || exit 0
        [ -n "$ends" ] || exit 0
        total=0
        for e in $ends; do
            ekey=${e%:*}; eo=${e##*:}
            # Default the begin offset to 0 rather than skipping the partition:
            # a partition missing from "begins" would otherwise contribute
            # nothing and silently undercount the topic. Match on the whole
            # topic:partition key so a line from another topic cannot supply
            # the begin offset for this partition. Matches the strategy.
            bo=0
            for b in $begins; do
                if [ "${b%:*}" = "$ekey" ]; then bo=${b##*:}; break; fi
            done
            total=$((total + eo - bo))
        done
        echo "$total"
    ' | tr -d '[:space:]'
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
        ends=$("$BIN"/kafka-get-offsets.sh --bootstrap-server "$BOOT" --topic "\Q$TOPIC\E" --time -1 2>/dev/null) || exit 0
        [ -n "$ends" ] || exit 0
        printf "%s\n" $ends | sort -t: -k2 -n | while IFS=: read -r t p end; do
            [ "${end:-0}" -gt 0 ] || continue
            "$BIN"/kafka-console-consumer.sh --bootstrap-server "$BOOT" --topic "$TOPIC" \
                --partition "$p" --offset 0 --max-messages "$end" --timeout-ms 60000 \
                --property print.key=true --property print.timestamp=false \
                | sed "s/^/$p\t/"
        done
    '
}

# Replication factor of the demo topic as the broker reports it. Prints a bare
# integer, or "" if the topic does not exist / cannot be reached. The restore
# steps compare it against TOPIC_REPLICAS: the strategy records each topic's
# replication factor in the backup manifest and recreates the topic with it,
# and this is the observable that proves the value survived the round-trip.
topic_replication_factor() {
    local app="$1"
    kafka_run "$app" '
        desc=$("$BIN"/kafka-topics.sh --bootstrap-server "$BOOT" --describe --topic "\Q$TOPIC\E" 2>/dev/null) || exit 0
        printf "%s\n" "$desc" | sed -n "s/.*ReplicationFactor: *\([0-9][0-9]*\).*/\1/p" | head -1
    ' | tr -d '[:space:]'
}

# Create the "<app>-backup-s3" Secret the Job strategy Pod consumes, from the
# bucket coordinates cached by 03-create-bucket.sh. The generic Job strategy -
# unlike the app-specific drivers - has no chart support to emit this Secret,
# so the demo provides it. Called for the source app (step 04) and the
# restore target (step 07). The S3 endpoint's CA, when 03-create-bucket.sh
# cached one, rides along as ca.crt: the strategy Pod mounts that key and
# verifies the endpoint against it instead of disabling verification.
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
    local ca_args=()
    if [[ -n "${S3_CA_B64:-}" ]]; then
        ca_args=(--from-literal=ca.crt="$(printf '%s' "$S3_CA_B64" | base64 -d)")
    fi
    kubectl -n "$NAMESPACE" create secret generic "${app}-backup-s3" \
        --from-literal=accessKey="$S3_ACCESS_KEY" \
        --from-literal=secretKey="$S3_SECRET_KEY" \
        --from-literal=endpoint="$S3_ENDPOINT" \
        --from-literal=region="$S3_REGION" \
        --from-literal=bucket="$S3_BUCKET" \
        ${ca_args[@]+"${ca_args[@]}"} \
        --dry-run=client -o yaml | kubectl apply -f -
}
