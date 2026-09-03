#!/bin/bash
# Shared helpers for the MongoDB backup/restore demo.
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
export BUCKET_NAME="${BUCKET_NAME:-mongodb-backups}"
# Bucket user declared in 00-bucket.yaml; the COSI flow materialises the
# credentials Secret as "bucket-<bucket>-<user>".
export BUCKET_USER="${BUCKET_USER:-backup}"
export MONGODB_SRC_NAME="${MONGODB_SRC_NAME:-mongodb-src}"
export MONGODB_TARGET_NAME="${MONGODB_TARGET_NAME:-mongodb-target}"
# The mongodb-rd ApplicationDefinition renders the HelmRelease with
# releaseName = "mongodb-" + appName (release.prefix in
# packages/system/mongodb-rd/cozyrds/mongodb.yaml), and the chart sets the
# psmdb.percona.com/PerconaServerMongoDB metadata.name to .Release.Name, so the
# operator-side CR (and its rs0 Service) is mongodb-<app>.
export MONGODB_SRC_CR="mongodb-${MONGODB_SRC_NAME}"
export MONGODB_TARGET_CR="mongodb-${MONGODB_TARGET_NAME}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-cozy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-mongodb-src-adhoc}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-mongodb-src-to-mongodb-target}"
export PLAN_NAME="${PLAN_NAME:-mongodb-src-daily}"

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
# failing fast on it keeps wall-clock (and the operator's pbm log, before its
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

# Wait for a HelmRelease to become Ready, with an existence backstop (the apps
# controller creates the HR asynchronously, so a bare `kubectl wait` right after
# `kubectl apply` races it) and a fail-fast on Stalled=True — a stalled HR has
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

# Wait until the psmdb.percona.com/PerconaServerMongoDB CR reports state=ready
# (the operator's own readiness signal — the rs0 members are up and initialised
# and, when backup.enabled, the pbm agents are running). No fail-fast value: the
# cluster state is recomputed every operator reconcile and can pass through
# "error" transiently during replset init before it converges to "ready", so a
# terminal-on-error gate would fail the whole run on one blip. Poll for the
# positive value only, bounded by the timeout.
wait_psmdb_ready() {
    local cr="$1" timeout="${2:-600}"
    wait_for_field perconaservermongodbs.psmdb.percona.com "$cr" \
        '{.status.state}' ready "$NAMESPACE" "$timeout"
}

# Name of the first data-bearing pod of a PerconaServerMongoDB CR's rs0 replica
# set. For the single-replica instances this demo uses, rs0-0 is the sole member
# and therefore the primary, so writes land there directly.
mongodb_primary_pod() {
    local cr="$1"
    echo "${cr}-rs0-0"
}

# Run a mongosh script against a PerconaServerMongoDB CR's rs0 primary as the
# operator-managed databaseAdmin system user (read from the internal users
# Secret the psmdb operator materialises), so writes and reads authenticate
# regardless of app-user grant timing. Args: <cr-name> <mongosh-eval>
mongosh_exec() {
    local cr="$1" js="$2"
    local pod secret admin_user admin_pw
    pod=$(mongodb_primary_pod "$cr")
    secret="internal-${cr}-users"
    admin_user=$(kubectl -n "$NAMESPACE" get secret "$secret" \
        -o jsonpath='{.data.MONGODB_DATABASE_ADMIN_USER}' 2>/dev/null | base64 -d)
    admin_pw=$(kubectl -n "$NAMESPACE" get secret "$secret" \
        -o jsonpath='{.data.MONGODB_DATABASE_ADMIN_PASSWORD}' 2>/dev/null | base64 -d)
    [[ -n "$admin_user" && -n "$admin_pw" ]] || { log_error "no databaseAdmin credentials in ${NAMESPACE}/${secret}"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c mongod -- \
        mongosh --quiet -u "$admin_user" -p "$admin_pw" --authenticationDatabase admin \
        --eval "$js"
}
