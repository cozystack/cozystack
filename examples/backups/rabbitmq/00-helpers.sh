#!/bin/bash
# Shared helpers for the RabbitMQ definitions backup/restore demo.
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
export RABBITMQ_SRC_NAME="${RABBITMQ_SRC_NAME:-rabbitmq-src}"
export RABBITMQ_TARGET_NAME="${RABBITMQ_TARGET_NAME:-rabbitmq-target}"
# The rabbitmq-rd ApplicationDefinition renders the HelmRelease with
# releaseName = "rabbitmq-" + appName (release.prefix in
# packages/system/rabbitmq-rd/cozyrds/rabbitmq.yaml), and the chart sets the
# rabbitmq.com/RabbitmqCluster metadata.name to .Release.Name, so the
# operator-side CR (its Service, default-user Secret and server pods) is
# rabbitmq-<app>.
export RABBITMQ_SRC_CR="rabbitmq-${RABBITMQ_SRC_NAME}"
export RABBITMQ_TARGET_CR="rabbitmq-${RABBITMQ_TARGET_NAME}"
# cozy-default routes RabbitMQ to the shipped cozy-default-rabbitmq Job
# strategy, which exports broker DEFINITIONS to the platform cozy-backups
# bucket. No demo Bucket and no app backup block are needed: the strategy
# carries the S3 coordinates and the controller projects cozy-backups-creds
# into this namespace before each run.
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-cozy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-rabbitmq-src-adhoc}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-rabbitmq-src-in-place}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-rabbitmq-src-to-rabbitmq-target}"
export PLAN_NAME="${PLAN_NAME:-rabbitmq-src-daily}"

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}> $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }

print_header() {
    echo -e "\n${MAGENTA}${BOLD}== $1 ==${NC}\n" >&2
}

# Wait until a JSONPath value on a resource matches the desired string.
# Optional 7th arg is a TERMINAL failure value: once the field reaches it the
# wait returns 1 immediately instead of polling to the timeout. BackupJob and
# RestoreJob settle on a terminal phase=Failed that never becomes Succeeded, so
# failing fast on it keeps wall-clock in reach.
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
# controller creates the HR asynchronously) and a fail-fast on Stalled=True.
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

# Wait until the rabbitmq.com/RabbitmqCluster CR reports AllReplicasReady=True
# (the cluster-operator's own readiness signal — every server pod is up and the
# management API is serving). No fail-fast value: the condition can be briefly
# absent or False during rolling startup before it converges.
wait_rabbitmq_ready() {
    local cr="$1" timeout="${2:-600}"
    wait_for_field rabbitmqclusters.rabbitmq.com "$cr" \
        '{.status.conditions[?(@.type=="AllReplicasReady")].status}' True "$NAMESPACE" "$timeout"
}

# Run rabbitmqctl inside a RabbitmqCluster's first server pod. rabbitmqctl talks
# to the local node over the Erlang dist port and needs no HTTP credentials, so
# it is the simplest way to mutate and read the DEFINITIONS (vhosts, policies)
# this demo uses as its round-trip sentinel. Args: <cr-name> <rabbitmqctl args...>
rabbitmqctl_exec() {
    local cr="$1"; shift
    kubectl -n "$NAMESPACE" exec "${cr}-server-0" -c rabbitmq -- rabbitmqctl "$@"
}

# Seed the sentinel: a vhost plus a policy on it. Both are broker DEFINITIONS
# captured by /api/definitions, so their survival through S3 proves the backup
# round-tripped the definitions (not merely that a Job ran). Args: <cr> <token>
rabbitmq_seed_sentinel() {
    local cr="$1" token="$2"
    rabbitmqctl_exec "$cr" add_vhost "$token" >/dev/null
    rabbitmqctl_exec "$cr" set_policy -p "$token" sentinel-policy '.*' \
        '{"max-length":1000}' --apply-to queues >/dev/null
}

# True (0) when the sentinel vhost exists, false (1) when it is absent. The
# exec is captured separately and a FAILED exec aborts rather than being folded
# into "absent" - otherwise a transient exec failure would read as "the sentinel
# is not there", silently passing the negative pre-checks in run-all.sh.
# Args: <cr> <token>
rabbitmq_has_sentinel() {
    local cr="$1" token="$2" out
    if ! out=$(rabbitmqctl_exec "$cr" list_vhosts name 2>/dev/null); then
        log_error "rabbitmqctl list_vhosts failed on $cr; cannot determine sentinel presence"
        exit 1
    fi
    printf '%s\n' "$out" | grep -qxF "$token"
}
