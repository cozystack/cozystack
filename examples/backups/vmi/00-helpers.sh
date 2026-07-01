#!/bin/bash
# Helper functions and variables for the VMInstance backup/restore demo
# Source this file in other scripts: source "$(dirname "$0")/00-helpers.sh"

# ANSI color codes
export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export BLUE='\033[0;34m'
export MAGENTA='\033[0;35m'
export CYAN='\033[0;36m'
export WHITE='\033[1;37m'
export NC='\033[0m' # No Color
export BOLD='\033[1m'

# Default settings
export NAMESPACE="${NAMESPACE:-tenant-root}"
export BACKUP_STORAGE_LOCATION="${BACKUP_STORAGE_LOCATION:-default}"

# Logging functions (output to stderr to avoid polluting captured output)
log_info() {
    echo -e "${BLUE}ℹ${NC} $*" >&2
}

log_success() {
    echo -e "${GREEN}✔${NC} $*" >&2
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $*" >&2
}

log_error() {
    echo -e "${RED}✖${NC} $*" >&2
}

log_step() {
    echo -e "\n${MAGENTA}${BOLD}▶ $*${NC}" >&2
}

log_substep() {
    echo -e "${CYAN}  → $*${NC}" >&2
}

log_command() {
    echo -e "${WHITE}  \$ $*${NC}" >&2
}

# Wait for user to press Enter
wait_for_enter() {
    echo -e "\n${CYAN}Press Enter to continue...${NC}" >&2
    read -r
}

# Check if a Kubernetes resource exists
resource_exists() {
    local resource_type="$1"
    local resource_name="$2"
    local namespace="${3:-}"

    if [[ -n "$namespace" ]]; then
        kubectl get "$resource_type" "$resource_name" -n "$namespace" &>/dev/null
    else
        kubectl get "$resource_type" "$resource_name" &>/dev/null
    fi
}

# Wait for a resource field to reach a desired value
wait_for_field() {
    local resource_type="$1"
    local resource_name="$2"
    local jsonpath="$3"
    local desired="$4"
    local namespace="${5:-}"
    local timeout="${6:-300}"

    log_substep "Waiting for $resource_type/$resource_name $jsonpath to become '$desired'..."

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
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
        echo -n "." >&2
    done
}

# Print a separator line
separator() {
    echo -e "\n${CYAN}────────────────────────────────────────────────────────────${NC}\n" >&2
}

# Print script header
print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}╔════════════════════════════════════════════════════════════╗${NC}" >&2
    echo -e "${MAGENTA}${BOLD}║${NC} ${WHITE}${BOLD}$title${NC}" >&2
    echo -e "${MAGENTA}${BOLD}╚════════════════════════════════════════════════════════════╝${NC}\n" >&2
}
