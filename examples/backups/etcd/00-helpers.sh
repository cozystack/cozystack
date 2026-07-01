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

# Ephemeral etcdctl pod name. Created on first etcdctl_exec call; reused
# for subsequent calls in the same script run. cleanup.sh removes it.
export ETCDCTL_POD="${ETCDCTL_POD:-etcdctl}"

# ensure_etcdctl_pod (re-)creates a long-running etcdctl pod with the
# client TLS material mounted. We can't `kubectl exec etcd-0 -- env ...
# etcdctl ...` because:
#   - the chart-rendered etcd image (quay.io/coreos/etcd) is distroless:
#     no `env`, no shell, only the etcd/etcdctl binaries
#   - the etcd member pod does NOT mount the client TLS pair
#     (etcd-client-tls is consumed by external etcdctl callers, not by
#     the etcd member itself), so etcdctl-from-inside has no client cert
# So spawn a separate pod whose ONLY job is to run blocking etcdctl
# commands; mount etcd-client-tls (TLS pair + ca.crt) on it and use
# `kubectl exec` to fire one-shot etcdctl invocations.
#
# Holding the pod open: `etcdctl watch <key>` blocks until the key
# receives a write OR the context is cancelled. We watch a sentinel
# key (different from the demo's data sentinel) so the pod stays Ready
# without doing any work.
ensure_etcdctl_pod() {
    if kubectl -n "$NAMESPACE" get pod "$ETCDCTL_POD" >/dev/null 2>&1; then
        kubectl -n "$NAMESPACE" wait pod "$ETCDCTL_POD" --for=condition=ready --timeout=120s >/dev/null
        return 0
    fi
    log_substep "Provisioning ephemeral etcdctl pod '$ETCDCTL_POD' in $NAMESPACE..."
    kubectl apply -f - <<EOF >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${ETCDCTL_POD}
  namespace: ${NAMESPACE}
spec:
  restartPolicy: Never
  containers:
  - name: etcdctl
    image: quay.io/coreos/etcd:v3.5.12
    command:
    - etcdctl
    - "--cacert=/etc/etcd/pki/client/ca.crt"
    - "--cert=/etc/etcd/pki/client/tls.crt"
    - "--key=/etc/etcd/pki/client/tls.key"
    - "--endpoints=https://${ETCD_NAME}.${NAMESPACE}.svc:2379"
    - watch
    - "__etcdctl_keep_alive__"
    securityContext:
      allowPrivilegeEscalation: false
      runAsNonRoot: true
      runAsUser: 1001
      capabilities: { drop: ["ALL"] }
      seccompProfile: { type: RuntimeDefault }
    volumeMounts:
    - { name: client-tls, mountPath: /etc/etcd/pki/client, readOnly: true }
  volumes:
  - name: client-tls
    secret:
      secretName: etcd-client-tls
EOF
    kubectl -n "$NAMESPACE" wait pod "$ETCDCTL_POD" --for=condition=ready --timeout=180s >/dev/null
}

# Run an etcdctl command against the source EtcdCluster. Args: <etcdctl args...>
etcdctl_exec() {
    ensure_etcdctl_pod
    kubectl -n "$NAMESPACE" exec "$ETCDCTL_POD" -- etcdctl \
        --cacert=/etc/etcd/pki/client/ca.crt \
        --cert=/etc/etcd/pki/client/tls.crt \
        --key=/etc/etcd/pki/client/tls.key \
        --endpoints="https://${ETCD_NAME}.${NAMESPACE}.svc:2379" \
        "$@"
}
