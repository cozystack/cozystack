#!/bin/bash
# Script 12: Clean up Redis instances in tenant-node2
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-node2"

print_header "Step 12: Clean up tenant-node2"

log_step "Deleting all Redis instances in $NAMESPACE..."

kubectl delete redis --all -n "$NAMESPACE" --ignore-not-found >&2

log_info "Waiting for pods to terminate..."
sleep 10

log_step "Remaining pods:"
show_pods "$NAMESPACE"

separator
log_success "Cleanup complete. Demo finished!"
