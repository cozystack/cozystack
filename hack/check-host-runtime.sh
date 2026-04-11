#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# check-host-runtime.sh — operator preflight warning
#
# Purpose:
#   Warn when a standalone containerd.service or docker.service is running on
#   the host alongside the embedded k3s runtime. This mismatch is silent on
#   day 0 (k3s uses its own containerd at /run/k3s/containerd/containerd.sock
#   and /var/lib/rancher/k3s/agent/containerd) but over time the standalone
#   runtime accumulates unpruned images and build cache in /var/lib/containerd
#   — enough to trigger DiskPressure and crash cozystack-api with eviction
#   loops. The script does NOT block install; it only prints a warning.
#
# When to run:
#   Before `helm install cozy-installer` on an Ubuntu host prepared with k3s
#   or kubeadm (cozystack "generic" variant). Irrelevant on Talos where the
#   container runtime lifecycle is fully managed.
#
# Exit code:
#   Always 0 (warning, not a blocker). Warnings go to stderr.
#
# Environment variables (test hooks — override default probe paths):
#   COZYSTACK_CONTAINERD_SOCKET        standalone containerd socket path
#   COZYSTACK_DOCKER_SOCKET_PATHS      space-separated list of docker socket paths
#   COZYSTACK_CONTAINERD_DIR           standalone containerd data directory
#   COZYSTACK_DOCKER_DIR               standalone docker data directory
#   COZYSTACK_PREFLIGHT_FORCE_NO_SYSTEMCTL=1    pretend systemctl is absent
# -----------------------------------------------------------------------------
set -euo pipefail

YELLOW='\033[1;33m'
RESET='\033[0m'

CONTAINERD_SOCKET=${COZYSTACK_CONTAINERD_SOCKET:-/run/containerd/containerd.sock}
DOCKER_SOCKET_PATHS=${COZYSTACK_DOCKER_SOCKET_PATHS:-/run/docker.sock /var/run/docker.sock}
CONTAINERD_DIR=${COZYSTACK_CONTAINERD_DIR:-/var/lib/containerd}
DOCKER_DIR=${COZYSTACK_DOCKER_DIR:-/var/lib/docker}

WARNINGS=0

warn() {
    printf '%bWARNING:%b %s\n' "$YELLOW" "$RESET" "$1" >&2
    WARNINGS=$((WARNINGS + 1))
}

detect_systemctl() {
    if [ "${COZYSTACK_PREFLIGHT_FORCE_NO_SYSTEMCTL:-0}" = "1" ]; then
        return 1
    fi
    if command -v systemctl >/dev/null 2>&1 && systemctl --version >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

disk_usage() {
    local path=$1
    local usage
    if [ -d "$path" ]; then
        usage=$(du -sh "$path" 2>/dev/null | awk '{print $1}' || true)
        if [ -n "${usage:-}" ]; then
            printf ' (%s uses %s)' "$path" "$usage"
            return 0
        fi
    fi
    printf ''
}

service_active() {
    local service=$1
    if [ "$HAS_SYSTEMCTL" = "1" ]; then
        if systemctl is-active "$service" >/dev/null 2>&1; then
            return 0
        fi
    fi
    return 1
}

check_containerd() {
    local detail=""
    local found=0
    if service_active containerd.service; then
        found=1
    fi
    if [ -e "$CONTAINERD_SOCKET" ]; then
        found=1
    fi
    if [ "$found" -eq 1 ]; then
        detail=$(disk_usage "$CONTAINERD_DIR")
        warn "standalone containerd.service detected alongside k3s embedded runtime${detail}"
    fi
}

check_docker() {
    local detail=""
    local found=0
    if service_active docker.service; then
        found=1
    fi
    if [ "$found" -eq 0 ]; then
        for sock in $DOCKER_SOCKET_PATHS; do
            if [ -e "$sock" ]; then
                found=1
                break
            fi
        done
    fi
    if [ "$found" -eq 1 ]; then
        detail=$(disk_usage "$DOCKER_DIR")
        warn "standalone docker.service detected alongside k3s embedded runtime${detail}"
    fi
}

if detect_systemctl; then
    HAS_SYSTEMCTL=1
else
    HAS_SYSTEMCTL=0
fi

check_containerd
check_docker

if [ "$WARNINGS" -gt 0 ]; then
    printf '%bHINT:%b cozystack runs its own containerd under k3s. To stop the shadow runtime:\n' "$YELLOW" "$RESET" >&2
    printf '  sudo systemctl disable --now docker.service containerd.service\n' >&2
    printf '  sudo rm -rf /var/lib/docker /var/lib/containerd\n' >&2
fi

exit 0
