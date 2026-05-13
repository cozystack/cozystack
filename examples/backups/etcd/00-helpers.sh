#!/bin/bash
# Shared helpers for the Etcd backup/restore demo.
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
# The chart pins the Helm release name (and the operator-side
# etcd.aenix.io/EtcdCluster name) to "etcd" via
# templates/check-release-name.yaml, so the apps.cozystack.io/Etcd CR
# MUST be named "etcd" per namespace. To-copy in the same namespace is
# unsupported by design (chart constraint).
export ETCD_NAME="${ETCD_NAME:-etcd}"
export BUCKET_NAME="${BUCKET_NAME:-etcd-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-etcd-default}"
export STRATEGY_NAME="${STRATEGY_NAME:-etcd-strategy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-etcd-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-etcd-restore-inplace}"

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

# Run an etcdctl command inside an etcd-0 pod. Args: <etcdctl args...>
# The chart's EtcdCluster mounts the client TLS Secret at
# /etc/etcd/pki/client, which is where the etcd binary picks the cert
# pair up. We exec into etcd-0 which always exists for a healthy cluster.
etcdctl_exec() {
    kubectl -n "$NAMESPACE" exec etcd-0 -- env \
        ETCDCTL_API=3 \
        ETCDCTL_CACERT=/etc/etcd/pki/client/ca.crt \
        ETCDCTL_CERT=/etc/etcd/pki/client/tls.crt \
        ETCDCTL_KEY=/etc/etcd/pki/client/tls.key \
        ETCDCTL_ENDPOINTS=https://127.0.0.1:2379 \
        etcdctl "$@"
}
