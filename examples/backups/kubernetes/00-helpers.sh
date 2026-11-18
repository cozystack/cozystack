#!/bin/bash
# Shared helpers for the tenant Kubernetes backup/restore walkthrough.
# Source this file in other scripts: source "$(dirname "$0")/00-helpers.sh"

export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export BLUE='\033[0;34m'
export MAGENTA='\033[0;35m'
export CYAN='\033[0;36m'
export NC='\033[0m'
export BOLD='\033[1m'

export NAMESPACE="${NAMESPACE:-tenant-root}"
export CLUSTER_NAME="${CLUSTER_NAME:-backup-demo}"
export POOL_NAME="${POOL_NAME:-md0}"
export WORKER_DISK_SIZE="${WORKER_DISK_SIZE:-20Gi}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-cozy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-kubernetes-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-kubernetes-restore-inplace}"
export RESTOREJOB_RECREATE_NAME="${RESTOREJOB_RECREATE_NAME:-kubernetes-restore-recreate}"
export TENANT_API_PORT="${TENANT_API_PORT:-16443}"
export MARKER_VALUE="${MARKER_VALUE:-roundtrip-7c2e91d4}"
export RELEASE="kubernetes-${CLUSTER_NAME}"

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }

print_header() {
    echo -e "\n${MAGENTA}${BOLD}== $1 ==${NC}\n" >&2
}

# shellcheck source-path=SCRIPTDIR source=../_lib/wait-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../_lib/wait-helpers.sh"

# The admin kubeconfig points at the in-cluster Service, so the walkthrough
# reaches the tenant API through a port-forward to it and keeps the TLS server
# name the certificate is issued for.
TENANT_KUBECONFIG="$(dirname "${BASH_SOURCE[0]}")/.tenant-kubeconfig"
export TENANT_KUBECONFIG

tenant_connect() {
    kubectl -n "$NAMESPACE" get secret "${RELEASE}-admin-kubeconfig" \
        -o jsonpath='{.data.super-admin\.svc}' | base64 -d > "$TENANT_KUBECONFIG"
    chmod 600 "$TENANT_KUBECONFIG"
    local cluster
    cluster=$(kubectl --kubeconfig "$TENANT_KUBECONFIG" config view -o jsonpath='{.clusters[0].name}')
    kubectl --kubeconfig "$TENANT_KUBECONFIG" config set-cluster "$cluster" \
        --server="https://127.0.0.1:${TENANT_API_PORT}" \
        --tls-server-name="${RELEASE}.${NAMESPACE}.svc" >/dev/null
    pkill -f "port-forward svc/${RELEASE} ${TENANT_API_PORT}:6443" 2>/dev/null || true
    kubectl -n "$NAMESPACE" port-forward "svc/${RELEASE}" "${TENANT_API_PORT}:6443" >/dev/null 2>&1 &
    for _ in $(seq 1 30); do
        tkubectl get --raw /readyz >/dev/null 2>&1 && return 0
        sleep 2
    done
    log_error "tenant API of ${RELEASE} is not reachable through the port-forward"
    return 1
}

tenant_disconnect() {
    pkill -f "port-forward svc/${RELEASE} ${TENANT_API_PORT}:6443" 2>/dev/null || true
}

tkubectl() {
    kubectl --kubeconfig "$TENANT_KUBECONFIG" "$@"
}

# The CA fingerprint is what proves a restore brought back the cluster rather
# than a new one: nodes, kubeconfigs and tokens all trust this CA.
cluster_ca_fingerprint() {
    kubectl -n "$NAMESPACE" get secret "${RELEASE}-ca" -o jsonpath='{.data.ca\.crt}' \
        | base64 -d | openssl x509 -noout -fingerprint -sha256 | cut -d= -f2
}

# wait_tenant_marker checks both halves of the backup: the ConfigMap comes back
# with etcd, the file on the PVC with the volumes.
wait_tenant_marker() {
    local timeout="${1:-900}" elapsed=0 cm data
    while true; do
        cm=$(tkubectl -n demo get configmap marker -o jsonpath='{.data.marker}' 2>/dev/null || true)
        data=$(tkubectl -n demo exec deploy/writer -- cat /data/marker 2>/dev/null || true)
        if [[ "$cm" == "$MARKER_VALUE" && "$data" == "$MARKER_VALUE" ]]; then
            log_success "marker present in etcd (ConfigMap) and on the volume"
            return 0
        fi
        (( elapsed >= timeout )) && { log_error "marker not restored: configmap='${cm}' volume='${data}'"; return 1; }
        sleep 10
        elapsed=$((elapsed + 10))
    done
}
