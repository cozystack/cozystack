#!/bin/bash
# Shared helpers for the PostgreSQL backup/restore demo.
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
export BUCKET_NAME="${BUCKET_NAME:-pg-backups}"
export PG_SRC_NAME="${PG_SRC_NAME:-pg-src}"
export PG_TARGET_NAME="${PG_TARGET_NAME:-pg-target}"
# The apps/postgres chart names the cnpg.io Cluster (and its Pods'
# cnpg.io/cluster label) after the Helm release, which is postgres-<app>.
export PG_SRC_CLUSTER="postgres-${PG_SRC_NAME}"
export PG_TARGET_CLUSTER="postgres-${PG_TARGET_NAME}"
export STRATEGY_NAME="${STRATEGY_NAME:-cnpg-strategy-default}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-postgres-cnpg}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-pg-src-adhoc}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-pg-src-to-pg-target}"
export PLAN_NAME="${PLAN_NAME:-pg-src-daily}"
# App user password baked into 05-postgres-src.yaml (REPLACE_WITH_PASSWORD).
export PG_PASSWORD="${PG_PASSWORD:-Xai7Wepo0aeThie8}"

# S3 endpoint CA. cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; the demo copies its
# ca.crt into a per-app Secret the barman-cloud ObjectStore trusts via
# endpointCA. On a cluster whose S3 endpoint is signed by a publicly-trusted
# CA, set S3_CA_SECRET="" to skip the copy and drop endpointCA from the
# manifests.
export S3_CA_SECRET="${S3_CA_SECRET:-seaweedfs-system-ca-cert}"
export S3_CA_NAMESPACE="${S3_CA_NAMESPACE:-tenant-root}"
export S3_CA_KEY="${S3_CA_KEY:-ca.crt}"

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
# failing fast on it keeps wall-clock (and the snapshot Pod's log, before its
# TTL reaper fires) in reach.
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

# Wait for a HelmRelease to become Ready, with an existence backstop (the
# apps controller creates the HR asynchronously, so a bare `kubectl wait`
# right after `kubectl apply` races it) and a fail-fast on Stalled=True —
# a stalled HR has exhausted its remediation retries and will never turn
# Ready, so polling to the timeout only hides the real error.
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

# Name of the primary pod of a cnpg.io Cluster (the one accepting writes).
cnpg_primary_pod() {
    local cluster="$1"
    kubectl -n "$NAMESPACE" get pod \
        -l "cnpg.io/cluster=${cluster},cnpg.io/instanceRole=primary" \
        -o name 2>/dev/null | head -n1
}

# Run a psql statement on a cnpg.io Cluster's primary. Args: <cluster> <db> <sql>
psql_exec() {
    local cluster="$1" db="$2" sql="$3"
    local pod
    pod=$(cnpg_primary_pod "$cluster")
    [[ -n "$pod" ]] || { log_error "no primary pod for cnpg cluster '$cluster'"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c postgres -- \
        psql -U postgres -d "$db" -tAc "$sql"
}
