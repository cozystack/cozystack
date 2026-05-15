#!/bin/bash
# Helper functions for the scheduling classes demo
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

log_command() {
    echo -e "${WHITE}  \$ $*${NC}" >&2
}

separator() {
    echo -e "\n${CYAN}────────────────────────────────────────────────────────────${NC}\n" >&2
}

print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}╔════════════════════════════════════════════════════════════╗${NC}" >&2
    echo -e "${MAGENTA}${BOLD}║${NC} ${WHITE}${BOLD}$title${NC}" >&2
    echo -e "${MAGENTA}${BOLD}╚════════════════════════════════════════════════════════════╝${NC}\n" >&2
}

# Display a YAML manifest with syntax highlighting (just colored output)
show_manifest() {
    echo -e "${YELLOW}$1${NC}" >&2
}

# Wait for user to press any key
pause() {
    echo -e "\n${CYAN}Press any key to continue...${NC}" >&2
    read -rsn1
}

# Wait for pods to appear and stabilize
wait_for_pods() {
    local namespace="$1"
    local label="${2:-}"
    local timeout="${3:-120}"
    local elapsed=0

    log_info "Waiting for pods in $namespace..."
    while true; do
        local count
        if [[ -n "$label" ]]; then
            count=$(kubectl get pods -n "$namespace" -l "$label" --no-headers 2>/dev/null | wc -l)
        else
            count=$(kubectl get pods -n "$namespace" --no-headers 2>/dev/null | wc -l)
        fi
        if [[ "$count" -gt 0 ]]; then
            break
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_warning "Timed out waiting for pods"
            return 1
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    # Give pods a moment to get node assignments
    sleep 3
}

# Show pods with node placement
show_pods() {
    local namespace="$1"
    echo "" >&2
    kubectl get pods -n "$namespace" -o wide 2>&1 >&2
    echo "" >&2
}

# Wait for a Redis resource to be ready (HelmRelease applied)
wait_for_redis_ready() {
    local namespace="$1"
    local name="$2"
    local timeout="${3:-180}"
    local elapsed=0

    log_info "Waiting for Redis $name in $namespace to become ready..."
    while true; do
        local phase
        phase=$(kubectl get redis "$name" -n "$namespace" -o jsonpath='{.status.conditions[0].status}' 2>/dev/null || echo "")
        if [[ "$phase" == "True" ]]; then
            log_success "Redis $name is ready"
            break
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_warning "Timed out waiting for Redis to be ready, continuing anyway..."
            break
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}
