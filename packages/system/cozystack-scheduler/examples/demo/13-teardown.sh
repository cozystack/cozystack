#!/bin/bash
# Script 13: Tear down all tenants and scheduling classes
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Teardown: Remove tenants and scheduling classes"

log_step "Deleting tenants..."
for t in colocate onepernode spread node2; do
    kubectl delete tenant "$t" -n tenant-root --ignore-not-found >&2
done

log_info "Waiting for tenant namespaces to be cleaned up..."
sleep 15

log_step "Deleting scheduling classes..."
for sc in colocate one-per-node spread-evenly pin-to-node2; do
    kubectl delete schedulingclass "$sc" --ignore-not-found >&2
done

separator
log_success "Teardown complete."
