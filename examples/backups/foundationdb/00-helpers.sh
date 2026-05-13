#!/bin/bash
# Shared helpers for the FoundationDB backup/restore demo.
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
export FDB_NAME="${FDB_NAME:-fdb-src}"
export FDB_RESTORE_NAME="${FDB_RESTORE_NAME:-fdb-dst}"
export BUCKET_NAME="${BUCKET_NAME:-foundationdb-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-foundationdb-default}"
export STRATEGY_NAME="${STRATEGY_NAME:-foundationdb-strategy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-foundationdb-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-foundationdb-restore-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-foundationdb-restore-to-copy}"

# Cozystack foundationdb ApplicationDefinition prefixes the release name with
# "foundationdb-", so the operator-side apps.foundationdb.org/FoundationDBCluster
# (and FoundationDBBackup / FoundationDBRestore CRs the driver materialises)
# carry that prefix. Tests against the operator CRDs use these.
export FDB_CLUSTER_NAME="foundationdb-${FDB_NAME}"
export FDB_DST_CLUSTER_NAME="foundationdb-${FDB_RESTORE_NAME}"

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

# Run an fdbcli statement against a FoundationDB cluster of the given app
# instance. Args: <app-name> <fdbcli-script>
#
# Uses one of the operator-managed cluster_controller pods. The
# foundationdb-kubernetes-sidecar container ships fdbcli and bind-mounts the
# generated cluster file at /var/dynamic-conf/fdb.cluster, which is what
# fdbcli reads when invoked with --cluster-file.
fdbcli_exec() {
    local app="$1"
    local script="$2"
    local cluster_name="foundationdb-${app}"
    local pod
    pod=$(kubectl -n "$NAMESPACE" get pods \
        -l "foundationdb.org/fdb-cluster-name=${cluster_name},foundationdb.org/fdb-process-class=cluster_controller" \
        --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [[ -z "$pod" ]]; then
        log_error "No running cluster_controller pod found for ${cluster_name}"
        return 1
    fi
    kubectl -n "$NAMESPACE" exec -i "$pod" -c foundationdb -- \
        fdbcli --cluster-file=/var/dynamic-conf/fdb.cluster --exec "$script"
}
