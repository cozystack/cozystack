#!/bin/bash
# Shared helpers for the NATS JetStream backup/restore demo.
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
export NATS_NAME="${NATS_NAME:-nats-test}"
export NATS_RESTORE_NAME="${NATS_RESTORE_NAME:-nats-restore}"
export NATS_USER="${NATS_USER:-backup}"
export NATS_PASSWORD="${NATS_PASSWORD:-jetstream-demo-pw}"
export STREAM_NAME="${STREAM_NAME:-ORDERS}"
export MESSAGE_COUNT="${MESSAGE_COUNT:-10}"
export BUCKET_NAME="${BUCKET_NAME:-nats-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-nats-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-nats-job}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-nats-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-nats-restore-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-nats-restore-to-copy}"
# natsio/nats-box ships the `nats` CLI plus curl + tar + sh - everything the
# generic Job strategy needs, with no purpose-built backup image. The seed /
# verify helpers below run it as a throwaway Pod via `kubectl run`.
export NATS_BOX_IMAGE="${NATS_BOX_IMAGE:-natsio/nats-box:0.14.5}"

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
wait_for_field() {
    local resource_type="$1"
    local resource_name="$2"
    local jsonpath="$3"
    local desired="$4"
    local namespace="${5:-}"
    local timeout="${6:-300}"

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
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# In-cluster NATS client URL for the given application instance. The NATS app
# names its client Service after the application (fullnameOverride=<name>) and
# stores per-user passwords in the "<name>-credentials" Secret. The demo sets a
# fixed password (NATS_PASSWORD) on both the source and restore-target apps, so
# a single URL shape works everywhere.
nats_url() {
    local app="$1"
    echo "nats://${NATS_USER}:${NATS_PASSWORD}@${app}.${NAMESPACE}.svc:4222"
}

# Run a `nats` CLI invocation against an application instance from a throwaway
# nats-box Pod. Args after the app name are passed verbatim to `nats`.
# Example: nats_cli "$NATS_NAME" stream ls
nats_cli() {
    local app="$1"; shift
    kubectl -n "$NAMESPACE" run "nats-cli-$RANDOM" \
        --image="$NATS_BOX_IMAGE" --restart=Never --rm -i --quiet \
        --command -- nats --server "$(nats_url "$app")" "$@"
}

# Number of messages currently stored in a JetStream stream, or "" if the
# stream does not exist.
stream_message_count() {
    local app="$1"
    local stream="$2"
    nats_cli "$app" stream info "$stream" --json 2>/dev/null \
        | jq -r '.state.messages // empty' 2>/dev/null | tr -d '[:space:]'
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
