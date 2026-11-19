#!/bin/bash
# Script 05: Clean up Redis instances in tenant-colocate
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-colocate"

print_header "Step 5: Clean up tenant-colocate"

log_step "Deleting all Redis instances in $NAMESPACE..."

kubectl delete redis --all -n "$NAMESPACE" --ignore-not-found >&2

log_info "Waiting for pods to terminate..."
sleep 10

log_step "Remaining pods:"
show_pods "$NAMESPACE"

separator
log_success "Cleanup complete."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./06-onepernode-demo.sh"
