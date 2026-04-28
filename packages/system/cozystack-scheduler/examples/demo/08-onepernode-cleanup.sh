#!/bin/bash
# Script 08: Clean up Redis instances in tenant-onepernode
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-onepernode"

print_header "Step 8: Clean up tenant-onepernode"

log_step "Deleting all Redis instances in $NAMESPACE..."

kubectl delete redis --all -n "$NAMESPACE" --ignore-not-found >&2

log_info "Waiting for pods to terminate..."
sleep 10

log_step "Remaining pods:"
show_pods "$NAMESPACE"

separator
log_success "Cleanup complete."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./09-spread-demo.sh"
