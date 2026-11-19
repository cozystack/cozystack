#!/bin/bash
# Script 07: Second Redis in one-per-node tenant - shows cross-app independence
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-onepernode"
REDIS_NAME="redis-demo2"

print_header "Step 7: One-per-node - Second Redis (cross-app independence)"

log_step "Cleaning up $REDIS_NAME if it already exists..."
kubectl delete redis "$REDIS_NAME" -n "$NAMESPACE" --ignore-not-found >&2
sleep 3

separator

log_step "Creating a second Redis in $NAMESPACE"
log_info "Anti-affinity only applies WITHIN the same application."
log_info "This second Redis has no anti-affinity against the first one."

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

log_info "Waiting for RedisFailover to appear..."
sleep 10

log_step "Patching sentinels to 2 for the second Redis as well"
kubectl patch rf redis-"$REDIS_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"sentinel":{"replicas":2}}}'

log_info "Waiting for pods to be scheduled..."
wait_for_pods "$NAMESPACE"
sleep 5

log_step "Pod placement for both Redis instances:"
log_info "Each Redis spreads its own pods one-per-node independently."
log_info "The two Redises CAN share nodes — anti-affinity is per-application."
show_pods "$NAMESPACE"

separator
log_success "Second Redis demo complete."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./08-onepernode-cleanup.sh"
