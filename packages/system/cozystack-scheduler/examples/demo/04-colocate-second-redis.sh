#!/bin/bash
# Script 04: Second Redis in colocate tenant - shows cross-app independence
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-colocate"
REDIS_NAME="redis-demo2"

print_header "Step 4: Colocate - Second Redis (cross-app independence)"

log_step "Cleaning up $REDIS_NAME if it already exists..."
kubectl delete redis "$REDIS_NAME" -n "$NAMESPACE" --ignore-not-found >&2
sleep 3

separator

log_step "Creating a second Redis in $NAMESPACE"
log_info "This Redis will colocate its own pods together,"
log_info "but may land on a DIFFERENT node than the first Redis."

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS_NAME
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false"

pause

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS_NAME
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false
EOF

log_success "Redis $REDIS_NAME created"

log_step "Waiting for pods to be scheduled..."
wait_for_redis_ready "$NAMESPACE" "$REDIS_NAME"
wait_for_pods "$NAMESPACE"

log_step "Pod placement for both Redis instances:"
log_info "Each Redis colocates its own pods, but the two Redises are independent."
show_pods "$NAMESPACE"

separator
log_success "Second Redis demo complete."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./05-colocate-cleanup.sh"
