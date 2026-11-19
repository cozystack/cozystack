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
# verify helpers below run it as a long-lived Pod they `kubectl exec` into.
export NATS_BOX_IMAGE="${NATS_BOX_IMAGE:-natsio/nats-box:0.14.5}"
# Name of that long-lived CLI Pod; cleanup.sh removes it.
export NATS_CLI_POD="${NATS_CLI_POD:-nats-cli}"

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

# In-cluster NATS client URL for the given application instance. The NATS app
# names its client Service after the application (fullnameOverride=<name>) and
# stores per-user passwords in the "<name>-credentials" Secret. The demo sets a
# fixed password (NATS_PASSWORD) on both the source and restore-target apps, so
# a single URL shape works everywhere. The password is not in the URL: nats_cli
# passes it to the CLI in NATS_PASSWORD on stdin, to keep it out of the exec
# request URL the apiserver records in its audit log.
nats_url() {
    local app="$1"
    echo "nats://${NATS_USER}@${app}.${NAMESPACE}.svc:4222"
}

# Ensure the long-lived nats-box CLI Pod exists and is Ready, so nats_cli can
# exec into it. Idempotent: a Ready Pod this demo owns is reused across calls and
# across the numbered demo scripts; a leftover in a terminal phase (Succeeded /
# Failed) is replaced rather than waited on; and a same-named Pod this demo does
# not own is refused rather than hijacked or deleted. cleanup.sh removes it.
# Every step returns on failure explicitly: this runs as the left side of
# `|| return 1`, and some callers also wrap it in $(...), so errexit never
# applies inside it and a bare failure would carry on to the next step.
nats_cli_pod() {
    local phase owner
    phase=$(kubectl -n "$NAMESPACE" get pod "$NATS_CLI_POD" --ignore-not-found -o jsonpath='{.status.phase}') || return 1
    owner=$(kubectl -n "$NAMESPACE" get pod "$NATS_CLI_POD" --ignore-not-found -o jsonpath='{.metadata.labels.cozystack\.io/backup-demo}') || return 1
    if [ -n "$phase" ] && [ "$owner" != "nats" ]; then
        log_error "Pod $NAMESPACE/$NATS_CLI_POD exists but this demo does not own it; refusing to use or delete it"
        return 1
    fi
    if [ "$phase" != "Running" ] && [ "$phase" != "Pending" ]; then
        kubectl -n "$NAMESPACE" delete pod "$NATS_CLI_POD" --grace-period=1 --ignore-not-found >/dev/null || return 1
        kubectl -n "$NAMESPACE" run "$NATS_CLI_POD" --image="$NATS_BOX_IMAGE" \
            --labels=cozystack.io/backup-demo=nats \
            --restart=Never --command -- sleep infinity >/dev/null || return 1
    fi
    kubectl -n "$NAMESPACE" wait --for=condition=Ready "pod/$NATS_CLI_POD" \
        --timeout=5m >/dev/null
}

# Run a `nats` CLI invocation against an application instance. Args after the app
# name are passed verbatim to `nats`. Example: nats_cli "$NATS_NAME" stream ls
#
# The command runs by `kubectl exec` in the long-lived nats-box Pod, not a
# throwaway `kubectl run -i` Pod per call. A throwaway Pod's stdout comes back
# over an attach that only carries what the container writes after the attach
# registers, so a CLI that finishes in between returns empty with exit 0. An
# exec'd process owns its pipes, so its output cannot be missed that way, and
# kubectl's and the CLI's stderr stay attached so a failed read says why.
#
# The password travels in NATS_PASSWORD on stdin rather than in the --server URL,
# which the exec request carries into the apiserver's audit log. The nats CLI
# reads NATS_PASSWORD from its environment.
nats_cli() {
    local app="$1"; shift
    nats_cli_pod || return 1
    printf '%s\n' "$NATS_PASSWORD" | kubectl -n "$NAMESPACE" exec -i "$NATS_CLI_POD" -- sh -c '
            IFS= read -r NATS_PASSWORD || true
            export NATS_PASSWORD
            server=$1; shift
            exec nats --server "$server" "$@"
        ' sh "$(nats_url "$app")" "$@"
}

# Number of messages currently stored in a JetStream stream, or "" if the
# stream does not exist. Only jq's stderr is dropped (a missing stream prints a
# harmless "stream not found" there); nats_cli's own stderr stays, so an exec
# that fails says why instead of surfacing as an empty count.
stream_message_count() {
    local app="$1"
    local stream="$2"
    nats_cli "$app" stream info "$stream" --json \
        | jq -r '.state.messages // empty' 2>/dev/null | tr -d '[:space:]'
}

# Create the "<app>-backup-s3" Secret the Job strategy Pod consumes, from the
# bucket coordinates cached by 03-create-bucket.sh. The generic Job strategy -
# unlike the app-specific drivers - has no chart support to emit this Secret,
# so the demo provides it. Called for the source app (step 04) and the
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
