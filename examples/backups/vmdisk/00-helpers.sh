#!/bin/bash
# Helper functions and variables for the VMDisk backup/restore demo.
# Source this file in other scripts: source "$(dirname "$0")/00-helpers.sh"

# ANSI color codes
export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export MAGENTA='\033[0;35m'
export CYAN='\033[0;36m'
export WHITE='\033[1;37m'
export NC='\033[0m'
export BOLD='\033[1m'

# Default settings
export NAMESPACE="${NAMESPACE:-tenant-root}"
# The platform ships the SeaweedFS-backed BackupStorageLocation as cozy-default
# (packages/system/backupstrategy-controller). Point the demo/e2e strategy at it
# by default so the flow works out of the box on a backups-enabled cluster;
# override for an external S3 whose BSL carries a different name.
export BACKUP_STORAGE_LOCATION="${BACKUP_STORAGE_LOCATION:-cozy-default}"

# Logging functions (output to stderr to avoid polluting captured output)
log_info()    { echo -e "${CYAN}ℹ${NC} $*" >&2; }
log_success() { echo -e "${GREEN}✔${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}⚠${NC} $*" >&2; }
log_error()   { echo -e "${RED}✖${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}▶ $*${NC}" >&2; }
log_command() { echo -e "${WHITE}  \$ $*${NC}" >&2; }

separator() {
    echo -e "\n${CYAN}────────────────────────────────────────────────────────────${NC}\n" >&2
}

print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}== ${title} ==${NC}\n" >&2
}

# Wait for a resource field to reach a desired value.
# Args: type name jsonpath desired [namespace] [timeout] [fail_value]
wait_for_field() {
    local resource_type="$1"
    local resource_name="$2"
    local jsonpath="$3"
    local desired="$4"
    local namespace="${5:-}"
    local timeout="${6:-300}"
    # Optional terminal-failure value: when the field reaches it, stop
    # immediately instead of polling out the timeout. Fail fast, fail loud.
    local fail_value="${7:-}"

    log_command "waiting for $resource_type/$resource_name $jsonpath == '$desired'"

    local elapsed=0
    local ns_flag=""
    [[ -n "$namespace" ]] && ns_flag="-n $namespace"

    while true; do
        local current
        # shellcheck disable=SC2086
        current=$(kubectl get "$resource_type" "$resource_name" $ns_flag -o jsonpath="$jsonpath" 2>/dev/null || true)
        if [[ "$current" == "$desired" ]]; then
            log_success "$resource_type/$resource_name reached '$desired'"
            return 0
        fi
        if [[ -n "$fail_value" && "$current" == "$fail_value" ]]; then
            log_error "$resource_type/$resource_name reached terminal failure state '$fail_value'"
            return 1
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
        echo -n "." >&2
    done
}
