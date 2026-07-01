#!/bin/bash
# Shared helpers for the ClickHouse backup/restore demo.
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
export CLICKHOUSE_NAME="${CLICKHOUSE_NAME:-clickhouse-test}"
export CLICKHOUSE_RESTORE_NAME="${CLICKHOUSE_RESTORE_NAME:-clickhouse-restore}"
export BUCKET_NAME="${BUCKET_NAME:-clickhouse-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-clickhouse-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-altinity}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-clickhouse-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-clickhouse-restore-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-clickhouse-restore-to-copy}"
# The clickhouse-backup sidecar lives in the application Pod (rendered by the
# chart when backup.enabled=true). Tenants don't manage a separate Secret;
# the chart projects bucket coordinates into <release>-backup-s3 from the
# spec.backup.* values populated by step 04 from the BucketInfo cache.

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

# Run a SQL statement against the ClickHouse cluster of the given app instance.
# Args: <release-name> <sql>
# Note: the Cozystack ClickHouse RD prefixes Helm release names with
# "clickhouse-", so the resources rendered by the chart (StatefulSet, Secret,
# etc.) carry that prefix even when the user-facing application name does not.
clickhouse_query() {
    local release="$1"
    local sql="$2"
    kubectl -n "$NAMESPACE" exec -i \
        "statefulset/chi-clickhouse-${release}-clickhouse-0-0" -c clickhouse -- \
        clickhouse-client -u backup --password "$(_clickhouse_password "$release")" -q "$sql"
}

# Read the auto-generated 'backup' user password from the chart-rendered Secret.
_clickhouse_password() {
    local release="$1"
    kubectl -n "$NAMESPACE" get secret "clickhouse-${release}-credentials" \
        -o jsonpath='{.data.backup}' | base64 -d
}
